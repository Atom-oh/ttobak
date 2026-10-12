package service

import (
	"context"
	"errors"

	"github.com/aws/smithy-go"
)

type refinementErrorClass uint8

const (
	refinementOther refinementErrorClass = iota
	refinementCanceled
	refinementDeadline
	refinementThrottled
	refinementAccessDenied
	refinementValidation
	refinementNotFound
	refinementServiceError
	refinementAWSOther
)

// No provider cause, message or code is retained in the returned error chain.
// The ordinal selects only a closed operator-facing class.
type refinementModelError struct{ class refinementErrorClass }

func (e *refinementModelError) category() string {
	names := [...]string{"other", "canceled", "deadline", "throttled", "access_denied",
		"validation", "not_found", "service_error", "aws_other"}
	if int(e.class) >= len(names) {
		return "other"
	}
	return names[e.class]
}
func (e *refinementModelError) Error() string {
	return errRefinementModel.Error() + ": " + e.category()
}
func (e *refinementModelError) Unwrap() error { return errRefinementModel }

func sanitizedRefinementModelError(err error) error {
	class := refinementOther
	var api smithy.APIError
	switch {
	case errors.Is(err, context.Canceled):
		class = refinementCanceled
	case errors.Is(err, context.DeadlineExceeded):
		class = refinementDeadline
	case errors.As(err, &api):
		class = refinementAWSOther
		switch api.ErrorCode() {
		case "ThrottlingException", "ServiceQuotaExceededException":
			class = refinementThrottled
		case "AccessDeniedException":
			class = refinementAccessDenied
		case "ValidationException":
			class = refinementValidation
		case "ResourceNotFoundException":
			class = refinementNotFound
		case "InternalServerException", "ServiceUnavailableException", "ModelErrorException",
			"ModelNotReadyException", "ModelStreamErrorException", "ModelTimeoutException":
			class = refinementServiceError
		}
	}
	return &refinementModelError{class: class}
}

func refinementFailureClass(err error) string {
	var sanitized *refinementModelError
	if errors.As(err, &sanitized) && sanitized != nil {
		return sanitized.category()
	}
	return "other"
}
