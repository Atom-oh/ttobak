package model

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
)

// GSI3 is keyed on meetingId, and DynamoDB rejects an empty string in an
// index key ("The AttributeValue for a key attribute cannot contain an empty
// string value. IndexName: GSI3, IndexKey: meetingId"), so KB, research and
// news observations must omit the attribute; meeting observations keep it.
func TestDictObservationOmitsEmptyMeetingID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		meetingID string
		want      bool
	}{
		{"source observation", "", false},
		{"meeting observation", "m-1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, err := attributevalue.MarshalMap(DictObservation{PK: "USER#u", SK: "DICTOBS#k#kb:x", MeetingID: tc.meetingID, SourceKind: DictSourceKB})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := item["meetingId"]; ok != tc.want {
				t.Fatalf("meetingId present = %v, want %v", ok, tc.want)
			}
		})
	}
}
