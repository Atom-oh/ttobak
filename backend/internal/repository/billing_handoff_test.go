package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeBillingHandoffClient struct {
	input *dynamodb.PutItemInput
	err   error
}

func (f *fakeBillingHandoffClient) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.input = in
	return &dynamodb.PutItemOutput{}, f.err
}

func TestPutBillingHandoffRequestShape(t *testing.T) {
	client := &fakeBillingHandoffClient{}
	repo := NewBillingHandoffRepository(client, "ttobak-main")
	if err := repo.PutBillingHandoff(context.Background(), "abc123", "sub-1", "user@example.com", 1760770860); err != nil {
		t.Fatal(err)
	}
	in := client.input
	if aws.ToString(in.TableName) != "ttobak-main" {
		t.Fatalf("table = %q", aws.ToString(in.TableName))
	}
	want := map[string]string{
		"PK": "HANDOFF#abc123", "SK": "HANDOFF", "target": "billing-insight",
		"sub": "sub-1", "email": "user@example.com",
	}
	for name, value := range want {
		s, ok := in.Item[name].(*types.AttributeValueMemberS)
		if !ok || s.Value != value {
			t.Fatalf("%s = %#v, want %q", name, in.Item[name], value)
		}
	}
	ttl, ok := in.Item["pendingShareExpiresAt"].(*types.AttributeValueMemberN)
	if !ok || ttl.Value != "1760770860" {
		t.Fatalf("pendingShareExpiresAt = %#v", in.Item["pendingShareExpiresAt"])
	}
	if len(in.Item) != len(want)+1 {
		t.Fatalf("unexpected item attributes: %v", in.Item)
	}
	for _, gsi := range []string{"GSI1PK", "GSI1SK"} {
		if _, ok := in.Item[gsi]; ok {
			t.Fatalf("hand-off rows must not carry %s", gsi)
		}
	}
	if in.ConditionExpression == nil {
		t.Fatal("put must be conditional")
	}
	cond := *in.ConditionExpression
	var pkPlaceholder string
	for placeholder, name := range in.ExpressionAttributeNames {
		if name == "PK" {
			pkPlaceholder = placeholder
		}
	}
	if pkPlaceholder == "" || cond != "attribute_not_exists ("+pkPlaceholder+")" {
		t.Fatalf("condition = %q names = %v", cond, in.ExpressionAttributeNames)
	}
}

func TestPutBillingHandoffErrors(t *testing.T) {
	repo := NewBillingHandoffRepository(&fakeBillingHandoffClient{err: &types.ConditionalCheckFailedException{}}, "t")
	if err := repo.PutBillingHandoff(context.Background(), "h", "s", "e", 1); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("collision err = %v", err)
	}
	repo = NewBillingHandoffRepository(&fakeBillingHandoffClient{err: errors.New("throttled")}, "t")
	err := repo.PutBillingHandoff(context.Background(), "h", "s", "e", 1)
	if err == nil || errors.Is(err, ErrConditionFailed) {
		t.Fatalf("generic err = %v", err)
	}
}
