// prepare-project-invite-rollback is an operator command, not a Lambda entrypoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

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
	hash, revision, version, arn string
	configured, stable           bool
}
type operations struct {
	current  func(context.Context) (snapshot, error)
	fence    func(context.Context) (repository.ProjectInvitationControl, error)
	setFence func(context.Context, string, bool) (repository.ProjectInvitationControl, error)
	scan     func(context.Context, map[string]dt.AttributeValue) ([]model.PendingShare, map[string]dt.AttributeValue, error)
	remove   func(context.Context, *model.PendingShare) error
}

func sameServing(a, b snapshot) bool {
	return b.stable && a.hash == b.hash && a.revision == b.revision && a.version == b.version && a.arn == b.arn
}
func validateServing(expected string, s snapshot) error {
	if expected == "" || s.hash != expected || !s.stable {
		return errors.New("reviewed serving API code or alias state does not match")
	}
	return nil
}
func scanEmpty(ctx context.Context, ops operations) error {
	var cursor map[string]dt.AttributeValue
	for {
		rows, next, err := ops.scan(ctx, cursor)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			return errors.New("pending invitations remain; do not resume or roll back")
		}
		if len(next) == 0 {
			return nil
		}
		cursor = next
	}
}

// A persistent transactional fence covers every compatible writer/version,
// including delayed transactions. No latest-only mutation or timeout guess is used.
func prepare(ctx context.Context, ops operations, expected string, apply bool) (int, error) {
	initial, err := ops.current(ctx)
	if err != nil {
		return 0, err
	}
	if err := validateServing(expected, initial); err != nil {
		return 0, err
	}
	var paused repository.ProjectInvitationControl
	if apply {
		prior, err := ops.fence(ctx)
		if err != nil {
			return 0, err
		}
		paused, err = setFenceConfirmed(ctx, ops, prior.Revision, false)
		if err != nil {
			return 0, err
		}
		if paused.Enabled || paused.Revision == "" {
			return 0, errors.New("database writer fence was not established")
		}
	}
	check := func() error {
		live, err := ops.current(ctx)
		if err != nil {
			return err
		}
		if !sameServing(initial, live) {
			return errors.New("serving alias changed; stop rollback")
		}
		control, err := ops.fence(ctx)
		if err != nil {
			return err
		}
		if control.Enabled || control.Revision != paused.Revision {
			return errors.New("database fence changed; stop rollback")
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
		if err := scanEmpty(ctx, ops); err != nil {
			return count, err
		}
		if err := check(); err != nil {
			return count, err
		}
	}
	return count, nil
}

func resume(ctx context.Context, ops operations, expected string, apply bool) error {
	initial, err := ops.current(ctx)
	if err != nil {
		return err
	}
	if err := validateServing(expected, initial); err != nil {
		return err
	}
	if !initial.configured {
		return errors.New("activate the reviewed API infrastructure switch before resuming")
	}
	control, err := ops.fence(ctx)
	if err != nil {
		return err
	}
	if control.Enabled || control.Revision == "" {
		return errors.New("no paused database fence to resume")
	}
	if err := scanEmpty(ctx, ops); err != nil {
		return err
	}
	live, err := ops.current(ctx)
	if err != nil {
		return err
	}
	if !sameServing(initial, live) {
		return errors.New("serving alias changed before resume")
	}
	if apply {
		_, err = setFenceConfirmed(ctx, ops, control.Revision, true)
	}
	return err
}

// A conditional write may commit even if its response is lost. The repository
// returns the attempted revision on error, allowing a strong read to prove that
// exact transition without overwriting an intervening operator action.
func setFenceConfirmed(ctx context.Context, ops operations, expected string, enabled bool) (repository.ProjectInvitationControl, error) {
	next, err := ops.setFence(ctx, expected, enabled)
	if err == nil {
		return next, nil
	}
	observed, readErr := ops.fence(ctx)
	if readErr != nil {
		return next, fmt.Errorf("fence update outcome unknown; it may be enabled: write: %w; readback: %v; inspect the control row before any rollback", err, readErr)
	}
	if next.Revision != "" && next.Revision != expected && next.Enabled == enabled && observed == next {
		return next, nil
	}
	return observed, fmt.Errorf("fence update not confirmed; observed enabled=%t revision=%q; revalidate before any rollback: %w", observed.Enabled, observed.Revision, err)
}

func servingSnapshot(alias *lambda.GetAliasOutput, cfg *lambda.GetFunctionConfigurationOutput, table string) (snapshot, error) {
	if alias.RoutingConfig != nil && len(alias.RoutingConfig.AdditionalVersionWeights) > 0 {
		return snapshot{}, errors.New("weighted alias routing is not supported by this guard")
	}
	version := aws.ToString(alias.FunctionVersion)
	if version == "" || version == "$LATEST" || aws.ToString(cfg.Version) != version || aws.ToString(alias.AliasArn) == "" || aws.ToString(alias.RevisionId) == "" {
		return snapshot{}, errors.New("published serving version was not resolved")
	}
	if cfg.Environment == nil || cfg.Environment.Error != nil || cfg.Environment.Variables["TABLE_NAME"] != table {
		return snapshot{}, errors.New("serving API table/environment does not match")
	}
	return snapshot{hash: aws.ToString(cfg.CodeSha256), revision: aws.ToString(alias.RevisionId), version: version, arn: aws.ToString(alias.AliasArn), configured: cfg.Environment.Variables["PROJECT_INVITATIONS_ENABLED"] == "true", stable: cfg.State == lt.StateActive}, nil
}

func main() {
	region := flag.String("region", "ap-northeast-2", "AWS region")
	account := flag.String("expected-account", "", "required target AWS account")
	hash := flag.String("expected-code-sha256", "", "required serving compatible API ZIP hash from the verified release")
	function := flag.String("function", "ttobak-api", "API Lambda name")
	aliasName := flag.String("alias", "live", "actual serving alias; weighted routing is refused")
	table := flag.String("table", "ttobak-main", "API table; must match the serving version environment")
	apply := flag.Bool("run", false, "apply the pause/drain or resume; default is read-only")
	resumeMode := flag.Bool("resume", false, "resume only an empty queue on the reviewed compatible API")
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
	ops := operations{
		current: func(ctx context.Context) (snapshot, error) {
			alias, err := functions.GetAlias(ctx, &lambda.GetAliasInput{FunctionName: function, Name: aliasName})
			if err != nil {
				return snapshot{}, err
			}
			version, err := functions.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: function, Qualifier: alias.FunctionVersion})
			if err != nil {
				return snapshot{}, err
			}
			return servingSnapshot(alias, version, *table)
		},
		fence: repo.GetProjectInvitationControl, setFence: repo.SetProjectInvitationControl, remove: repo.DeletePendingProjectShareIfMatch,
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
	}
	if *resumeMode {
		fatal(resume(ctx, ops, *hash, *apply))
		fmt.Printf("resume validated apply=%t\n", *apply)
		return
	}
	count, err := prepare(ctx, ops, *hash, *apply)
	fatal(err)
	fmt.Printf("project invitations observed=%d apply=%t; existing memberships unchanged\n", count, *apply)
	if *apply {
		fmt.Println("Database fence remains paused across deployments. Legacy API rollback may proceed; resume only after verified upgrade and fresh invitations.")
	}
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
