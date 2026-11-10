package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// ReparentAccountForMerge moves a direct child of the merge source under the
// merge target. It is the operator-path counterpart of UpdateAccountParent,
// which needs the child's Owner and an open child. The child META moves from
// the source to the target, the source's CHILDREF is deleted, the target's is
// put, and the target's ancestor chain is rechecked. intoAncestors starts at
// the target with its parent link and ends at the root (empty parent). The
// target META is checked once, folded into the fence's open check; the
// target membership check is the fence's. At most 64 ancestors give 71 items.
// A child already under the target is success.
func (r *DynamoDBRepository) ReparentAccountForMerge(ctx context.Context, job *model.AccountMergeJob, childID string, intoAncestors []AccountParentLink) error {
	if childID == "" || job == nil || childID == job.SourceAccountID || childID == job.TargetAccountID {
		return fmt.Errorf("invalid merge reparent")
	}
	if len(intoAncestors) == 0 || intoAncestors[0].AccountID != job.TargetAccountID {
		return fmt.Errorf("invalid account hierarchy observations")
	}
	// The first two items are the target membership and META checks, both
	// already part of the fence; only the ancestors above the target remain.
	parentChecks, err := r.hierarchyParentChecks(childID, job.RequesterUserID, job.TargetAccountID, intoAncestors)
	if err != nil {
		return err
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	targetCond := expression.AttributeExists(expression.Name("PK")).
		And(accountParentCondition(intoAncestors[0].ParentAccountID)).And(accountOpenCondition())
	targetExpr, err := expression.NewBuilder().WithCondition(targetCond).Build()
	if err != nil {
		return fmt.Errorf("build merge target check: %w", err)
	}
	last := fence[len(fence)-1].ConditionCheck
	last.ConditionExpression, last.ExpressionAttributeNames, last.ExpressionAttributeValues =
		targetExpr.Condition(), targetExpr.Names(), targetExpr.Values()

	update := expression.Set(expression.Name("parentAccountId"), expression.Value(job.TargetAccountID)).
		Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC()))
	cond := expression.AttributeExists(expression.Name("PK")).
		And(accountParentCondition(job.SourceAccountID)).And(accountOpenCondition())
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("build merge reparent update: %w", err)
	}
	ref, err := r.childReferencePut(childID, job.TargetAccountID)
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{{Update: &types.Update{
		TableName: aws.String(r.tableName), Key: accountHierarchyKey(childID, model.SKAccountMeta),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}}
	items = append(items, fence...)
	items = append(items, types.TransactWriteItem{Delete: &types.Delete{
		TableName: aws.String(r.tableName), Key: accountHierarchyKey(job.SourceAccountID, accountChildPrefix+childID),
	}}, ref)
	if len(parentChecks) > 2 {
		items = append(items, parentChecks[2:]...)
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		moverFailed, fenceErr := classifyMergeFence(err)
		if fenceErr != nil {
			return fenceErr
		}
		if moverFailed {
			if done, readErr := r.childParentIs(ctx, childID, job.TargetAccountID); readErr != nil {
				return readErr
			} else if done {
				return nil
			}
			return fmt.Errorf("reparent account for merge: %w: %w", ErrConditionFailed, err)
		}
		return fmt.Errorf("reparent account for merge: %w", err)
	}
	return nil
}

func (r *DynamoDBRepository) childParentIs(ctx context.Context, childID, parentID string) (bool, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: accountHierarchyKey(childID, model.SKAccountMeta), ProjectionExpression: aws.String("parentAccountId"),
	})
	if err != nil {
		return false, fmt.Errorf("read child parent: %w", err)
	}
	v, ok := out.Item["parentAccountId"].(*types.AttributeValueMemberS)
	return ok && v.Value == parentID, nil
}

// accountLinkMove describes one canonical set plus reverse-reference pair that
// moves from the source account to the target.
type accountLinkMove struct {
	what      string // for errors
	configKey map[string]types.AttributeValue
	refSK     string
	newRef    func(config, sourceRef map[string]types.AttributeValue, targetID string) (map[string]types.AttributeValue, error)
}

// MoveProjectAccountLinkForMerge moves one project's account link from the
// merge source to the target: the project's accountIds set gains the target
// and loses the source, and the account-partition PROJECTREF# moves with it.
// DynamoDB rejects ADD and DELETE of one attribute in a single update, so the
// move is two fenced transactions; a crash between them leaves the project
// linked to both accounts, and a rerun finishes it.
func (r *DynamoDBRepository) MoveProjectAccountLinkForMerge(ctx context.Context, job *model.AccountMergeJob, projectID string) error {
	if projectID == "" {
		return fmt.Errorf("invalid merge project move")
	}
	return r.moveAccountLinkForMerge(ctx, job, accountLinkMove{
		what: "project",
		configKey: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
			"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
		},
		refSK: model.PrefixProjectRef + projectID,
		newRef: func(config, sourceRef map[string]types.AttributeValue, targetID string) (map[string]types.AttributeValue, error) {
			var ref model.ProjectRef
			if sourceRef != nil {
				if err := attributevalue.UnmarshalMap(sourceRef, &ref); err != nil {
					return nil, fmt.Errorf("unmarshal project ref: %w", err)
				}
			} else {
				var project model.Project
				if err := attributevalue.UnmarshalMap(config, &project); err != nil {
					return nil, fmt.Errorf("unmarshal project: %w", err)
				}
				ref = model.ProjectRef{OwnerUserID: project.OwnerUserID, Name: project.Name, CreatedAt: time.Now().UTC()}
			}
			ref.PK, ref.SK = model.PrefixAccount+targetID, model.PrefixProjectRef+projectID
			ref.AccountID, ref.ProjectID, ref.EntityType = targetID, projectID, "PROJECT_REF"
			return attributevalue.MarshalMap(ref)
		},
	})
}

// MoveResearchAccountLinkForMerge is MoveProjectAccountLinkForMerge for a
// research report and its RESEARCHREF# row.
func (r *DynamoDBRepository) MoveResearchAccountLinkForMerge(ctx context.Context, job *model.AccountMergeJob, researchID string) error {
	if researchID == "" {
		return fmt.Errorf("invalid merge research move")
	}
	return r.moveAccountLinkForMerge(ctx, job, accountLinkMove{
		what: "research",
		configKey: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		refSK: model.PrefixResearchRef + researchID,
		newRef: func(config, sourceRef map[string]types.AttributeValue, targetID string) (map[string]types.AttributeValue, error) {
			var ref model.ResearchRef
			if sourceRef != nil {
				if err := attributevalue.UnmarshalMap(sourceRef, &ref); err != nil {
					return nil, fmt.Errorf("unmarshal research ref: %w", err)
				}
			} else {
				var research model.Research
				if err := attributevalue.UnmarshalMap(config, &research); err != nil {
					return nil, fmt.Errorf("unmarshal research: %w", err)
				}
				ref = model.ResearchRef{OwnerUserID: research.UserID, Topic: research.Topic, CreatedAt: time.Now().UTC()}
			}
			ref.PK, ref.SK = model.PrefixAccount+targetID, model.PrefixResearchRef+researchID
			ref.AccountID, ref.ResearchID, ref.EntityType = targetID, researchID, model.EntityTypeResearchRef
			return attributevalue.MarshalMap(ref)
		},
	})
}

func (r *DynamoDBRepository) moveAccountLinkForMerge(ctx context.Context, job *model.AccountMergeJob, m accountLinkMove) error {
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	cfg, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Key: m.configKey})
	if err != nil {
		return fmt.Errorf("read %s for merge: %w", m.what, err)
	}
	if !linkedTo(cfg.Item, job.SourceAccountID) {
		return nil // deleted, never linked, or already moved
	}
	srcRef, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: accountHierarchyKey(job.SourceAccountID, m.refSK)})
	if err != nil {
		return fmt.Errorf("read %s ref for merge: %w", m.what, err)
	}
	targetRef, err := m.newRef(cfg.Item, srcRef.Item, job.TargetAccountID)
	if err != nil {
		return err
	}

	buildUpdate := func(update expression.UpdateBuilder, cond expression.ConditionBuilder) (*types.Update, error) {
		expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
		if err != nil {
			return nil, fmt.Errorf("build %s link move: %w", m.what, err)
		}
		return &types.Update{TableName: aws.String(r.tableName), Key: m.configKey,
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}, nil
	}
	ss := func(id string) expression.ValueBuilder {
		return expression.Value(&types.AttributeValueMemberSS{Value: []string{id}})
	}
	accountIDs := expression.Name("accountIds")
	hasSource := expression.Contains(accountIDs, job.SourceAccountID)

	add, err := buildUpdate(expression.Add(accountIDs, ss(job.TargetAccountID)), hasSource)
	if err != nil {
		return err
	}
	put := types.TransactWriteItem{Put: &types.Put{TableName: aws.String(r.tableName), Item: targetRef}}
	if err := r.runMergeLinkTx(ctx, m.what, types.TransactWriteItem{Update: add}, fence, put, true); err != nil {
		return err
	}
	del, err := buildUpdate(expression.Delete(accountIDs, ss(job.SourceAccountID)),
		hasSource.And(expression.Contains(accountIDs, job.TargetAccountID)))
	if err != nil {
		return err
	}
	drop := types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: accountHierarchyKey(job.SourceAccountID, m.refSK)}}
	return r.runMergeLinkTx(ctx, m.what, types.TransactWriteItem{Update: del}, fence, drop, false)
}

// runMergeLinkTx writes mover item, fence checks, then the reference write.
// When the mover condition fails: the first step treats it as "source link
// already gone" (success); the second step cannot assume that, because the
// target link may have been removed meanwhile, so it asks for a retry.
func (r *DynamoDBRepository) runMergeLinkTx(ctx context.Context, what string, mover types.TransactWriteItem, fence []types.TransactWriteItem, ref types.TransactWriteItem, firstStep bool) error {
	items := append([]types.TransactWriteItem{mover}, fence...)
	items = append(items, ref)
	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return nil
	}
	moverFailed, fenceErr := classifyMergeFence(err)
	if fenceErr != nil {
		return fenceErr
	}
	if moverFailed && firstStep {
		return nil
	}
	if moverFailed {
		return fmt.Errorf("move %s link for merge: %w: %w", what, ErrConditionFailed, err)
	}
	return fmt.Errorf("move %s link for merge: %w", what, err)
}

func linkedTo(item map[string]types.AttributeValue, accountID string) bool {
	set, ok := item["accountIds"].(*types.AttributeValueMemberSS)
	if !ok {
		return false
	}
	for _, id := range set.Value {
		if id == accountID {
			return true
		}
	}
	return false
}

// MergedAccountTerms returns the target's terms followed by the source's
// additions, deduplicated case-insensitively after trimming surrounding
// space. Target terms keep their stored spelling; empty terms are dropped.
func MergedAccountTerms(target []string, additions ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(target)+len(additions))
	for _, list := range [][]string{target, additions} {
		for _, term := range list {
			term = strings.TrimSpace(term)
			key := strings.ToLower(term)
			if term == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, term)
		}
	}
	return out
}

// MergedAccountAliases is the target's aliases plus the source's name and aliases.
func MergedAccountAliases(target *model.Account, source *model.Account) []string {
	return MergedAccountTerms(target.Aliases, append([]string{source.Name}, source.Aliases...)...)
}

// MergedAccountDomains is the target's domains plus the source's domains.
func MergedAccountDomains(target *model.Account, source *model.Account) []string {
	return MergedAccountTerms(target.Domains, source.Domains...)
}

// MergeAccountMetadata writes the unioned aliases and domains to the target
// META. into is the observation the union was computed from: the update
// applies only while its updatedAt is unchanged, so a concurrent edit makes
// the caller re-read and recompute (ErrConditionFailed). The target open
// check is folded into this update because a transaction cannot touch the
// META row twice; a merge marker on it returns ErrAccountMerging.
func (r *DynamoDBRepository) MergeAccountMetadata(ctx context.Context, job *model.AccountMergeJob, into *model.Account, aliases, domains []string) error {
	if job == nil || into == nil || into.AccountID != job.TargetAccountID {
		return fmt.Errorf("invalid merge metadata target")
	}
	fence, err := r.mergeFenceChecks(job)
	if err != nil {
		return err
	}
	fence = fence[:mergeFenceCheckCount-1] // target open is folded into the update
	update := expression.Set(expression.Name("updatedAt"), expression.Value(time.Now().UTC()))
	for name, terms := range map[string][]string{"aliases": aliases, "domains": domains} {
		if len(terms) == 0 {
			update = update.Remove(expression.Name(name))
		} else {
			update = update.Set(expression.Name(name), expression.Value(terms))
		}
	}
	cond := expression.AttributeExists(expression.Name("PK")).And(accountOpenCondition()).
		And(expression.Name("updatedAt").Equal(expression.Value(into.UpdatedAt)))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(cond).Build()
	if err != nil {
		return fmt.Errorf("build merge metadata update: %w", err)
	}
	items := append([]types.TransactWriteItem{{Update: &types.Update{
		TableName: aws.String(r.tableName), Key: accountHierarchyKey(job.TargetAccountID, model.SKAccountMeta),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
	}}}, fence...)
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		if merging := mergeFenceError(err); merging != nil {
			return merging
		}
		var cancelled *types.TransactionCanceledException
		if errors.As(err, &cancelled) {
			for i := 1; i < len(cancelled.CancellationReasons) && i < len(items); i++ {
				if aws.ToString(cancelled.CancellationReasons[i].Code) == "ConditionalCheckFailed" {
					return fmt.Errorf("%w: %w", ErrMergeFenceLost, err)
				}
			}
			if len(cancelled.CancellationReasons) > 0 && aws.ToString(cancelled.CancellationReasons[0].Code) == "ConditionalCheckFailed" {
				return fmt.Errorf("merge account metadata: %w: %w", ErrConditionFailed, err)
			}
		}
		return fmt.Errorf("merge account metadata: %w", err)
	}
	return nil
}
