package repository

import (
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
)

// Only for a single SDK attempt: rejection cannot have published new spill refs.
// A retried write could have committed before a later attempt was rejected.
// TransactionCanceledException cancels the whole transaction regardless of reasons:
// https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactWriteItems.html
func summaryWriteRejected(err error) bool {
	var condition *types.ConditionalCheckFailedException
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &condition) || errors.As(err, &cancelled) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationException"
}
