package repository

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestNumberSetFenceMatchesTheStoredNumberSet(t *testing.T) {
	item := map[string]types.AttributeValue{
		"audioPartsReadySet": &types.AttributeValueMemberNS{Value: []string{"0", "1"}},
	}
	check, err := summaryCheck("USER#owner", "MEETING#m", item, []string{"audioPartsReadySet"})
	if err != nil {
		t.Fatal(err)
	}
	expr, err := expression.NewBuilder().WithCondition(summaryCondition(check)).Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range expr.Values() {
		if set, ok := value.(*types.AttributeValueMemberNS); ok && reflect.DeepEqual(set.Value, []string{"0", "1"}) {
			return
		}
	}
	t.Fatalf("fence re-encoded the stored number set: %#v", expr.Values())
}
