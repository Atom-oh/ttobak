package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

var ErrProjectInvitationsPaused = errors.New("project invitations paused")

var ErrInvalidCursor = errors.New("invalid continuation cursor")

func projectPendingReverseKey(p *model.PendingShare) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + p.ProjectID}, "SK": &types.AttributeValueMemberS{Value: model.PrefixPendingProjectMember + p.Email}}
}

// PutPendingProjectShare atomically binds the email queue and its owner-visible
// reverse row, while checking that the inviter still owns the project.
func (r *DynamoDBRepository) PutPendingProjectShare(ctx context.Context, p *model.PendingShare) error {
	control, err := r.GetProjectInvitationControl(ctx)
	if err != nil {
		return err
	}
	if !control.Enabled {
		return ErrProjectInvitationsPaused
	}
	fence, err := r.projectInvitationFence(&control.Revision)
	if err != nil {
		return err
	}
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	if p.ProjectID == "" || p.InvitedCognitoSub == "" || p.InvitedByUserID == "" || p.Email == "" {
		return fmt.Errorf("invalid project invitation")
	}
	p.PK = model.PrefixProjectInvites + p.Email
	p.SK = model.PrefixPendingProject + p.ProjectID
	p.Kind = model.PendingShareKindProject
	p.EntityType = model.EntityTypePendingShare
	p.CreatedAt = time.Now().UTC()
	p.TTL = p.CreatedAt.Add(model.PendingShareTTL).Unix()
	item, err := attributevalue.MarshalMap(p)
	if err != nil {
		return err
	}
	reverse := *p
	reverse.PK = model.PrefixProject + p.ProjectID
	reverse.SK = model.PrefixPendingProjectMember + p.Email
	reverseItem, err := attributevalue.MarshalMap(reverse)
	if err != nil {
		return err
	}
	owner, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).And(expression.Name("ownerUserId").Equal(expression.Value(p.InvitedByUserID)))).Build()
	if err != nil {
		return err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + p.ProjectID}, "SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig}}, ConditionExpression: owner.Condition(), ExpressionAttributeNames: owner.Names(), ExpressionAttributeValues: owner.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: item}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: reverseItem}},
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + p.ProjectID}, "SK": &types.AttributeValueMemberS{Value: model.PrefixProjectMember + p.InvitedCognitoSub}}, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
		fence,
	}})
	if err != nil {
		if failed, ok := transactionItemFailed(err, 4, 5); ok && failed {
			return ErrProjectInvitationsPaused
		}
		return mapProjectTransactionCanceledError(err, p.ProjectID, "project", "queue project invitation")
	}
	return nil
}

func pendingProjectVersion(p *model.PendingShare) (expression.Expression, error) {
	return expression.NewBuilder().WithCondition(expression.Name("createdAt").Equal(expression.Value(p.CreatedAt)).And(expression.Name("invitedCognitoSub").Equal(expression.Value(p.InvitedCognitoSub)))).Build()
}

// A missing reverse row is safe to clean; a newer one must never be deleted.
func (r *DynamoDBRepository) projectPendingDeletes(p *model.PendingShare) ([]types.TransactWriteItem, error) {
	version, err := pendingProjectVersion(p)
	if err != nil {
		return nil, err
	}
	reverse, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK")).Or(expression.Name("createdAt").Equal(expression.Value(p.CreatedAt)).And(expression.Name("invitedCognitoSub").Equal(expression.Value(p.InvitedCognitoSub))))).Build()
	if err != nil {
		return nil, err
	}
	return []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: pendingShareKey(p.Email, p.SK), ConditionExpression: version.Condition(), ExpressionAttributeNames: version.Names(), ExpressionAttributeValues: version.Values()}},
		{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: projectPendingReverseKey(p), ConditionExpression: reverse.Condition(), ExpressionAttributeNames: reverse.Names(), ExpressionAttributeValues: reverse.Values()}},
	}, nil
}

// DeletePendingProjectShareIfMatch preserves a concurrent re-invitation. A lost
// version race is a no-op, as with the account/meeting cleanup primitive.
func (r *DynamoDBRepository) DeletePendingProjectShareIfMatch(ctx context.Context, p *model.PendingShare) error {
	items, err := r.projectPendingDeletes(p)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		mapped := mapProjectTransactionCanceledError(err, p.ProjectID, "project", "clear project invitation")
		if errors.Is(mapped, ErrConditionFailed) {
			return nil
		}
		return mapped
	}
	return nil
}

// MaterializePendingProjectGrant cannot grant a deleted/reassigned project or
// consume a fresh invitation using an old snapshot. Callers supply a verified
// JWT email/sub; repository checks also bind the exact immutable invite identity.
func (r *DynamoDBRepository) MaterializePendingProjectGrant(ctx context.Context, p *model.PendingShare, userID, email string) (bool, error) {
	if p.Kind != model.PendingShareKindProject || p.ProjectID == "" || p.SK != model.PrefixPendingProject+p.ProjectID || p.InvitedCognitoSub != userID || !strings.EqualFold(p.Email, email) || p.TTL <= time.Now().Unix() {
		return false, nil
	}
	member := model.ProjectMember{PK: model.PrefixProject + p.ProjectID, SK: model.PrefixProjectMember + userID, ProjectID: p.ProjectID, UserID: userID, Email: p.Email, AddedAt: time.Now().UTC(), GSI1PK: model.PrefixUser + userID, GSI1SK: model.PrefixProject + p.ProjectID, EntityType: model.EntityTypeProjectMember}
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return false, err
	}
	owner, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).And(expression.Name("ownerUserId").Equal(expression.Value(p.InvitedByUserID)))).Build()
	if err != nil {
		return false, err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return false, err
	}
	deletes, err := r.projectPendingDeletes(p)
	if err != nil {
		return false, err
	}
	// Recheck expiry and grant identity at commit time, not just before the call.
	identity, err := expression.NewBuilder().WithCondition(expression.Name("createdAt").Equal(expression.Value(p.CreatedAt)).And(expression.Name("invitedCognitoSub").Equal(expression.Value(userID))).And(expression.Name("pendingShareExpiresAt").GreaterThan(expression.Value(time.Now().Unix()))).And(expression.Name("invitedByUserId").Equal(expression.Value(p.InvitedByUserID))).And(expression.Name("projectId").Equal(expression.Value(p.ProjectID)))).Build()
	if err != nil {
		return false, err
	}
	deletes[0].Delete.ConditionExpression = identity.Condition()
	deletes[0].Delete.ExpressionAttributeNames = identity.Names()
	deletes[0].Delete.ExpressionAttributeValues = identity.Values()
	items := []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + p.ProjectID}, "SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig}}, ConditionExpression: owner.Condition(), ExpressionAttributeNames: owner.Names(), ExpressionAttributeValues: owner.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: item, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
	}
	items = append(items, deletes...)
	fence, err := r.projectInvitationFence(nil)
	if err != nil {
		return false, err
	}
	items = append(items, fence)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return true, nil
	}
	if failed, ok := transactionItemFailed(err, 4, 5); ok && failed {
		return false, ErrProjectInvitationsPaused
	}
	// Authority gone or membership already exists: retire only this version so
	// removing an independently granted member cannot later resurrect this grant.
	ownerFailed, known := transactionItemFailed(err, 0, 5)
	memberFailed, _ := transactionItemFailed(err, 1, 5)
	if known && (ownerFailed || memberFailed) {
		if err := r.DeletePendingProjectShareIfMatch(ctx, p); err != nil {
			return false, err
		}
		return true, nil
	}
	mapped := mapProjectTransactionCanceledError(err, p.ProjectID, "project", "materialize project invitation")
	if errors.Is(mapped, ErrConditionFailed) {
		return false, nil
	}
	return false, mapped
}

// ListPendingProjectShares returns one bounded page. Reverse rows never grant
// access or prove an invitation exists: every result is checked canonically.
func (r *DynamoDBRepository) ListPendingProjectShares(ctx context.Context, projectID, cursor string) ([]model.PendingShare, string, error) {
	key := expression.Key("PK").Equal(expression.Value(model.PrefixProject + projectID)).And(expression.Key("SK").BeginsWith(model.PrefixPendingProjectMember))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true), Limit: aws.Int32(25)}
	if cursor != "" {
		if len(cursor) > 512 {
			return nil, "", ErrInvalidCursor
		}
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || !strings.HasPrefix(string(b), model.PrefixPendingProjectMember) || len(b) > 300 {
			return nil, "", ErrInvalidCursor
		}
		in.ExclusiveStartKey = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID}, "SK": &types.AttributeValueMemberS{Value: string(b)}}
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, "", err
	}
	result := []model.PendingShare{}
	for _, item := range out.Items {
		var reverse model.PendingShare
		if err := attributevalue.UnmarshalMap(item, &reverse); err != nil {
			return nil, "", err
		}
		p, err := r.GetPendingShare(ctx, reverse.Email, model.PrefixPendingProject+projectID)
		if err != nil {
			return nil, "", err
		}
		if p == nil || p.Kind != model.PendingShareKindProject || p.ProjectID != projectID || p.InvitedCognitoSub == "" || !p.CreatedAt.Equal(reverse.CreatedAt) || p.InvitedCognitoSub != reverse.InvitedCognitoSub {
			continue
		}
		if p.TTL <= time.Now().Unix() {
			if err := r.DeletePendingProjectShareIfMatch(ctx, p); err != nil {
				return nil, "", err
			}
			continue
		}
		result = append(result, *p)
	}
	next := ""
	if v, ok := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS); ok {
		next = base64.RawURLEncoding.EncodeToString([]byte(v.Value))
	}
	return result, next, nil
}

// Revocation checks current project ownership in the same transaction as removal.
func (r *DynamoDBRepository) RevokePendingProjectShare(ctx context.Context, p *model.PendingShare, ownerID string) error {
	deletes, err := r.projectPendingDeletes(p)
	if err != nil {
		return err
	}
	owner, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).And(expression.Name("ownerUserId").Equal(expression.Value(ownerID)))).Build()
	if err != nil {
		return err
	}
	items := append([]types.TransactWriteItem{{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + p.ProjectID}, "SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig}}, ConditionExpression: owner.Condition(), ExpressionAttributeNames: owner.Names(), ExpressionAttributeValues: owner.Values()}}}, deletes...)
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		return mapProjectTransactionCanceledError(err, p.ProjectID, "project", "revoke project invitation")
	}
	return nil
}

// A separate user queue keeps in-flight older API readers from deleting an
// unknown project kind during a rolling deployment. Only the new bootstrap
// consumes these rows; account/meeting queues retain their existing namespace.
func (r *DynamoDBRepository) ListPendingProjectSharesForUser(ctx context.Context, email, cursor string) ([]model.PendingShare, string, error) {
	key := expression.Key("PK").Equal(expression.Value(model.PrefixProjectInvites + strings.ToLower(email))).And(expression.Key("SK").BeginsWith(model.PrefixPendingProject))
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, "", err
	}
	in := &dynamodb.QueryInput{TableName: aws.String(r.tableName), KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(), ConsistentRead: aws.Bool(true), Limit: aws.Int32(25)}
	if cursor != "" {
		if len(cursor) > 512 {
			return nil, "", ErrInvalidCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 300 || len(raw) <= len(model.PrefixPendingProject) || !strings.HasPrefix(string(raw), model.PrefixPendingProject) {
			return nil, "", ErrInvalidCursor
		}
		in.ExclusiveStartKey = map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProjectInvites + strings.ToLower(email)}, "SK": &types.AttributeValueMemberS{Value: string(raw)}}
	}
	out, err := r.client.Query(ctx, in)
	if err != nil {
		return nil, "", err
	}
	var result []model.PendingShare
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &result); err != nil {
		return nil, "", err
	}
	next := ""
	if key, ok := out.LastEvaluatedKey["SK"].(*types.AttributeValueMemberS); ok {
		next = base64.RawURLEncoding.EncodeToString([]byte(key.Value))
	}
	return result, next, nil
}

// Legacy clients may add an already-registered user, but may not accidentally
// publish a membership alongside a pending grant they cannot consume.
func (r *DynamoDBRepository) PutRegisteredProjectMember(ctx context.Context, ownerID, projectID, userID, email string) error {
	member := model.ProjectMember{PK: model.PrefixProject + projectID, SK: model.PrefixProjectMember + userID, ProjectID: projectID, UserID: userID, Email: email, AddedAt: time.Now().UTC(), GSI1PK: model.PrefixUser + userID, GSI1SK: model.PrefixProject + projectID, EntityType: model.EntityTypeProjectMember}
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return err
	}
	owner, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK")).And(expression.Name("ownerUserId").Equal(expression.Value(ownerID)))).Build()
	if err != nil {
		return err
	}
	absent, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID}, "SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig}}, ConditionExpression: owner.Condition(), ExpressionAttributeNames: owner.Names(), ExpressionAttributeValues: owner.Values()}},
		{Put: &types.Put{TableName: aws.String(r.tableName), Item: item, ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
		{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: pendingShareKey(email, model.PrefixPendingProject+projectID), ConditionExpression: absent.Condition(), ExpressionAttributeNames: absent.Names()}},
	}})
	if err != nil {
		return mapProjectTransactionCanceledError(err, projectID, "project", "add registered member")
	}
	return nil
}

const projectInvitationControlPK = "CONTROL#PROJECT_INVITATIONS"

type ProjectInvitationControl struct {
	Enabled  bool
	Revision string
}

func projectInvitationControlKey() map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: projectInvitationControlPK}, "SK": &types.AttributeValueMemberS{Value: "STATE"}}
}
func (r *DynamoDBRepository) GetProjectInvitationControl(ctx context.Context) (ProjectInvitationControl, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: projectInvitationControlKey(), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return ProjectInvitationControl{}, err
	}
	if len(out.Item) == 0 {
		return ProjectInvitationControl{Enabled: true}, nil
	}
	enabled, ok := out.Item["enabled"].(*types.AttributeValueMemberBOOL)
	revision, rok := out.Item["revision"].(*types.AttributeValueMemberS)
	if !ok || !rok || revision.Value == "" {
		return ProjectInvitationControl{}, errors.New("invalid project invitation control")
	}
	return ProjectInvitationControl{Enabled: enabled.Value, Revision: revision.Value}, nil
}
func (r *DynamoDBRepository) ProjectInvitationsAllowed(ctx context.Context) (bool, error) {
	state, err := r.GetProjectInvitationControl(ctx)
	return state.Enabled, err
}

// Only the operator tool changes this non-expiring control row. API writers read
// it and include it in transactions; no web route can pause or resume it.
func (r *DynamoDBRepository) SetProjectInvitationControl(ctx context.Context, expected string, enabled bool) (ProjectInvitationControl, error) {
	next := ProjectInvitationControl{Enabled: enabled, Revision: uuid.NewString()}
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if expected != "" {
		condition = expression.Name("revision").Equal(expression.Value(expected))
	}
	update := expression.Set(expression.Name("enabled"), expression.Value(enabled)).Set(expression.Name("revision"), expression.Value(next.Revision)).Set(expression.Name("entityType"), expression.Value("PROJECT_INVITATION_CONTROL"))
	expr, err := expression.NewBuilder().WithCondition(condition).WithUpdate(update).Build()
	if err != nil {
		return next, err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: projectInvitationControlKey(), UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	var conflict *types.ConditionalCheckFailedException
	if errors.As(err, &conflict) {
		return next, ErrConditionFailed
	}
	return next, err
}

// Queue writes pin the epoch they observed, so a delayed request cannot publish
// after a pause/resume cycle. Claims additionally bind the canonical grant version.
func (r *DynamoDBRepository) projectInvitationFence(epoch *string) (types.TransactWriteItem, error) {
	condition := expression.AttributeNotExists(expression.Name("PK")).Or(expression.Name("enabled").Equal(expression.Value(true)).And(expression.AttributeExists(expression.Name("revision"))))
	if epoch != nil {
		if *epoch == "" {
			condition = expression.AttributeNotExists(expression.Name("PK"))
		} else {
			condition = expression.Name("enabled").Equal(expression.Value(true)).And(expression.Name("revision").Equal(expression.Value(*epoch)))
		}
	}
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{TableName: aws.String(r.tableName), Key: projectInvitationControlKey(), ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}}, nil
}
