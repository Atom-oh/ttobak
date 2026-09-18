// prepare-project-invite-rollback is an operator command, not a Lambda entrypoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lt "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type snapshot struct {
	hash, revision string
	paused, stable bool
	timeout        time.Duration
}
type operations struct {
	current func(context.Context) (snapshot, error)
	pause   func(context.Context, string) (snapshot, error)
	wait    func(context.Context, time.Duration) error
	scan    func(context.Context, map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error)
	remove  func(context.Context, *model.PendingShare) error
}

func samePaused(a, b snapshot) bool {
	return b.stable && b.paused && a.hash == b.hash && a.revision == b.revision
}

// Drain only after the reviewed compatible API has stopped accepting writers
// and all requests from the old environment have exceeded their Lambda budget.
func prepare(ctx context.Context, ops operations, expectedHash string, apply bool) (int, error) {
	initial, err := ops.current(ctx)
	if err != nil {
		return 0, err
	}
	if expectedHash == "" || initial.hash != expectedHash || !initial.stable {
		return 0, errors.New("reviewed API code hash or update state does not match")
	}
	observed := initial
	if apply {
		observed, err = ops.pause(ctx, initial.revision)
		if err != nil {
			return 0, err
		}
		if observed.hash != expectedHash || !observed.paused || !observed.stable || observed.timeout <= 0 || observed.timeout > 15*time.Minute {
			return 0, errors.New("compatible API was not paused")
		}
		if err := ops.wait(ctx, observed.timeout+5*time.Second); err != nil {
			return 0, err
		}
	}
	check := func() error {
		current, err := ops.current(ctx)
		if err != nil {
			return err
		}
		if !samePaused(observed, current) {
			return errors.New("API configuration changed during drain; stop rollback")
		}
		return nil
	}
	count := 0
	var cursor map[string]dt.AttributeValue
	for {
		if apply {
			if err := check(); err != nil {
				return count, err
			}
		}
		rows, next, err := ops.scan(ctx, cursor)
		if err != nil {
			return count, err
		}
		for i := range rows {
			p := &rows[i]
			if p.Kind != model.PendingShareKindProject || p.PK != model.PrefixProjectInvites+p.Email || p.SK != model.PrefixPendingProject+p.ProjectID || p.InvitedCognitoSub == "" || p.CreatedAt.IsZero() {
				return count, errors.New("unexpected pending identity; stop rollback")
			}
			if apply {
				if err := check(); err != nil {
					return count, err
				}
				if err := ops.remove(ctx, p); err != nil {
					return count, err
				}
			}
			count++
		}
		if len(next) == 0 {
			break
		}
		cursor = next
	}
	if apply {
		// Conditional cleanup may preserve a concurrent refresh. Refuse readiness
		// unless the entire canonical queue is empty and the writer fence still holds.
		cursor = nil
		for {
			rows, next, err := ops.scan(ctx, cursor)
			if err != nil {
				return count, err
			}
			if len(rows) > 0 {
				return count, errors.New("pending invitations remain; do not roll back API")
			}
			if len(next) == 0 {
				break
			}
			cursor = next
		}
		if err := check(); err != nil {
			return count, err
		}
	}
	return count, nil
}

func main() {
	region := flag.String("region", "ap-northeast-2", "AWS region")
	account := flag.String("expected-account", "", "required target AWS account")
	hash := flag.String("expected-code-sha256", "", "required compatible API ZIP hash from the verified release")
	function := flag.String("function", "ttobak-api", "API Lambda name")
	table := flag.String("table", "ttobak-main", "DynamoDB table")
	apply := flag.Bool("run", false, "pause writers and cancel queued invitations; default is read-only")
	flag.Parse()
	if *account == "" || *hash == "" {
		fmt.Fprintln(os.Stderr, "expected-account and expected-code-sha256 are required")
		os.Exit(2)
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(*region))
	fatal(err)
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	fatal(err)
	if aws.ToString(identity.Account) != *account {
		fatal(errors.New("unexpected AWS account"))
	}
	functions := lambda.NewFromConfig(cfg)
	db := dynamodb.NewFromConfig(cfg)
	repo := repository.NewDynamoDBRepository(db, *table)
	read := func(ctx context.Context) (*lambda.GetFunctionConfigurationOutput, error) {
		return functions.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: function})
	}
	snap := func(c *lambda.GetFunctionConfigurationOutput) snapshot {
		paused := c.Environment != nil && c.Environment.Variables["PROJECT_INVITATIONS_ENABLED"] == "false"
		return snapshot{hash: aws.ToString(c.CodeSha256), revision: aws.ToString(c.RevisionId), paused: paused, stable: c.LastUpdateStatus == lt.LastUpdateStatusSuccessful, timeout: time.Duration(aws.ToInt32(c.Timeout)) * time.Second}
	}
	ops := operations{
		current: func(ctx context.Context) (snapshot, error) {
			c, err := read(ctx)
			if err != nil {
				return snapshot{}, err
			}
			return snap(c), nil
		},
		pause: func(ctx context.Context, revision string) (snapshot, error) {
			current, err := read(ctx)
			if err != nil {
				return snapshot{}, err
			}
			if aws.ToString(current.RevisionId) != revision {
				return snapshot{}, errors.New("API revision changed before pause")
			}
			variables, err := pausedEnvironment(current)
			if err != nil {
				return snapshot{}, err
			}
			_, err = functions.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{FunctionName: function, RevisionId: aws.String(revision), Environment: &lt.Environment{Variables: variables}})
			if err != nil {
				return snapshot{}, err
			}
			if err := lambda.NewFunctionUpdatedV2Waiter(functions).Wait(ctx, &lambda.GetFunctionInput{FunctionName: function}, 2*time.Minute); err != nil {
				return snapshot{}, err
			}
			current, err = read(ctx)
			if err != nil {
				return snapshot{}, err
			}
			return snap(current), nil
		},
		wait: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		scan: func(ctx context.Context, cursor map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error) {
			filter, err := expression.NewBuilder().WithFilter(expression.BeginsWith(expression.Name("PK"), model.PrefixProjectInvites)).Build()
			if err != nil {
				return nil, nil, err
			}
			out, err := db.Scan(ctx, &dynamodb.ScanInput{TableName: table, ConsistentRead: aws.Bool(true), Limit: aws.Int32(100), ExclusiveStartKey: cursor, FilterExpression: filter.Filter(), ExpressionAttributeNames: filter.Names(), ExpressionAttributeValues: filter.Values()})
			if err != nil {
				return nil, nil, err
			}
			var rows []model.PendingShare
			if err := attributevalue.UnmarshalListOfMaps(out.Items, &rows); err != nil {
				return nil, nil, err
			}
			return rows, out.LastEvaluatedKey, nil
		},
		remove: repo.DeletePendingProjectShareIfMatch,
	}
	count, err := prepare(ctx, ops, *hash, *apply)
	fatal(err)
	fmt.Printf("project invitations observed=%d apply=%t; existing memberships were not deleted\n", count, *apply)
	if *apply {
		fmt.Println("Writer fence remains disabled. Legacy API rollback may now proceed; re-invite cancelled recipients after returning to the compatible API.")
	}
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pausedEnvironment(current *lambda.GetFunctionConfigurationOutput) (map[string]string, error) {
	if current.Environment == nil || current.Environment.Error != nil {
		return nil, errors.New("cannot safely preserve API environment")
	}
	variables := make(map[string]string, len(current.Environment.Variables)+1)
	for k, v := range current.Environment.Variables {
		variables[k] = v
	}
	variables["PROJECT_INVITATIONS_ENABLED"] = "false"
	return variables, nil
}
