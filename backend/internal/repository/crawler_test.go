package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

func TestSourceAliasUpdateRequiresExistingUnchangedRow(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		calls := 0
		client := dynamodb.New(dynamodb.Options{
			Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
			HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.UpdateItem" {
					t.Fatal("alias update must use conditional UpdateItem")
				}
				var input struct {
					UpdateExpression          string
					ConditionExpression       string
					ExpressionAttributeNames  map[string]string
					ExpressionAttributeValues map[string]map[string]json.RawMessage
				}
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				condition, update := input.ConditionExpression, input.UpdateExpression
				for placeholder, name := range input.ExpressionAttributeNames {
					condition = strings.ReplaceAll(condition, placeholder, name)
					update = strings.ReplaceAll(update, placeholder, name)
				}
				if !strings.Contains(condition, "attribute_exists (PK)") ||
					!strings.Contains(condition, "aliases =") || !strings.Contains(condition, "ownerId =") ||
					!strings.Contains(update, "SET aliases =") ||
					strings.Contains(update, "ownerId") {
					t.Fatalf("missing source existence/alias CAS: condition %s update %s", condition, update)
				}
				sawOld, sawNew, sawOwner := false, false, false
				for _, value := range input.ExpressionAttributeValues {
					if raw, ok := value["S"]; ok {
						sawOwner = sawOwner || string(raw) == `"owner"`
					}
					if raw, ok := value["L"]; ok {
						var aliases []map[string]string
						if err := json.Unmarshal(raw, &aliases); err != nil {
							t.Fatal(err)
						}
						sawOld = sawOld || len(aliases) == 1 && aliases[0]["S"] == "saved"
						sawNew = sawNew || len(aliases) == 2 && aliases[1]["S"] == "new"
					}
				}
				if !sawOld || !sawNew || !sawOwner {
					t.Fatal("alias values were not encoded as native DynamoDB string lists")
				}
				status, body := http.StatusOK, "{}"
				if rejected {
					status, body = http.StatusBadRequest, `{"__type":"ConditionalCheckFailedException","message":"changed"}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(body))}, nil
			}),
		})
		aliases := []string{"saved", "new"}
		owner := "owner"
		err := NewCrawlerRepository(client, "news").UpdateSourcePartial(context.Background(), "source",
			&model.CrawlerSource{Aliases: []string{"saved"}, OwnerID: owner},
			SourcePartialFields{Aliases: &aliases, RequireOwnerID: &owner})
		if calls != 1 || rejected && !errors.Is(err, ErrConditionFailed) || !rejected && err != nil {
			t.Fatalf("conditional write result lost: calls %d rejected %v err %v", calls, rejected, err)
		}
	}
}

// TestSourceFieldUnchangedCondition_EmptyValueAcceptsAbsentAttribute is a
// regression test: a legacy crawler source written before a given field
// existed (or one of the union fields Unsubscribe's last-subscriber branch
// clears to nil) has that attribute genuinely ABSENT/NULL in DynamoDB, not
// present-as-an-empty-list. Building the condition as a plain Equal against
// an empty list would then never match a real GetItem result for such an
// item, deterministically failing UpdateSourcePartial's condition (and
// exhausting the caller's bounded retry) even with no concurrent write at
// all. The fix ORs attribute_not_exists in for this case.
func TestSourceFieldUnchangedCondition_EmptyValueAcceptsAbsentAttribute(t *testing.T) {
	cond := sourceFieldUnchangedCondition("newsSources", []string(nil))

	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		t.Fatalf("failed to build expression: %v", err)
	}

	condStr := *expr.Condition()
	if !strings.Contains(condStr, "attribute_not_exists") {
		t.Errorf("expected condition to include attribute_not_exists for an empty value, got %q", condStr)
	}
	if !strings.Contains(condStr, "OR") {
		t.Errorf("expected condition to OR attribute_not_exists with an equality fallback, got %q", condStr)
	}
}

// TestSourceFieldUnchangedCondition_NonEmptyValueIsPlainEquality asserts the
// non-empty path stays a strict equality check (no OR/attribute_not_exists
// laxness) -- a genuine concurrent change to a populated field must still
// fail the condition.
func TestSourceFieldUnchangedCondition_NonEmptyValueIsPlainEquality(t *testing.T) {
	cond := sourceFieldUnchangedCondition("newsSources", []string{"aws-blog"})

	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		t.Fatalf("failed to build expression: %v", err)
	}

	condStr := *expr.Condition()
	if strings.Contains(condStr, "attribute_not_exists") {
		t.Errorf("expected a plain equality condition for a non-empty value, got %q", condStr)
	}
	if strings.Contains(condStr, "OR") {
		t.Errorf("expected no OR clause for a non-empty value, got %q", condStr)
	}
}

// TestSourceFieldUnchangedCondition_ScalarField covers the status field's
// plain-Equal path (never omitempty, so never treated as an absent-list
// case even though it's passed through the same helper).
func TestSourceFieldUnchangedCondition_ScalarField(t *testing.T) {
	cond := sourceFieldUnchangedCondition("status", "idle")

	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		t.Fatalf("failed to build expression: %v", err)
	}

	condStr := *expr.Condition()
	if strings.Contains(condStr, "attribute_not_exists") || strings.Contains(condStr, "OR") {
		t.Errorf("expected a plain equality condition for a scalar field, got %q", condStr)
	}
}
