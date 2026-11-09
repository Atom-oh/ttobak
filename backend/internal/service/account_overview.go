package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	signalsNewsWindow       = 30 * 24 * time.Hour
	maxSignalsNews          = 10
	maxSignalsOpportunities = 20
	maxSignalsInsights      = 10
	maxSignalsLinkedIDs     = 20
	maxSignalsEntities      = 10
	maxSignalsTextRunes     = 280
	maxSignalsURLBytes      = 2048
	// MaxAccountSignalsBytes bounds the encoded signals response.
	MaxAccountSignalsBytes = 64 * 1024
	// maxOverviewLinkRows bounds each per-endpoint link read. Hitting it keeps
	// the rows read and sets AccountSignals.LinksTruncated.
	maxOverviewLinkRows = 200
	// maxOverviewMeetingsPerProject bounds meeting reads per project (newest first).
	maxOverviewMeetingsPerProject = 100
)

// signalInsightTypes are the insight types shown in the signals section.
var signalInsightTypes = []string{"need", "opportunity", "risk"}

type accountOverviewRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	ListProjectRefsForAccount(ctx context.Context, accountID string) ([]model.ProjectRef, error)
	GetProject(ctx context.Context, projectID string) (*model.Project, error)
	ListProjectMeetingRefsForProject(ctx context.Context, projectID string) ([]model.ProjectMeetingRef, error)
	GetMeetingActionState(ctx context.Context, ownerID, meetingID string) (*model.Meeting, error)
	GetAccountEvent(ctx context.Context, account, id string) (*model.AccountEvent, string, error)
	ListAccountLinksByPrefix(ctx context.Context, accountID, prefix string, maxRows int) ([]model.AccountLink, bool, error)
	ListProjectStageHistory(ctx context.Context, projectID string, limit int) ([]model.ProjectStageChange, error)
	ListAccountEvents(ctx context.Context, account, fromKey, toKey string, descending bool, limit int, afterSK string) ([]model.AccountEvent, string, error)
	ListMeetingRefsForAccountPage(ctx context.Context, accountID, cursor string, limit int32) ([]model.MeetingRef, *string, error)
	ListAccountLinks(ctx context.Context, accountID, cursor string, limit int32) ([]model.AccountLink, *string, error)
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	GetAccountContact(ctx context.Context, account, id string) (*model.AccountContact, error)
}

// accountOverviewNews is implemented by AccountNewsService.
type accountOverviewNews interface {
	accountSources(ctx context.Context, accountID string) ([]model.CrawlerSource, error)
	CurrentNews(ctx context.Context, accountID string, from, to time.Time) ([]model.CrawledDocument, error)
}

// accountOverviewInsights is implemented by AccountService.
type accountOverviewInsights interface {
	ListAccountInsightsPage(ctx context.Context, userID, accountID string, from, to time.Time, types []string) (*model.AccountInsightsPage, error)
}

// AccountOverviewService builds the account page's derived views (signals).
// Every call checks direct account membership first; nothing is cached
// across calls.
type AccountOverviewService struct {
	repo     accountOverviewRepo
	docs     accountLinkDocuments
	news     accountOverviewNews
	insights accountOverviewInsights
}

func NewAccountOverviewService(repo accountOverviewRepo, docs accountLinkDocuments, news *AccountNewsService, accounts *AccountService) *AccountOverviewService {
	return &AccountOverviewService{repo: repo, docs: docs, news: news, insights: accounts}
}

func (s *AccountOverviewService) authorize(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	_, member, err := s.authorizeAccount(ctx, accountID, userID)
	return member, err
}

func (s *AccountOverviewService) authorizeAccount(ctx context.Context, accountID, userID string) (*model.Account, *model.AccountMember, error) {
	if validateAccountHierarchyIDs(accountID, "") != nil || validateAccountHierarchyIDs(userID, "") != nil {
		return nil, nil, ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if account == nil {
		return nil, nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, nil, err
	}
	if member == nil {
		return nil, nil, ErrForbidden
	}
	return account, member, nil
}

// overviewReads caches per-call reads and link revalidation; it never
// outlives one request.
type overviewReads struct {
	meetings      map[string]*model.Meeting
	events        map[string]*model.AccountEvent
	projects      map[string]bool                // project ID -> still lists the account
	projectEvents map[string][]model.AccountLink // project ID -> event→project links
	truncated     bool                           // a per-endpoint link read hit its bound
}

func newOverviewReads() *overviewReads {
	return &overviewReads{meetings: map[string]*model.Meeting{}, events: map[string]*model.AccountEvent{}, projects: map[string]bool{},
		projectEvents: map[string][]model.AccountLink{}}
}

// projectInAccount reports whether the project still lists the account.
func (s *AccountOverviewService) projectInAccount(ctx context.Context, accountID, projectID string, reads *overviewReads) (bool, error) {
	if ok, seen := reads.projects[projectID]; seen {
		return ok, nil
	}
	project, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return false, err
	}
	ok := project != nil && project.ProjectID == projectID && contains(project.AccountIDs, accountID)
	reads.projects[projectID] = ok
	return ok, nil
}

// endpointLinks reads the link rows under one endpoint prefix, keeping only
// well-formed rows. A hit bound is recorded on reads, never an error.
func (s *AccountOverviewService) endpointLinks(ctx context.Context, accountID, prefix string, reads *overviewReads) ([]model.AccountLink, error) {
	return s.endpointLinksN(ctx, accountID, prefix, maxOverviewLinkRows, reads)
}

func (s *AccountOverviewService) endpointLinksN(ctx context.Context, accountID, prefix string, maxRows int, reads *overviewReads) ([]model.AccountLink, error) {
	rows, truncated, err := s.repo.ListAccountLinksByPrefix(ctx, accountID, prefix, maxRows)
	if err != nil {
		return nil, err
	}
	if truncated {
		reads.truncated = true
	}
	out := make([]model.AccountLink, 0, len(rows))
	for _, link := range rows {
		if validateLinkShape(link.Input()) == nil {
			out = append(out, link)
		}
	}
	return out, nil
}

// newsProjects returns the projects a news item is linked to, revalidating
// that each project still lists the account. The news itself comes from the
// account's current sources.
func (s *AccountOverviewService) newsProjects(ctx context.Context, accountID, newsID string, reads *overviewReads) ([]string, error) {
	links, err := s.endpointLinks(ctx, accountID, model.PrefixAccountLink+model.LinkKindNews+"#"+newsID+"#"+model.LinkKindProject+"#", reads)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, link := range links {
		if link.FromKind != model.LinkKindNews || link.FromID != newsID || link.ToKind != model.LinkKindProject || contains(out, link.ToID) {
			continue
		}
		ok, err := s.projectInAccount(ctx, accountID, link.ToID, reads)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, link.ToID)
		}
	}
	return out, nil
}

// projectEventLinks returns the distinct event→project links of a project,
// read once per call. Events are revalidated by their readers.
func (s *AccountOverviewService) projectEventLinks(ctx context.Context, accountID, projectID string, reads *overviewReads) ([]model.AccountLink, error) {
	if links, ok := reads.projectEvents[projectID]; ok {
		return links, nil
	}
	links, err := s.endpointLinks(ctx, accountID, model.PrefixAccountLinkTo+model.LinkKindProject+"#"+projectID+"#"+model.LinkKindEvent+"#", reads)
	if err != nil {
		return nil, err
	}
	out := []model.AccountLink{}
	seen := map[string]bool{}
	for _, link := range links {
		if link.FromKind == model.LinkKindEvent && link.ToKind == model.LinkKindProject && link.ToID == projectID && !seen[link.FromID] {
			seen[link.FromID] = true
			out = append(out, link)
		}
	}
	reads.projectEvents[projectID] = out
	return out, nil
}

// projectEvents returns the event IDs linked to a project (event→project).
func (s *AccountOverviewService) projectEvents(ctx context.Context, accountID, projectID string, reads *overviewReads) ([]string, error) {
	links, err := s.projectEventLinks(ctx, accountID, projectID, reads)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(links))
	for _, link := range links {
		out = append(out, link.FromID)
	}
	return out, nil
}

// capRunes truncates s to at most n Unicode code points.
func capRunes(s string, n int) string {
	if !utf8.ValidString(s) {
		s = string([]rune(s))
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// signalURL keeps only absolute http(s) URLs within the byte bound; a
// truncated URL would point elsewhere, so it is dropped instead.
func signalURL(raw string) string {
	if raw == "" || len(raw) > maxSignalsURLBytes || !utf8.ValidString(raw) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return raw
}

// StallReasons returns the stall signals for a project. Terminal stages never
// stall. openActionAges are the ages of open action items, each measured from
// its meeting's date (ActionItem has no creation time).
func StallReasons(p model.Project, openActionAges []time.Duration, hasUpcomingEvent bool, now time.Time) []model.StallReason {
	stage, _ := model.NormalizeProjectStage(p.Stage)
	if model.IsTerminalStage(stage) {
		return []model.StallReason{}
	}
	out := []model.StallReason{}
	if p.StageChangedAt != nil {
		if age := now.Sub(*p.StageChangedAt); age >= model.StallStageAfter {
			out = append(out, model.StallReason{Code: model.StallStageStale, Detail: fmt.Sprintf("stage unchanged for %d days", int(age/(24*time.Hour)))})
		}
	}
	old := 0
	for _, age := range openActionAges {
		if age >= model.StallActionAfter {
			old++
		}
	}
	if old > 0 {
		out = append(out, model.StallReason{Code: model.StallOpenAction, Detail: fmt.Sprintf("%d open action items older than 14 days", old)})
	}
	if !hasUpcomingEvent {
		out = append(out, model.StallReason{Code: model.StallNoUpcoming, Detail: "no upcoming account event linked to this project"})
	}
	return out
}

// Signals returns the account's AWS signals: recent news of its own news
// sources, its linked projects with stall signals, and recent need,
// opportunity and risk insights.
func (s *AccountOverviewService) Signals(ctx context.Context, userID, accountID string, now time.Time) (*model.AccountSignals, error) {
	member, err := s.authorize(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	reads := newOverviewReads()
	out := &model.AccountSignals{News: []model.SignalNews{}, Opportunities: []model.SignalOppty{}, Insights: []model.AccountInsightDTO{}}
	if out.NewsState, out.News, err = s.signalNews(ctx, accountID, now, reads); err != nil {
		return nil, err
	}
	if out.Opportunities, err = s.signalOpportunities(ctx, userID, accountID, member, now, reads); err != nil {
		return nil, err
	}
	page, err := s.insights.ListAccountInsightsPage(ctx, userID, accountID, time.Time{}, time.Time{}, signalInsightTypes)
	if err != nil {
		return nil, err
	}
	out.Insights, out.RestrictedCount = signalInsights(page.Insights), page.RestrictedCount
	out.LinksTruncated = reads.truncated
	if err := fitSignals(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *AccountOverviewService) signalNews(ctx context.Context, accountID string, now time.Time, reads *overviewReads) (string, []model.SignalNews, error) {
	sources, err := s.news.accountSources(ctx, accountID)
	if err != nil {
		return "", nil, err
	}
	if len(sources) == 0 {
		return model.NewsStateNoSource, []model.SignalNews{}, nil
	}
	docs, err := s.news.CurrentNews(ctx, accountID, now.Add(-signalsNewsWindow), now)
	if err != nil {
		return "", nil, err
	}
	kept := make([]model.CrawledDocument, 0, len(docs))
	for _, doc := range docs {
		if doc.QualityStatus == "DELETED" || doc.SourceID == "" || doc.DocHash == "" {
			continue
		}
		kept = append(kept, doc)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].QualityScore != kept[j].QualityScore {
			return kept[i].QualityScore > kept[j].QualityScore
		}
		return kept[i].CrawledAt > kept[j].CrawledAt
	})
	if len(kept) > maxSignalsNews {
		kept = kept[:maxSignalsNews]
	}
	news := make([]model.SignalNews, 0, len(kept))
	for _, doc := range kept {
		id := doc.SourceID + ":" + doc.DocHash
		linked, err := s.newsProjects(ctx, accountID, id, reads)
		if err != nil {
			return "", nil, err
		}
		sort.Strings(linked)
		if len(linked) > maxSignalsLinkedIDs {
			linked = linked[:maxSignalsLinkedIDs]
		}
		news = append(news, model.SignalNews{
			ID: id, Title: capRunes(doc.Title, maxSignalsTextRunes), URL: signalURL(doc.URL),
			PubDate: capRunes(doc.PubDate, maxSignalsTextRunes), AWSRelevance: capRunes(doc.AWSRelevance, maxSignalsTextRunes),
			QualityScore: doc.QualityScore, LinkedProjectIDs: linked,
			Candidate: model.IsNewOpportunityCandidate(doc, len(linked) > 0),
		})
	}
	if len(news) == 0 {
		return model.NewsStateNoRecentNews, news, nil
	}
	return model.NewsStateOK, news, nil
}

func (s *AccountOverviewService) signalOpportunities(ctx context.Context, userID, accountID string, member *model.AccountMember, now time.Time, reads *overviewReads) ([]model.SignalOppty, error) {
	refs, err := s.repo.ListProjectRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []model.SignalOppty{}
	for _, ref := range refs {
		if len(out) == maxSignalsOpportunities {
			break
		}
		if seen[ref.ProjectID] || !validLinkPlainID(ref.ProjectID) {
			continue
		}
		seen[ref.ProjectID] = true
		project, err := s.repo.GetProject(ctx, ref.ProjectID)
		if err != nil {
			return nil, err
		}
		// The ref is only a reverse index; the project must still list the account.
		if project == nil || project.ProjectID != ref.ProjectID || !contains(project.AccountIDs, accountID) {
			continue
		}
		item, err := s.opptyItem(ctx, userID, accountID, member, project, now, reads)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// opptyItem builds a project's opportunity signal, including stall reasons
// for nonterminal stages. The caller has checked the project lists the account.
func (s *AccountOverviewService) opptyItem(ctx context.Context, userID, accountID string, member *model.AccountMember, project *model.Project, now time.Time, reads *overviewReads) (model.SignalOppty, error) {
	stage, mapped := model.NormalizeProjectStage(project.Stage)
	if !mapped {
		stage = project.Stage
	}
	item := model.SignalOppty{ProjectID: project.ProjectID, Name: capRunes(project.Name, maxSignalsTextRunes),
		Stage: capRunes(stage, maxSignalsTextRunes), StageMapped: mapped, Terminal: mapped && model.IsTerminalStage(stage), Stall: []model.StallReason{}}
	if project.StageChangedAt != nil {
		days := max(int(now.Sub(*project.StageChangedAt)/(24*time.Hour)), 0)
		item.DaysInStage = &days
	}
	if item.Terminal {
		return item, nil
	}
	ages, err := s.openActionAges(ctx, userID, accountID, member, project.ProjectID, now, reads)
	if err != nil {
		return item, err
	}
	events, err := s.projectEvents(ctx, accountID, project.ProjectID, reads)
	if err != nil {
		return item, err
	}
	upcoming, err := s.hasUpcomingEvent(ctx, accountID, events, now, reads)
	if err != nil {
		return item, err
	}
	item.Stall = StallReasons(*project, ages, upcoming, now)
	return item, nil
}

// openActionAges returns the age of every open action item in meetings that
// list the project and are currently published to the account.
func (s *AccountOverviewService) openActionAges(ctx context.Context, userID, accountID string, member *model.AccountMember, projectID string, now time.Time, reads *overviewReads) ([]time.Duration, error) {
	refs, err := s.repo.ListProjectMeetingRefsForProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	ages := []time.Duration{}
	done := map[string]bool{}
	for _, ref := range refs {
		if len(done) == maxOverviewMeetingsPerProject {
			break
		}
		if !validLinkPlainID(ref.OwnerUserID) || !validLinkPlainID(ref.MeetingID) {
			continue
		}
		key := ref.OwnerUserID + ":" + ref.MeetingID
		if done[key] {
			continue
		}
		done[key] = true
		meeting, err := s.publishedMeeting(ctx, accountID, ref.OwnerUserID, ref.MeetingID, reads)
		if err != nil {
			return nil, err
		}
		if meeting == nil || !contains(meeting.ProjectIDs, projectID) || meeting.Date.IsZero() {
			continue
		}
		// Action items are derived summary content: honour the same reader
		// guard as account insights.
		if userID != meeting.UserID && accountMemberNeedsSummaryGuard(member) && !meeting.AccountSummaryMetadataShareable() {
			continue
		}
		items, err := storedActionItems(meeting.ActionItems)
		if err != nil {
			continue // malformed saved items are unknown, not open
		}
		for _, item := range items {
			if !item.Completed {
				ages = append(ages, now.Sub(meeting.Date))
			}
		}
	}
	return ages, nil
}

// publishedMeeting returns the meeting's metadata view when it is currently
// published to the account, else nil. Reads are cached per call.
func (s *AccountOverviewService) publishedMeeting(ctx context.Context, accountID, ownerID, meetingID string, reads *overviewReads) (*model.Meeting, error) {
	key := ownerID + ":" + meetingID
	if meeting, cached := reads.meetings[key]; cached {
		return meeting, nil
	}
	meeting, err := s.repo.GetMeetingActionState(ctx, ownerID, meetingID)
	if err != nil {
		return nil, err
	}
	if !meetingPublishedToAccount(meeting, accountID, ownerID, meetingID) {
		meeting = nil
	}
	reads.meetings[key] = meeting
	return meeting, nil
}

// accountEvent returns the account event by ID, or nil when it no longer
// exists. Reads are cached per call.
func (s *AccountOverviewService) accountEvent(ctx context.Context, accountID, id string, reads *overviewReads) (*model.AccountEvent, error) {
	if event, cached := reads.events[id]; cached {
		return event, nil
	}
	event, _, err := s.repo.GetAccountEvent(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	if event != nil && event.EventID != id {
		event = nil
	}
	reads.events[id] = event
	return event, nil
}

// hasUpcomingEvent reports whether any linked account event starts after now.
// Per-user calendar entries are deliberately excluded (ADR-064).
func (s *AccountOverviewService) hasUpcomingEvent(ctx context.Context, accountID string, eventIDs []string, now time.Time, reads *overviewReads) (bool, error) {
	for _, id := range eventIDs {
		event, err := s.accountEvent(ctx, accountID, id, reads)
		if err != nil {
			return false, err
		}
		if event == nil {
			continue
		}
		start, err := time.Parse(time.RFC3339, event.StartAt)
		if err == nil && start.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func signalInsights(in []model.AccountInsightDTO) []model.AccountInsightDTO {
	items := append([]model.AccountInsightDTO{}, in...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].OccurredAt.After(items[j].OccurredAt) })
	if len(items) > maxSignalsInsights {
		items = items[:maxSignalsInsights]
	}
	for i := range items {
		it := &items[i]
		it.Text = capRunes(it.Text, maxSignalsTextRunes)
		it.Implication = capRunes(it.Implication, maxSignalsTextRunes)
		it.NextAction = capRunes(it.NextAction, maxSignalsTextRunes)
		it.TsMarker = capRunes(it.TsMarker, maxSignalsTextRunes)
		it.SourceID = capRunes(it.SourceID, maxSignalsTextRunes)
		if len(it.Entities) > maxSignalsEntities {
			it.Entities = it.Entities[:maxSignalsEntities]
		}
		entities := make([]string, len(it.Entities))
		for j, e := range it.Entities {
			entities[j] = capRunes(e, maxSignalsTextRunes)
		}
		if len(entities) > 0 {
			it.Entities = entities
		}
	}
	return items
}

// fitSignals drops trailing items from the longest list until the encoded
// response fits MaxAccountSignalsBytes.
func fitSignals(sig *model.AccountSignals) error {
	for {
		raw, err := json.Marshal(sig)
		if err != nil {
			return err
		}
		if len(raw) <= MaxAccountSignalsBytes {
			return nil
		}
		n, o, i := len(sig.News), len(sig.Opportunities), len(sig.Insights)
		switch {
		case n == 0 && o == 0 && i == 0:
			return fmt.Errorf("account signals exceed the response bound")
		case n >= o && n >= i:
			sig.News = sig.News[:n-1]
		case o >= i:
			sig.Opportunities = sig.Opportunities[:o-1]
		default:
			sig.Insights = sig.Insights[:i-1]
		}
	}
}

const (
	maxFlowRows        = 20
	maxFlowColumnItems = 5
	maxFlowHistory     = 10
	// maxFlowProjectRefs bounds project reads; more refs set Truncated.
	maxFlowProjectRefs = 100
	// maxFlowEventsPerProject bounds event reads per project row.
	maxFlowEventsPerProject = 20
	// maxFlowUnassignedReads bounds the newest account events and meeting
	// refs examined for the unassigned row.
	maxFlowUnassignedReads = 50
	// maxFlowEventLinkRows bounds each LINK#event#{id}#project# read.
	maxFlowEventLinkRows = 20
	// MaxAccountFlowBytes bounds the encoded flow response.
	MaxAccountFlowBytes = 64 * 1024

	flowEventKeyLow  = "0000-01-01T00:00:00Z"
	flowEventKeyHigh = "9999-12-31T23:59:59Z"
)

// Flow returns the account's engagement flow: one row per project that still
// lists the account, plus an unassigned row. Items are assigned only through
// stored links (event→project links, meeting projectIds); nothing is guessed.
func (s *AccountOverviewService) Flow(ctx context.Context, userID, accountID string, now time.Time) (*model.AccountFlow, error) {
	member, err := s.authorize(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	reads := newOverviewReads()
	out := &model.AccountFlow{Rows: []model.FlowRow{}}
	projects, truncated, err := s.flowProjects(ctx, accountID, reads)
	if err != nil {
		return nil, err
	}
	needs, err := s.flowNeeds(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		row, err := s.flowRow(ctx, userID, accountID, member, project, needs, now, reads)
		if err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, row)
	}
	unassigned, unassignedTruncated, err := s.flowUnassigned(ctx, accountID, needs, reads)
	if err != nil {
		return nil, err
	}
	out.Unassigned = unassigned
	out.Truncated = truncated || unassignedTruncated || reads.truncated
	if err := fitFlow(out); err != nil {
		return nil, err
	}
	return out, nil
}

func flowTerminal(p *model.Project) bool {
	stage, mapped := model.NormalizeProjectStage(p.Stage)
	return mapped && model.IsTerminalStage(stage)
}

// flowProjects returns at most maxFlowRows projects that still list the
// account: nonterminal first, then by stage change time, newest first.
func (s *AccountOverviewService) flowProjects(ctx context.Context, accountID string, reads *overviewReads) ([]*model.Project, bool, error) {
	refs, err := s.repo.ListProjectRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, false, err
	}
	truncated := false
	seen := map[string]bool{}
	projects := []*model.Project{}
	for _, ref := range refs {
		if seen[ref.ProjectID] || !validLinkPlainID(ref.ProjectID) {
			continue
		}
		if len(seen) == maxFlowProjectRefs {
			truncated = true
			break
		}
		seen[ref.ProjectID] = true
		project, err := s.repo.GetProject(ctx, ref.ProjectID)
		if err != nil {
			return nil, false, err
		}
		ok := project != nil && project.ProjectID == ref.ProjectID && contains(project.AccountIDs, accountID)
		reads.projects[ref.ProjectID] = ok
		if ok {
			projects = append(projects, project)
		}
	}
	sort.SliceStable(projects, func(i, j int) bool {
		a, b := projects[i], projects[j]
		if ta, tb := flowTerminal(a), flowTerminal(b); ta != tb {
			return !ta
		}
		if (a.StageChangedAt == nil) != (b.StageChangedAt == nil) {
			return a.StageChangedAt != nil
		}
		if a.StageChangedAt != nil && !a.StageChangedAt.Equal(*b.StageChangedAt) {
			return a.StageChangedAt.After(*b.StageChangedAt)
		}
		return a.ProjectID < b.ProjectID
	})
	if len(projects) > maxFlowRows {
		projects, truncated = projects[:maxFlowRows], true
	}
	return projects, truncated, nil
}

// flowNeeds maps meeting IDs to their newest readable need-type insight text.
// The insights read applies the account publication and privacy guards.
func (s *AccountOverviewService) flowNeeds(ctx context.Context, userID, accountID string) (map[string]string, error) {
	page, err := s.insights.ListAccountInsightsPage(ctx, userID, accountID, time.Time{}, time.Time{}, []string{"need"})
	if err != nil {
		return nil, err
	}
	newest := map[string]time.Time{}
	out := map[string]string{}
	for _, ins := range page.Insights {
		if ins.Type != "need" || ins.SourceType != "meeting" || ins.SourceID == "" || ins.Text == "" {
			continue
		}
		if at, ok := newest[ins.SourceID]; ok && !ins.OccurredAt.After(at) {
			continue
		}
		newest[ins.SourceID] = ins.OccurredAt
		out[ins.SourceID] = capRunes(ins.Text, maxSignalsTextRunes)
	}
	return out, nil
}

func flowEventItem(e *model.AccountEvent, link *model.AccountLink) model.FlowItem {
	item := model.FlowItem{Kind: model.FlowItemEvent, ID: e.EventID, Title: capRunes(e.Title, maxSignalsTextRunes), Date: capRunes(e.StartAt, maxSignalsTextRunes)}
	if link != nil {
		item.Origin = link.Origin
		item.Evidence = capRunes(link.Evidence, maxSignalsTextRunes)
	}
	return item
}

func flowMeetingItem(m *model.Meeting, fallbackTitle string, needs map[string]string) model.FlowItem {
	title := m.Title
	if title == "" {
		title = fallbackTitle
	}
	item := model.FlowItem{Kind: model.FlowItemMeeting, ID: m.UserID + ":" + m.MeetingID, Title: capRunes(title, maxSignalsTextRunes), Need: needs[m.MeetingID]}
	if !m.Date.IsZero() {
		item.Date = m.Date.UTC().Format(time.RFC3339)
	}
	return item
}

// newestEarliestFirst keeps the newest n items and returns them earliest first.
func newestEarliestFirst(items []model.FlowItem, n int) []model.FlowItem {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Date > items[j].Date })
	if len(items) > n {
		items = items[:n]
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items
}

func (s *AccountOverviewService) flowRow(ctx context.Context, userID, accountID string, member *model.AccountMember, project *model.Project, needs map[string]string, now time.Time, reads *overviewReads) (model.FlowRow, error) {
	row := model.FlowRow{ProjectID: project.ProjectID, Activity: []model.FlowItem{}, Response: []model.FlowItem{}, History: []model.ProjectStageChange{}}
	oppty, err := s.opptyItem(ctx, userID, accountID, member, project, now, reads)
	if err != nil {
		return row, err
	}
	row.Oppty = &oppty

	links, err := s.projectEventLinks(ctx, accountID, project.ProjectID, reads)
	if err != nil {
		return row, err
	}
	if len(links) > maxFlowEventsPerProject {
		links, reads.truncated = links[:maxFlowEventsPerProject], true
	}
	for i := range links {
		event, err := s.accountEvent(ctx, accountID, links[i].FromID, reads)
		if err != nil {
			return row, err
		}
		if event != nil {
			row.Activity = append(row.Activity, flowEventItem(event, &links[i]))
		}
	}
	row.Activity = newestEarliestFirst(row.Activity, maxFlowColumnItems)

	refs, err := s.repo.ListProjectMeetingRefsForProject(ctx, project.ProjectID)
	if err != nil {
		return row, err
	}
	done := map[string]bool{}
	for _, ref := range refs {
		if len(row.Response) == maxFlowColumnItems {
			break
		}
		if len(done) == maxOverviewMeetingsPerProject {
			reads.truncated = true
			break
		}
		if !validLinkPlainID(ref.OwnerUserID) || !validLinkPlainID(ref.MeetingID) || done[ref.OwnerUserID+":"+ref.MeetingID] {
			continue
		}
		done[ref.OwnerUserID+":"+ref.MeetingID] = true
		meeting, err := s.publishedMeeting(ctx, accountID, ref.OwnerUserID, ref.MeetingID, reads)
		if err != nil {
			return row, err
		}
		if meeting != nil && contains(meeting.ProjectIDs, project.ProjectID) {
			row.Response = append(row.Response, flowMeetingItem(meeting, ref.Title, needs))
		}
	}
	row.Response = newestEarliestFirst(row.Response, maxFlowColumnItems)

	history, err := s.repo.ListProjectStageHistory(ctx, project.ProjectID, maxFlowHistory)
	if err != nil {
		return row, err
	}
	for _, h := range history {
		if len(row.History) == maxFlowHistory {
			break
		}
		if h.PK != model.PrefixProject+project.ProjectID || !strings.HasPrefix(h.SK, model.PrefixProjectStageHistory) ||
			h.ProjectID != project.ProjectID || h.EntityType != model.EntityTypeProjectStageChange {
			continue
		}
		h.From, h.To, h.ChangedBy = capRunes(h.From, maxSignalsTextRunes), capRunes(h.To, maxSignalsTextRunes), capRunes(h.ChangedBy, maxSignalsTextRunes)
		row.History = append(row.History, h)
	}

	row.Result = model.FlowResult{Stall: oppty.Stall}
	switch {
	case oppty.Terminal:
		row.Result.State = model.FlowStateTerminal
	case !oppty.StageMapped || (project.StageChangedAt == nil && len(row.History) == 0):
		row.Result.State = model.FlowStateUnknown
	case len(oppty.Stall) > 0:
		row.Result.State = model.FlowStateStalled
	default:
		row.Result.State = model.FlowStateActive
	}
	return row, nil
}

// flowUnassigned holds the newest account events with no link to a project
// still in the account, and the newest published meetings listing no such
// project. Each column holds at most maxFlowColumnItems, newest first.
func (s *AccountOverviewService) flowUnassigned(ctx context.Context, accountID string, needs map[string]string, reads *overviewReads) (model.FlowRow, bool, error) {
	row := model.FlowRow{Activity: []model.FlowItem{}, Response: []model.FlowItem{}, History: []model.ProjectStageChange{},
		Result: model.FlowResult{State: model.FlowStateUnknown, Stall: []model.StallReason{}}}
	truncated := false

	events, next, err := s.repo.ListAccountEvents(ctx, accountID, flowEventKeyLow, flowEventKeyHigh, true, maxFlowUnassignedReads, "")
	if err != nil {
		return row, false, err
	}
	for i := range events {
		if len(row.Activity) == maxFlowColumnItems {
			break
		}
		event := &events[i]
		if !validContactID(event.EventID) {
			continue
		}
		reads.events[event.EventID] = event
		links, err := s.endpointLinksN(ctx, accountID, model.PrefixAccountLink+model.LinkKindEvent+"#"+event.EventID+"#"+model.LinkKindProject+"#", maxFlowEventLinkRows, reads)
		if err != nil {
			return row, false, err
		}
		linked := false
		for _, link := range links {
			if link.FromKind != model.LinkKindEvent || link.FromID != event.EventID || link.ToKind != model.LinkKindProject {
				continue
			}
			if linked, err = s.projectInAccount(ctx, accountID, link.ToID, reads); err != nil {
				return row, false, err
			}
			if linked {
				break
			}
		}
		if !linked {
			row.Activity = append(row.Activity, flowEventItem(event, nil))
		}
	}
	if len(row.Activity) < maxFlowColumnItems && next != "" {
		truncated = true
	}

	refs, cursor, err := s.repo.ListMeetingRefsForAccountPage(ctx, accountID, "", maxFlowUnassignedReads)
	if err != nil {
		return row, false, err
	}
	for _, ref := range refs {
		if len(row.Response) == maxFlowColumnItems {
			break
		}
		if ref.AccountID != accountID || ref.PK != model.PrefixAccount+accountID || ref.EntityType != model.EntityTypeMeetingRef ||
			ref.SK != model.PrefixMeetingRef+ref.Date.UTC().Format(time.RFC3339)+"#"+ref.MeetingID ||
			!validLinkPlainID(ref.OwnerUserID) || !validLinkPlainID(ref.MeetingID) {
			continue
		}
		meeting, err := s.publishedMeeting(ctx, accountID, ref.OwnerUserID, ref.MeetingID, reads)
		if err != nil {
			return row, false, err
		}
		if meeting == nil {
			continue
		}
		assigned := false
		for _, projectID := range meeting.ProjectIDs {
			if !validLinkPlainID(projectID) {
				continue
			}
			if assigned, err = s.projectInAccount(ctx, accountID, projectID, reads); err != nil {
				return row, false, err
			}
			if assigned {
				break
			}
		}
		if !assigned {
			row.Response = append(row.Response, flowMeetingItem(meeting, ref.Title, needs))
		}
	}
	if len(row.Response) < maxFlowColumnItems && cursor != nil {
		truncated = true
	}
	return row, truncated, nil
}

// fitFlow drops trailing items from the longest column until the encoded
// response fits MaxAccountFlowBytes, then trailing rows; any trim sets
// Truncated.
func fitFlow(flow *model.AccountFlow) error {
	for {
		raw, err := json.Marshal(flow)
		if err != nil {
			return err
		}
		if len(raw) <= MaxAccountFlowBytes {
			return nil
		}
		flow.Truncated = true
		var longest *[]model.FlowItem
		var history *[]model.ProjectStageChange
		size := 0
		rows := make([]*model.FlowRow, 0, len(flow.Rows)+1)
		for i := range flow.Rows {
			rows = append(rows, &flow.Rows[i])
		}
		rows = append(rows, &flow.Unassigned)
		for _, row := range rows {
			for _, col := range []*[]model.FlowItem{&row.Activity, &row.Response} {
				if len(*col) > size {
					longest, history, size = col, nil, len(*col)
				}
			}
			if len(row.History) > size {
				longest, history, size = nil, &row.History, len(row.History)
			}
		}
		switch {
		case history != nil:
			*history = (*history)[:size-1]
		case longest != nil:
			*longest = (*longest)[:size-1]
		case len(flow.Rows) > 0:
			flow.Rows = flow.Rows[:len(flow.Rows)-1]
		default:
			return fmt.Errorf("account flow exceeds the response bound")
		}
	}
}

const (
	maxGraphNodes      = 150
	maxGraphEdges      = 300
	maxGraphLabelRunes = 60
	// maxGraphEvents bounds the newest account events read as nodes.
	maxGraphEvents = 50
	// maxGraphMeetingRefs bounds the newest account meeting refs read.
	maxGraphMeetingRefs = 100
	// maxGraphLinkRows bounds the LINK# rows read; more rows set Truncated.
	maxGraphLinkRows  = 1000
	graphLinkPageSize = 100
	// maxGraphLinkLookups bounds the distinct uncached endpoint reads (project,
	// meeting publication, event, document) that link rows may issue; rows
	// past it are skipped and set Truncated.
	maxGraphLinkLookups = 200
	// MaxAccountGraphBytes bounds the encoded graph response.
	MaxAccountGraphBytes = 64 * 1024
)

// graphBuilder accumulates nodes and edges in insertion order under the node
// and edge caps. A rejected addition sets truncated.
type graphBuilder struct {
	g        *model.AccountGraph
	nodes    map[string]bool
	edges    map[string]bool
	projects map[string]*model.Project // revalidated projects, nil when not in the account
	contacts map[string]*model.AccountContact
	docs     map[string]*model.CrawledDocument
	sources  map[string]bool
	lookups  int // uncached endpoint reads spent by link rows
}

func graphNodeID(kind, id string) string { return kind + ":" + id }

// node adds a node; it reports whether the node is present afterwards.
func (b *graphBuilder) node(kind, id, label string) bool {
	nodeID := graphNodeID(kind, id)
	if b.nodes[nodeID] {
		return true
	}
	if len(b.g.Nodes) >= maxGraphNodes {
		b.g.Truncated = true
		return false
	}
	b.nodes[nodeID] = true
	b.g.Nodes = append(b.g.Nodes, model.GraphNode{ID: nodeID, Kind: kind, Label: capRunes(label, maxGraphLabelRunes)})
	return true
}

func (b *graphBuilder) hasNode(kind, id string) bool { return b.nodes[graphNodeID(kind, id)] }

// edge adds an edge between two present nodes.
func (b *graphBuilder) edge(from, to, kind, origin string) {
	if !b.nodes[from] || !b.nodes[to] {
		return
	}
	key := from + "\x00" + to + "\x00" + kind
	if b.edges[key] {
		return
	}
	if len(b.g.Edges) >= maxGraphEdges {
		b.g.Truncated = true
		return
	}
	b.edges[key] = true
	b.g.Edges = append(b.g.Edges, model.GraphEdge{From: from, To: to, Kind: kind, Origin: origin})
}

// Graph returns the account's relationship graph: the account, projects that
// still list it, meetings currently published to it, its newest events,
// revalidated LINK# rows, direct members and event attendees that reference
// an existing contact. Volume never fails the call; bounds set Truncated.
func (s *AccountOverviewService) Graph(ctx context.Context, userID, accountID string) (*model.AccountGraph, error) {
	account, _, err := s.authorizeAccount(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	reads := newOverviewReads()
	b := &graphBuilder{g: &model.AccountGraph{Nodes: []model.GraphNode{}, Edges: []model.GraphEdge{}}, nodes: map[string]bool{}, edges: map[string]bool{},
		projects: map[string]*model.Project{}, contacts: map[string]*model.AccountContact{}, docs: map[string]*model.CrawledDocument{}}
	accountNode := graphNodeID(model.GraphNodeAccount, accountID)
	b.node(model.GraphNodeAccount, accountID, account.Name)

	steps := []func(context.Context, string, *graphBuilder, *overviewReads) error{
		s.graphProjects, s.graphMeetings, s.graphEvents, s.graphLinks, s.graphMembers,
	}
	for _, step := range steps {
		if err := step(ctx, accountID, b, reads); err != nil {
			return nil, err
		}
	}
	if reads.truncated {
		b.g.Truncated = true
	}
	if err := fitGraph(b.g, accountNode); err != nil {
		return nil, err
	}
	return b.g, nil
}

// graphProject returns the project when it still lists the account, adding
// its node and project_account edge. Reads are cached per call.
func (s *AccountOverviewService) graphProject(ctx context.Context, accountID, projectID string, b *graphBuilder, reads *overviewReads) (*model.Project, error) {
	project, seen := b.projects[projectID]
	if !seen {
		if !validLinkPlainID(projectID) {
			return nil, nil
		}
		var err error
		if project, err = s.repo.GetProject(ctx, projectID); err != nil {
			return nil, err
		}
		if project == nil || project.ProjectID != projectID || !contains(project.AccountIDs, accountID) {
			project = nil
		}
		b.projects[projectID], reads.projects[projectID] = project, project != nil
	}
	if project == nil || !b.node(model.GraphNodeProject, projectID, project.Name) {
		return nil, nil
	}
	b.edge(graphNodeID(model.GraphNodeProject, projectID), graphNodeID(model.GraphNodeAccount, accountID), model.GraphEdgeProjectAccount, "")
	return project, nil
}

func (s *AccountOverviewService) graphProjects(ctx context.Context, accountID string, b *graphBuilder, reads *overviewReads) error {
	refs, err := s.repo.ListProjectRefsForAccount(ctx, accountID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.ProjectID] || !validLinkPlainID(ref.ProjectID) {
			continue
		}
		if len(seen) == maxFlowProjectRefs || len(b.g.Nodes) >= maxGraphNodes {
			b.g.Truncated = true
			break
		}
		seen[ref.ProjectID] = true
		if _, err := s.graphProject(ctx, accountID, ref.ProjectID, b, reads); err != nil {
			return err
		}
	}
	return nil
}

// graphMeeting adds a meeting node only when the meeting is currently
// published to the account, with its meeting_account and meeting_project
// edges. It reports whether the node is present.
func (s *AccountOverviewService) graphMeeting(ctx context.Context, accountID, ownerID, meetingID, fallbackTitle string, b *graphBuilder, reads *overviewReads) (bool, error) {
	if b.hasNode(model.GraphNodeMeeting, ownerID+":"+meetingID) {
		return true, nil
	}
	meeting, err := s.publishedMeeting(ctx, accountID, ownerID, meetingID, reads)
	if err != nil || meeting == nil {
		return false, err
	}
	title := meeting.Title
	if title == "" {
		title = fallbackTitle
	}
	id := ownerID + ":" + meetingID
	if !b.node(model.GraphNodeMeeting, id, title) {
		return false, nil
	}
	nodeID := graphNodeID(model.GraphNodeMeeting, id)
	b.edge(nodeID, graphNodeID(model.GraphNodeAccount, accountID), model.GraphEdgeMeetingAccount, "")
	for _, projectID := range meeting.ProjectIDs {
		project, err := s.graphProject(ctx, accountID, projectID, b, reads)
		if err != nil {
			return false, err
		}
		if project != nil {
			b.edge(nodeID, graphNodeID(model.GraphNodeProject, projectID), model.GraphEdgeMeetingProject, "")
		}
	}
	return true, nil
}

func (s *AccountOverviewService) graphMeetings(ctx context.Context, accountID string, b *graphBuilder, reads *overviewReads) error {
	refs, cursor, err := s.repo.ListMeetingRefsForAccountPage(ctx, accountID, "", maxGraphMeetingRefs)
	if err != nil {
		return err
	}
	if cursor != nil {
		b.g.Truncated = true
	}
	for _, ref := range refs {
		if len(b.g.Nodes) >= maxGraphNodes {
			b.g.Truncated = true
			break
		}
		if ref.AccountID != accountID || ref.PK != model.PrefixAccount+accountID || ref.EntityType != model.EntityTypeMeetingRef ||
			ref.SK != model.PrefixMeetingRef+ref.Date.UTC().Format(time.RFC3339)+"#"+ref.MeetingID ||
			!validLinkPlainID(ref.OwnerUserID) || !validLinkPlainID(ref.MeetingID) {
			continue
		}
		if _, err := s.graphMeeting(ctx, accountID, ref.OwnerUserID, ref.MeetingID, ref.Title, b, reads); err != nil {
			return err
		}
	}
	return nil
}

// graphEvent adds an existing event's node plus attends edges from attendees
// whose ContactID references a contact that still exists. It reports
// whether the node is present.
func (s *AccountOverviewService) graphEvent(ctx context.Context, accountID string, event *model.AccountEvent, b *graphBuilder) (bool, error) {
	if b.hasNode(model.GraphNodeEvent, event.EventID) {
		return true, nil
	}
	if !b.node(model.GraphNodeEvent, event.EventID, event.Title) {
		return false, nil
	}
	eventNode := graphNodeID(model.GraphNodeEvent, event.EventID)
	for _, attendee := range event.Attendees {
		if !validContactID(attendee.ContactID) {
			continue
		}
		contact, seen := b.contacts[attendee.ContactID]
		if !seen {
			var err error
			contact, err = s.repo.GetAccountContact(ctx, accountID, attendee.ContactID)
			if graphRowUnreadable(err, repository.ErrContactUnreadable) {
				// One unopenable contact is skipped, not fatal.
				contact, err, b.g.Truncated = nil, nil, true
			}
			if err != nil {
				return false, err
			}
			if contact != nil && contact.ContactID != attendee.ContactID {
				contact = nil
			}
			b.contacts[attendee.ContactID] = contact
		}
		if contact == nil {
			continue
		}
		if b.node(model.GraphNodeContact, contact.ContactID, contact.Name) {
			b.edge(graphNodeID(model.GraphNodeContact, contact.ContactID), eventNode, model.GraphEdgeAttends, "")
		}
	}
	return true, nil
}

func (s *AccountOverviewService) graphEvents(ctx context.Context, accountID string, b *graphBuilder, reads *overviewReads) error {
	events, next, err := s.repo.ListAccountEvents(ctx, accountID, flowEventKeyLow, flowEventKeyHigh, true, maxGraphEvents, "")
	if err != nil {
		return err
	}
	if next != "" {
		b.g.Truncated = true
	}
	for i := range events {
		event := &events[i]
		if !validContactID(event.EventID) {
			continue
		}
		reads.events[event.EventID] = event
		if _, err := s.graphEvent(ctx, accountID, event, b); err != nil {
			return err
		}
	}
	return nil
}

// graphDocument returns the stored document of the given type when it exists
// and is not deleted. Reads are cached per call.
func (s *AccountOverviewService) graphDocument(ctx context.Context, sourceID, docHash, docType string, b *graphBuilder) (*model.CrawledDocument, error) {
	key := sourceID + ":" + docHash
	doc, seen := b.docs[key]
	if !seen {
		var err error
		doc, err = s.docs.GetDocument(ctx, sourceID, docHash)
		if errors.Is(err, repository.ErrInvalidDocument) {
			doc, err = nil, nil
		}
		if err != nil {
			return nil, err
		}
		if doc != nil && (doc.QualityStatus == "DELETED" || doc.Type != docType) {
			doc = nil
		}
		b.docs[key] = doc
	}
	return doc, nil
}

// graphRowUnreadable reports a single-row open failure that the graph skips.
// A missing sealing key or a transport failure is never skipped.
func graphRowUnreadable(err, unreadable error) bool {
	return err != nil && errors.Is(err, unreadable) && !errors.Is(err, repository.ErrContactEncryptionUnavailable)
}

// graphLinkFrom revalidates a link's from endpoint and adds its node.
func (s *AccountOverviewService) graphLinkFrom(ctx context.Context, accountID string, link model.AccountLink, b *graphBuilder, reads *overviewReads) (bool, error) {
	switch link.FromKind {
	case model.LinkKindNews, model.LinkKindTech:
		source, hash, _ := splitLinkDocID(link.FromID)
		docType := "tech"
		if link.FromKind == model.LinkKindNews {
			if b.sources == nil {
				sources, err := s.news.accountSources(ctx, accountID)
				if err != nil {
					return false, err
				}
				b.sources = map[string]bool{}
				for _, src := range sources {
					b.sources[src.SourceID] = true
				}
			}
			if !b.sources[source] {
				return false, nil
			}
			docType = "news"
		}
		if b.hasNode(link.FromKind, link.FromID) {
			return true, nil
		}
		doc, err := s.graphDocument(ctx, source, hash, docType, b)
		if err != nil || doc == nil {
			return false, err
		}
		return b.node(link.FromKind, link.FromID, doc.Title), nil
	case model.LinkKindEvent:
		event, err := s.accountEvent(ctx, accountID, link.FromID, reads)
		if graphRowUnreadable(err, repository.ErrEventUnreadable) {
			// One unopenable event drops its link row, not the graph.
			reads.events[link.FromID], event, err, b.g.Truncated = nil, nil, nil, true
		}
		if err != nil || event == nil {
			return false, err
		}
		return s.graphEvent(ctx, accountID, event, b)
	}
	return false, nil
}

// graphLinkTo revalidates a link's to endpoint and adds its node.
func (s *AccountOverviewService) graphLinkTo(ctx context.Context, accountID string, link model.AccountLink, b *graphBuilder, reads *overviewReads) (bool, error) {
	switch link.ToKind {
	case model.LinkKindProject:
		project, err := s.graphProject(ctx, accountID, link.ToID, b, reads)
		return project != nil, err
	case model.LinkKindMeeting:
		owner, meetingID, _ := splitMeetingLinkID(link.ToID)
		return s.graphMeeting(ctx, accountID, owner, meetingID, "", b, reads)
	}
	return false, nil
}

// graphLinks pages through the account's LINK# rows (at most
// maxGraphLinkRows). A row produces nodes and an edge only when both
// endpoints revalidate; the to endpoint is checked first so a dropped row
// never adds a dangling from node.
func (s *AccountOverviewService) graphLinks(ctx context.Context, accountID string, b *graphBuilder, reads *overviewReads) error {
	cursor := ""
	for read := 0; ; {
		if read >= maxGraphLinkRows {
			b.g.Truncated = true
			return nil
		}
		links, next, err := s.repo.ListAccountLinks(ctx, accountID, cursor, int32(min(graphLinkPageSize, maxGraphLinkRows-read)))
		if err != nil {
			return err
		}
		read += len(links)
		for _, link := range links {
			if validateLinkShape(link.Input()) != nil {
				continue
			}
			if len(b.g.Edges) >= maxGraphEdges {
				b.g.Truncated = true
				return nil
			}
			// With the node cap full, only rows between present nodes can
			// add output; never hydrate endpoints that could not be added.
			if len(b.g.Nodes) >= maxGraphNodes && (!b.hasNode(link.FromKind, link.FromID) || !b.hasNode(link.ToKind, link.ToID)) {
				b.g.Truncated = true
				continue
			}
			if need := graphLinkLookups(link, b, reads); need > 0 {
				if b.lookups+need > maxGraphLinkLookups {
					b.g.Truncated = true
					continue
				}
				b.lookups += need
			}
			if ok, err := s.graphLinkToCheck(ctx, accountID, link, b, reads); err != nil {
				return err
			} else if !ok {
				continue
			}
			from, err := s.graphLinkFrom(ctx, accountID, link, b, reads)
			if err != nil {
				return err
			}
			if !from {
				continue
			}
			if ok, err := s.graphLinkTo(ctx, accountID, link, b, reads); err != nil {
				return err
			} else if ok {
				b.edge(graphNodeID(link.FromKind, link.FromID), graphNodeID(link.ToKind, link.ToID), model.GraphEdgeLink, link.Origin)
			}
		}
		if next == nil || len(links) == 0 {
			return nil
		}
		cursor = *next
	}
}

// graphLinkLookups counts the uncached endpoint reads a link row would issue
// before its nodes are known to fit.
func graphLinkLookups(link model.AccountLink, b *graphBuilder, reads *overviewReads) int {
	n := 0
	switch link.ToKind {
	case model.LinkKindProject:
		if _, seen := reads.projects[link.ToID]; !seen {
			n++
		}
	case model.LinkKindMeeting:
		owner, meetingID, _ := splitMeetingLinkID(link.ToID)
		if _, seen := reads.meetings[owner+":"+meetingID]; !seen {
			n++
		}
	}
	switch link.FromKind {
	case model.LinkKindNews, model.LinkKindTech:
		source, hash, _ := splitLinkDocID(link.FromID)
		if _, seen := b.docs[source+":"+hash]; !seen && !b.hasNode(link.FromKind, link.FromID) {
			n++
		}
	case model.LinkKindEvent:
		if _, seen := reads.events[link.FromID]; !seen {
			n++
		}
	}
	return n
}

// graphLinkToCheck revalidates a link's to endpoint without adding a node.
func (s *AccountOverviewService) graphLinkToCheck(ctx context.Context, accountID string, link model.AccountLink, b *graphBuilder, reads *overviewReads) (bool, error) {
	switch link.ToKind {
	case model.LinkKindProject:
		return s.projectInAccount(ctx, accountID, link.ToID, reads)
	case model.LinkKindMeeting:
		owner, meetingID, _ := splitMeetingLinkID(link.ToID)
		meeting, err := s.publishedMeeting(ctx, accountID, owner, meetingID, reads)
		return meeting != nil, err
	}
	return false, nil
}

// graphMembers adds the account's direct members, labelled by email (as the
// account detail route shows) and role.
func (s *AccountOverviewService) graphMembers(ctx context.Context, accountID string, b *graphBuilder, _ *overviewReads) error {
	members, err := s.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return err
	}
	accountNode := graphNodeID(model.GraphNodeAccount, accountID)
	for _, m := range members {
		if m.AccountID != accountID || m.PK != model.PrefixAccount+accountID || m.SK != model.PrefixMember+m.UserID || !validLinkPlainID(m.UserID) {
			continue
		}
		name := m.Email
		if name == "" {
			name = m.UserID
		}
		if m.Role != "" {
			name += " (" + m.Role + ")"
		}
		if !b.node(model.GraphNodeMember, m.UserID, name) {
			return nil
		}
		b.edge(graphNodeID(model.GraphNodeMember, m.UserID), accountNode, model.GraphEdgeMemberOf, "")
	}
	return nil
}

// fitGraph drops the newest nodes (never the account node) with their edges
// until the encoded graph fits MaxAccountGraphBytes; any trim sets Truncated.
func fitGraph(g *model.AccountGraph, accountNode string) error {
	for {
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		if len(raw) <= MaxAccountGraphBytes {
			return nil
		}
		g.Truncated = true
		switch {
		case len(g.Nodes) > 1 && g.Nodes[len(g.Nodes)-1].ID != accountNode:
			drop := g.Nodes[len(g.Nodes)-1].ID
			g.Nodes = g.Nodes[:len(g.Nodes)-1]
			kept := g.Edges[:0]
			for _, e := range g.Edges {
				if e.From != drop && e.To != drop {
					kept = append(kept, e)
				}
			}
			g.Edges = kept
		case len(g.Edges) > 0:
			g.Edges = g.Edges[:len(g.Edges)-1]
		default:
			return fmt.Errorf("account graph exceeds the response bound")
		}
	}
}
