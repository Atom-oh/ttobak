package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// accountSweepRepo records relink sweeps (ADR-008 FU4) and the meeting's
// accountId seen when each ran, so a test can tell the sweep followed the
// committed link change.
type accountSweepRepo struct {
	*mockMeetingRepo
	swept    []string
	linkSeen []string
	err      error
}

func (r *accountSweepRepo) SweepMeetingAccountTerms(_ context.Context, ownerID, meetingID string) (repository.TermsAccountSweep, error) {
	r.swept = append(r.swept, ownerID+"/"+meetingID)
	if m := r.meetings[meetingKey(ownerID, meetingID)]; m != nil {
		r.linkSeen = append(r.linkSeen, m.AccountID)
	}
	return repository.TermsAccountSweep{Stale: 1, Dropped: 1}, r.err
}

func TestAccountLinkChangesSweepTheAccountsLeft(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		actor    string
		run      func(*MeetingService, string) error
		wantLink string
	}{
		{"creator links to another account", "owner-1", func(s *MeetingService, actor string) error {
			return s.LinkMeetingToAccount(ctx, actor, "m-1", "other")
		}, "other"},
		{"member reclassifies within the account", "member-1", func(s *MeetingService, actor string) error {
			return s.LinkMeetingToAccount(ctx, actor, "m-1", "acc")
		}, "acc"},
		{"creator publishes to another account", "owner-1", func(s *MeetingService, actor string) error {
			_, err := s.ShareMeetingToAccount(ctx, actor, "owner@example.com", "m-1", "other")
			return err
		}, "other"},
		{"member republishes", "member-1", func(s *MeetingService, actor string) error {
			_, err := s.ShareMeetingToAccount(ctx, actor, "member@example.com", "m-1", "acc")
			return err
		}, "acc"},
	} {
		for _, sweepErr := range []error{nil, errors.New("throttled")} {
			t.Run(tc.name, func(t *testing.T) {
				base := managementRepo()
				base.addMember("other", "owner-1", model.RoleSA)
				repo := &accountSweepRepo{mockMeetingRepo: base, err: sweepErr}
				if err := tc.run(newMeetingServiceWithRepo(repo), tc.actor); err != nil {
					t.Fatalf("a committed link change must not fail on its sweep: %v", err)
				}
				if len(repo.swept) != 1 || repo.swept[0] != "owner-1/m-1" {
					t.Fatalf("the owner's meeting must be swept once: %v", repo.swept)
				}
				if repo.linkSeen[0] != tc.wantLink {
					t.Fatalf("the sweep must run after the link commits: saw %q", repo.linkSeen[0])
				}
			})
		}
	}
}

func TestRefusedAccountLinkDoesNotSweep(t *testing.T) {
	ctx := context.Background()
	base := managementRepo()
	unpublished := *base.meetingsByID["m-1"]
	unpublished.SharedToAccount = false
	base.meetings[meetingKey("owner-1", "m-1")] = &unpublished
	repo := &accountSweepRepo{mockMeetingRepo: base}
	svc := newMeetingServiceWithRepo(repo)
	if err := svc.LinkMeetingToAccount(ctx, "member-1", "m-1", "acc"); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("link: %v", err)
	}
	if _, err := svc.ShareMeetingToAccount(ctx, "member-1", "member@example.com", "m-1", "acc"); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("share: %v", err)
	}
	if err := svc.LinkMeetingToAccount(ctx, "owner-1", "m-1", "not-a-member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member link: %v", err)
	}
	if len(repo.swept) != 0 {
		t.Fatalf("a refused link change must not sweep: %v", repo.swept)
	}
}

type sweepRecorder struct {
	calls []string
	err   error
}

func (r *sweepRecorder) SweepMeetingAccountTerms(_ context.Context, ownerID, meetingID string) (repository.TermsAccountSweep, error) {
	r.calls = append(r.calls, ownerID+"/"+meetingID)
	return repository.TermsAccountSweep{}, r.err
}

func TestSweepUnlinkedAccountTermsIsBestEffortAndDetached(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &sweepRecorder{err: errors.New("down")}
	var seen error
	probe := sweeperFunc(func(ctx context.Context, ownerID, meetingID string) (repository.TermsAccountSweep, error) {
		seen = ctx.Err()
		return rec.SweepMeetingAccountTerms(ctx, ownerID, meetingID)
	})
	if err := sweepUnlinkedAccountTerms(cancelled, probe, "link", "o", "m"); err == nil {
		t.Fatal("the failure is returned to callers that count it")
	}
	if seen != nil {
		t.Fatalf("the sweep must not inherit the caller's cancellation: %v", seen)
	}
	if err := sweepUnlinkedAccountTerms(context.Background(), struct{}{}, "link", "o", "m"); err != nil {
		t.Fatal("a repository without the sweep is a no-op")
	}
	if err := sweepUnlinkedAccountTerms(context.Background(), rec, "link", "", "m"); err != nil || len(rec.calls) != 1 {
		t.Fatalf("an empty owner is never swept: %v %v", err, rec.calls)
	}
	if !hasStaleTermsAccount([]string{"a2", "a1"}, "a2") || hasStaleTermsAccount([]string{"a2", ""}, "a2") || hasStaleTermsAccount(nil, "") {
		t.Fatal("hasStaleTermsAccount")
	}
}

// mergeSweepRepo adds the relink sweep to the merge mover fake.
type mergeSweepRepo struct {
	*fakeMoverRepo
	swept    []string
	linkSeen []string
	err      error
}

func (r *mergeSweepRepo) SweepMeetingAccountTerms(_ context.Context, ownerID, meetingID string) (repository.TermsAccountSweep, error) {
	r.swept = append(r.swept, ownerID+"/"+meetingID)
	r.linkSeen = append(r.linkSeen, r.meetings[meetingID].AccountID)
	return repository.TermsAccountSweep{Stale: 1}, r.err
}

// The merge retires the source and leaves its dictionary layer there (an
// unmoved category); each moved meeting's own evidence leaves it after the
// move, a resumed meeting is swept again (idempotent retry), a meeting the
// source no longer holds is not, and a sweep failure never fails the step.
func TestMoveMeetingsSweepsTheSourceAccountTerms(t *testing.T) {
	for _, sweepErr := range []error{nil, errors.New("throttled")} {
		base, _, job := meetingMoverFixture()
		repo := &mergeSweepRepo{fakeMoverRepo: base, err: sweepErr}
		s := &AccountMergeService{mover: repo}
		base.putMeeting(&model.Meeting{MeetingID: "m1", UserID: "author", AccountID: "src", SharedToAccount: true}, "src")
		base.putMeeting(&model.Meeting{MeetingID: "m2", UserID: "author", AccountID: "tgt", SharedToAccount: true}, "src") // interrupted after its move
		base.putMeeting(&model.Meeting{MeetingID: "m3", UserID: "author", AccountID: "elsewhere", SharedToAccount: true}, "src")
		res, err := s.moveMeetings(newMergeFence(context.Background()), job)
		if err != nil || res.Moved != 1 || res.Resumed != 1 || res.Skipped != 1 {
			t.Fatalf("result %+v err=%v", res, err)
		}
		swept := map[string]bool{}
		for _, s := range repo.swept {
			swept[s] = true
		}
		if len(repo.swept) != 2 || !swept["author/m1"] || !swept["author/m2"] {
			t.Fatalf("swept %v", repo.swept)
		}
		for _, seen := range repo.linkSeen {
			if seen != "tgt" {
				t.Fatalf("the sweep must follow the committed move: %v", repo.linkSeen)
			}
		}
	}
}

// relinkTermsRepo is termsRepoFake with a refusable RecordTermsAccount and
// the relink sweep; sweeps lists the run state's accountIds at each sweep.
type relinkTermsRepo struct {
	*termsRepoFake
	recordErr error
	sweeps    []string
}

func (r *relinkTermsRepo) RecordTermsAccount(ctx context.Context, ownerID, meetingID, runID, accountID string, keys []string) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	return r.termsRepoFake.RecordTermsAccount(ctx, ownerID, meetingID, runID, accountID, keys)
}

func (r *relinkTermsRepo) SweepMeetingAccountTerms(context.Context, string, string) (repository.TermsAccountSweep, error) {
	r.sweeps = append(r.sweeps, strings.Join(r.state.AccountIDs, ","))
	return repository.TermsAccountSweep{Stale: 1, Dropped: 1}, nil
}

// relinkTermsRun runs one Process for a meeting now linked to a1 whose run
// state already records prior Accounts.
func relinkTermsRun(t *testing.T, prior []string, recordErr error, prep func(partitionedDictStore)) (partitionedDictStore, *relinkTermsRepo, string) {
	t.Helper()
	logs := capturePrivacyLogs(t)
	meeting := termsTestMeeting()
	meeting.AccountID = "a1"
	state := queuedTermsState(meeting)
	state.AccountIDs = prior
	base := &termsRepoFake{meeting: meeting, state: state}
	repo := &relinkTermsRepo{termsRepoFake: base, recordErr: recordErr}
	store := partitionedDictStore{}
	store.at("ACCOUNT#a1").members = map[string]bool{"a1/owner": true}
	if prep != nil {
		prep(store)
	}
	s, _ := newTermsTestService(base, &termsModelFake{outputs: []string{termsGoodOutput}}, NewTermsMerger(store), termsAccountOn)
	s.repo = repo
	s.SetAccountMemberLister(func(context.Context, string) ([]string, error) { return []string{"owner"}, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.Process(ctx, model.TermsRequested{OwnerID: "owner", MeetingID: "m1", RunID: "run-1"}); err != nil || base.completed == nil {
		t.Fatalf("err=%v completed=%v failed=%s", err, base.completed, base.failed)
	}
	return store, repo, logs.String()
}

// Race: the run read accountId a1, then the meeting was relinked. The record
// and every Account write are conditioned on the link, so the run writes
// nothing to a1 and logs UNLINKED; the personal run still succeeds.
func TestProcessAccountLayerRefusedAfterRelink(t *testing.T) {
	t.Run("record refused", func(t *testing.T) {
		store, repo, logs := relinkTermsRun(t, nil, repository.ErrConditionFailed, nil)
		if acct := store.at("ACCOUNT#a1"); len(acct.terms) != 0 || len(acct.obs) != 0 {
			t.Fatal("a refused record must stop the Account merge")
		}
		if !strings.Contains(logs, "layer=account outcome=UNLINKED") || len(repo.sweeps) != 0 {
			t.Fatalf("logs=%s sweeps=%v", logs, repo.sweeps)
		}
	})
	t.Run("observation write refused", func(t *testing.T) {
		store, _, logs := relinkTermsRun(t, nil, nil, func(p partitionedDictStore) {
			p.at("ACCOUNT#a1").failOn["create"] = fmt.Errorf("failed to merge: %w", repository.ErrDictSourceMeetingGone)
		})
		if acct := store.at("ACCOUNT#a1"); len(acct.terms) != 0 {
			t.Fatal("no Account term may be written")
		}
		if !strings.Contains(logs, "layer=account outcome=UNLINKED") || strings.Contains(logs, "outcome=FAILED") {
			t.Fatalf("logs=%s", logs)
		}
	})
}

// A run retries an earlier relink sweep for Accounts other than the current
// one, after its own record; a meeting that only ever used its current
// Account is not swept.
func TestProcessRetriesTheRelinkSweep(t *testing.T) {
	_, repo, _ := relinkTermsRun(t, []string{"old"}, nil, nil)
	if len(repo.sweeps) != 1 || repo.sweeps[0] != "old,a1" {
		t.Fatalf("one sweep after the run's own record expected: %v", repo.sweeps)
	}
	_, repo, _ = relinkTermsRun(t, []string{"a1"}, nil, nil)
	if len(repo.sweeps) != 0 {
		t.Fatalf("nothing stale, nothing swept: %v", repo.sweeps)
	}
}

type sweeperFunc func(context.Context, string, string) (repository.TermsAccountSweep, error)

func (f sweeperFunc) SweepMeetingAccountTerms(ctx context.Context, ownerID, meetingID string) (repository.TermsAccountSweep, error) {
	return f(ctx, ownerID, meetingID)
}
