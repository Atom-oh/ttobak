package main

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/ttobak/backend/internal/model"
)

func TestInvalidRecoveryScopeFailsBeforeAWSAccess(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, arguments := range [][]string{
		nil,
		{"--meeting-id", "meeting"},
		{"--meeting-id", "meeting", "--expected-account", "123456789012"},
		{"--meeting-id", "meeting", "--bucket", "bucket"},
		{"--expected-account", "123456789012", "--bucket", "bucket"},
		{"--meeting-id", "meeting", "--expected-account", "123456789012", "--bucket", "bucket", "another-meeting"},
		{"--meeting-id", "meeting", "--expected-account", "123456789012", "--bucket", "bucket", "--request", "--request-action-items"},
	} {
		err := run(arguments, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "required") && !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("invalid scope reached AWS: %v", err)
		}
	}
}

func TestRecoveryAccountGuardRejectsUnknownOrDifferentAccount(t *testing.T) {
	for _, identity := range []*sts.GetCallerIdentityOutput{
		nil, {}, {Account: aws.String("999999999999")},
	} {
		if err := verifyRecoveryAccount(identity, "123456789012"); err == nil {
			t.Fatal("unverified account accepted")
		}
	}
	if err := verifyRecoveryAccount(&sts.GetCallerIdentityOutput{Account: aws.String("123456789012")}, "123456789012"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryValidatesOnlySelectedTranscriptAndApplicableSegments(t *testing.T) {
	for _, example := range []struct {
		meeting model.Meeting
		fields  []string
	}{
		{model.Meeting{TranscriptA: "source A", TranscriptB: "s3://unavailable", SelectedTranscript: "A"}, []string{"transcriptA", "transcriptSegments"}},
		{model.Meeting{TranscriptA: "s3://unavailable", TranscriptB: "source B", SelectedTranscript: "B"}, []string{"transcriptB", "transcriptSegments"}},
		{model.Meeting{TranscriptA: " \t", TranscriptB: "source B"}, []string{"transcriptB", "transcriptSegments"}},
		{model.Meeting{Notes: "saved note", TranscriptSegments: "s3://unused"}, nil},
	} {
		if fields := summaryInputFields(&example.meeting); !reflect.DeepEqual(fields, example.fields) {
			t.Fatalf("wrong recovery sources: %v", fields)
		}
	}
}
