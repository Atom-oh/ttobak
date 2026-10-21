package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// NewsReadKey identifies only canonical crawler configuration or document rows.
// Keeping raw attributes preserves absent fields and exact numeric values for
// the cross-language publication contract.
type NewsReadKey struct {
	SourceID string
	SortKey  string
}

// Valid reports whether this identity fits the bounded canonical read contract.
func (key NewsReadKey) Valid() bool {
	return key.SourceID != "" && len(key.SourceID) <= 512 &&
		(key.SortKey == "CONFIG" || (strings.HasPrefix(key.SortKey, "DOC#") && len(key.SortKey) <= 132))
}

// ReadNewsItems completely reads a bounded selection. Missing rows are absent
// from the result; unprocessed keys are never reported as missing publications.
func (r *CrawlerRepository) ReadNewsItems(ctx context.Context, keys []NewsReadKey) (map[NewsReadKey]map[string]types.AttributeValue, error) {
	if len(keys) > 1000 {
		return nil, fmt.Errorf("news read selection exceeds limit")
	}
	selection := make(map[NewsReadKey]bool, len(keys))
	pending := make([]map[string]types.AttributeValue, 0, len(keys))
	for _, key := range keys {
		if !key.Valid() {
			return nil, fmt.Errorf("invalid news read key")
		}
		if selection[key] {
			continue
		}
		selection[key] = true
		pending = append(pending, map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CRAWLER#" + key.SourceID},
			"SK": &types.AttributeValueMemberS{Value: key.SortKey},
		})
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rows := make(map[NewsReadKey]map[string]types.AttributeValue, len(selection))
	fields := []expression.NameBuilder{}
	for _, name := range []string{
		"PK", "SK", "sourceId", "sourceName", "aliases", "newsQueries", "customUrls", "status", "ownerId",
		"docHash", "schemaVersion", "qualityStatus", "type", "attemptToken", "sourceRevision",
		"s3Key", "artifactKey", "markdownSha256", "artifactSha256", "title", "url", "source",
		"summary", "awsServices", "tags", "crawledAt", "inKB", "pubDate",
	} {
		fields = append(fields, expression.Name(name))
	}
	projection, err := expression.NewBuilder().WithProjection(expression.NamesList(fields[0], fields[1:]...)).Build()
	if err != nil {
		return nil, err
	}
	batchFor := func(keys []map[string]types.AttributeValue) types.KeysAndAttributes {
		return types.KeysAndAttributes{Keys: keys, ConsistentRead: aws.Bool(true),
			ProjectionExpression: projection.Projection(), ExpressionAttributeNames: projection.Names()}
	}
	totalBytes := 0
	for start := 0; start < len(pending); start += 100 {
		end := min(start+100, len(pending))
		batch := batchFor(pending[start:end])
		for attempt := 0; ; attempt++ {
			response, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: map[string]types.KeysAndAttributes{r.tableName: batch},
			})
			if err != nil {
				return nil, fmt.Errorf("read current news state: %w", err)
			}
			for _, row := range response.Responses[r.tableName] {
				encoded, err := json.Marshal(row)
				if err != nil {
					return nil, fmt.Errorf("encode current news state: %w", err)
				}
				totalBytes += len(encoded)
				if totalBytes > 8*1024*1024 {
					return nil, fmt.Errorf("current news state exceeds read budget")
				}
				pk, pkOK := row["PK"].(*types.AttributeValueMemberS)
				sk, skOK := row["SK"].(*types.AttributeValueMemberS)
				if !pkOK || !skOK || !strings.HasPrefix(pk.Value, "CRAWLER#") {
					return nil, fmt.Errorf("invalid news state response")
				}
				key := NewsReadKey{SourceID: strings.TrimPrefix(pk.Value, "CRAWLER#"), SortKey: sk.Value}
				if !selection[key] || rows[key] != nil {
					return nil, fmt.Errorf("unexpected news state response")
				}
				rows[key] = row
			}
			unprocessed := response.UnprocessedKeys[r.tableName].Keys
			if len(unprocessed) == 0 {
				break
			}
			if attempt >= 2 {
				return nil, fmt.Errorf("current news state read incomplete")
			}
			batch = batchFor(unprocessed)
			timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return rows, nil
}
