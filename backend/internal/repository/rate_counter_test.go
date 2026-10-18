package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeRateCounterClient struct {
	input *dynamodb.UpdateItemInput
	out   *dynamodb.UpdateItemOutput
	err   error
}

func (f *fakeRateCounterClient) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.input = in
	return f.out, f.err
}

func TestIncrementWindowCounterRequestShape(t *testing.T) {
	client := &fakeRateCounterClient{out: &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{
		"count": &types.AttributeValueMemberN{Value: "7"},
	}}}
	repo := NewRateCounterRepository(client, "ttobak-main")
	got, err := repo.IncrementWindowCounter(context.Background(), "user-1", "RATE#translate#2026-10-18T04:00Z", 2, 1760770800)
	if err != nil || got != 7 {
		t.Fatalf("got %d, %v", got, err)
	}
	in := client.input
	if pk := in.Key["PK"].(*types.AttributeValueMemberS).Value; pk != "USER#user-1" {
		t.Fatalf("PK = %q", pk)
	}
	if sk := in.Key["SK"].(*types.AttributeValueMemberS).Value; sk != "RATE#translate#2026-10-18T04:00Z" {
		t.Fatalf("SK = %q", sk)
	}
	expr := *in.UpdateExpression
	if !strings.Contains(expr, "ADD ") || !strings.Contains(expr, "SET ") {
		t.Fatalf("update expression %q must ADD the count and SET the TTL", expr)
	}
	names := map[string]bool{}
	for _, name := range in.ExpressionAttributeNames {
		names[name] = true
	}
	if !names["count"] || !names["pendingShareExpiresAt"] {
		t.Fatalf("attribute names = %v", in.ExpressionAttributeNames)
	}
	values := map[string]bool{}
	for _, value := range in.ExpressionAttributeValues {
		if n, ok := value.(*types.AttributeValueMemberN); ok {
			values[n.Value] = true
		}
	}
	if !values["2"] || !values["1760770800"] {
		t.Fatalf("attribute values = %v", in.ExpressionAttributeValues)
	}
	if in.ConditionExpression != nil {
		t.Fatal("a counter increment must not be conditional")
	}
	if in.ReturnValues != types.ReturnValueUpdatedNew {
		t.Fatalf("ReturnValues = %v", in.ReturnValues)
	}
}

func TestIncrementWindowCounterErrors(t *testing.T) {
	tests := []struct {
		name string
		out  *dynamodb.UpdateItemOutput
		err  error
	}{
		{"update failure", nil, errors.New("throttled")},
		{"missing count", &dynamodb.UpdateItemOutput{}, nil},
		{"non-numeric count", &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{
			"count": &types.AttributeValueMemberN{Value: "x"},
		}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewRateCounterRepository(&fakeRateCounterClient{out: tt.out, err: tt.err}, "t")
			if _, err := repo.IncrementWindowCounter(context.Background(), "u", "RATE#b#k", 1, 1); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
