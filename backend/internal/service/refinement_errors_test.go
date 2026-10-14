package service

import (
	"context"
	"errors"
	"fmt"
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
				var cause error = &smithy.GenericAPIError{Code: test.code, Message: canary}
				if wrapped {
					cause = &smithy.OperationError{ServiceID: canary, OperationName: canary, Err: cause}
				}
				safe := sanitizedRefinementModelError(cause)
				if !errors.Is(safe, errRefinementModel) || errors.Is(safe, cause) ||
					strings.Contains(safe.Error(), canary) || strings.Contains(safe.Error(), test.code) {
					t.Fatal("sanitized model error retained a provider cause/code/message")
				}
				var original smithy.APIError
				if errors.As(safe, &original) {
					t.Fatal("provider error escaped through Unwrap")
				}
				if refinementFailureClass(safe) != test.class {
					t.Fatal("SDK failure class was not closed")
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
