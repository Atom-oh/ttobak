package main

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lt "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"testing"
	"time"
)

func fixtureOps(t *testing.T) (operations, *snapshot, *repository.ProjectInvitationControl, *bool, *int) {
	t.Helper()
	live := &snapshot{hash: "reviewed", revision: "alias-rev", version: "12", arn: "api:live", stable: true, configured: true}
	control := &repository.ProjectInvitationControl{Enabled: true}
	removed := false
	writes := 0
	p := model.PendingShare{PK: "PROJECT_INVITES#user@example.com", SK: "PENDING_PROJECT#p", Kind: model.PendingShareKindProject, ProjectID: "p", Email: "user@example.com", InvitedCognitoSub: "sub", CreatedAt: time.Now()}
	ops := operations{current: func(context.Context) (snapshot, error) { return *live, nil }, fence: func(context.Context) (repository.ProjectInvitationControl, error) { return *control, nil }, setFence: func(_ context.Context, expected string, enabled bool) (repository.ProjectInvitationControl, error) {
		if expected != control.Revision {
			return *control, errors.New("fence changed")
		}
		writes++
		control.Enabled = enabled
		control.Revision = "new-revision"
		return *control, nil
	}, scan: func(context.Context, map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error) {
		if removed {
			return nil, nil, nil
		}
		return []model.PendingShare{p}, nil, nil
	}, remove: func(context.Context, *model.PendingShare) error {
		if control.Enabled {
			t.Fatal("delete without fence")
		}
		removed = true
		return nil
	}}
	return ops, live, control, &removed, &writes
}
func TestDryRunAndWrongHashNeverMutate(t *testing.T) {
	ops, _, _, removed, writes := fixtureOps(t)
	if _, err := prepare(context.Background(), ops, "wrong", true); err == nil {
		t.Fatal("wrong code accepted")
	}
	if n, err := prepare(context.Background(), ops, "reviewed", false); err != nil || n != 1 || *removed || *writes != 0 {
		t.Fatalf("n=%d err=%v writes=%d", n, err, *writes)
	}
}
func TestPrepareFencesTransactionsAndResumeRequiresEmptyQueue(t *testing.T) {
	ops, _, control, removed, writes := fixtureOps(t)
	n, err := prepare(context.Background(), ops, "reviewed", true)
	if err != nil || n != 1 || !*removed || control.Enabled || *writes != 1 {
		t.Fatalf("n=%d err=%v fence=%+v", n, err, control)
	}
	if err := resume(context.Background(), ops, "reviewed", true); err != nil || !control.Enabled {
		t.Fatalf("resume err=%v", err)
	}
}
func TestAliasChangeOrLeftoverQueueRefusesReadiness(t *testing.T) {
	for _, aliasChange := range []bool{false, true} {
		ops, live, _, _, _ := fixtureOps(t)
		baseSet := ops.setFence
		ops.setFence = func(ctx context.Context, rev string, on bool) (repository.ProjectInvitationControl, error) {
			c, e := baseSet(ctx, rev, on)
			if aliasChange {
				live.revision = "changed"
			}
			return c, e
		}
		ops.remove = func(context.Context, *model.PendingShare) error { return nil }
		if _, err := prepare(context.Background(), ops, "reviewed", true); err == nil {
			t.Fatal("unsafe readiness")
		}
	}
}
func TestConcurrentResumeAndNonemptyQueueAreRejected(t *testing.T) {
	ops, _, control, _, _ := fixtureOps(t)
	control.Enabled = false
	control.Revision = "paused"
	if err := resume(context.Background(), ops, "reviewed", true); err == nil {
		t.Fatal("remaining grant could resurrect")
	}
	ops.scan = func(context.Context, map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error) {
		control.Revision = "external"
		return nil, nil, nil
	}
	if err := resume(context.Background(), ops, "reviewed", true); err == nil {
		t.Fatal("stale fence resumed")
	}
}
func TestServingSnapshotRequiresActualVersionAndRejectsWeightedAlias(t *testing.T) {
	alias := &lambda.GetAliasOutput{AliasArn: aws.String("api:live"), FunctionVersion: aws.String("12"), RevisionId: aws.String("r")}
	cfg := &lambda.GetFunctionConfigurationOutput{Version: aws.String("12"), CodeSha256: aws.String("hash"), State: lt.StateActive, Environment: &lt.EnvironmentResponse{Variables: map[string]string{"TABLE_NAME": "table", "PROJECT_INVITATIONS_ENABLED": "true"}}}
	if s, err := servingSnapshot(alias, cfg, "table"); err != nil || !s.stable || s.version != "12" {
		t.Fatalf("s=%+v err=%v", s, err)
	}
	cfg.Version = aws.String("$LATEST")
	if _, err := servingSnapshot(alias, cfg, "table"); err == nil {
		t.Fatal("latest substituted for serving version")
	}
	cfg.Version = aws.String("12")
	alias.RoutingConfig = &lt.AliasRoutingConfiguration{AdditionalVersionWeights: map[string]float64{"13": 0.1}}
	if _, err := servingSnapshot(alias, cfg, "table"); err == nil {
		t.Fatal("unfenced weighted traffic accepted")
	}
	alias.RoutingConfig = nil
	if _, err := servingSnapshot(alias, cfg, "wrong-table"); err == nil {
		t.Fatal("wrong table accepted")
	}
}
