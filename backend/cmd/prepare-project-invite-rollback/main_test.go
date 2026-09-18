package main

import (
	"context"
	"errors"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lt "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/ttobak/backend/internal/model"
	"testing"
	"time"
)

func TestPrepareRequiresCompatibleStableCodeAndKeepsDryRunReadOnly(t *testing.T) {
	for _, test := range []struct {
		hash      string
		stable    bool
		wantError bool
	}{{"old", true, true}, {"new", false, true}, {"new", true, false}} {
		paused, removed := false, false
		ops := operations{current: func(context.Context) (snapshot, error) { return snapshot{hash: test.hash, stable: test.stable}, nil }, pause: func(context.Context, string) (snapshot, error) { paused = true; return snapshot{}, nil }, scan: func(context.Context, map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error) {
			return nil, nil, nil
		}, remove: func(context.Context, *model.PendingShare) error { removed = true; return nil }}
		_, err := prepare(context.Background(), ops, "new", false)
		if (err != nil) != test.wantError || paused || removed {
			t.Fatalf("err=%v paused=%v removed=%v", err, paused, removed)
		}
	}
}
func TestPrepareWaitsForOldRequestsThenChecksEmptyQueueBeforeRollback(t *testing.T) {
	state := snapshot{hash: "new", revision: "before", stable: true, timeout: 30 * time.Second}
	waited := false
	removed := false
	p := model.PendingShare{PK: "PROJECT_INVITES#user@example.com", SK: "PENDING_PROJECT#p", Kind: model.PendingShareKindProject, ProjectID: "p", Email: "user@example.com", InvitedCognitoSub: "sub", CreatedAt: time.Now()}
	ops := operations{current: func(context.Context) (snapshot, error) { return state, nil }, pause: func(_ context.Context, revision string) (snapshot, error) {
		if revision != "before" {
			t.Fatal(revision)
		}
		state.paused = true
		state.revision = "paused"
		return state, nil
	}, wait: func(_ context.Context, d time.Duration) error {
		if d != 35*time.Second {
			t.Fatal(d)
		}
		waited = true
		return nil
	}, scan: func(context.Context, map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error) {
		if !waited {
			t.Fatal("readiness checked before old requests drained")
		}
		if removed {
			return nil, nil, nil
		}
		return []model.PendingShare{p}, nil, nil
	}, remove: func(_ context.Context, got *model.PendingShare) error {
		if !waited || got.InvitedCognitoSub != "sub" {
			t.Fatal("unsafe cleanup")
		}
		removed = true
		return nil
	}}
	count, err := prepare(context.Background(), ops, "new", true)
	if err != nil || count != 1 || !removed || !state.paused {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
func TestPrepareStopsIfWriterFenceChangesOrConditionalDeleteLeavesGrant(t *testing.T) {
	for _, changed := range []bool{false, true} {
		state := snapshot{hash: "new", revision: "before", stable: true, timeout: 30 * time.Second}
		removed := 0
		p := model.PendingShare{PK: "PROJECT_INVITES#user@example.com", SK: "PENDING_PROJECT#p", Kind: model.PendingShareKindProject, ProjectID: "p", Email: "user@example.com", InvitedCognitoSub: "sub", CreatedAt: time.Now()}
		ops := operations{current: func(context.Context) (snapshot, error) { return state, nil }, pause: func(context.Context, string) (snapshot, error) {
			state.paused = true
			state.revision = "paused"
			return state, nil
		}, wait: func(context.Context, time.Duration) error {
			if changed {
				state.revision = "external change"
			}
			return nil
		}, scan: func(context.Context, map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error) {
			return []model.PendingShare{p}, nil, nil
		}, remove: func(context.Context, *model.PendingShare) error { removed++; return nil }}
		_, err := prepare(context.Background(), ops, "new", true)
		if err == nil || (changed && removed != 0) {
			t.Fatalf("changed=%v removed=%d err=%v", changed, removed, err)
		}
	}
}
func TestPreparePropagatesPauseFailureWithoutDeleting(t *testing.T) {
	boom := errors.New("pause failed")
	ops := operations{current: func(context.Context) (snapshot, error) {
		return snapshot{hash: "new", revision: "r", stable: true}, nil
	}, pause: func(context.Context, string) (snapshot, error) { return snapshot{}, boom }}
	if _, err := prepare(context.Background(), ops, "new", true); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestPausePreservesEnvironmentAndRefusesUnreadableConfiguration(t *testing.T) {
	source := &lambda.GetFunctionConfigurationOutput{Environment: &lt.EnvironmentResponse{Variables: map[string]string{"KEEP": "value", "PROJECT_INVITATIONS_ENABLED": "true"}}}
	got, err := pausedEnvironment(source)
	if err != nil || got["KEEP"] != "value" || got["PROJECT_INVITATIONS_ENABLED"] != "false" || source.Environment.Variables["PROJECT_INVITATIONS_ENABLED"] != "true" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	source.Environment.Error = &lt.EnvironmentError{}
	if _, err := pausedEnvironment(source); err == nil {
		t.Fatal("unreadable environment must not be replaced")
	}
}
