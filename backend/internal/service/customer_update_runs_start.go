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

// fitRunPayload keeps the packs (in order, at most 40) that are each at most
// 8,000 bytes and drops accounts from the tail until the payload fits maxBytes.
func fitRunPayload(p runPayload, maxBytes int) ([]byte, runPayload, error) {
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
	for len(p.Accounts) > 0 {
		b, err := marshalRunPayload(p)
		if err != nil {
			return nil, p, err
		}
		if len(b) <= maxBytes {
			return b, p, nil
		}
		p.Accounts = p.Accounts[:len(p.Accounts)-1]
	}
	return nil, p, errRunNoAccountsFit
}

// definiteRefusal reports whether an invoke failure is a definite answer, so
// no run is running: a runtime answer that is not an acceptance, or an AWS
// API error response (access, validation, not found, throttling, any status).
// Ambiguous are failures without a service response (timeouts, context
// errors, transport errors), an unreadable or invalid answer
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
	if len(packs) == 0 {
		return "", r.noPacks(ctx, userID, valid, build, now, stats)
	}
	runID, err := r.newRunID()
	if err != nil {
		return "", err
	}
	p := runPayload{
		Version: 1, RunID: runID, Items: make([]runPayloadItem, 0, len(valid)), Accounts: packs,
		Budgets: runPayloadBudgets{
			MaxResearchPairs: min(maxRunResearchPairs, researchLeft),
			MaxDrafts:        min(maxRunDraftsBudget, maxDayDrafts-day.Total),
		},
	}
	lease := &model.UpdateRun{RunID: runID, StartedAt: now, LeaseUntil: now.Add(runLeaseDuration)}
	for _, it := range valid {
		p.Items = append(p.Items, runPayloadItem{URLHash: it.URLHash, URL: it.URL})
		lease.ItemHashes = append(lease.ItemHashes, it.URLHash)
	}
	body, p, err := fitRunPayload(p, r.maxPayloadBytes)
	if err != nil {
		return runID, err
	}
	for _, a := range p.Accounts {
		lease.AccountIDs = append(lease.AccountIDs, a.AccountID)
	}
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

// noPacks handles a user whose build produced no pack. Every in-scope account
// at today's cap: hold the items until the next KST day (the mark stays). A
// failed account read: hold the items for one hour. Nothing in scope: skip
// the items (attempts unchanged).
func (r *CustomerUpdateRuns) noPacks(ctx context.Context, userID string, items []model.UpdateItem, build BuildStats, now time.Time, stats *RunStats) error {
	switch {
	case build.Capped > 0:
		return r.holdAll(ctx, userID, items, nextKSTDayStart(now), stats)
	case build.Failed > 0:
		return r.holdAll(ctx, userID, items, now.Add(failedBuildHold), stats)
	}
	for _, it := range items {
		if err := r.setItem(ctx, userID, it.URLHash, model.UpdateItemSkipped, 0); err != nil {
			return err
		}
	}
	return nil
}
