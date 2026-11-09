package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
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
	news     accountOverviewNews
	insights accountOverviewInsights
}

func NewAccountOverviewService(repo accountOverviewRepo, news *AccountNewsService, accounts *AccountService) *AccountOverviewService {
	return &AccountOverviewService{repo: repo, news: news, insights: accounts}
}

func (s *AccountOverviewService) authorize(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	if validateAccountHierarchyIDs(accountID, "") != nil || validateAccountHierarchyIDs(userID, "") != nil {
		return nil, ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	return member, nil
}

// overviewReads caches per-call reads and link revalidation; it never
// outlives one request.
type overviewReads struct {
	meetings  map[string]*model.Meeting
	events    map[string]*model.AccountEvent
	projects  map[string]bool // project ID -> still lists the account
	truncated bool            // a per-endpoint link read hit its bound
}

func newOverviewReads() *overviewReads {
	return &overviewReads{meetings: map[string]*model.Meeting{}, events: map[string]*model.AccountEvent{}, projects: map[string]bool{}}
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
	rows, truncated, err := s.repo.ListAccountLinksByPrefix(ctx, accountID, prefix, maxOverviewLinkRows)
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

// projectEvents returns the event IDs linked to a project (event→project).
// Events are revalidated when read in hasUpcomingEvent.
func (s *AccountOverviewService) projectEvents(ctx context.Context, accountID, projectID string, reads *overviewReads) ([]string, error) {
	links, err := s.endpointLinks(ctx, accountID, model.PrefixAccountLinkTo+model.LinkKindProject+"#"+projectID+"#"+model.LinkKindEvent+"#", reads)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, link := range links {
		if link.FromKind == model.LinkKindEvent && link.ToKind == model.LinkKindProject && link.ToID == projectID && !contains(out, link.FromID) {
			out = append(out, link.FromID)
		}
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
		if !item.Terminal {
			ages, err := s.openActionAges(ctx, userID, accountID, member, project.ProjectID, now, reads)
			if err != nil {
				return nil, err
			}
			events, err := s.projectEvents(ctx, accountID, project.ProjectID, reads)
			if err != nil {
				return nil, err
			}
			upcoming, err := s.hasUpcomingEvent(ctx, accountID, events, now, reads)
			if err != nil {
				return nil, err
			}
			item.Stall = StallReasons(*project, ages, upcoming, now)
		}
		out = append(out, item)
	}
	return out, nil
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
		meeting, cached := reads.meetings[key]
		if !cached {
			if meeting, err = s.repo.GetMeetingActionState(ctx, ref.OwnerUserID, ref.MeetingID); err != nil {
				return nil, err
			}
			if !meetingPublishedToAccount(meeting, accountID, ref.OwnerUserID, ref.MeetingID) {
				meeting = nil
			}
			reads.meetings[key] = meeting
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

// hasUpcomingEvent reports whether any linked account event starts after now.
// Per-user calendar entries are deliberately excluded (ADR-064).
func (s *AccountOverviewService) hasUpcomingEvent(ctx context.Context, accountID string, eventIDs []string, now time.Time, reads *overviewReads) (bool, error) {
	for _, id := range eventIDs {
		event, cached := reads.events[id]
		if !cached {
			var err error
			if event, _, err = s.repo.GetAccountEvent(ctx, accountID, id); err != nil {
				return false, err
			}
			if event != nil && event.EventID != id {
				event = nil
			}
			reads.events[id] = event
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
