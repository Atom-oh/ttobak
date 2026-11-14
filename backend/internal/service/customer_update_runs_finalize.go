package service

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type runPair struct{ urlHash, accountID string }

// accountView is one account's current scope, block list and stages, read
// once per finalize call.
type accountView struct {
	inScope   bool
	blockList []string
	open      []AccountPackOpportunity
	stages    []string
}

// finalizeRun holds one done run's validated state for one finalize call.
type finalizeRun struct {
	userID   string
	run      *model.UpdateRun
	items    map[string]*model.UpdateItem
	inLease  map[string]bool
	accounts map[string]bool
	day      string
	now      time.Time
	// drafting holds pairs whose DRAFT row may be materialized.
	drafting map[runPair]bool
	// heldForDay marks items with a draft deferred by a day budget (Go's, or
	// the runtime's account_budget/draft_budget).
	heldForDay map[string]bool
	// keepNew marks items with a pair deferred for research_budget: they stay
	// new with attempts unchanged.
	keepNew map[string]bool
	views   map[string]*accountView
	// researched counts accepted DEC rows whose runtime outcome is drafted or
	// discarded; it is added to the day row after the lease delete.
	researched int
}

// finalize handles a user's lease. It returns true when the lease is gone
// and a new run may start.
func (r *CustomerUpdateRuns) finalize(ctx context.Context, userID string, run *model.UpdateRun, stats *RunStats) (bool, error) {
	rows, err := r.deps.Repo.ReadRunRows(ctx, run.RunID)
	if errors.Is(err, repository.ErrUpdateRunTooLarge) {
		// Never finalized: it waits for lease expiry like a run without DONE.
		stats.Oversized++
		rows, err = &model.RunRows{}, nil
	}
	if err != nil {
		return false, err
	}
	now := r.now()
	if !rows.Done {
		if now.Before(run.LeaseUntil) {
			return false, nil
		}
		return r.expire(ctx, userID, run, stats)
	}
	before := *stats
	stats.Malformed += rows.Malformed
	stats.Unreadable += rows.Unreadable
	f := &finalizeRun{
		userID: userID, run: run, items: map[string]*model.UpdateItem{}, inLease: map[string]bool{}, accounts: map[string]bool{},
		day: kstDay(now), now: now, drafting: map[runPair]bool{}, heldForDay: map[string]bool{}, keepNew: map[string]bool{}, views: map[string]*accountView{},
	}
	if err := r.loadItems(ctx, userID, run, f.items); err != nil {
		return false, err
	}
	for _, h := range run.ItemHashes {
		f.inLease[h] = true
	}
	for _, a := range run.AccountIDs {
		f.accounts[a] = true
	}
	for _, dec := range rows.Decisions {
		if err := r.acceptDecision(ctx, f, dec, stats); err != nil {
			return false, err
		}
	}
	materialized := map[runPair]bool{}
	for _, d := range rows.Drafts {
		pair := runPair{d.URLHash, d.AccountID}
		if !f.drafting[pair] || materialized[pair] {
			stats.Ignored++
			continue
		}
		materialized[pair] = true
		if err := r.materialize(ctx, f, d, stats); err != nil {
			return false, err
		}
	}
	for _, dec := range rows.Decisions {
		pair := runPair{dec.URLHash, dec.AccountID}
		if f.drafting[pair] && !materialized[pair] {
			if err := r.setOutcome(ctx, f, pair, model.UpdateOutcomeDiscarded, discardDraftMissing); err != nil {
				return false, err
			}
			stats.Discarded++
		}
	}
	// Item updates run only after the lease delete succeeds, which happens
	// once per run id: a repeated finalize (a crash before the delete, or an
	// overlapping tick) cannot add attempts twice; a crash after it leaves
	// attempts under-counted, never doubled.
	if err := r.deps.Repo.DeleteRunIfMatch(ctx, userID, run.RunID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return false, nil
		}
		return false, err
	}
	stats.Finalized++
	if f.researched > 0 {
		r.logItemErr(userID, r.deps.Repo.AddDayResearched(ctx, userID, f.day, f.researched), stats)
	}
	r.applyItemRows(ctx, f, rows.Items, stats)
	r.deleteRows(ctx, run.RunID)
	log.Printf("customer update run finalized user=%s run=%s drafts=%d discarded=%d deferred=%d ignored=%d",
		userHashPrefix(userID), run.RunID, stats.Drafts-before.Drafts, stats.Discarded-before.Discarded,
		stats.Deferred-before.Deferred, stats.Ignored-before.Ignored)
	return true, nil
}

func (r *CustomerUpdateRuns) loadItems(ctx context.Context, userID string, run *model.UpdateRun, into map[string]*model.UpdateItem) error {
	for _, h := range run.ItemHashes {
		it, err := r.deps.Repo.GetItem(ctx, userID, h)
		if err != nil {
			return err
		}
		if it != nil {
			into[h] = it
		}
	}
	return nil
}

// expire ends a lease whose run never finished: after the lease delete
// succeeded, each leased item gets one more attempt (skipped at 3).
func (r *CustomerUpdateRuns) expire(ctx context.Context, userID string, run *model.UpdateRun, stats *RunStats) (bool, error) {
	items := map[string]*model.UpdateItem{}
	if err := r.loadItems(ctx, userID, run, items); err != nil {
		return false, err
	}
	if err := r.deps.Repo.DeleteRunIfMatch(ctx, userID, run.RunID); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return false, nil
		}
		return false, err
	}
	stats.Expired++
	for _, h := range run.ItemHashes {
		if it := items[h]; it != nil {
			r.bumpItem(ctx, userID, it, stats)
		}
	}
	r.deleteRows(ctx, run.RunID)
	log.Printf("customer update run expired user=%s run=%s items=%d", userHashPrefix(userID), run.RunID, len(run.ItemHashes))
	return true, nil
}

// deleteRows removes a finished run's rows; leftovers expire by their 2-day TTL.
func (r *CustomerUpdateRuns) deleteRows(ctx context.Context, runID string) {
	if err := r.deps.Repo.DeleteRunRows(ctx, runID); err != nil {
		log.Printf("customer update run rows delete failed run=%s err=%s", runID, runErrorClass(err))
	}
}

// bumpItem adds one attempt; the third failed attempt skips the item.
func (r *CustomerUpdateRuns) bumpItem(ctx context.Context, userID string, it *model.UpdateItem, stats *RunStats) {
	state := model.UpdateItemNew
	if it.Attempts+1 >= maxItemAttempts {
		state = model.UpdateItemSkipped
	}
	r.logItemErr(userID, r.setItem(ctx, userID, it.URLHash, state, 1), stats)
}

func (r *CustomerUpdateRuns) logItemErr(userID string, err error, stats *RunStats) {
	if err != nil {
		stats.ItemErrors++
		log.Printf("customer update item update failed user=%s err=%s", userHashPrefix(userID), runErrorClass(err))
	}
}

// applyItemRows maps ITEM rows of leased items to inbox states. First match
// wins: an item with a pair deferred by a day budget (Go's, or the runtime's
// account_budget/draft_budget) is held until the next KST day; one with a pair
// deferred for research_budget stays new; both keep attempts unchanged. Else
// a decided or skipped row sets the state, and a deferred row (gate_error,
// budget) or no row adds one attempt.
func (r *CustomerUpdateRuns) applyItemRows(ctx context.Context, f *finalizeRun, rows []model.RunItemRow, stats *RunStats) {
	byHash := map[string]model.RunItemRow{}
	for _, row := range rows {
		if !f.inLease[row.URLHash] {
			stats.Ignored++
			continue
		}
		byHash[row.URLHash] = row
	}
	for _, h := range f.run.ItemHashes {
		it := f.items[h]
		if it == nil {
			continue
		}
		if f.heldForDay[h] {
			r.logItemErr(f.userID, r.holdItem(ctx, f.userID, h, f.now), stats)
			stats.Held++
			continue
		}
		if f.keepNew[h] {
			continue
		}
		switch byHash[h].State {
		case "decided":
			r.logItemErr(f.userID, r.setItem(ctx, f.userID, h, model.UpdateItemDecided, 0), stats)
		case "skipped":
			r.logItemErr(f.userID, r.setItem(ctx, f.userID, h, model.UpdateItemSkipped, 0), stats)
		default: // deferred, or no row
			r.bumpItem(ctx, f.userID, it, stats)
		}
	}
}

// storedCode keeps a runtime-written code only when it is a short code.
func storedCode(code string, pattern *regexp.Regexp, fallback string) string {
	if len(code) <= maxStoredCodeBytes && pattern.MatchString(code) {
		return code
	}
	return fallback
}

// acceptDecision stores a DEC row of a leased (item, account) pair whose
// account is still in scope (else nothing is stored), records whether its
// DRAFT row may be materialized, and notes runtime budget deferrals and
// researched pairs.
func (r *CustomerUpdateRuns) acceptDecision(ctx context.Context, f *finalizeRun, dec model.RunDecisionRow, stats *RunStats) error {
	if !f.inLease[dec.URLHash] || !f.accounts[dec.AccountID] {
		stats.Ignored++
		return nil
	}
	v, err := r.view(ctx, f, dec.AccountID)
	if err != nil {
		return err
	}
	if !v.inScope {
		stats.OutOfScope++
		return nil
	}
	switch dec.Outcome {
	case model.UpdateOutcomeDrafted, model.UpdateOutcomeDiscarded:
		f.researched++
	case model.UpdateOutcomeDeferred:
		switch dec.DiscardReason {
		case runtimeAccountBudget, runtimeDraftBudget:
			f.heldForDay[dec.URLHash] = true
		case runtimeResearchBudget:
			f.keepNew[dec.URLHash] = true
		}
	}
	pair := runPair{dec.URLHash, dec.AccountID}
	wantsDraft := dec.Choice == model.UpdateChoiceNeeded && dec.Outcome == model.UpdateOutcomeDrafted
	d := &model.UpdateDecision{
		URLHash: dec.URLHash, AccountID: dec.AccountID, Choice: dec.Choice, Score: dec.Score, Reason: dec.Reason,
		Backend: storedCode(dec.Backend, runBackendCodePattern, "unknown"), RunID: f.run.RunID,
		Outcome: dec.Outcome, DiscardReason: storedCode(dec.DiscardReason, runDiscardCodePattern, "invalid"),
		DecidedAt: f.now, ExpiresAt: f.now.Add(model.UpdateDecisionTTL).Unix(),
	}
	if dec.Choice == model.UpdateChoiceNeeded {
		d.GateInput = dec.GateInput
	} else {
		d.Outcome, d.DiscardReason = "", "" // a not_needed decision has no outcome
	}
	err = r.deps.Repo.PutDecisionIfAbsent(ctx, f.userID, d)
	switch {
	case err == nil:
		f.drafting[pair] = wantsDraft
		return nil
	case errors.Is(err, repository.ErrInvalidUpdateRun):
		stats.Ignored++ // a row the repository refuses must not block the user
		return nil
	case !errors.Is(err, repository.ErrConditionFailed):
		return err
	}
	stored, err := r.deps.Repo.GetDecision(ctx, f.userID, dec.URLHash, dec.AccountID)
	if err != nil {
		if errors.Is(err, repository.ErrUpdateDecisionUnreadable) {
			return nil // treated as already decided
		}
		return err
	}
	if stored == nil || stored.Choice != model.UpdateChoiceNeeded || !wantsDraft {
		return nil
	}
	switch {
	case stored.RunID == f.run.RunID:
		// A repeated finalize of this run; the stored choice decides.
		switch stored.Outcome {
		case "", model.UpdateOutcomeDrafted, model.UpdateOutcomeDeferred:
			f.drafting[pair] = true
		}
	case stored.Outcome == model.UpdateOutcomeDeferred:
		// An earlier run's draft was held by a day budget.
		f.drafting[pair] = true
	}
	return nil
}

func (r *CustomerUpdateRuns) setOutcome(ctx context.Context, f *finalizeRun, pair runPair, outcome, reason string) error {
	err := r.deps.Repo.SetDecisionOutcome(ctx, f.userID, pair.urlHash, pair.accountID, outcome, reason)
	if err != nil && !errors.Is(err, repository.ErrConditionFailed) {
		return err
	}
	return nil
}

// scopeDenied reports a direct-membership denial from the finalize reads.
func scopeDenied(err error) bool {
	return errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput)
}

// view reads (once per finalize call) whether the account is still in scope,
// and its full block list and open opportunities.
func (r *CustomerUpdateRuns) view(ctx context.Context, f *finalizeRun, accountID string) (*accountView, error) {
	if v := f.views[accountID]; v != nil {
		return v, nil
	}
	v := &accountView{}
	ok, err := r.deps.Context.InScope(ctx, f.userID, accountID)
	if err != nil {
		return nil, err
	}
	if ok {
		v.blockList, err = r.deps.Context.BlockList(ctx, f.userID, accountID)
		if err == nil {
			v.open, err = r.deps.Context.OpenOpportunities(ctx, f.userID, accountID)
		}
		switch {
		case err == nil:
			v.inScope = true
			for _, o := range v.open {
				if o.Stage != "" {
					v.stages = append(v.stages, o.Stage)
				}
			}
		case !scopeDenied(err):
			return nil, err
		}
	}
	f.views[accountID] = v
	return v, nil
}

// allowedFetched keeps the fetchedUrls entries that are absolute https URLs
// of at most 2,048 characters that NormalizeUpdateURL accepts (so internal
// and malformed hosts never join the allow list).
func allowedFetched(urls []string) []string {
	out := []string{}
	for _, u := range urls {
		if !strings.HasPrefix(u, "https://") || len(u) > model.MaxUpdateURLLength {
			continue
		}
		if _, _, err := model.NormalizeUpdateURL(u); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// draftRejection validates a DRAFT row against the item URL, the current
// block list and stages; it returns "" when the draft passes, else the code.
func draftRejection(d model.RunDraftRow, itemURL string, blockList, stages []string) string {
	allowed := append(allowedFetched(d.FetchedURLs), itemURL)
	body := UpdateDraftCheckInput{Body: d.Body, Links: d.Links, AllowedURLs: allowed, BlockList: blockList, Stages: stages}
	why := body
	why.Body = d.Why
	if ValidateUpdateDraftEncoding(body) != nil || ValidateUpdateDraftEncoding(why) != nil || !utf8.ValidString(d.SourceTitle) ||
		ValidateUpdateDraftEncoding(UpdateDraftCheckInput{Links: d.FetchedURLs}) != nil {
		return discardEncoding
	}
	allowedSet := map[string]bool{}
	for _, u := range allowed {
		allowedSet[u] = true
	}
	if len(d.Links) > maxRunDraftLinksStored {
		return discardLinksInvalid
	}
	for _, l := range d.Links {
		if !allowedSet[l] {
			return discardLinksInvalid
		}
	}
	codes := CheckUpdateDraft(body)
	whyBad := strings.Trim(d.Why, updateWhitespace) == "" || utf8.RuneCountInString(d.Why) > maxRunWhyRunes
	for _, c := range CheckUpdateDraft(why) {
		if c != "link_missing" && c != "no_meeting_offer" {
			whyBad = true
		}
	}
	if whyBad {
		codes = append(codes, discardWhyInvalid)
	}
	return strings.Join(codes, ",")
}

// draftOpportunities resolves the row's opportunity ids against the current
// open opportunities; with none left, all current ones up to 10.
func draftOpportunities(ids []string, open []AccountPackOpportunity) []model.UpdateDraftOppty {
	byID := map[string]AccountPackOpportunity{}
	for _, o := range open {
		byID[o.ProjectID] = o
	}
	out, seen := []model.UpdateDraftOppty{}, map[string]bool{}
	for _, id := range ids {
		if o, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, model.UpdateDraftOppty{ProjectID: o.ProjectID, Name: o.Name, Stage: o.Stage})
		}
	}
	if len(out) == 0 {
		for _, o := range open {
			if len(out) == maxDraftFallbackOpptys {
				break
			}
			out = append(out, model.UpdateDraftOppty{ProjectID: o.ProjectID, Name: o.Name, Stage: o.Stage})
		}
	}
	return out
}

// materialize validates one DRAFT row whose pair may draft and writes it.
// Only transport-level failures are returned; rejections set the stored
// decision's outcome.
func (r *CustomerUpdateRuns) materialize(ctx context.Context, f *finalizeRun, d model.RunDraftRow, stats *RunStats) error {
	pair := runPair{d.URLHash, d.AccountID}
	discard := func(reason string) error {
		stats.Discarded++
		return r.setOutcome(ctx, f, pair, model.UpdateOutcomeDiscarded, reason)
	}
	item := f.items[d.URLHash]
	if item == nil {
		return discard(discardItemMissing)
	}
	v, err := r.view(ctx, f, d.AccountID)
	if err != nil {
		return err
	}
	if !v.inScope {
		return discard(discardOutOfScope)
	}
	if reason := draftRejection(d, item.URL, v.blockList, v.stages); reason != "" {
		return discard(reason)
	}

	existing, err := r.deps.Repo.GetDraft(ctx, f.userID, d.AccountID, d.URLHash)
	switch {
	case errors.Is(err, repository.ErrUpdateDraftUnreadable),
		err == nil && existing != nil && (existing.ExpiresAt == 0 || existing.ExpiresAt > f.now.Unix()):
		// Already materialized (a repeated finalize or a crash after the put).
		return r.setOutcome(ctx, f, pair, model.UpdateOutcomeDrafted, "")
	case err != nil:
		return err
	}
	if err := r.deps.Repo.AddDayDraft(ctx, f.userID, f.day, d.AccountID, maxDayDrafts, maxAccountDayDrafts); err != nil {
		if !errors.Is(err, repository.ErrConditionFailed) {
			return err
		}
		day, err := r.deps.Repo.GetDay(ctx, f.userID, f.day)
		if err != nil {
			return err
		}
		reason := deferAccountDayBudget
		if day.Total >= maxDayDrafts {
			reason = deferDayBudget
		}
		stats.Deferred++
		f.heldForDay[d.URLHash] = true
		return r.setOutcome(ctx, f, pair, model.UpdateOutcomeDeferred, reason)
	}
	draft := &model.UpdateDraft{
		AccountID: d.AccountID, URLHash: d.URLHash, SourceURL: item.URL,
		SourceTitle: capRunes(collapseSpace(d.SourceTitle), maxRunSourceTitleRunes),
		Why:         d.Why, Body: d.Body, Opportunities: draftOpportunities(d.OpportunityIDs, v.open),
		Links: append([]string{}, d.Links...), Status: model.UpdateDraftNew, Revision: 0, RunID: f.run.RunID,
		CreatedAt: f.now, UpdatedAt: f.now, ExpiresAt: f.now.Add(model.UpdateDraftTTL).Unix(),
	}
	draft.NormalizeSlices()
	if err := r.deps.Repo.PutDraftIfAbsent(ctx, f.userID, draft); err != nil {
		// The day slot was taken without a new draft (accepted leak).
		stats.Leaked++
		if errors.Is(err, repository.ErrConditionFailed) {
			return r.setOutcome(ctx, f, pair, model.UpdateOutcomeDrafted, "")
		}
		return err
	}
	stats.Drafts++
	return r.setOutcome(ctx, f, pair, model.UpdateOutcomeDrafted, "")
}
