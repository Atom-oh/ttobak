package main

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lt "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"testing"
	"time"
)

type boundedScanStub struct {
	input  *dynamodb.ScanInput
	output *dynamodb.ScanOutput
}

func (stub *boundedScanStub) Scan(_ context.Context, input *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	stub.input = input
	return stub.output, nil
}

func TestInvitationScanThrottlingHonorsOperationDeadline(t *testing.T) {
	client := &boundedScanStub{output: &dynamodb.ScanOutput{
		ConsumedCapacity: &dt.ConsumedCapacity{CapacityUnits: aws.Float64(100)},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, _, err := scanProjectInvitations(ctx, client, "table", nil, 20)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scan ignored its deadline: %v", err)
	}
	if !aws.ToBool(client.input.ConsistentRead) || aws.ToInt32(client.input.Limit) != 100 || client.input.ReturnConsumedCapacity != dt.ReturnConsumedCapacityTotal {
		t.Fatalf("scan lost authoritative bounded-page accounting: %+v", client.input)
	}
}

func TestInvitationScanPreservesPaginationAndRequiresCapacityAccounting(t *testing.T) {
	cursor := map[string]dt.AttributeValue{"PK": &dt.AttributeValueMemberS{Value: "next"}}
	client := &boundedScanStub{output: &dynamodb.ScanOutput{
		LastEvaluatedKey: cursor, ConsumedCapacity: &dt.ConsumedCapacity{CapacityUnits: aws.Float64(0)},
	}}
	_, next, err := scanProjectInvitations(context.Background(), client, "table", cursor, 20)
	if err != nil || len(next) != 1 || len(client.input.ExclusiveStartKey) != 1 {
		t.Fatalf("scan lost continuation: next=%v err=%v", next, err)
	}
	client.output.ConsumedCapacity = nil
	if _, _, err := scanProjectInvitations(context.Background(), client, "table", nil, 20); err == nil {
		t.Fatal("missing capacity accounting was accepted")
	}
	client.input = nil
	if _, _, err := scanProjectInvitations(context.Background(), client, "table", nil, 0); err == nil || client.input != nil {
		t.Fatal("invalid budget reached the database")
	}
}

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

func TestResumeReconcilesLostWriteResponse(t *testing.T) {
	for _, mode := range []string{"committed", "conflict", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			ops, _, control, removed, writes := fixtureOps(t)
			control.Enabled = false
			control.Revision = "paused"
			*removed = true
			original := ops.setFence
			ops.setFence = func(ctx context.Context, revision string, enabled bool) (repository.ProjectInvitationControl, error) {
				next, err := original(ctx, revision, enabled)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "conflict" {
					control.Revision = "another-operator"
				}
				if mode == "unreadable" {
					ops.fence = func(context.Context) (repository.ProjectInvitationControl, error) {
						return repository.ProjectInvitationControl{}, errors.New("read failed")
					}
				}
				return next, errors.New("response lost")
			}
			// Keep the fence closure dynamic to simulate readback becoming unavailable.
			read := ops.fence
			ops.fence = func(ctx context.Context) (repository.ProjectInvitationControl, error) {
				if mode == "unreadable" && *writes > 0 {
					return repository.ProjectInvitationControl{}, errors.New("read failed")
				}
				return read(ctx)
			}
			err := resume(context.Background(), ops, "reviewed", true)
			if (err == nil) != (mode == "committed") || !control.Enabled || *writes != 1 {
				t.Fatalf("mode=%s err=%v state=%+v writes=%d", mode, err, control, *writes)
			}
		})
	}
}
