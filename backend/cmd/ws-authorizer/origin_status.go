package main

import (
	"context"
	"errors"
	"net"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type originReason uint8

const (
	originUnknown originReason = iota
	originAccepted
	originUnconfigured
	originHeaderMissing
	originHeaderMalformed
	originHeaderDuplicate
	originHeaderConflict
	originSecretUnavailable
	originMismatch
)

func (r originReason) String() string {
	labels := [...]string{"unknown", "accepted", "unconfigured", "header_missing", "header_malformed",
		"header_duplicate", "header_conflict", "secret_unavailable", "mismatch"}
	if int(r) >= len(labels) {
		return "unknown"
	}
	return labels[r]
}

type secretErrorCategory uint8

const (
	secretErrorNone secretErrorCategory = iota
	secretErrorCanceled
	secretErrorDeadline
	secretErrorTransport
	secretErrorAccessDenied
	secretErrorNotFound
	secretErrorThrottled
	secretErrorService
	secretErrorDecryption
	secretErrorInvalidRequest
	secretErrorCredentials
	secretErrorInvalidSecret
	secretErrorAWSOther
	secretErrorOther
)

func (c secretErrorCategory) String() string {
	labels := [...]string{"none", "canceled", "deadline", "transport", "access_denied", "not_found",
		"throttled", "service_error", "decryption_failed", "invalid_request", "credentials",
		"invalid_secret", "aws_other", "other"}
	if int(c) >= len(labels) {
		return "other"
	}
	return labels[c]
}

type originResult struct {
	reason   originReason
	category secretErrorCategory
}

func (r originResult) allowed() bool {
	return r.reason == originAccepted && r.category == secretErrorNone
}

// Only closed categories leave this boundary. Never log an error's message,
// arbitrary AWS code, request URL, header, token, secret value or SecretId.
func classifySecretError(err error) secretErrorCategory {
	switch {
	case errors.Is(err, context.Canceled):
		return secretErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return secretErrorDeadline
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "AccessDeniedException":
			return secretErrorAccessDenied
		case "ResourceNotFoundException":
			return secretErrorNotFound
		case "ThrottlingException", "TooManyRequestsException":
			return secretErrorThrottled
		case "InternalServiceError", "ServiceUnavailableException":
			return secretErrorService
		case "DecryptionFailure":
			return secretErrorDecryption
		case "InvalidRequestException", "InvalidParameterException":
			return secretErrorInvalidRequest
		case "ExpiredTokenException", "UnrecognizedClientException", "InvalidSignatureException":
			return secretErrorCredentials
		default:
			return secretErrorAWSOther
		}
	}
	var sendError *smithyhttp.RequestSendError
	var networkError net.Error
	if errors.As(err, &sendError) || errors.As(err, &networkError) {
		return secretErrorTransport
	}
	return secretErrorOther
}
