package repository

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Batch and saved-source summary captures pin noteLanguage, so a language change
// after capture changes the resummary source hash and fails publication CAS.
func TestSummaryCapturesPinNoteLanguage(t *testing.T) {
	for name, fields := range map[string][]string{
		"batch":     withSummaryClearanceFields(batchSummaryFields),
		"resummary": withSummaryClearanceFields(summaryMeetingFields),
	} {
		t.Run(name, func(t *testing.T) {
			row := func(language string) map[string]types.AttributeValue {
				item := map[string]types.AttributeValue{"meetingId": &types.AttributeValueMemberS{Value: "m"}}
				if language != "absent" {
					item["noteLanguage"] = &types.AttributeValueMemberS{Value: language}
				}
				return item
			}
			hashes := map[string]bool{}
			for _, language := range []string{"absent", "", "ko", "en"} {
				check, err := summaryCheck("USER#u", "MEETING#m", row(language), fields)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(check)
				hashes[string(body)] = true
				expr, err := expression.NewBuilder().WithCondition(summaryCondition(check)).Build()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, attr := range expr.Names() {
					found = found || attr == "noteLanguage"
				}
				if !found {
					t.Fatalf("%s condition does not pin noteLanguage", language)
				}
			}
			if len(hashes) != 4 {
				t.Fatalf("noteLanguage states collapse: %d distinct checks", len(hashes))
			}
		})
	}
}
