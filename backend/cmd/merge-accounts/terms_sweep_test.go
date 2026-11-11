package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/repository"
)

// sweepingUsers adds the ADR-008 FU4 relink sweep to the user row fake.
type sweepingUsers struct {
	*fakeUsers
	swept []string
	err   error
}

func (s *sweepingUsers) SweepMeetingAccountTerms(_ context.Context, ownerID, meetingID string) (repository.TermsAccountSweep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swept = append(s.swept, ownerID+"/"+meetingID)
	appendLog(s.log, "sweep")
	return repository.TermsAccountSweep{Stale: 1, Dropped: 1}, s.err
}

// A moved link-only meeting's evidence leaves the source Account after the
// move; a published meeting is the worker's, and a failed sweep is counted
// without failing the row.
func TestMergeCLISweepsMovedLinkOnlyMeetingTerms(t *testing.T) {
	for _, sweepErr := range []error{nil, errors.New("throttled")} {
		h := newHarness()
		users := &sweepingUsers{fakeUsers: h.users, err: sweepErr}
		h.d.users = users
		o := opts()
		o.Apply, o.AcceptUnmoved = true, true
		res, err := run(context.Background(), h.d, o)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(users.swept, ",") != "u2/m1" {
			t.Fatalf("only the moved link-only meeting is swept: %v", users.swept)
		}
		if log := strings.Join(h.log, ","); !strings.Contains(log, "move,sweep") {
			t.Fatalf("the sweep must follow the move: %s", log)
		}
		if res.UserRows["linkOnlyMeetingsMoved"] != 1 {
			t.Fatalf("result %+v", res.UserRows)
		}
		if sweepErr == nil && (res.UserRows["termsAccountsDropped"] != 1 || res.UserRows["termsSweepFailed"] != 0) ||
			sweepErr != nil && (res.UserRows["termsSweepFailed"] != 1 || res.UserRows["errors"] != 0) {
			t.Fatalf("sweep counts %+v", res.UserRows)
		}
	}
}
