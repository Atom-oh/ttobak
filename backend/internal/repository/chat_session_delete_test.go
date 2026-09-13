package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestDeleteChatSessionRemovesAllOwnedRowsAtomically(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "transaction_failure"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
				calls++
				if !strings.HasSuffix(request.Header.Get("X-Amz-Target"), ".TransactWriteItems") {
					t.Fatalf("session deletion must be one transaction, got %s", request.Header.Get("X-Amz-Target"))
				}
				var payload struct {
					TransactItems []struct {
						Delete struct {
							TableName string
							Key       map[string]map[string]string
						}
					}
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				var keys []string
				for _, item := range payload.TransactItems {
					if item.Delete.TableName != "table" {
						t.Fatalf("unexpected table %s", item.Delete.TableName)
					}
					keys = append(keys, item.Delete.Key["PK"]["S"]+"/"+item.Delete.Key["SK"]["S"])
				}
				sort.Strings(keys)
				want := []string{
					"SESSION#owner#chat-session/MESSAGES",
					"SESSION#owner#chat-session/SOURCE_DETAILS",
					"USER#owner/CHAT_SESSION#chat-session",
				}
				if !reflect.DeepEqual(keys, want) {
					t.Fatalf("deleted keys = %v, want %v", keys, want)
				}
				if failure {
					return summaryHTTPResponse(400, `{"__type":"TransactionCanceledException","Message":"synthetic rejection"}`, nil), nil
				}
				return summaryHTTPResponse(200, `{}`, nil), nil
			})
			err := repo.DeleteChatSession(context.Background(), "owner", "chat-session")
			if failure {
				var canceled *types.TransactionCanceledException
				if !errors.As(err, &canceled) {
					t.Fatalf("transaction failure must be preserved, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want one atomic delete", calls)
			}
		})
	}
}
