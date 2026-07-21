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
		EntityType: "PROJECT_INDEX",
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

// ListProjectsForUser follows the owner index and skips dangling index items.
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
	projects := make([]model.Project, 0, len(indexes))
	for _, index := range indexes {
		project, err := r.GetProject(ctx, index.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("get project %s: %w", index.ProjectID, err)
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
func (r *DynamoDBRepository) DeleteProject(ctx context.Context, projectID, ownerUserID string) error {
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Delete: &types.Delete{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: model.PrefixProject + projectID},
					"SK": &types.AttributeValueMemberS{Value: model.SKProjectConfig},
				},
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
		return fmt.Errorf("delete project transaction: %w", err)
	}
	return nil
}

// AddProjectAccountLink atomically adds accountID to project.accountIds.
func (r *DynamoDBRepository) AddProjectAccountLink(ctx context.Context, projectID, accountID string) error {
	// attribute_exists(PK) prevents UpdateItem from creating a zombie CONFIG
	// item if the project is concurrently deleted after the service read.
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build add project account link expression: %w", err)
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
		return fmt.Errorf("add project account link: %w", err)
	}
	return nil
}

// RemoveProjectAccountLink atomically removes accountID from project.accountIds.
func (r *DynamoDBRepository) RemoveProjectAccountLink(ctx context.Context, projectID, accountID string) error {
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build remove project account link expression: %w", err)
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
		return fmt.Errorf("remove project account link: %w", err)
	}
	return nil
}

// AddMeetingProjectLink atomically adds projectID to meeting.projectIds.
func (r *DynamoDBRepository) AddMeetingProjectLink(ctx context.Context, ownerUserID, meetingID, projectID string) error {
	// The condition prevents UpdateItem's default upsert from resurrecting a
	// meeting deleted between its ownership check and this mutation.
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build add meeting project link expression: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerUserID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: meeting %s not found", ErrConditionFailed, meetingID)
		}
		return fmt.Errorf("add meeting project link: %w", err)
	}
	return nil
}

// RemoveMeetingProjectLink atomically removes projectID from meeting.projectIds.
func (r *DynamoDBRepository) RemoveMeetingProjectLink(ctx context.Context, ownerUserID, meetingID, projectID string) error {
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build remove meeting project link expression: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixUser + ownerUserID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + meetingID},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: meeting %s not found", ErrConditionFailed, meetingID)
		}
		return fmt.Errorf("remove meeting project link: %w", err)
	}
	return nil
}

// AddResearchProjectLink atomically adds projectID to research.projectIds.
func (r *DynamoDBRepository) AddResearchProjectLink(ctx context.Context, researchID, projectID string) error {
	// The condition prevents UpdateItem's default upsert from resurrecting a
	// research CONFIG item deleted between the service read and this write.
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Add(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build add research project link expression: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: research %s not found", ErrConditionFailed, researchID)
		}
		return fmt.Errorf("add research project link: %w", err)
	}
	return nil
}

// RemoveResearchProjectLink atomically removes projectID from research.projectIds.
func (r *DynamoDBRepository) RemoveResearchProjectLink(ctx context.Context, researchID, projectID string) error {
	expr, err := expression.NewBuilder().
		WithCondition(expression.AttributeExists(expression.Name("PK"))).
		WithUpdate(expression.Delete(expression.Name("projectIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{projectID}}))).
		Build()
	if err != nil {
		return fmt.Errorf("build remove research project link expression: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixResearch + researchID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixConfig},
		},
		ConditionExpression:       expr.Condition(),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return fmt.Errorf("%w: research %s not found", ErrConditionFailed, researchID)
		}
		return fmt.Errorf("remove research project link: %w", err)
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
