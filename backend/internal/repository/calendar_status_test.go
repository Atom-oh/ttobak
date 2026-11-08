package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestCalendarCompletionRevisionSurvivesEqualAndRegressingAttempts(test *testing.T) {
	for _, offset := range []time.Duration{0, -time.Second} {
		test.Run(offset.String(), func(test *testing.T) {
			stored := calendarKey("owner", "CALSOURCE#"+calendarTestSource)
			mock := &calendarSDKMock{
				get: func(input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
					if calendarTestString(test, input.Key, "SK") == "CALSOURCE#"+calendarTestSource {
						return &dynamodb.GetItemOutput{Item: stored}, nil
					}
					return &dynamodb.GetItemOutput{}, nil
				},
				transact: func(input *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
					update := input.TransactItems[0].Update
					for _, assignment := range regexp.MustCompile(`(#[0-9]+) = (:[0-9]+)`).FindAllStringSubmatch(aws.ToString(update.UpdateExpression), -1) {
						stored[update.ExpressionAttributeNames[assignment[1]]] = update.ExpressionAttributeValues[assignment[2]]
					}
					return &dynamodb.TransactWriteItemsOutput{}, nil
				},
				query: func(*dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
					return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{stored}}, nil
				},
			}
			repo := calendarTestRepo(test, mock)
			batch := calendarTestWrite()
			batch.Mutations = nil
			batch.Receipt.Upserted = 0
			batch.Receipt.Completed = true
			if _, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); err != nil {
				test.Fatal(err)
			}
			var persisted map[string]any
			if err := attributevalue.UnmarshalMap(stored, &persisted); err != nil {
				test.Fatal(err)
			}
			if persisted["lastCompletedRevision"] != float64(1) {
				test.Fatalf("completed transaction must persist revision 1; got %v", persisted["lastCompletedRevision"])
			}
			batch.ExpectedRevision = 1
			batch.Receipt.Revision = 2
			batch.Receipt.BatchID = "dc93f9a3-3595-4871-a183-087fa81c5106"
			batch.Receipt.Completed = false
			batch.AcceptedAt = batch.AcceptedAt.Add(offset)
			batch.ExpiresAt = batch.AcceptedAt.Add(7 * 24 * time.Hour).Unix()
			batch.WindowStart = batch.WindowStart.Add(time.Hour)
			if _, err := repo.ApplyCalendarBatch(context.Background(), "owner", calendarTestSource, batch); err != nil {
				test.Fatal(err)
			}
			page, err := repo.ListCalendarSources(context.Background(), "owner", "", 50)
			if err != nil || len(page.Sources) != 1 {
				test.Fatalf("read status: %v, %v", page, err)
			}
			encoded, err := json.Marshal(page.Sources[0])
			if err != nil {
				test.Fatal(err)
			}
			var status map[string]any
			if err := json.Unmarshal(encoded, &status); err != nil {
				test.Fatal(err)
			}
			if status["revision"] != float64(2) || status["lastCompletedRevision"] != float64(1) ||
				!page.Sources[0].LastCompletedAt.Equal(calendarTestNow) ||
				!page.Sources[0].CompletedWindowStart.Equal(calendarTestWrite().WindowStart) {
				test.Fatalf("partial progress must preserve the completion revision and window: %s", encoded)
			}
		})
	}
}
