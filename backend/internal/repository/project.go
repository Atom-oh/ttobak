package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// projectIndexItem is the user index record for an owned project.
type projectIndexItem struct {
	PK         string `dynamodbav:"PK"`
	SK         string `dynamodbav:"SK"`
	EntityType string `dynamodbav:"entityType"`
	ProjectID  string `dynamodbav:"projectId"`
}

// CreateProject atomically writes the project CONFIG item and its owner index.
func (r *DynamoDBRepository) CreateProject(ctx context.Context, project *model.Project) error {
	projectItem, err := attributevalue.MarshalMap(project)
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}
	indexItem, err := attributevalue.MarshalMap(projectIndexItem{
		PK:         model.PrefixUser + project.OwnerUserID,
		SK:         model.PrefixProject + project.ProjectID,
		EntityType: model.EntityTypeProjectIndex,
		ProjectID:  project.ProjectID,
	})
	if err != nil {
		return fmt.Errorf("marshal project index: %w", err)
	}

	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: projectItem}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: indexItem}},
		},
	}); err != nil {
		return fmt.Errorf("create project transaction: %w", err)
	}
	return nil
}

// GetProject retrieves the canonical project CONFIG item with a consistent read.
func (r *DynamoDBRepository) GetProject(ctx context.Context, projectID string) (*model.Project, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
			"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var project model.Project
	if err := attributevalue.UnmarshalMap(result.Item, &project); err != nil {
		return nil, fmt.Errorf("unmarshal project: %w", err)
	}
	return &project, nil
}

// GetProjectMember retrieves a direct project membership with a consistent read.
func (r *DynamoDBRepository) GetProjectMember(ctx context.Context, projectID, userID string) (*model.ProjectMember, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixProjectMember + userID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get project member: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var member model.ProjectMember
	if err := attributevalue.UnmarshalMap(result.Item, &member); err != nil {
		return nil, fmt.Errorf("unmarshal project member: %w", err)
	}
	return &member, nil
}

func (r *DynamoDBRepository) PutProjectMember(ctx context.Context, member *model.ProjectMember) error {
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return fmt.Errorf("marshal project member: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put project member: %w", err)
	}
	return nil
}

// DeleteProjectMember removes a direct project membership row. Used for
// access revocation -- without this, a member added via AddMember could
// never be removed short of deleting the whole project.
func (r *DynamoDBRepository) DeleteProjectMember(ctx context.Context, projectID, userID string) error {
	if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixProjectMember + userID},
		},
	}); err != nil {
		return fmt.Errorf("delete project member: %w", err)
	}
	return nil
}

// ListProjectMembers queries every MEMBER# item in the project partition.
func (r *DynamoDBRepository) ListProjectMembers(ctx context.Context, projectID string) ([]model.ProjectMember, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixProject + projectID)).
		And(expression.Key("SK").BeginsWith(model.PrefixProjectMember))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build project members query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query project members: %w", err)
	}
	members := []model.ProjectMember{}
	if err := attributevalue.UnmarshalListOfMaps(items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal project members: %w", err)
	}
	return members, nil
}

// ListProjectMembershipsForUser reverse-looks-up direct project memberships
// via GSI1 (GSI1PK=USER#{userId}, GSI1SK begins_with PROJECT#) -- mirrors
// ListAccountsForUser. GSI1 is shared with meeting date-sorting rows and
// ACCOUNT#-prefixed membership rows for the same user, so the begins_with
// condition is what isolates project-membership rows specifically.
func (r *DynamoDBRepository) ListProjectMembershipsForUser(ctx context.Context, userID string) ([]model.ProjectMember, error) {
	keyEx := expression.Key("GSI1PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("GSI1SK").BeginsWith(model.PrefixProject))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build project memberships query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("GSI1"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query project memberships: %w", err)
	}
	members := []model.ProjectMember{}
	if err := attributevalue.UnmarshalListOfMaps(items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal project memberships: %w", err)
	}
	return members, nil
}

// ListProjectsForUser is a query primitive: projects the user owns (the
// PROJECT# owner index) UNION projects they were added to via AddMember
// (the GSI1 membership reverse index). It does NOT include the
// account-inherited leg of requireProjectAccess's hybrid access check --
// that policy decision (which accounts count, how a candidate is
// fail-closed re-verified against canonical state) lives in
// ProjectService.ListMyProjects, not here, mirroring where
// ListAccountResearch's equivalent canonical re-verification lives (the
// service layer) rather than in the repository.
func (r *DynamoDBRepository) ListProjectsForUser(ctx context.Context, userID string) ([]model.Project, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("SK").BeginsWith(model.PrefixProject))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build projects-for-user query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query projects for user: %w", err)
	}
	var indexes []projectIndexItem
	if err := attributevalue.UnmarshalListOfMaps(items, &indexes); err != nil {
		return nil, fmt.Errorf("unmarshal project index items: %w", err)
	}
	memberships, err := r.ListProjectMembershipsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list project memberships: %w", err)
	}
	seen := make(map[string]bool, len(indexes)+len(memberships))
	projectIDs := make([]string, 0, len(indexes)+len(memberships))
	for _, index := range indexes {
		if !seen[index.ProjectID] {
			seen[index.ProjectID] = true
			projectIDs = append(projectIDs, index.ProjectID)
		}
	}
	for _, member := range memberships {
		if !seen[member.ProjectID] {
			seen[member.ProjectID] = true
			projectIDs = append(projectIDs, member.ProjectID)
		}
	}
	projects := make([]model.Project, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		project, err := r.GetProject(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("get project %s: %w", projectID, err)
		}
		if project != nil {
			projects = append(projects, *project)
		}
	}
	return projects, nil
}

// UpdateProjectFields atomically updates only the supplied fields and refuses
// to upsert a project deleted between the service read and this write.
func (r *DynamoDBRepository) UpdateProjectFields(ctx context.Context, projectID string, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	var update expression.UpdateBuilder
	first := true
	for name, value := range fields {
		if first {
			update = expression.Set(expression.Name(name), expression.Value(value))
			first = false
		} else {
			update = update.Set(expression.Name(name), expression.Value(value))
		}
	}
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(update).
		Build()
	if err != nil {
		return fmt.Errorf("build project update expression: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
			"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: project %s not found", ErrConditionFailed, projectID)
		}
		return fmt.Errorf("update project fields: %w", err)
	}
	return nil
}

// DeleteProject removes the canonical project and owner index atomically.
// The CONFIG delete additionally asserts accountIds is still empty --
// closing the narrowest, cheapest-to-close slice of the service layer's
// check-then-delete race: a LinkAccount that commits between
// ProjectService.DeleteProject's non-atomic guard reads and this
// transaction would otherwise land unnoticed, since accountIds lives
// directly on the item being deleted here and can be asserted in the same
// transaction. The equivalent race for MEMBER#/MEETINGREF#/RESEARCHREF#
// items (which live on OTHER items this transaction doesn't touch) is a
// known, accepted residual risk -- see ADR-024's Risks section for why
// closing it fully would need a broader tombstone-and-reject-concurrent-
// writes redesign disproportionate to the actual impact (unreachable dead
// storage in a deleted project's own partition, never incorrectly
// exposed -- every read path re-verifies canonical state independently).
func (r *DynamoDBRepository) DeleteProject(ctx context.Context, projectID, ownerUserID string) error {
	noAccountsExpr, err := expression.NewBuilder().
		WithCondition(expression.Or(
			expression.AttributeNotExists(expression.Name("accountIds")),
			expression.Size(expression.Name("accountIds")).Equal(expression.Value(0)),
		)).
		Build()
	if err != nil {
		return fmt.Errorf("build no-accounts-linked condition: %w", err)
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
				},
				ConditionExpression:       noAccountsExpr.Condition(),
				ExpressionAttributeNames:  noAccountsExpr.Names(),
				ExpressionAttributeValues: noAccountsExpr.Values(),
			}},
			{Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerUserID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
				},
			}},
		},
	}); err != nil {
		return mapProjectTransactionCanceledError(err, projectID, "project", "delete project")
	}
	return nil
}

// mapProjectTransactionCanceledError inspects a TransactWriteItems
// cancellation and maps a ConditionalCheckFailed reason to ErrConditionFailed
// (mirrors ResearchRepository's mapTransactionCanceledError, same package).
func mapProjectTransactionCanceledError(err error, entityID, entityLabel, verb string) error {
	var tce *types.TransactionCanceledException
	if errors.As(err, &tce) {
		// Check EVERY reason, not just index 0: CancellationReasons is
		// positional (one per TransactItem, "None" for items that weren't
		// the cause), and the meeting/research-link transactions have a
		// ConditionCheck on the project CONFIG item at index 2, not 0 --
		// checking only index 0 would silently miss a condition failure
		// there and fall through to the generic "retry" error instead of
		// ErrConditionFailed/ErrNotFound, defeating that ConditionCheck's
		// entire purpose (closing the delete-vs-link race).
		for _, reason := range tce.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return fmt.Errorf("%w: %s %s not found", ErrConditionFailed, entityLabel, entityID)
			}
		}
		return fmt.Errorf("failed to %s: transaction canceled, retry: %w", verb, err)
	}
	return fmt.Errorf("failed to %s transactionally: %w", verb, err)
}

// ProjectAccountLinkTransactional atomically ADDs accountID to
// project.accountIds AND puts the PROJECTREF# reverse-index item in one
// TransactWriteItems call. Two separate requests here (the original
// AddProjectAccountLink + PutProjectRef) would leave a gap: a concurrent
// Link/Unlink pair for the SAME (project, account) could interleave such
// that the canonical set ends up linked but the reverse-index ref ends up
// deleted -- ListAccountProjects reads from that ref, so the project would
// then be permanently invisible from the account's list. One transaction
// makes that interleaving impossible, not just less likely (mirrors
// ResearchRepository.LinkAccountTransactional).
func (r *DynamoDBRepository) ProjectAccountLinkTransactional(ctx context.Context, projectID, accountID string, ref *model.ProjectRef) error {
	refItem, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal project ref: %w", err)
	}
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build project account link expression: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
				},
				ConditionExpression:       updateExpr.Condition(),
				UpdateExpression:          updateExpr.Update(),
				ExpressionAttributeNames:  updateExpr.Names(),
				ExpressionAttributeValues: updateExpr.Values(),
			}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: refItem}},
		},
	})
	if err != nil {
		return mapProjectTransactionCanceledError(err, projectID, "project", "link project account")
	}
	return nil
}

// ProjectAccountUnlinkTransactional is ProjectAccountLinkTransactional's
// inverse: atomic set-DELETE + ref-delete in one TransactWriteItems call.
func (r *DynamoDBRepository) ProjectAccountUnlinkTransactional(ctx context.Context, projectID, accountID string) error {
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build project account unlink expression: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
				},
				ConditionExpression:       updateExpr.Condition(),
				UpdateExpression:          updateExpr.Update(),
				ExpressionAttributeNames:  updateExpr.Names(),
				ExpressionAttributeValues: updateExpr.Values(),
			}},
			{Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixProjectRef + projectID},
				},
			}},
		},
	})
	if err != nil {
		return mapProjectTransactionCanceledError(err, projectID, "project", "unlink project account")
	}
	return nil
}

// MeetingProjectLinkTransactional atomically ADDs projectID to
// meeting.projectIds AND puts the project's MEETINGREF# item in one
// TransactWriteItems call (same interleaving hazard/fix as
// ProjectAccountLinkTransactional above).
func (r *DynamoDBRepository) MeetingProjectLinkTransactional(ctx context.Context, ownerUserID, meetingID, projectID string, ref *model.ProjectMeetingRef) error {
	refItem, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal project meeting ref: %w", err)
	}
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build meeting project link expression: %w", err)
	}
	projectExistsExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return fmt.Errorf("build project-exists condition: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerUserID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
				},
				ConditionExpression:       updateExpr.Condition(),
				UpdateExpression:          updateExpr.Update(),
				ExpressionAttributeNames:  updateExpr.Names(),
				ExpressionAttributeValues: updateExpr.Values(),
			}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: refItem}},
			// Guards against linking to a project concurrently deleted after
			// requireProjectAccess's read but before this transaction commits
			// -- without this, the link could succeed against a project that
			// canonically no longer exists (an orphan ref + a ghost projectId
			// left in the meeting's ProjectIDs set), the same class of
			// delete-vs-link race attribute_exists(PK) already guards against
			// elsewhere (e.g. ProjectAccountLinkTransactional's own Update).
			{ConditionCheck: &types.ConditionCheck{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
				},
				ConditionExpression:      projectExistsExpr.Condition(),
				ExpressionAttributeNames: projectExistsExpr.Names(),
			}},
		},
	})
	if err != nil {
		return mapProjectTransactionCanceledError(err, meetingID, "meeting", "link meeting project")
	}
	return nil
}

// MeetingProjectUnlinkTransactional is MeetingProjectLinkTransactional's
// inverse. refSK is the ref's exact existing SK (looked up by the caller,
// not recomputed from the meeting's current Date) -- Date is a mutable
// field, so recomputing it here could target a different item than the one
// LinkMeeting actually wrote, orphaning the original ref.
func (r *DynamoDBRepository) MeetingProjectUnlinkTransactional(ctx context.Context, ownerUserID, meetingID, projectID, refSK string) error {
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build meeting project unlink expression: %w", err)
	}
	transactItems := []types.TransactWriteItem{
		{Update: &types.Update{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerUserID},
				"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
			},
			ConditionExpression:       updateExpr.Condition(),
			UpdateExpression:          updateExpr.Update(),
			ExpressionAttributeNames:  updateExpr.Names(),
			ExpressionAttributeValues: updateExpr.Values(),
		}},
	}
	if refSK != "" {
		transactItems = append(transactItems, types.TransactWriteItem{
			Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: refSK},
				},
			},
		})
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: transactItems}); err != nil {
		return mapProjectTransactionCanceledError(err, meetingID, "meeting", "unlink meeting project")
	}
	return nil
}

// ResearchProjectLinkTransactional atomically ADDs projectID to
// research.projectIds AND puts the project's RESEARCHREF# item in one
// TransactWriteItems call (same interleaving hazard/fix as the account and
// meeting variants above).
func (r *DynamoDBRepository) ResearchProjectLinkTransactional(ctx context.Context, researchID, projectID string, ref *model.ProjectResearchRef) error {
	refItem, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal project research ref: %w", err)
	}
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build research project link expression: %w", err)
	}
	projectExistsExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		Build()
	if err != nil {
		return fmt.Errorf("build project-exists condition: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
				},
				ConditionExpression:       updateExpr.Condition(),
				UpdateExpression:          updateExpr.Update(),
				ExpressionAttributeNames:  updateExpr.Names(),
				ExpressionAttributeValues: updateExpr.Values(),
			}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: refItem}},
			// See MeetingProjectLinkTransactional's comment on this same
			// ConditionCheck -- closes the delete-vs-link race for research too.
			{ConditionCheck: &types.ConditionCheck{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
				},
				ConditionExpression:      projectExistsExpr.Condition(),
				ExpressionAttributeNames: projectExistsExpr.Names(),
			}},
		},
	})
	if err != nil {
		return mapProjectTransactionCanceledError(err, researchID, "research", "link research project")
	}
	return nil
}

// ResearchProjectUnlinkTransactional is ResearchProjectLinkTransactional's inverse.
func (r *DynamoDBRepository) ResearchProjectUnlinkTransactional(ctx context.Context, researchID, projectID string) error {
	updateExpr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build research project unlink expression: %w", err)
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
				},
				ConditionExpression:       updateExpr.Condition(),
				UpdateExpression:          updateExpr.Update(),
				ExpressionAttributeNames:  updateExpr.Names(),
				ExpressionAttributeValues: updateExpr.Values(),
			}},
			{Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: model.PrefixProjectResearchRef + researchID},
				},
			}},
		},
	})
	if err != nil {
		return mapProjectTransactionCanceledError(err, researchID, "research", "unlink research project")
	}
	return nil
}

func (r *DynamoDBRepository) PutProjectRef(ctx context.Context, ref *model.ProjectRef) error {
	item, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal project ref: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item}); err != nil {
		return fmt.Errorf("put project ref: %w", err)
	}
	return nil
}

func (r *DynamoDBRepository) DeleteProjectRef(ctx context.Context, accountID, projectID string) error {
	if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixProjectRef + projectID},
		},
	}); err != nil {
		return fmt.Errorf("delete project ref: %w", err)
	}
	return nil
}

// ListProjectRefsForAccount returns account project refs newest first.
func (r *DynamoDBRepository) ListProjectRefsForAccount(ctx context.Context, accountID string) ([]model.ProjectRef, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixProjectRef))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build project refs query: %w", err)
	}
	// SK contains a random project ID rather than a timestamp, so sort by
	// CreatedAt explicitly instead of relying on ScanIndexForward.
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query project refs: %w", err)
	}
	refs := []model.ProjectRef{}
	if err := attributevalue.UnmarshalListOfMaps(items, &refs); err != nil {
		return nil, fmt.Errorf("unmarshal project refs: %w", err)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].CreatedAt.After(refs[j].CreatedAt) })
	return refs, nil
}

func (r *DynamoDBRepository) PutProjectMeetingRef(ctx context.Context, ref *model.ProjectMeetingRef) error {
	item, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal project meeting ref: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item}); err != nil {
		return fmt.Errorf("put project meeting ref: %w", err)
	}
	return nil
}

// DeleteProjectMeetingRef deletes by the complete MEETINGREF# timestamp/id SK.
func (r *DynamoDBRepository) DeleteProjectMeetingRef(ctx context.Context, projectID, sk string) error {
	if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
			"SK": &types.AttributeValueMemberS{Value: sk},
		},
	}); err != nil {
		return fmt.Errorf("delete project meeting ref: %w", err)
	}
	return nil
}

// ListProjectMeetingRefsForProject uses the timestamp-bearing SK for newest-first order.
func (r *DynamoDBRepository) ListProjectMeetingRefsForProject(ctx context.Context, projectID string) ([]model.ProjectMeetingRef, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixProject + projectID)).
		And(expression.Key("SK").BeginsWith(model.PrefixProjectMeetingRef))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build project meeting refs query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query project meeting refs: %w", err)
	}
	refs := []model.ProjectMeetingRef{}
	if err := attributevalue.UnmarshalListOfMaps(items, &refs); err != nil {
		return nil, fmt.Errorf("unmarshal project meeting refs: %w", err)
	}
	return refs, nil
}

func (r *DynamoDBRepository) PutProjectResearchRef(ctx context.Context, ref *model.ProjectResearchRef) error {
	item, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal project research ref: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item}); err != nil {
		return fmt.Errorf("put project research ref: %w", err)
	}
	return nil
}

func (r *DynamoDBRepository) DeleteProjectResearchRef(ctx context.Context, projectID, researchID string) error {
	if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixProjectResearchRef + researchID},
		},
	}); err != nil {
		return fmt.Errorf("delete project research ref: %w", err)
	}
	return nil
}

// ListProjectResearchRefsForProject returns research refs newest first.
func (r *DynamoDBRepository) ListProjectResearchRefsForProject(ctx context.Context, projectID string) ([]model.ProjectResearchRef, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixProject + projectID)).
		And(expression.Key("SK").BeginsWith(model.PrefixProjectResearchRef))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build project research refs query: %w", err)
	}
	// The SK contains a random research ID, not a timestamp; sort by the
	// explicit CreatedAt field after collecting every query page.
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query project research refs: %w", err)
	}
	refs := []model.ProjectResearchRef{}
	if err := attributevalue.UnmarshalListOfMaps(items, &refs); err != nil {
		return nil, fmt.Errorf("unmarshal project research refs: %w", err)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].CreatedAt.After(refs[j].CreatedAt) })
	return refs, nil
}

// GetResearchByID exposes the canonical research read needed by ProjectService.
// It delegates to the existing research repository implementation so key and
// unmarshalling behavior stay identical.
func (r *DynamoDBRepository) GetResearchByID(ctx context.Context, researchID string) (*model.Research, error) {
	return NewResearchRepository(r.client, r.tableName).GetResearch(ctx, researchID)
}

// BatchGetResearchByIDs exposes the existing batched research read to
// ProjectService without introducing a second repository dependency.
func (r *DynamoDBRepository) BatchGetResearchByIDs(ctx context.Context, ids []string) ([]model.Research, error) {
	return NewResearchRepository(r.client, r.tableName).BatchGetResearch(ctx, ids)
}
