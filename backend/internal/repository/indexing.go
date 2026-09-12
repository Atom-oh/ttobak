package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

var ErrInvalidIndexCursor = errors.New("invalid index scan cursor")

func indexKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: sk}}
}

func (r *DynamoDBRepository) GetIndexSource(ctx context.Context, key model.IndexResource) (*model.IndexRecord, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: indexKey(key.PK, key.SK), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var values map[string]interface{}
	if err := attributevalue.UnmarshalMap(out.Item, &values); err != nil {
		return nil, err
	}
	fields := map[string]interface{}{}
	for _, name := range model.IndexSourceFields(key.Kind) {
		if v, ok := values[name]; ok {
			fields[name] = v
		}
	}
	return &model.IndexRecord{Resource: key, Fields: fields}, nil
}

func (r *DynamoDBRepository) GetIndexJob(ctx context.Context, key model.IndexResource) (*model.IndexJob, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: indexKey(model.IndexJobsPK, key.Hash()), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var job model.IndexJob
	if err := attributevalue.UnmarshalMap(out.Item, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *DynamoDBRepository) RequestIndexResource(ctx context.Context, key model.IndexResource, revision string, now int64) error {
	for attempt := 0; attempt < 5; attempt++ {
		prior, err := r.GetIndexJob(ctx, key)
		if err != nil {
			return err
		}
		if prior != nil && revision != "" {
			if prior.State == model.IndexFailed && prior.DesiredRevision == revision && prior.RetryAfter > now {
				return nil
			}
			if prior.State == model.IndexPending && prior.DesiredRevision == revision {
				return nil
			}
			if prior.State == model.IndexPreparing && prior.DesiredRevision == revision && prior.LeaseUntil > now {
				// Duplicate delivery must not invalidate an active preparation.
				// Expired leases and genuinely changed revisions still queue work.
				return nil
			}
			switch prior.State {
			case model.IndexIndexed, model.IndexDeleted, model.IndexWaitingSync, model.IndexWaitingSource:
				if prior.Revision == revision {
					return nil
				}
			}
		}
		next := model.IndexJob{Resource: key}
		version := int64(0)
		if prior != nil {
			next, version = *prior, prior.Version
		}
		next.State, next.DesiredRevision, next.UpdatedAt, next.RetryAfter = model.IndexPending, revision, now, 0
		next.Version = version + 1
		err = r.indexWrite(ctx, indexKey(model.IndexJobsPK, key.Hash()), next, indexVersion(version), nil)
		if errors.Is(err, ErrConditionFailed) {
			continue
		}
		return err
	}
	return ErrConditionFailed
}

func indexVersion(version int64) expression.ConditionBuilder {
	if version == 0 {
		return expression.AttributeNotExists(expression.Name("PK"))
	}
	return expression.AttributeExists(expression.Name("PK")).And(expression.Name("version").Equal(expression.Value(version)))
}

func (r *DynamoDBRepository) SaveIndexJob(ctx context.Context, prior, next *model.IndexJob, source *model.IndexRecord, checkSource bool, now int64) error {
	condition := indexVersion(prior.Version)
	if prior.RunID != "" {
		condition = condition.And(expression.Name("runId").Equal(expression.Value(prior.RunID)))
	}
	if prior.State == model.IndexPreparing {
		lease := expression.Name("leaseUntil")
		if next.RunID == prior.RunID {
			condition = condition.And(lease.GreaterThan(expression.Value(now)))
		} else {
			condition = condition.And(lease.LessThanEqual(expression.Value(now)))
		}
	}
	next.Version = prior.Version + 1
	var check *types.ConditionCheck
	if checkSource {
		guard := expression.AttributeNotExists(expression.Name("PK"))
		if source != nil {
			guard = expression.AttributeExists(expression.Name("PK"))
			for _, field := range model.IndexSourceFields(next.Resource.Kind) {
				if value, ok := source.Fields[field]; ok {
					guard = guard.And(expression.Name(field).Equal(expression.Value(value)))
				} else {
					guard = guard.And(expression.AttributeNotExists(expression.Name(field)))
				}
			}
		}
		expr, err := expression.NewBuilder().WithCondition(guard).Build()
		if err != nil {
			return err
		}
		check = &types.ConditionCheck{TableName: aws.String(r.tableName), Key: indexKey(next.Resource.PK, next.Resource.SK),
			ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}
	}
	return r.indexWrite(ctx, indexKey(model.IndexJobsPK, next.Resource.Hash()), next, condition, check)
}

func (r *DynamoDBRepository) GetIndexControl(ctx context.Context) (*model.IndexControl, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(r.tableName), Key: indexKey(model.IndexControlPK, "STATE"), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	control := &model.IndexControl{Phase: "IDLE"}
	if len(out.Item) != 0 {
		if err := attributevalue.UnmarshalMap(out.Item, control); err != nil {
			return nil, err
		}
	}
	return control, nil
}

func (r *DynamoDBRepository) SaveIndexControl(ctx context.Context, prior, next *model.IndexControl, now int64) error {
	condition := indexVersion(prior.Version)
	if prior.Version != 0 {
		condition = condition.And(expression.Name("owner").Equal(expression.Value(prior.Owner)))
		if prior.Owner != next.Owner {
			condition = condition.And(expression.Name("leaseUntil").LessThanEqual(expression.Value(now)))
		} else {
			condition = condition.And(expression.Name("leaseUntil").GreaterThan(expression.Value(now)))
		}
	}
	next.Version = prior.Version + 1
	return r.indexWrite(ctx, indexKey(model.IndexControlPK, "STATE"), next, condition, nil)
}

func (r *DynamoDBRepository) indexWrite(ctx context.Context, key map[string]types.AttributeValue, value interface{}, condition expression.ConditionBuilder, check *types.ConditionCheck) error {
	attrs, err := attributevalue.MarshalMap(value)
	if err != nil {
		return err
	}
	var update expression.UpdateBuilder
	for name, value := range attrs {
		update = update.Set(expression.Name(name), expression.Value(value))
	}
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return err
	}
	if check == nil {
		_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(r.tableName), Key: key,
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	} else {
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
			{ConditionCheck: check}, {Update: &types.Update{TableName: aws.String(r.tableName), Key: key,
				UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
				ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()}},
		}})
	}
	var failed *types.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return ErrConditionFailed
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) {
		for _, reason := range cancelled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				return ErrConditionFailed
			}
		}
	}
	return err
}

func indexCursor(key map[string]types.AttributeValue) string {
	if len(key) == 0 {
		return ""
	}
	values := map[string]string{}
	for name, value := range key {
		if s, ok := value.(*types.AttributeValueMemberS); ok {
			values[name] = s.Value
		}
	}
	data, _ := json.Marshal(values)
	return base64.RawURLEncoding.EncodeToString(data)
}
func indexCursorKey(cursor string) (map[string]types.AttributeValue, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, errors.Join(ErrInvalidIndexCursor, err)
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) != nil || values["PK"] == "" || values["SK"] == "" {
		return nil, ErrInvalidIndexCursor
	}
	return indexKey(values["PK"], values["SK"]), nil
}

func (r *DynamoDBRepository) ScanIndexSources(ctx context.Context, cursor string, limit int32) ([]model.IndexResource, string, error) {
	start, err := indexCursorKey(cursor)
	if err != nil {
		return nil, "", err
	}
	user := expression.BeginsWith(expression.Name("PK"), "USER#")
	account := expression.BeginsWith(expression.Name("PK"), "ACCOUNT#")
	doc := expression.BeginsWith(expression.Name("SK"), "DOC#")
	filter := user.And(expression.BeginsWith(expression.Name("SK"), "MEETING#").Or(doc)).Or(account.And(doc))
	expr, err := expression.NewBuilder().WithFilter(filter).WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("SK"))).Build()
	if err != nil {
		return nil, "", err
	}
	out, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		ExclusiveStartKey: start, Limit: aws.Int32(limit), FilterExpression: expr.Filter(),
		ProjectionExpression: expr.Projection(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	if err != nil {
		return nil, "", err
	}
	keys := []model.IndexResource{}
	for _, item := range out.Items {
		var key struct{ PK, SK string }
		if err := attributevalue.UnmarshalMap(item, &key); err != nil {
			return nil, "", err
		}
		if resource, ok := model.CanonicalIndexResource(key.PK, key.SK); ok {
			keys = append(keys, resource)
		}
	}
	return keys, indexCursor(out.LastEvaluatedKey), nil
}

func (r *DynamoDBRepository) ListIndexJobs(ctx context.Context, cursor string, limit int32) ([]model.IndexJob, string, error) {
	start, err := indexCursorKey(cursor)
	if err != nil {
		return nil, "", err
	}
	expr, err := expression.NewBuilder().WithKeyCondition(expression.Key("PK").Equal(expression.Value(model.IndexJobsPK))).Build()
	if err != nil {
		return nil, "", err
	}
	out, err := r.client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true), Limit: aws.Int32(limit),
		ExclusiveStartKey: start, KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values()})
	if err != nil {
		return nil, "", err
	}
	jobs := []model.IndexJob{}
	for _, item := range out.Items {
		var job model.IndexJob
		if err := attributevalue.UnmarshalMap(item, &job); err != nil {
			return nil, "", err
		}
		jobs = append(jobs, job)
	}
	return jobs, indexCursor(out.LastEvaluatedKey), nil
}

// IndexTranscriptKey reuses the same bucket/authorized-ID/field guard as reads.
func IndexTranscriptKey(bucket, meetingID, field, ref string) (string, error) {
	_, key, err := validateTranscriptRef(bucket, meetingID, field, ref)
	return key, err
}
