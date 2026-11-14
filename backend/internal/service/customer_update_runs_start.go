package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var errRunNoAccountsFit = errors.New("no account fits the run payload")

type runPayloadItem struct {
	URLHash string `json:"urlHash"`
	URL     string `json:"url"`
	// DecidedAccounts lists the payload accounts whose pair with the item
	// already has a final stored decision; the runtime skips those pairs.
	DecidedAccounts []string `json:"decidedAccounts"`
}

type runPayloadBudgets struct {
	MaxResearchPairs int `json:"maxResearchPairs"`
	MaxDrafts        int `json:"maxDrafts"`
}

type runPayload struct {
	Version  int               `json:"version"`
	RunID    string            `json:"runId"`
	Items    []runPayloadItem  `json:"items"`
	Accounts []AccountPack     `json:"accounts"`
	Budgets  runPayloadBudgets `json:"budgets"`
}

// marshalJSONRaw marshals without HTML escaping and without a trailing newline.
func marshalJSONRaw(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func marshalRunPayload(p runPayload) ([]byte, error) { return marshalJSONRaw(p) }

// payloadItem reports whether an inbox item can go into a payload: its URL is
// already the normalized form and urlHash is that form's hash, exactly as
// model.NormalizeUpdateURL computes them, and the URL passes the runtime's
// item URL rule (contract.py _valid_url).
func payloadItem(it model.UpdateItem) bool {
	normalized, hash, err := model.NormalizeUpdateURL(it.URL)
	return err == nil && normalized == it.URL && hash == it.URLHash && runtimeValidURL(it.URL)
}

// runtimeValidURL mirrors contract.py _valid_url: an https:// URL of at most
// 2,048 code points with no Unicode whitespace or control character (< 0x20,
// 0x7F) and a non-empty host.
func runtimeValidURL(u string) bool {
	if !strings.HasPrefix(u, "https://") || !utf8.ValidString(u) || utf8.RuneCountInString(u) > model.MaxUpdateURLLength {
		return false
	}
	for _, c := range u {
		if unicode.IsSpace(c) || c < 0x20 || c == 0x7f {
			return false
		}
	}
	parsed, err := url.Parse(u)
	return err == nil && parsed.Hostname() != ""
}

// decidedPairs holds, per item hash, the accounts whose pair already has a
// final stored decision.
type decidedPairs map[string]map[string]bool

// hasOpen reports whether some account in ids has an undecided pair with
// some item in items.
func (d decidedPairs) hasOpen(items []runPayloadItem, ids []string) bool {
	for _, it := range items {
		for _, id := range ids {
			if !d[it.URLHash][id] {
				return true
			}
		}
	}
	return false
}

// pruneDecided leaves out, until nothing changes, items whose every account
// is decided and accounts decided for every remaining item (they have nothing
// to do, and leaving them out makes room for the others). It returns the
// pruned payload, with each item's decidedAccounts among the kept accounts.
func pruneDecided(p runPayload, items []runPayloadItem, decided decidedPairs) runPayload {
	accounts := p.Accounts
	for {
		keptItems := make([]runPayloadItem, 0, len(items))
		for _, it := range items {
			for _, a := range accounts {
				if !decided[it.URLHash][a.AccountID] {
					keptItems = append(keptItems, it)
					break
				}
			}
		}
		keptAccounts := make([]AccountPack, 0, len(accounts))
		for _, a := range accounts {
			if decided.hasOpen(keptItems, []string{a.AccountID}) {
				keptAccounts = append(keptAccounts, a)
			}
		}
		if len(keptItems) == len(items) && len(keptAccounts) == len(accounts) {
			break
		}
		items, accounts = keptItems, keptAccounts
	}
	p.Accounts, p.Items = accounts, make([]runPayloadItem, 0, len(items))
	for _, it := range items {
		it.DecidedAccounts = []string{}
		for _, a := range accounts {
			if decided[it.URLHash][a.AccountID] {
				it.DecidedAccounts = append(it.DecidedAccounts, a.AccountID)
			}
		}
		p.Items = append(p.Items, it)
	}
	return p
}

// fitRunPayload keeps the packs (in order, at most 40) that are each at most
// 8,000 bytes, prunes decided items and accounts (pruneDecided), and drops
// accounts from the tail until the payload fits maxBytes, pruning again after
// each drop. It returns the body (nil with no item left), the payload, the
// items left out as fully decided, and the account ids dropped for size.
func fitRunPayload(p runPayload, items []runPayloadItem, decided decidedPairs, maxBytes int) ([]byte, runPayload, []runPayloadItem, []string, error) {
	kept := make([]AccountPack, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		if len(kept) == model.MaxUpdateRunAccounts {
			break
		}
		if b, err := marshalJSONRaw(a); err == nil && len(b) <= maxRunAccountBytes {
			kept = append(kept, a)
		}
	}
	p.Accounts = kept
	if len(kept) == 0 {
		return nil, p, nil, nil, errRunNoAccountsFit
	}
	var trimmed []string
	for {
		q := pruneDecided(p, items, decided)
		inQ := map[string]bool{}
		for _, it := range q.Items {
			inQ[it.URLHash] = true
		}
		done := []runPayloadItem{}
		for _, it := range items {
			if !inQ[it.URLHash] {
				done = append(done, it)
			}
		}
		if len(q.Items) == 0 {
			return nil, q, done, trimmed, nil
		}
		b, err := marshalRunPayload(q)
		if err != nil {
			return nil, q, done, trimmed, err
		}
		if len(b) <= maxBytes {
			return b, q, done, trimmed, nil
		}
		if len(q.Accounts) == 1 {
			return nil, q, done, trimmed, errRunNoAccountsFit
		}
		trimmed = append(trimmed, q.Accounts[len(q.Accounts)-1].AccountID)
		p.Accounts = q.Accounts[:len(q.Accounts)-1]
	}
}

// definiteRefusal reports whether an invoke failure is a definite answer, so
// no run is running: a runtime answer that is not an acceptance, or an AWS
// API client-fault response (access, validation, not found, throttling).
// Ambiguous are server faults (fault Server or HTTP 5xx: the service may
// have failed after the runtime claimed the run id), failures without a
// service response (timeouts, context errors, transport errors), an
// unreadable or invalid answer
// (ErrRunOutcomeUnknown) and RUN_EXISTS (the runId was claimed, so a run with
// it may be running).
func definiteRefusal(err error) (refused, contract bool) {
	var na *RunNotAcceptedError
	var api smithy.APIError
	var resp *awshttp.ResponseError
	switch {
	case errors.As(err, &na):
		return na.Code != "RUN_EXISTS", na.Contract && na.Code != "RUN_EXISTS"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, ErrRunOutcomeUnknown):
		return false, false
	case errors.As(err, &resp) && resp.HTTPStatusCode() >= 500,
		errors.As(err, &api) && api.ErrorFault() == smithy.FaultServer:
		// A server fault may come after the runtime claimed the run id.
		return false, false
	case errors.As(err, &api), errors.As(err, &resp):
		return true, false
	}
	return false, false
}

// start begins a run for a user without a lease. The pending mark is cleared
// only after a full cycle of the user's items found neither eligible nor held
// new items; held items keep it, because nothing else re-marks the user when
// a hold ends.
func (r *CustomerUpdateRuns) start(ctx context.Context, mark model.UpdatePendingMark, stats *RunStats) (string, error) {
	userID := mark.UserID
	repo := r.deps.Repo
	items, held, complete, err := repo.ListEligibleItems(ctx, userID, maxRunItems)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		if held > 0 || !complete {
			return "", nil
		}
		// A condition failure means fan-out re-marked the user: keep the mark.
		if err := repo.ClearPending(ctx, mark); err != nil && !errors.Is(err, repository.ErrConditionFailed) {
			return "", err
		}
		return "", nil
	}
	now := r.now()
	valid := make([]model.UpdateItem, 0, len(items))
	for _, it := range items {
		// Items first seen more than 3 days ago, and URLs the runtime would
		// refuse, are skipped without an attempt.
		if now.Sub(it.FirstSeenAt) <= maxItemAge && payloadItem(it) {
			valid = append(valid, it)
		} else if err := r.setItem(ctx, userID, it.URLHash, model.UpdateItemSkipped, 0); err != nil {
			return "", err
		}
	}
	if len(valid) == 0 {
		return "", nil
	}
	day, err := repo.GetDay(ctx, userID, kstDay(now))
	if err != nil {
		return "", err
	}
	if day.Total >= maxDayDrafts {
		return "", nil // re-checked next tick; the mark stays
	}
	researchLeft := r.researchCap - day.Researched
	if researchLeft <= 0 {
		return "", r.holdAll(ctx, userID, valid, nextKSTDayStart(now), stats)
	}
	packs, build, err := r.deps.Context.BuildWithStats(ctx, userID, day.PerAccount)
	stats.ServicesUnavailable += build.ServicesUnavailable
	if err != nil {
		return "", err
	}
	if build.Failed > 0 {
		// A failed account would silently miss every item of this batch:
		// start nothing and retry the whole batch in an hour.
		stats.BuildFailedHeld++
		log.Printf("customer update run held user=%s failedAccounts=%d", userHashPrefix(userID), build.Failed)
		return "", r.holdAll(ctx, userID, valid, now.Add(failedBuildHold), stats)
	}
	if len(packs) == 0 {
		return "", r.noPacks(ctx, userID, valid, build, now, stats)
	}
	decided := decidedPairs{}
	toSend := make([]runPayloadItem, 0, len(valid))
	for _, it := range valid {
		ids, err := repo.ListDecidedAccounts(ctx, userID, it.URLHash)
		if err != nil {
			return "", err
		}
		decided[it.URLHash] = map[string]bool{}
		for _, id := range ids {
			decided[it.URLHash][id] = true
		}
		toSend = append(toSend, runPayloadItem{URLHash: it.URLHash, URL: it.URL})
	}
	runID, err := r.newRunID()
	if err != nil {
		return "", err
	}
	p := runPayload{
		Version: 1, RunID: runID, Accounts: packs,
		Budgets: runPayloadBudgets{
			MaxResearchPairs: min(maxRunResearchPairs, researchLeft),
			MaxDrafts:        min(maxRunDraftsBudget, maxDayDrafts-day.Total),
		},
	}
	body, p, done, trimmed, err := fitRunPayload(p, toSend, decided, r.maxPayloadBytes)
	if err != nil {
		return runID, err
	}
	// Every sent pair of these items was decided in an earlier run. One with
	// an undecided pair for an account left out waits for it: until the next
	// KST day for a capped account, else (trimmed) it stays new.
	for _, it := range done {
		one := []runPayloadItem{it}
		var err error
		switch {
		case decided.hasOpen(one, build.CappedAccounts):
			err = r.holdItem(ctx, userID, it.URLHash, now)
			stats.Held++
		case decided.hasOpen(one, trimmed):
		default:
			err = r.setItem(ctx, userID, it.URLHash, model.UpdateItemDecided, 0)
		}
		if err != nil {
			return "", err
		}
	}
	if len(p.Items) == 0 {
		return "", nil
	}
	lease := &model.UpdateRun{RunID: runID, StartedAt: now, LeaseUntil: now.Add(runLeaseDuration)}
	switch {
	case decided.hasOpen(p.Items, trimmed):
		lease.Uncovered = model.UpdateRunUncoveredTrimmed
	case decided.hasOpen(p.Items, build.CappedAccounts):
		lease.Uncovered = model.UpdateRunUncoveredCapped
	}
	inRun := map[string]bool{}
	for _, it := range p.Items {
		lease.ItemHashes = append(lease.ItemHashes, it.URLHash)
		inRun[it.URLHash] = true
	}
	for _, a := range p.Accounts {
		lease.AccountIDs = append(lease.AccountIDs, a.AccountID)
	}
	sent := make([]model.UpdateItem, 0, len(p.Items))
	for _, it := range valid {
		if inRun[it.URLHash] {
			sent = append(sent, it)
		}
	}
	valid = sent
	if err := repo.CreateRunIfAbsent(ctx, userID, lease); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return runID, nil
		}
		return runID, err
	}
	if err := r.deps.Invoker.Invoke(ctx, runID, body); err != nil {
		return runID, r.startFailed(ctx, userID, runID, valid, err, stats)
	}
	stats.Started++
	log.Printf("customer update run started user=%s run=%s items=%d accounts=%d", userHashPrefix(userID), runID, len(p.Items), len(p.Accounts))
	return runID, nil
}

// startFailed handles a failed invoke. A definite refusal deletes the lease (a
// contract rejection also adds one attempt to every item of the batch, so a
// payload the runtime always rejects is bounded). Any other failure keeps the
// lease: a run that started is finalized; one that never started expires
// after the lease with one more attempt.
func (r *CustomerUpdateRuns) startFailed(ctx context.Context, userID, runID string, items []model.UpdateItem, err error, stats *RunStats) error {
	refused, contract := definiteRefusal(err)
	if !refused {
		stats.StartAmbiguous++
		log.Printf("customer update run start ambiguous user=%s run=%s err=%s", userHashPrefix(userID), runID, runErrorClass(err))
		return nil
	}
	stats.StartFailed++
	log.Printf("customer update run start failed user=%s run=%s err=%s", userHashPrefix(userID), runID, runErrorClass(err))
	if derr := r.deps.Repo.DeleteRunIfMatch(ctx, userID, runID); derr != nil {
		if errors.Is(derr, repository.ErrConditionFailed) {
			return nil
		}
		return derr
	}
	if contract {
		for i := range items {
			r.bumpItem(ctx, userID, &items[i], stats)
		}
	}
	return nil
}

// holdAll holds every item until the given time; the pending mark stays.
func (r *CustomerUpdateRuns) holdAll(ctx context.Context, userID string, items []model.UpdateItem, until time.Time, stats *RunStats) error {
	for _, it := range items {
		if err := r.holdItemUntil(ctx, userID, it.URLHash, until); err != nil {
			return err
		}
	}
	stats.Held += len(items)
	return nil
}

// noPacks handles a user whose build produced no pack and had no failed
// account (start holds those for one hour). Every in-scope account at today's
// cap: hold the items until the next KST day (the mark stays). Nothing in
// scope: skip the items (attempts unchanged).
func (r *CustomerUpdateRuns) noPacks(ctx context.Context, userID string, items []model.UpdateItem, build BuildStats, now time.Time, stats *RunStats) error {
	if build.Capped > 0 {
		return r.holdAll(ctx, userID, items, nextKSTDayStart(now), stats)
	}
	for _, it := range items {
		if err := r.setItem(ctx, userID, it.URLHash, model.UpdateItemSkipped, 0); err != nil {
			return err
		}
	}
	return nil
}
