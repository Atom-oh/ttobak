package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	maxContextAccounts        = 40
	maxAccountPackBytes       = 6000
	customerUpdateDailyDrafts = 3
	maxPackNameRunes          = 120
	maxPackOpportunities      = 10
	maxPackNews               = 5
	maxPackInsights           = 5
	maxPackTextRunes          = 200
	maxPackNotes              = 2
	maxPackNoteRunes          = 600
	maxPackNoteReads          = 10
	maxPackBlockList          = 100
	maxFinalizeBlockList      = 500
	minBlockNameRunes         = 2
	packNewsWindow            = 30 * 24 * time.Hour
	packInsightWindow         = 90 * 24 * time.Hour

	// customerUpdateIncludeNotesEnv switches meeting notes in context packs.
	// Unset means on; when set, only "1" or "true" (case-insensitive,
	// surrounding space ignored) mean on, and anything else, including an
	// empty value, means off. It is read once at construction.
	customerUpdateIncludeNotesEnv = "CUSTOMER_UPDATE_INCLUDE_NOTES"
)

var packInsightTypes = []string{model.InsightNeed, model.InsightOpportunity, model.InsightRisk, model.InsightTech}

// Dependencies of CustomerUpdateContext. Each is satisfied by an existing
// service method; the ones taking a user id enforce that user's access.
type (
	// UpdateContextAccountLister is AccountService.ListAccounts.
	UpdateContextAccountLister interface {
		ListAccounts(ctx context.Context, userID string) ([]model.AccountSummary, error)
	}
	// UpdateContextSettingReader is CustomerUpdateRepository.GetAccountOpt (missing row = on).
	UpdateContextSettingReader interface {
		GetAccountOpt(ctx context.Context, userID, accountID string) (bool, error)
	}
	// UpdateContextProjectLister is ProjectService.ListAccountProjects.
	UpdateContextProjectLister interface {
		ListAccountProjects(ctx context.Context, userID, accountID string) ([]model.ProjectSummary, error)
	}
	// UpdateContextInsightLister is AccountService.ListAccountInsights.
	UpdateContextInsightLister interface {
		ListAccountInsights(ctx context.Context, userID, accountID string, from, to time.Time, types []string) ([]model.AccountInsightDTO, error)
	}
	// UpdateContextMeetingLister is AccountService.ListAccountMeetings.
	UpdateContextMeetingLister interface {
		ListAccountMeetings(ctx context.Context, userID, accountID string) ([]model.MeetingRefDTO, error)
	}
	// UpdateContextMeetingReader is an authorized, notes-only, write-free
	// meeting read: MeetingNotesReader (NewMeetingNotesReader). It must never
	// be MeetingService.GetMeetingDetail, which can write (summary-retry expiry,
	// stuck-status repair) and hydrates transcripts, attachments and shares.
	// ErrForbidden or ErrNotFound means the user may not read the meeting.
	UpdateContextMeetingReader interface {
		MeetingNotes(ctx context.Context, userID, meetingID string) (string, error)
	}
	// UpdateContextNewsReader is AccountNewsService.CurrentNews. It does no
	// authorization; CustomerUpdateContext calls it only for in-scope accounts.
	UpdateContextNewsReader interface {
		CurrentNews(ctx context.Context, accountID string, from, to time.Time) ([]model.CrawledDocument, error)
	}
	// UpdateContextMemberNames returns display names of an account's current
	// members as visible to userID; the adapter must authorize userID.
	UpdateContextMemberNames interface {
		AccountMemberNames(ctx context.Context, userID, accountID string) ([]string, error)
	}
)

// CustomerUpdateContextDeps are the reads used to scope accounts and build packs.
type CustomerUpdateContextDeps struct {
	Accounts      UpdateContextAccountLister
	Members       directMemberChecker
	Settings      UpdateContextSettingReader
	Projects      UpdateContextProjectLister
	Insights      UpdateContextInsightLister
	Meetings      UpdateContextMeetingLister
	MeetingReader UpdateContextMeetingReader
	News          UpdateContextNewsReader
	MemberNames   UpdateContextMemberNames
	Services      ServicesInUseReader
}

// AccountPack is one account's context, field for field as the accounts[]
// entries of contracts/customer-updates/run-v1.json.
type AccountPack struct {
	AccountID       string                   `json:"accountId"`
	Name            string                   `json:"name"`
	Services        []string                 `json:"services"`
	Opportunities   []AccountPackOpportunity `json:"opportunities"`
	News            []AccountPackNews        `json:"news"`
	Insights        []AccountPackInsight     `json:"insights"`
	Notes           []string                 `json:"notes"`
	BlockList       []string                 `json:"blockList"`
	DraftsRemaining int                      `json:"draftsRemaining"`
}

// AccountPackOpportunity is one open opportunity.
type AccountPackOpportunity struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Stage     string `json:"stage"`
}

// AccountPackNews is one recent news item.
type AccountPackNews struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// AccountPackInsight is one recent insight.
type AccountPackInsight struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// BuildStats are counts only, safe to log beside a run id.
type BuildStats struct {
	Candidates     int
	InScope        int
	Failed         int
	Oversized      int
	ServicesErrors int
	// ServicesUnavailable counts accounts whose services object read was
	// denied (AccessDenied: the normal "no object" answer without ListBucket).
	ServicesUnavailable int
	// Capped counts in-scope accounts left out because they already have
	// their daily drafts; CappedAccounts holds their ids (not for logs).
	Capped         int
	CappedAccounts []string
}

// CustomerUpdateContext decides which accounts a user's run covers and builds
// their packs. Pack contents go to models, so every content read is made as
// the user through authorizing services, after the direct-member scope check.
// It writes nothing and logs only account-id hash prefixes and error types.
type CustomerUpdateContext struct {
	deps         CustomerUpdateContextDeps
	now          func() time.Time
	includeNotes bool
}

// NewCustomerUpdateContext creates the builder; a nil now uses time.Now.
func NewCustomerUpdateContext(deps CustomerUpdateContextDeps, now func() time.Time) *CustomerUpdateContext {
	if now == nil {
		now = time.Now
	}
	return &CustomerUpdateContext{deps: deps, now: now, includeNotes: includeNotesFromEnv()}
}

func includeNotesFromEnv() bool {
	v, set := os.LookupEnv(customerUpdateIncludeNotesEnv)
	if !set {
		return true
	}
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true"
}

// InScope is the single scope rule, used when building and again at
// finalize: the user is a direct member (owner included; ancestor or Billing
// read alone never qualifies), the account's setting is on, and at least one
// project has an SFDC opportunity id and a non-terminal stage (unset is open).
// Denials are (false, nil); read failures are returned.
func (c *CustomerUpdateContext) InScope(ctx context.Context, userID, accountID string) (bool, error) {
	if !customerUpdateAccountIDPattern.MatchString(accountID) {
		return false, nil
	}
	if err := c.deps.Members.RequireDirectMember(ctx, userID, accountID); err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) {
			return false, nil
		}
		return false, err
	}
	enabled, err := c.deps.Settings.GetAccountOpt(ctx, userID, accountID)
	if err != nil || !enabled {
		return false, err
	}
	projects, err := c.deps.Projects.ListAccountProjects(ctx, userID, accountID)
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return len(openOpportunities(projects)) > 0, nil
}

func openOpportunities(projects []model.ProjectSummary) []model.ProjectSummary {
	out := []model.ProjectSummary{}
	for _, p := range projects {
		if p.SfdcOpptyID != "" && !model.IsTerminalStage(p.Stage) {
			out = append(out, p)
		}
	}
	return out
}

// Build returns the packs for the user's run; see BuildWithStats.
func (c *CustomerUpdateContext) Build(ctx context.Context, userID string, dayCounts map[string]int) ([]AccountPack, error) {
	packs, _, err := c.BuildWithStats(ctx, userID, dayCounts)
	return packs, err
}

// BuildWithStats lists the accounts the user can read, keeps (in account-id
// order) the first 40 that are InScope and have drafts left today
// (3 - dayCounts[id] > 0), and builds a pack for each. An account that fails
// to build or cannot fit 6,000 bytes is skipped and counted; a context
// cancellation or deadline, or a failed account listing, fails the call.
func (c *CustomerUpdateContext) BuildWithStats(ctx context.Context, userID string, dayCounts map[string]int) ([]AccountPack, BuildStats, error) {
	var stats BuildStats
	all, err := c.deps.Accounts.ListAccounts(ctx, userID)
	if err != nil {
		return nil, stats, err
	}
	accounts := uniqueAccountsByID(all)
	packs := []AccountPack{}
	for _, account := range accounts {
		if stats.InScope == maxContextAccounts {
			break
		}
		if !customerUpdateAccountIDPattern.MatchString(account.AccountID) {
			continue
		}
		remaining := customerUpdateDailyDrafts - dayCounts[account.AccountID]
		if remaining <= 0 {
			// Count in-scope accounts at today's cap, so a caller can tell
			// "wait for tomorrow" from "nothing in scope".
			ok, err := c.InScope(ctx, userID, account.AccountID)
			if err != nil {
				if ctxErr := contextError(ctx, err); ctxErr != nil {
					return nil, stats, ctxErr
				}
				stats.Failed++
				logPackFailure(account.AccountID, "scope", err)
			} else if ok {
				stats.Capped++
				stats.CappedAccounts = append(stats.CappedAccounts, account.AccountID)
			}
			continue
		}
		stats.Candidates++
		ok, err := c.InScope(ctx, userID, account.AccountID)
		if err != nil {
			if ctxErr := contextError(ctx, err); ctxErr != nil {
				return nil, stats, ctxErr
			}
			stats.Failed++
			logPackFailure(account.AccountID, "scope", err)
			continue
		}
		if !ok {
			continue
		}
		stats.InScope++
		pack, err := c.buildPack(ctx, userID, account, accounts, remaining, &stats)
		if err != nil {
			if ctxErr := contextError(ctx, err); ctxErr != nil {
				return nil, stats, ctxErr
			}
			stats.Failed++
			logPackFailure(account.AccountID, "pack", err)
			continue
		}
		fitted, fits := fitAccountPack(pack)
		if !fits {
			stats.Oversized++
			continue
		}
		packs = append(packs, fitted)
	}
	return packs, stats, nil
}

func uniqueAccountsByID(all []model.AccountSummary) []model.AccountSummary {
	seen := map[string]bool{}
	out := make([]model.AccountSummary, 0, len(all))
	for _, a := range all {
		if !seen[a.AccountID] {
			seen[a.AccountID] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

func contextError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ctx.Err()
}

func accountHashPrefix(accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return hex.EncodeToString(sum[:])[:12]
}

func logPackFailure(accountID, stage string, err error) {
	log.Printf("customer update context %s failed account=%s err=%T", stage, accountHashPrefix(accountID), err)
}

func capText(s string, n int) string {
	return capRunes(collapseSpace(s), n)
}

// buildPack reads one in-scope account's content as the user.
func (c *CustomerUpdateContext) buildPack(ctx context.Context, userID string, account model.AccountSummary, accounts []model.AccountSummary, remaining int, stats *BuildStats) (AccountPack, error) {
	id := account.AccountID
	now := c.now().UTC()
	pack := AccountPack{
		AccountID: id, Name: capText(account.Name, maxPackNameRunes),
		Services: []string{}, Opportunities: []AccountPackOpportunity{}, News: []AccountPackNews{},
		Insights: []AccountPackInsight{}, Notes: []string{}, BlockList: []string{},
		DraftsRemaining: remaining,
	}

	services, err := c.deps.Services.ServicesInUse(ctx, id)
	if err != nil {
		if ctxErr := contextError(ctx, err); ctxErr != nil {
			return pack, ctxErr
		}
		// A billing failure must not fail the account or the run.
		if errors.Is(err, ErrServicesUnavailable) {
			stats.ServicesUnavailable++
		} else {
			stats.ServicesErrors++
			logPackFailure(id, "services", err)
		}
		services = nil
	}
	for _, s := range services {
		if s = capText(s, maxServiceNameRunes); s != "" && len(pack.Services) < maxServicesInUse {
			pack.Services = append(pack.Services, s)
		}
	}

	projects, err := c.deps.Projects.ListAccountProjects(ctx, userID, id)
	if err != nil {
		return pack, err
	}
	pack.Opportunities = packOpportunities(projects, maxPackOpportunities)

	// CurrentNews does no authorization; InScope's direct-member check ran first.
	news, err := c.deps.News.CurrentNews(ctx, id, now.Add(-packNewsWindow), now)
	if err != nil {
		return pack, err
	}
	sort.SliceStable(news, func(i, j int) bool { return news[i].CrawledAt > news[j].CrawledAt })
	for _, n := range news {
		if len(pack.News) == maxPackNews {
			break
		}
		title := capText(n.Title, maxPackTextRunes)
		if title == "" || n.CrawledAt < now.Add(-packNewsWindow).Unix() {
			continue
		}
		pack.News = append(pack.News, AccountPackNews{Title: title, Summary: capText(n.Summary, maxPackTextRunes)})
	}

	insights, err := c.deps.Insights.ListAccountInsights(ctx, userID, id, now.Add(-packInsightWindow), now, packInsightTypes)
	if err != nil {
		return pack, err
	}
	sort.SliceStable(insights, func(i, j int) bool { return insights[i].OccurredAt.After(insights[j].OccurredAt) })
	for _, in := range insights {
		if len(pack.Insights) == maxPackInsights {
			break
		}
		text := capText(in.Text, maxPackTextRunes)
		if !packInsightType(in.Type) || text == "" || in.OccurredAt.Before(now.Add(-packInsightWindow)) {
			continue
		}
		pack.Insights = append(pack.Insights, AccountPackInsight{Type: in.Type, Text: text})
	}

	if c.includeNotes {
		if pack.Notes, err = c.packNotes(ctx, userID, id); err != nil {
			return pack, err
		}
	}

	memberNames, err := c.deps.MemberNames.AccountMemberNames(ctx, userID, id)
	if err != nil {
		return pack, err
	}
	pack.BlockList = packBlockList(pack.Name, id, accounts, memberNames, maxPackBlockList)
	return pack, nil
}

func packInsightType(t string) bool {
	for _, allowed := range packInsightTypes {
		if t == allowed {
			return true
		}
	}
	return false
}

// packNotes returns notes from the two newest account meetings the user may
// read. A meeting the read denies contributes nothing and does not count; a
// readable meeting counts toward the two even when its notes are empty.
func (c *CustomerUpdateContext) packNotes(ctx context.Context, userID, accountID string) ([]string, error) {
	refs, err := c.deps.Meetings.ListAccountMeetings(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if !refs[i].Date.Equal(refs[j].Date) {
			return refs[i].Date.After(refs[j].Date)
		}
		return refs[i].MeetingID < refs[j].MeetingID
	})
	notes, readable := []string{}, 0
	for i, ref := range refs {
		if readable == maxPackNotes || i == maxPackNoteReads {
			break
		}
		raw, err := c.deps.MeetingReader.MeetingNotes(ctx, userID, ref.MeetingID)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		readable++
		if text := capText(raw, maxPackNoteRunes); text != "" {
			notes = append(notes, text)
		}
	}
	return notes, nil
}

// packBlockList lists this account's member names first, then the user's
// other account names (account-id order): each collapsed, at least 2 runes,
// ASCII-case de-duplicated, at most limit. Members come first so they are the
// last lost to the cap or to size trimming. Entries equal to or contained in
// this account's own name are dropped, because the runtime matches entries as
// substrings and such an entry would reject every draft (accepted limit).
func packBlockList(ownName, ownID string, accounts []model.AccountSummary, memberNames []string, limit int) []string {
	own := strings.ToLower(ownName)
	out, seen := []string{}, map[string]bool{}
	add := func(name string) {
		name = capText(name, maxPackNameRunes)
		key := strings.ToLower(name)
		if len(out) == limit || utf8.RuneCountInString(name) < minBlockNameRunes || seen[key] || strings.Contains(own, key) {
			return
		}
		seen[key] = true
		out = append(out, name)
	}
	for _, name := range memberNames {
		add(name)
	}
	for _, a := range accounts {
		if a.AccountID != ownID {
			add(a.Name)
		}
	}
	return out
}

// BlockList is the full, untrimmed block list used to re-check drafts at
// finalize: the pack rules without the 6,000-byte trimming, at most 500
// entries. The user must be a direct member; a denial is returned as an error.
func (c *CustomerUpdateContext) BlockList(ctx context.Context, userID, accountID string) ([]string, error) {
	if !customerUpdateAccountIDPattern.MatchString(accountID) {
		return nil, ErrInvalidInput
	}
	if err := c.deps.Members.RequireDirectMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	all, err := c.deps.Accounts.ListAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	accounts := uniqueAccountsByID(all)
	ownName, found := "", false
	for _, a := range accounts {
		if a.AccountID == accountID {
			ownName, found = capText(a.Name, maxPackNameRunes), true
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	memberNames, err := c.deps.MemberNames.AccountMemberNames(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	return packBlockList(ownName, accountID, accounts, memberNames, maxFinalizeBlockList), nil
}

// OpenOpportunities returns every current open opportunity of the account
// (SFDC id set, stage not terminal, unset stage open) in projectId order, with
// the pack's name and stage normalization and no count cap, for finalize
// checks (stage words, stored opportunity names). The user must be a direct
// member; a denial is returned as an error.
func (c *CustomerUpdateContext) OpenOpportunities(ctx context.Context, userID, accountID string) ([]AccountPackOpportunity, error) {
	if !customerUpdateAccountIDPattern.MatchString(accountID) {
		return nil, ErrInvalidInput
	}
	if err := c.deps.Members.RequireDirectMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	projects, err := c.deps.Projects.ListAccountProjects(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	return packOpportunities(projects, 0), nil
}

// packOpportunities maps open projects in projectId order; limit 0 is no cap.
func packOpportunities(projects []model.ProjectSummary, limit int) []AccountPackOpportunity {
	open := openOpportunities(projects)
	sort.Slice(open, func(i, j int) bool { return open[i].ProjectID < open[j].ProjectID })
	out := []AccountPackOpportunity{}
	for _, p := range open {
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, AccountPackOpportunity{
			ProjectID: p.ProjectID, Name: capText(p.Name, maxPackNameRunes), Stage: capText(p.Stage, maxPackNameRunes),
		})
	}
	return out
}

// MeetingNotesReader is the notes-only meeting read for context packs. It
// authorizes with MeetingService.checkAccess over the repository's metadata
// view (owner, direct share, or live account-team grant with the account
// read projection: the same decision the bounded reading endpoint's notes
// section makes) and performs no writes and no transcript, attachment,
// share-list or action-status reads. MeetingReadingService.Read is not used
// because its notes path also runs ActionItemsAnalysisService.Describe, which
// can write an interrupted state for an expired action-analysis lease.
type MeetingNotesReader struct {
	access func(ctx context.Context, userID, meetingID string) (*model.Meeting, string, error)
}

// NewMeetingNotesReader builds the reader over repo's metadata view.
func NewMeetingNotesReader(repo *repository.DynamoDBRepository) *MeetingNotesReader {
	return &MeetingNotesReader{access: NewMeetingService(repo.MetadataView()).checkAccess}
}

// MeetingNotes returns the meeting's saved notes; no access is ErrNotFound.
func (r *MeetingNotesReader) MeetingNotes(ctx context.Context, userID, meetingID string) (string, error) {
	if userID == "" || !readingID.MatchString(meetingID) {
		return "", ErrNotFound
	}
	meeting, _, err := r.access(ctx, userID, meetingID)
	if err != nil {
		return "", err
	}
	if meeting == nil {
		return "", ErrNotFound
	}
	return meeting.Notes, nil
}

func accountPackSize(p AccountPack) int {
	b, err := json.Marshal(p)
	if err != nil {
		return maxAccountPackBytes + 1
	}
	return len(b)
}

// fitAccountPack shrinks a pack to at most 6,000 bytes of JSON, dropping from
// the tail: notes, then insights, then news, then block-list entries, then
// opportunities down to one. It reports false when even that cannot fit.
func fitAccountPack(p AccountPack) (AccountPack, bool) {
	fits := func() bool { return accountPackSize(p) <= maxAccountPackBytes }
	for !fits() && len(p.Notes) > 0 {
		p.Notes = p.Notes[:len(p.Notes)-1]
	}
	for !fits() && len(p.Insights) > 0 {
		p.Insights = p.Insights[:len(p.Insights)-1]
	}
	for !fits() && len(p.News) > 0 {
		p.News = p.News[:len(p.News)-1]
	}
	for !fits() && len(p.BlockList) > 0 {
		p.BlockList = p.BlockList[:len(p.BlockList)-1]
	}
	for !fits() && len(p.Opportunities) > 1 {
		p.Opportunities = p.Opportunities[:len(p.Opportunities)-1]
	}
	return p, fits()
}
