package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// audioBindWire emulates one meeting row and evaluates BindMeetingAudioKey's
// condition the way DynamoDB would, after checking the request carries it.
type audioBindWire struct {
	exists   bool
	audioKey string // "" means the attribute is absent
	status   string
	updates  int
}

func (w *audioBindWire) repository(t *testing.T) *DynamoDBRepository {
	t.Helper()
	return summarySDKRepo(summaryHTTP(func(req *http.Request) (*http.Response, error) {
		if op := req.Header.Get("X-Amz-Target"); op != "DynamoDB_20120810.UpdateItem" {
			t.Fatalf("unexpected operation %s", op)
		}
		var input notesRevisionRequest
		var raw map[string]any
		if err := json.NewDecoder(req.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(raw)
		if err := json.Unmarshal(wire, &input); err != nil {
			t.Fatal(err)
		}
		if raw["ReturnValuesOnConditionCheckFailure"] != "ALL_OLD" {
			t.Fatal("a failed condition must return the item to tell 'already bound' from 'missing'")
		}
		cond := input.ConditionExpression
		if !strings.Contains(cond, "attribute_exists") || !strings.Contains(cond, "attribute_not_exists") || !strings.Contains(cond, "<>") {
			t.Fatalf("condition does not guard the same key: %s", cond)
		}
		key, ok := notesWireValue(input, input.UpdateExpression, "audioKey")
		status, _ := notesWireValue(input, input.UpdateExpression, "status")
		if !ok || status != "transcribing" {
			t.Fatalf("update must set audioKey and status transcribing: %s", input.UpdateExpression)
		}
		switch {
		case !w.exists:
			return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
		case w.audioKey == key:
			item := `{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#m"},"audioKey":{"S":"` + key + `"},"status":{"S":"` + w.status + `"}}`
			return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException","Item":`+item+`}`, nil), nil
		}
		w.audioKey, w.status = key, status
		w.updates++
		return summaryHTTPResponse(200, `{}`, nil), nil
	}))
}

func TestBindMeetingAudioKey(t *testing.T) {
	const key = "audio/owner/m/1700000000000_mcp_upload_1700000000000.wav"
	cases := []struct {
		name       string
		wire       audioBindWire
		wantBound  bool
		wantErr    error
		wantStatus string
	}{
		{"first bind moves a recording meeting to transcribing", audioBindWire{exists: true, status: "recording"}, true, nil, "transcribing"},
		{"same key never resets a finished meeting", audioBindWire{exists: true, audioKey: key, status: "done"}, false, nil, "done"},
		{"same key while transcribing is a no-op", audioBindWire{exists: true, audioKey: key, status: "transcribing"}, false, nil, "transcribing"},
		{"a different key still replaces the audio", audioBindWire{exists: true, audioKey: "audio/owner/m/old.m4a", status: "done"}, true, nil, "transcribing"},
		{"a missing meeting reports the failed condition", audioBindWire{}, false, ErrConditionFailed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.wire
			bound, err := wire.repository(t).BindMeetingAudioKey(context.Background(), "owner", "m", key)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if bound != tc.wantBound {
				t.Fatalf("bound = %v, want %v", bound, tc.wantBound)
			}
			if wire.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", wire.status, tc.wantStatus)
			}
			if !tc.wantBound && wire.updates != 0 {
				t.Fatal("an unbound call must not write")
			}
		})
	}
}
