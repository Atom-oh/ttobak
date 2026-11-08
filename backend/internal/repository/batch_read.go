package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
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
	batchReadAttempts    = 4
	batchReadKeyLimit    = 10000
)

// batchReadBackoff is the base delay before retrying UnprocessedKeys.
var batchReadBackoff = 25 * time.Millisecond

type batchRowKey struct{ PK, SK string }

// batchGetConsistent reads every distinct key with ConsistentRead. Missing rows
// are absent from the result. Persistent UnprocessedKeys, a response outside
// the requested keys, or a duplicate response fail the whole read: a partial
// result is never reported as a set of missing rows.
func (r *DynamoDBRepository) batchGetConsistent(ctx context.Context, keys []batchRowKey) (map[batchRowKey]map[string]types.AttributeValue, error) {
	if len(keys) > batchReadKeyLimit {
		return nil, fmt.Errorf("batch read selection exceeds limit")
	}
	requested := make(map[batchRowKey]bool, len(keys))
	pending := make([]map[string]types.AttributeValue, 0, len(keys))
	for _, key := range keys {
		if key.PK == "" || key.SK == "" {
			return nil, fmt.Errorf("invalid batch read key")
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
	rows := make(map[batchRowKey]map[string]types.AttributeValue, len(pending))
	var mu sync.Mutex
	accept := func(row map[string]types.AttributeValue) error {
		pk, pkOK := row["PK"].(*types.AttributeValueMemberS)
		sk, skOK := row["SK"].(*types.AttributeValueMemberS)
		if !pkOK || !skOK {
			return fmt.Errorf("invalid batch read response")
		}
		key := batchRowKey{PK: pk.Value, SK: sk.Value}
		mu.Lock()
		defer mu.Unlock()
		if !requested[key] || rows[key] != nil {
			return fmt.Errorf("unexpected batch read response")
		}
		rows[key] = row
		return nil
	}
	readChunk := func(ctx context.Context, chunk []map[string]types.AttributeValue) error {
		for attempt := 1; ; attempt++ {
			out, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: map[string]types.KeysAndAttributes{
					r.tableName: {Keys: chunk, ConsistentRead: aws.Bool(true)},
				},
			})
			if err != nil {
				return fmt.Errorf("batch get rows: %w", err)
			}
			for _, row := range out.Responses[r.tableName] {
				if err := accept(row); err != nil {
					return err
				}
			}
			chunk = out.UnprocessedKeys[r.tableName].Keys
			if len(chunk) == 0 {
				return nil
			}
			if attempt >= batchReadAttempts {
				return fmt.Errorf("batch get rows: unprocessed keys remain after %d attempts", attempt)
			}
			timer := time.NewTimer(time.Duration(attempt) * batchReadBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
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
		return nil, errs[stop]
	}
	return rows, nil
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
	for _, key := range keys {
		if key.AccountID == "" {
			return nil, fmt.Errorf("invalid account row key")
		}
		sk := model.SKAccountMeta
		if key.UserID != "" {
			sk = model.PrefixMember + key.UserID
		}
		rowKeys = append(rowKeys, batchRowKey{PK: model.PrefixAccount + key.AccountID, SK: sk})
	}
	rows, err := r.batchGetConsistent(ctx, rowKeys)
	if err != nil {
		return nil, fmt.Errorf("batch get account rows: %w", err)
	}
	out := &AccountRows{Accounts: map[string]*model.Account{}, Members: map[AccountRowKey]*model.AccountMember{}}
	for _, key := range keys {
		pk := model.PrefixAccount + key.AccountID
		if key.UserID == "" {
			item := rows[batchRowKey{PK: pk, SK: model.SKAccountMeta}]
			if item == nil || out.Accounts[key.AccountID] != nil {
				continue
			}
			var account model.Account
			if err := attributevalue.UnmarshalMap(item, &account); err != nil {
				return nil, fmt.Errorf("unmarshal account: %w", err)
			}
			out.Accounts[key.AccountID] = &account
			continue
		}
		item := rows[batchRowKey{PK: pk, SK: model.PrefixMember + key.UserID}]
		if item == nil || out.Members[key] != nil {
			continue
		}
		var member model.AccountMember
		if err := attributevalue.UnmarshalMap(item, &member); err != nil {
			return nil, fmt.Errorf("unmarshal member: %w", err)
		}
		out.Members[key] = &member
	}
	return out, nil
}

// BatchGetProjects is the batched equivalent of GetProject for canonical
// PROJECT#/CONFIG rows. Missing projects are absent from the result.
func (r *DynamoDBRepository) BatchGetProjects(ctx context.Context, projectIDs []string) (map[string]*model.Project, error) {
	rowKeys := make([]batchRowKey, 0, len(projectIDs))
	for _, id := range projectIDs {
		if id == "" {
			return nil, fmt.Errorf("invalid project row key")
		}
		rowKeys = append(rowKeys, batchRowKey{PK: model.PrefixProject + id, SK: model.SKProjectConfig})
	}
	rows, err := r.batchGetConsistent(ctx, rowKeys)
	if err != nil {
		return nil, fmt.Errorf("batch get projects: %w", err)
	}
	out := make(map[string]*model.Project, len(rows))
	for _, id := range projectIDs {
		item := rows[batchRowKey{PK: model.PrefixProject + id, SK: model.SKProjectConfig}]
		if item == nil || out[id] != nil {
			continue
		}
		var project model.Project
		if err := attributevalue.UnmarshalMap(item, &project); err != nil {
			return nil, fmt.Errorf("unmarshal project: %w", err)
		}
		out[id] = &project
	}
	return out, nil
}
