package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const cliOwner = "cli-host-42"

type fakeSvc struct {
	calls     []string
	startReq  *model.StartAccountMergeRequest
	startErr  error
	preview   *model.AccountMergePreview
	status    *model.AccountMergeJobDTO
	processed int
	// finishAfter is the Process call that moves the job to final.
	finishAfter int
	final       *model.AccountMergeJob
	processErr  error
	owners      []string
	exactOwners []string
	jobs        *fakeJobs
}

func (f *fakeSvc) Preview(_ context.Context, requester, from, into string) (*model.AccountMergePreview, error) {
	f.calls = append(f.calls, "preview")
	return f.preview, nil
}
func (f *fakeSvc) Status(_ context.Context, user, from string) (*model.AccountMergeJobDTO, error) {
	f.calls = append(f.calls, "status")
	return f.status, nil
}
func (f *fakeSvc) Start(_ context.Context, requester, from string, req *model.StartAccountMergeRequest) (*model.AccountMergeJobDTO, error) {
	f.calls = append(f.calls, "start")
	f.startReq = req
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &model.AccountMergeJobDTO{FromAccountID: from, IntoAccountID: req.IntoAccountID, State: model.MergeStateRunning, Step: "members"}, nil
}
func (f *fakeSvc) ProcessAccountMergeRequested(ctx context.Context, e model.AccountMergeRequested, deadline time.Time) error {
	f.calls = append(f.calls, "process")
	f.exactOwners = append(f.exactOwners, service.MergeWorkerOwner(ctx))
	if lc, ok := lambdacontext.FromContext(ctx); ok {
		f.owners = append(f.owners, lc.AwsRequestID)
	}
	f.processed++
	if f.processed >= f.finishAfter && f.final != nil {
		f.jobs.job = f.final
	} else if f.jobs.job != nil {
		j := *f.jobs.job
		j.UpdatedAt = j.UpdatedAt.Add(time.Second)
		f.jobs.job = &j
	}
	return f.processErr
}

type fakeJobs struct {
	job      *model.AccountMergeJob
	claims   []string
	released int
	failed   []string
	claimErr error
	log      *[]string
}

func (f *fakeJobs) GetAccountMergeJob(context.Context, string) (*model.AccountMergeJob, error) {
	return f.job, nil
}
func (f *fakeJobs) ClaimMergeLease(_ context.Context, source, owner string, _ time.Time, _ time.Duration) (*model.AccountMergeJob, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	f.claims = append(f.claims, owner)
	*f.log = append(*f.log, "claim")
	return &model.AccountMergeJob{SourceAccountID: source, TargetAccountID: "tgt", RequesterUserID: "owner-1", State: model.MergeStateRunning, LeaseOwner: owner}, nil
}
func (f *fakeJobs) FailMergeJob(_ context.Context, _ *model.AccountMergeJob, code string) error {
	f.failed = append(f.failed, code)
	*f.log = append(*f.log, "fail:"+code)
	return nil
}
func (f *fakeJobs) ReleaseMergeLease(context.Context, *model.AccountMergeJob) error {
	f.released++
	*f.log = append(*f.log, "release")
	return nil
}

type fakeUsers struct {
	mu        sync.Mutex
	rows      []repository.MergeUserRow // returned for segment 0 only
	invalid   int
	segments  map[int]bool
	moves     []string
	cipher    string
	refs      []*model.MeetingRef
	failTask  error
	skipTask  int // the next n task moves fail their version condition
	fence     error
	fenceTask error
	log       *[]string
}

func (f *fakeUsers) ScanMergeUserRowsSegmentPage(_ context.Context, account string, segment, segments int, cursor string) (*repository.MergeUserRowsPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if account != "src" || segments != scanSegments {
		return nil, errors.New("unexpected scan")
	}
	if f.segments == nil {
		f.segments = map[int]bool{}
	}
	f.segments[segment] = true
	if segment != 0 {
		return &repository.MergeUserRowsPage{}, nil
	}
	if cursor == "" {
		return &repository.MergeUserRowsPage{Rows: f.rows[:1], NextCursor: "next", Invalid: f.invalid}, nil
	}
	return &repository.MergeUserRowsPage{Rows: f.rows[1:]}, nil
}
func (f *fakeUsers) MoveSpecReqTaskRowForMerge(_ context.Context, job *model.AccountMergeJob, row repository.MergeUserRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if job == nil || job.LeaseOwner != cliOwner {
		return errors.New("task moved without the CLI lease")
	}
	f.moves = append(f.moves, "task:"+job.SourceAccountID+">"+job.TargetAccountID)
	if f.fenceTask != nil {
		return f.fenceTask
	}
	if f.skipTask > 0 {
		f.skipTask--
		return repository.ErrConditionFailed
	}
	return f.failTask
}
func (f *fakeUsers) MoveCalendarEventRowForMerge(_ context.Context, job *model.AccountMergeJob, row repository.MergeUserRow, ciphertext string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if job == nil || job.LeaseOwner != cliOwner {
		return errors.New("event moved without the CLI lease")
	}
	f.moves = append(f.moves, "event:"+job.SourceAccountID+">"+job.TargetAccountID)
	f.cipher = ciphertext
	return nil
}
func (f *fakeUsers) MoveMeetingForAccountMerge(_ context.Context, job *model.AccountMergeJob, owner, meetingID string, ref *model.MeetingRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if job == nil || job.LeaseOwner != cliOwner {
		return errors.New("meeting moved without the CLI lease")
	}
	f.moves = append(f.moves, "meeting:"+meetingID)
	f.refs = append(f.refs, ref)
	return f.fence
}

// fakeCipher "seals" by prefixing, so the test can read the rewritten payload.
type fakeCipher struct{}

func (fakeCipher) Decrypt(_ context.Context, _, _, _, ciphertext string) ([]byte, error) {
	return []byte(strings.TrimPrefix(ciphertext, "sealed:")), nil
}
func (fakeCipher) Encrypt(_ context.Context, _, _, _ string, plaintext []byte) (string, error) {
	return "sealed:" + string(plaintext), nil
}

type harness struct {
	d     deps
	svc   *fakeSvc
	jobs  *fakeJobs
	users *fakeUsers
	log   []string
}

func newHarness() *harness {
	h := &harness{}
	h.jobs = &fakeJobs{log: &h.log}
	h.svc = &fakeSvc{jobs: h.jobs, finishAfter: 2,
		final: &model.AccountMergeJob{SourceAccountID: "src", TargetAccountID: "tgt", State: model.MergeStateDone, Step: "finish", Counts: map[string]int{"members.moved": 3}},
		preview: &model.AccountMergePreview{Counts: map[string]int{"meetings": 5}, RequiresAcceptUnmoved: true,
			Unmoved:     []model.MergeUnmovedCategory{{Category: "kb", Present: true}, {Category: "documents"}},
			NewAudience: model.MergeAudience{IntoMembersNotInFrom: 2, IntoHasAncestors: true}}}
	h.jobs.job = &model.AccountMergeJob{SourceAccountID: "src", TargetAccountID: "tgt", State: model.MergeStateRunning, Step: "members", UpdatedAt: time.Unix(100, 0)}
	payload := `{"externalId":"x","title":"t","startsAt":"2026-11-01T00:00:00Z","endsAt":"2026-11-01T01:00:00Z","timeZone":"UTC","accountId":"src","shareAccountId":"shared","candidateAccountIds":["src","other","tgt"]}`
	h.users = &fakeUsers{log: &h.log, rows: []repository.MergeUserRow{
		{Kind: repository.MergeUserRowSpecReqTask, PK: "USER#u1", SK: "SRTASK#e1", UserID: "u1", EventID: "e1", Version: 2},
		{Kind: repository.MergeUserRowCalendar, PK: "USER#u1", SK: "CALEVENT#s1#e2", UserID: "u1", SourceID: "s1", EventID: "e2", Ciphertext: "sealed:" + payload, Revision: 4, ShareAccountID: "shared"},
		{Kind: repository.MergeUserRowMeeting, PK: "USER#u2", SK: "MEETING#m1", UserID: "u2", MeetingID: "m1", Date: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{Kind: repository.MergeUserRowMeeting, PK: "USER#u2", SK: "MEETING#m2", UserID: "u2", MeetingID: "m2", SharedToAccount: true},
	}}
	h.d = deps{svc: h.svc, jobs: h.jobs, members: members{"tgt:owner-1": true}, users: h.users, cipher: fakeCipher{}, owner: cliOwner,
		now: func() time.Time { return time.Unix(1000, 0) }, pause: func(context.Context, time.Duration) error { return nil }}
	return h
}

type members map[string]bool

func (m members) GetMember(_ context.Context, account, user string) (*model.AccountMember, error) {
	if m[account+":"+user] {
		return &model.AccountMember{AccountID: account, UserID: user}, nil
	}
	return nil, nil
}

func opts() options {
	return options{From: "src", Into: "tgt", Requester: "owner-1", ScanUsers: true, MaxPages: 10}
}

func TestMergeCLIDryRunWritesNothing(t *testing.T) {
	h := newHarness()
	res, err := run(context.Background(), h.d, opts())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.svc.calls, ",") != "preview" || len(h.jobs.claims) != 0 || h.jobs.released != 0 || len(h.users.moves) != 0 {
		t.Fatalf("dry run wrote: calls=%v claims=%v moves=%v", h.svc.calls, h.jobs.claims, h.users.moves)
	}
	if res.NewAudience == nil || res.NewAudience.IntoMembersNotInFrom != 2 || !res.NewAudience.IntoHasAncestors {
		t.Fatalf("dry run must disclose the new audience: %+v", res.NewAudience)
	}
	if res.Mode != "dry-run" || res.Counts["meetings"] != 5 || !res.RequiresAcceptUnmoved || len(res.UnmovedPresent) != 1 || res.UnmovedPresent[0] != "kb" {
		t.Fatalf("preview not reported: %+v", res)
	}
	want := map[string]int{"specreqTasks": 1, "calendarEvents": 1, "linkOnlyMeetings": 1, "publishedMeetings": 1}
	for k, v := range want {
		if res.UserRows[k] != v {
			t.Errorf("userRows[%s] = %d, want %d (%v)", k, res.UserRows[k], v, res.UserRows)
		}
	}
	for segment := 0; segment < scanSegments; segment++ {
		if !h.users.segments[segment] {
			t.Errorf("segment %d not scanned", segment)
		}
	}
	// Output is counts only: no user, event, meeting or account row identifiers.
	var out bytes.Buffer
	writeResult(&out, res)
	for _, forbidden := range []string{"u1", "u2", "e1", "e2", "m1", "m2", "owner-1", "sealed"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("output leaks %q: %s", forbidden, out.String())
		}
	}
}

func TestMergeCLIRequiresRequester(t *testing.T) {
	for name, mutate := range map[string]func(*options){
		"missing requester": func(o *options) { o.Requester = "" },
		"bad requester":     func(o *options) { o.Requester = "a#b" },
		"missing from":      func(o *options) { o.From = "" },
		"from equals into":  func(o *options) { o.Into = o.From },
		"zero max pages":    func(o *options) { o.MaxPages = 0 },
	} {
		for _, apply := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s apply=%v", name, apply), func(t *testing.T) {
				h := newHarness()
				o := opts()
				o.Apply = apply
				mutate(&o)
				if _, err := run(context.Background(), h.d, o); err == nil || errorCode(err) != "INVALID_INPUT" {
					t.Fatalf("got %v", err)
				}
				if len(h.svc.calls) != 0 || len(h.users.moves) != 0 || len(h.jobs.claims) != 0 {
					t.Fatalf("invalid options reached services: %v", h.svc.calls)
				}
			})
		}
	}
}

func TestMergeCLIStatusNeedsOnlySourceAndRequester(t *testing.T) {
	h := newHarness()
	h.svc.status = &model.AccountMergeJobDTO{FromAccountID: "src", IntoAccountID: "tgt", State: model.MergeStateFailed, Step: "meetings", ErrorCode: service.MergeErrorRequesterRevoked, Counts: map[string]int{"meetings.moved": 4}}
	res, err := run(context.Background(), h.d, options{From: "src", Requester: "owner-1", Status: true, MaxPages: 1})
	if err != nil || res.Mode != "status" || res.State != model.MergeStateFailed || res.ErrorCode != "REQUESTER_REVOKED" || res.Into != "tgt" {
		t.Fatalf("status %+v / %v", res, err)
	}
	if _, err := run(context.Background(), h.d, options{From: "src", Status: true, MaxPages: 1}); err == nil {
		t.Fatal("status without requester must fail")
	}
}

func TestMergeCLIApplyRewritesUserRowsUnderLeaseThenRunsWorker(t *testing.T) {
	h := newHarness()
	o := opts()
	o.Apply, o.AcceptUnmoved = true, true
	res, err := run(context.Background(), h.d, o)
	if err != nil {
		t.Fatal(err)
	}
	if !h.svc.startReq.AcceptUnmoved || h.svc.startReq.IntoAccountID != "tgt" {
		t.Fatalf("start request %+v", h.svc.startReq)
	}
	if got := strings.Join(h.svc.calls, ","); got != "start,process,process" {
		t.Fatalf("calls %s", got)
	}
	// The scan lease is never released before the worker runs: the worker
	// claims as exactly the CLI owner and continues from it.
	if got := strings.Join(h.log, ","); got != "claim" || h.jobs.claims[0] != cliOwner {
		t.Fatalf("lease log %q owners %v", got, h.jobs.claims)
	}
	for _, owner := range h.svc.exactOwners {
		if owner != cliOwner {
			t.Errorf("worker must claim as the CLI owner, got %q", owner)
		}
	}
	for _, owner := range h.svc.owners {
		if !strings.HasPrefix(owner, cliOwner) {
			t.Errorf("worker owner %q", owner)
		}
	}
	moves := strings.Join(h.users.moves, " ")
	for _, want := range []string{"task:src>tgt", "event:src>tgt", "meeting:m1"} {
		if !strings.Contains(moves, want) {
			t.Errorf("missing move %q in %q", want, moves)
		}
	}
	if strings.Contains(moves, "meeting:m2") {
		t.Error("a published meeting must be left to the worker")
	}
	ref := h.users.refs[0]
	if ref.PK != "ACCOUNT#src" || ref.SK != "MEETINGREF#2026-11-01T00:00:00Z#m1" || ref.OwnerUserID != "u2" {
		t.Errorf("link-only meeting ref %+v", ref)
	}
	var event model.CalendarEventInput
	if err := json.Unmarshal([]byte(strings.TrimPrefix(h.users.cipher, "sealed:")), &event); err != nil {
		t.Fatal(err)
	}
	if event.AccountID != "tgt" || event.ShareAccountID != "shared" || strings.Join(event.CandidateAccountIDs, ",") != "other,tgt" {
		t.Errorf("event payload not rewritten: %+v", event)
	}
	if res.State != model.MergeStateDone || res.Counts["members.moved"] != 3 || res.UserRows["specreqTasksMoved"] != 1 ||
		res.UserRows["calendarEventsMoved"] != 1 || res.UserRows["linkOnlyMeetingsMoved"] != 1 || res.UserRows["publishedMeetings"] != 1 {
		t.Fatalf("result %+v", res)
	}
}

func TestMergeCLIApplyWithoutScanOnlyStartsAndDrives(t *testing.T) {
	h := newHarness()
	o := opts()
	o.Apply, o.ScanUsers = true, false
	if _, err := run(context.Background(), h.d, o); err != nil {
		t.Fatal(err)
	}
	if len(h.jobs.claims) != 0 || len(h.users.moves) != 0 || strings.Join(h.svc.calls, ",") != "start,process,process" {
		t.Fatalf("calls %v claims %v moves %v", h.svc.calls, h.jobs.claims, h.users.moves)
	}
}

func TestMergeCLIScanErrorsKeepWorkerStopped(t *testing.T) {
	h := newHarness()
	h.users.failTask = errors.New("boom")
	o := opts()
	o.Apply = true
	res, err := run(context.Background(), h.d, o)
	if !errors.Is(err, errScanIncomplete) || errorCode(err) != "SCAN_INCOMPLETE" {
		t.Fatalf("got %v", err)
	}
	if res.UserRows["errors"] != 1 || strings.Contains(strings.Join(h.svc.calls, ","), "process") || h.jobs.released != 0 ||
		strings.Join(h.jobs.failed, ",") != scanIncompleteCode {
		t.Fatalf("a failed scan must fail the job, not release it, and not run the worker: %+v calls=%v released=%d failed=%v", res, h.svc.calls, h.jobs.released, h.jobs.failed)
	}
}

func TestMergeCLIChangedRowsAreSkippedAndFenceAbortsScan(t *testing.T) {
	h := newHarness()
	h.users.failTask = repository.ErrConditionFailed
	o := opts()
	o.Apply = true
	res, err := run(context.Background(), h.d, o)
	if errorCode(err) != "SCAN_INCOMPLETE" || res.UserRows["specreqTasksSkipped"] != 1 || res.UserRows["errors"] != 0 ||
		h.jobs.released != 0 || len(h.jobs.claims) != 1 || strings.Join(h.jobs.failed, ",") != scanIncompleteCode || strings.Contains(strings.Join(h.svc.calls, ","), "process") {
		t.Fatalf("a row still skipped after every pass must fail the job under one held lease: %+v / %v released=%d claims=%d failed=%v calls=%v", res, err, h.jobs.released, len(h.jobs.claims), h.jobs.failed, h.svc.calls)
	}

	// A row skipped once is picked up by the next pass and the worker runs.
	h = newHarness()
	h.users.skipTask = 1
	res, err = run(context.Background(), h.d, o)
	if err != nil || res.UserRows["specreqTasksSkipped"] != 0 || res.UserRows["specreqTasksMoved"] < 1 || !strings.Contains(strings.Join(h.svc.calls, ","), "process") {
		t.Fatalf("a re-scan must pick up a once-skipped row: %+v / %v calls=%v", res, err, h.svc.calls)
	}

	h = newHarness()
	h.users.fence = repository.ErrMergeFenceLost
	res, err = run(context.Background(), h.d, o)
	if errorCode(err) != "FENCE_LOST" || h.jobs.released != 1 || len(h.jobs.failed) != 0 || strings.Contains(strings.Join(h.svc.calls, ","), "process") {
		t.Fatalf("lost fence must abort before the worker: %v released=%d calls=%v", err, h.jobs.released, h.svc.calls)
	}
}

func TestMergeCLIRequesterRevokedJobIsReported(t *testing.T) {
	h := newHarness()
	h.svc.final = &model.AccountMergeJob{SourceAccountID: "src", TargetAccountID: "tgt", State: model.MergeStateFailed, Step: "meetings",
		ErrorCode: service.MergeErrorRequesterRevoked, Counts: map[string]int{"meetings.moved": 2}}
	o := opts()
	o.Apply, o.ScanUsers = true, false
	res, err := run(context.Background(), h.d, o)
	if err == nil || res.State != model.MergeStateFailed || res.ErrorCode != "REQUESTER_REVOKED" || res.Step != "meetings" || res.Counts["meetings.moved"] != 2 {
		t.Fatalf("failed job not reported: %+v / %v", res, err)
	}
}

func TestMergeCLIStartRefusalsKeepTheirCodes(t *testing.T) {
	for want, startErr := range map[string]error{
		"FORBIDDEN":               service.ErrForbidden,
		"INELIGIBLE":              fmt.Errorf("%w: x", service.ErrAccountMergeIneligible),
		"JOB_RUNNING":             service.ErrMergeJobRunning,
		"START_CONFLICT":          service.ErrAccountMergeConflict,
		"CONTACT_INDEX_TOO_LARGE": service.ErrMergeContactIndexTooLarge,
		"INVALID_INPUT":           service.ErrInvalidInput,
		"FAILED":                  errors.New("sdk detail with ids"),
	} {
		h := newHarness()
		h.svc.startErr = startErr
		o := opts()
		o.Apply = true
		if _, err := run(context.Background(), h.d, o); errorCode(err) != want {
			t.Errorf("%v -> %q, want %q", startErr, errorCode(err), want)
		}
		if len(h.jobs.claims) != 0 || len(h.users.moves) != 0 {
			t.Errorf("a refused start must not scan or write")
		}
	}
}

func TestMergeCLIWorkerWithoutProgressStops(t *testing.T) {
	h := newHarness()
	h.svc.final = nil
	// Process leaves UpdatedAt alone: another worker holds the lease.
	h.svc.jobs = &fakeJobs{job: h.jobs.job, log: &h.log}
	h.d.jobs = h.svc.jobs
	o := opts()
	o.Apply, o.ScanUsers = true, false
	h.d.svc = &stuckSvc{fakeSvc: h.svc}
	if _, err := run(context.Background(), h.d, o); !errors.Is(err, errNoProgress) {
		t.Fatalf("got %v", err)
	}
}

type stuckSvc struct{ *fakeSvc }

func (s *stuckSvc) ProcessAccountMergeRequested(context.Context, model.AccountMergeRequested, time.Time) error {
	return nil
}

func calendarPayload(account, share string, candidates ...string) string {
	extra := ""
	if share != "" {
		extra += `,"shareAccountId":"` + share + `"`
	}
	if len(candidates) > 0 {
		extra += `,"candidateAccountIds":["` + strings.Join(candidates, `","`) + `"]`
	}
	return `{"externalId":"x","title":"a <b> & c","startsAt":"2026-11-01T00:00:00Z","endsAt":"2026-11-01T01:00:00Z","timeZone":"UTC","accountId":"` + account + `"` + extra + `}`
}

func TestRewriteCalendarPayload(t *testing.T) {
	cases := []struct {
		name, payload, share string
		candidates           []string
	}{
		{"account only", calendarPayload("src", ""), "", nil},
		// shareAccountId stays A (its CALSHARE# row stays on A); candidate A becomes B.
		{"share A, candidates [B]", calendarPayload("src", "src", "tgt"), "src", []string{"tgt"}},
		// Replacing candidate A with B would equal the share account: dropped.
		{"share B, candidates [A]", calendarPayload("src", "tgt", "src"), "tgt", nil},
		{"candidates deduplicated", calendarPayload("src", "", "src", "tgt", "other"), "", []string{"tgt", "other"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := rewriteCalendarPayload([]byte(tc.payload), "src", "tgt")
			if err != nil {
				t.Fatal(err)
			}
			var event model.CalendarEventInput
			if err := json.Unmarshal(out, &event); err != nil {
				t.Fatal(err)
			}
			if event.AccountID != "tgt" || event.ShareAccountID != tc.share || !strings.Contains(string(out), "a <b> & c") {
				t.Fatalf("payload %s", out)
			}
			got := append([]string(nil), event.CandidateAccountIDs...)
			want := append([]string(nil), tc.candidates...)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("candidates %v, want %v", got, want)
			}
			// The result passes the stored-payload normalization the reader applies.
			if _, err := service.NormalizeCalendarPayload(event); err != nil {
				t.Errorf("reader would reject the rewritten payload: %v", err)
			}
		})
	}
	for name, payload := range map[string]string{
		"other account":  calendarPayload("zzz", ""),
		"no account":     strings.Replace(calendarPayload("src", ""), `,"accountId":"src"`, "", 1),
		"unknown field":  strings.Replace(calendarPayload("src", ""), `"accountId":"src"`, `"accountId":"src","extra":1`, 1),
		"trailing value": calendarPayload("src", "") + ` {}`,
		"not json":       "nope",
	} {
		if _, err := rewriteCalendarPayload([]byte(payload), "src", "tgt"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMergeCLIVerifyIsReadOnlyAndChecksTargetMembership(t *testing.T) {
	h := newHarness()
	o := opts()
	o.Verify = true
	res, err := run(context.Background(), h.d, o)
	if err != nil || res.Mode != "verify" || res.UserRows["specreqTasks"] != 1 || res.UserRows["linkOnlyMeetings"] != 1 {
		t.Fatalf("verify %+v / %v", res, err)
	}
	if len(h.svc.calls) != 0 || len(h.users.moves) != 0 || len(h.jobs.claims) != 0 {
		t.Fatalf("verify wrote or previewed: %v %v", h.svc.calls, h.users.moves)
	}
	o.Requester = "stranger"
	if _, err := run(context.Background(), h.d, o); errorCode(err) != "FORBIDDEN" {
		t.Fatalf("non-member verify: %v", err)
	}
	o.Requester, o.Apply = "owner-1", true
	if _, err := run(context.Background(), h.d, o); errorCode(err) != "INVALID_INPUT" {
		t.Fatalf("verify with apply: %v", err)
	}
}

func TestMergeCLICountsScanErrorsByReason(t *testing.T) {
	h := newHarness()
	h.d.cipher = brokenCipher{}
	o := opts()
	o.Apply = true
	res, _ := run(context.Background(), h.d, o)
	if res.UserRows["errors"] != 1 || res.UserRows["errors.decrypt"] != 1 {
		t.Fatalf("errors by reason: %v", res.UserRows)
	}
}

type brokenCipher struct{ fakeCipher }

func (brokenCipher) Decrypt(context.Context, string, string, string, string) ([]byte, error) {
	return nil, errors.New("kms")
}

func TestMergeCLIFenceLostOnTaskAbortsScan(t *testing.T) {
	h := newHarness()
	h.users.fenceTask = repository.ErrMergeFenceLost
	o := opts()
	o.Apply = true
	_, err := run(context.Background(), h.d, o)
	if errorCode(err) != "FENCE_LOST" || strings.Contains(strings.Join(h.svc.calls, ","), "process") {
		t.Fatalf("got %v calls %v", err, h.svc.calls)
	}
}

func TestLeaseOwnerShape(t *testing.T) {
	if got := leaseOwner("my host/1", 77); got != "cli-my_host_1-77" {
		t.Fatalf("got %q", got)
	}
}
