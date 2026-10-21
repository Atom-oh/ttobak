package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestCrawlHistoryPreservesDecimalCostAndUnknownUsage(t *testing.T) {
	var history CrawlHistory
	err := attributevalue.UnmarshalMap(map[string]types.AttributeValue{
		"chargedUsd":    &types.AttributeValueMemberN{Value: "0.123456789012"},
		"usageComplete": &types.AttributeValueMemberBOOL{Value: false},
		"queryCount":    &types.AttributeValueMemberN{Value: "0"},
		"workerOutcomes": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"REJECTED": &types.AttributeValueMemberN{Value: "2"},
		}},
	}, &history)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(history)
	if err != nil || !strings.Contains(string(data), `"chargedUsd":"0.123456789012"`) ||
		!strings.Contains(string(data), `"usageComplete":false`) ||
		!strings.Contains(string(data), `"queryCount":0`) ||
		strings.Contains(string(data), `"staleRejected"`) || history.WorkerOutcomes["REJECTED"] != 2 {
		t.Fatalf("lost history precision or unknown-usage warning: %s %v", data, err)
	}
	legacy, err := json.Marshal(CrawlHistory{})
	if err != nil || strings.Contains(string(legacy), "usageComplete") ||
		strings.Contains(string(legacy), "chargedUsd") || strings.Contains(string(legacy), "queryCount") {
		t.Fatalf("legacy history invented cost or usage: %s %v", legacy, err)
	}
}
