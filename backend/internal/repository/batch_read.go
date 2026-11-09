package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/fanout"
	"github.com/ttobak/backend/internal/model"
)

// Consistent batch reads replace per-item GetItem fan-out with the same
// strongly consistent row reads. BatchGetItem accepts at most 100 keys.
const (
	batchReadChunk       = 100
	batchReadConcurrency = 4
	// batchReadStalls bounds responses per chunk that drain no keys. A
	// partial response that drains some keys (DynamoDB's 16 MB response or
	// per-partition limits) is progress and does not count; every chunk
	// therefore finishes within 100 progressing responses plus the stalls.
	batchReadStalls   = 4
	batchReadKeyLimit = 10000
)

// batchReadBackoff is the base delay before retrying UnprocessedKeys, and
// batchReadDeadline caps one batch read call however its chunks progress.
var (
	batchReadBackoff  = 25 * time.Millisecond
	batchReadDeadline = 10 * time.Second
)

type batchRowKey struct{ PK, SK string }

// batchGetConsistent reads every distinct key with ConsistentRead and hands
// each returned row to decode exactly once, so raw items are dropped as they
// arrive. Missing rows are never passed to decode. Persistent UnprocessedKeys,
// the call deadline, a response outside the requested keys, a duplicate
// response, or a decode error fail the whole read: a partial result is never
// reported as a set of missing rows.
func (r *DynamoDBRepository) batchGetConsistent(ctx context.Context, keys []batchRowKey, projection *expression.Expression, decode func(batchRowKey, map[string]types.AttributeValue) error) error {
	if len(keys) > batchReadKeyLimit {
		return fmt.Errorf("batch read selection exceeds limit")
	}
	requested := make(map[batchRowKey]bool, len(keys))
	pending := make([]map[string]types.AttributeValue, 0, len(keys))
	for _, key := range keys {
		if key.PK == "" || key.SK == "" {
			return fmt.Errorf("invalid batch read key")
		}
		if requested[key] {
			continue
		}
		requested[key] = true
		pending = append(pending, map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: key.PK},
			"SK": &types.AttributeValueMemberS{Value: key.SK},
		})
	}
	if len(pending) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, batchReadDeadline)
	defer cancel()
	var mu sync.Mutex
	received := make(map[batchRowKey]bool, len(pending))
	accept := func(row map[string]types.AttributeValue) error {
		pk, pkOK := row["PK"].(*types.AttributeValueMemberS)
		sk, skOK := row["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK {
			return fmt.Errorf("invalid batch read response")
		}
		key := batchRowKey{PK: pk.Value, SK: sk.Value}
		mu.Lock()
		defer mu.Unlock()
		if !requested[key] || received[key] {
			return fmt.Errorf("unexpected batch read response")
		}
		received[key] = true
		return decode(key, row)
	}
	request := func(chunk []map[string]types.AttributeValue) types.KeysAndAttributes {
		batch := types.KeysAndAttributes{Keys: chunk, ConsistentRead: aws.Bool(true)}
		if projection != nil {
			batch.ProjectionExpression = projection.Projection()
			batch.ExpressionAttributeNames = projection.Names()
		}
		return batch
	}
	readChunk := func(ctx context.Context, chunk []map[string]types.AttributeValue) error {
		for stalls := 0; ; {
			out, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: map[string]types.KeysAndAttributes{r.tableName: request(chunk)},
			})
			if err != nil {
				return fmt.Errorf("batch get rows: %w", err)
			}
			for _, row := range out.Responses[r.tableName] {
				if err := accept(row); err != nil {
					return err
				}
			}
			next := out.UnprocessedKeys[r.tableName].Keys
			if len(next) == 0 {
				return nil
			}
			if len(next) >= len(chunk) {
				if stalls++; stalls >= batchReadStalls {
					return fmt.Errorf("batch get rows: unprocessed keys remain after %d stalled responses", stalls)
				}
			}
			chunk = next
			timer := time.NewTimer(time.Duration(stalls+1) * batchReadBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("batch get rows: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}
	chunks := (len(pending) + batchReadChunk - 1) / batchReadChunk
	errs := make([]error, chunks)
	stop := fanout.Ordered(ctx, chunks, batchReadConcurrency, func(ctx context.Context, c int) error {
		errs[c] = readChunk(ctx, pending[c*batchReadChunk:min((c+1)*batchReadChunk, len(pending))])
		return errs[c]
	})
	if stop < chunks {
		return errs[stop]
	}
	return nil
}

// AccountRowKey selects an account META row (UserID empty) or the exact
// ACCOUNT#{AccountID}/MEMBER#{UserID} membership row.
type AccountRowKey struct{ AccountID, UserID string }

// AccountRows holds only rows that exist; requested keys absent from a map
// were read and found missing.
type AccountRows struct {
	Accounts map[string]*model.Account
	Members  map[AccountRowKey]*model.AccountMember
}

// BatchGetAccountRows is the batched equivalent of GetAccount and GetMember:
// the same keys, consistent reads and unmarshalling, without identity checks
// the single-item primitives do not make either.
func (r *DynamoDBRepository) BatchGetAccountRows(ctx context.Context, keys []AccountRowKey) (*AccountRows, error) {
	rowKeys := make([]batchRowKey, 0, len(keys))
	members := map[batchRowKey]AccountRowKey{}
	for _, key := range keys {
		if key.AccountID == "" {
			return nil, fmt.Errorf("invalid account row key")
		}
		row := batchRowKey{PK: model.PrefixAccount + key.AccountID, SK: model.SKAccountMeta}
		if key.UserID != "" {
			row.SK = model.PrefixMember + key.UserID
			members[row] = key
		}
		rowKeys = append(rowKeys, row)
	}
	out := &AccountRows{Accounts: map[string]*model.Account{}, Members: map[AccountRowKey]*model.AccountMember{}}
	err := r.batchGetConsistent(ctx, rowKeys, nil, func(row batchRowKey, item map[string]types.AttributeValue) error {
		if key, ok := members[row]; ok {
			var member model.AccountMember
			if err := attributevalue.UnmarshalMap(item, &member); err != nil {
				return fmt.Errorf("unmarshal member: %w", err)
			}
			out.Members[key] = &member
			return nil
		}
		var account model.Account
		if err := attributevalue.UnmarshalMap(item, &account); err != nil {
			return fmt.Errorf("unmarshal account: %w", err)
		}
		out.Accounts[row.PK[len(model.PrefixAccount):]] = &account
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("batch get account rows: %w", err)
	}
	return out, nil
}

// ProjectListRowFields are the only attributes BatchGetProjectListRows reads:
// row identity, the ProjectSummary fields and the canonical account links.
// Description and other unbounded fields are never transferred.
var ProjectListRowFields = []string{"PK", "SK", "entityType", "projectId", "name", "stage", "stageChangedAt", "sfdcOpptyId", "ownerUserId", "accountIds"}

// BatchGetProjects is the batched equivalent of GetProject for canonical
// PROJECT#/CONFIG rows. Missing projects are absent from the result.
func (r *DynamoDBRepository) BatchGetProjects(ctx context.Context, projectIDs []string) (map[string]*model.Project, error) {
	return r.batchGetProjects(ctx, projectIDs, nil)
}

// BatchGetProjectListRows reads only ProjectListRowFields of canonical project
// rows; the returned records are partial and must not be written back.
func (r *DynamoDBRepository) BatchGetProjectListRows(ctx context.Context, projectIDs []string) (map[string]*model.Project, error) {
	names := make([]expression.NameBuilder, 0, len(ProjectListRowFields))
	for _, name := range ProjectListRowFields {
		names = append(names, expression.Name(name))
	}
	projection, err := expression.NewBuilder().WithProjection(expression.NamesList(names[0], names[1:]...)).Build()
	if err != nil {
		return nil, fmt.Errorf("build project list projection: %w", err)
	}
	return r.batchGetProjects(ctx, projectIDs, &projection)
}

func (r *DynamoDBRepository) batchGetProjects(ctx context.Context, projectIDs []string, projection *expression.Expression) (map[string]*model.Project, error) {
	rowKeys := make([]batchRowKey, 0, len(projectIDs))
	for _, id := range projectIDs {
		if id == "" {
			return nil, fmt.Errorf("invalid project row key")
		}
		rowKeys = append(rowKeys, batchRowKey{PK: model.PrefixProject + id, SK: model.SKProjectConfig})
	}
	out := make(map[string]*model.Project, len(rowKeys))
	err := r.batchGetConsistent(ctx, rowKeys, projection, func(row batchRowKey, item map[string]types.AttributeValue) error {
		var project model.Project
		if err := attributevalue.UnmarshalMap(item, &project); err != nil {
			return fmt.Errorf("unmarshal project: %w", err)
		}
		out[row.PK[len(model.PrefixProject):]] = &project
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("batch get projects: %w", err)
	}
	return out, nil
}
