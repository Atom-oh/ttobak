package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

func TestRefinementSDKClassesAreClosedAndDiscardPrivateCauses(t *testing.T) {
	const canary = "SYNTHETIC_PRIVATE_SDK_DETAIL"
	cases := []struct{ code, class string }{
		{"ThrottlingException", "throttled"}, {"ServiceQuotaExceededException", "throttled"},
		{"AccessDeniedException", "access_denied"}, {"ValidationException", "validation"},
		{"ResourceNotFoundException", "not_found"}, {"InternalServerException", "service_error"},
		{"ServiceUnavailableException", "service_error"}, {"ModelErrorException", "service_error"},
		{"ModelNotReadyException", "service_error"}, {"ModelStreamErrorException", "service_error"},
		{"ModelTimeoutException", "service_error"}, {canary, "aws_other"},
	}
	for _, test := range cases {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%v", test.class, wrapped), func(t *testing.T) {
				logs := capturePrivacyLogs(t)
				var cause error = &smithy.GenericAPIError{Code: test.code, Message: canary}
				if wrapped {
					cause = &smithy.OperationError{ServiceID: canary, OperationName: canary, Err: cause}
				}
				safe := sanitizedRefinementModelError(cause)
				logRefinementFallback(0, 1, fmt.Errorf("%s: %w", canary, safe))
				if !errors.Is(safe, errRefinementModel) || errors.Is(safe, cause) ||
					strings.Contains(safe.Error(), canary) || strings.Contains(safe.Error(), test.code) {
					t.Fatal("sanitized model error retained a provider cause/code/message")
				}
				var original smithy.APIError
				if errors.As(safe, &original) {
					t.Fatal("provider error escaped through Unwrap")
				}
				if strings.Contains(logs.String(), canary) || strings.Contains(logs.String(), test.code) ||
					!strings.Contains(logs.String(), "code=model_failure class="+test.class) {
					t.Fatal("SDK diagnostic was not closed and redacted")
				}
			})
		}
	}
	for _, test := range []struct {
		err   error
		class string
	}{
		{fmt.Errorf("%s: %w", canary, context.Canceled), "canceled"},
		{fmt.Errorf("%s: %w", canary, context.DeadlineExceeded), "deadline"},
		{errors.New(canary), "other"},
	} {
		safe := sanitizedRefinementModelError(test.err)
		if refinementFailureClass(safe) != test.class || strings.Contains(safe.Error(), canary) {
			t.Fatal("context/unknown failure was not safely classified")
		}
	}
	if refinementFailureClass(&refinementModelError{class: 255}) != "other" {
		t.Fatal("unknown class did not fail closed")
	}
}

func TestRefinementInvocationRetainsOnlySafeSDKClass(t *testing.T) {
	logs := capturePrivacyLogs(t)
	svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "SYNTHETIC_PRIVATE_TRANSPORT_MESSAGE"}
	}))
	_, _, quality, err := svc.RefineTranscriptWithQuality(context.Background(), []WhisperSegment{{Start: 0, End: 1, Text: "synthetic source"}})
	if err != nil || !quality.Degraded || quality.Refinement != "raw" ||
		!strings.Contains(logs.String(), "code=model_failure class=access_denied") ||
		strings.Contains(logs.String(), "SYNTHETIC_PRIVATE") {
		t.Fatal("actual invocation lost safe classification or raw fallback")
	}
}
