package main

import (
	"io"
	"strings"
	"testing"
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
