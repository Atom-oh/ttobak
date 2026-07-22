package repository

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// TestMapProjectTransactionCanceledError mirrors research.go's
// TestMapTransactionCanceledError, adapted for the 3-item shape
// MeetingProjectLinkTransactional/ResearchProjectLinkTransactional use:
// [0]=Update(meeting/research, attribute_exists(PK)), [1]=Put(ref, no
// condition), [2]=ConditionCheck(project CONFIG, attribute_exists(PK)).
// Unlike research's mapTransactionCanceledError (which only checks index 0,
// correct for its 2-item shape with exactly one conditional item), this
// function must scan every reason -- a project deletion racing the link
// fails specifically at index 2, and an index-0-only check would silently
// miss it (this was a real, shipped bug: see git history).
func TestMapProjectTransactionCanceledError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantCondition bool
	}{
		{
			name: "index 0 ConditionalCheckFailed -- meeting/research genuinely gone",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: aws.String("ConditionalCheckFailed")},
					{Code: aws.String("None")},
					{Code: aws.String("None")},
				},
			},
			wantCondition: true,
		},
		{
			name: "index 2 ConditionalCheckFailed -- project deleted concurrently with the link (the round-4 bug)",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: aws.String("None")},
					{Code: aws.String("None")},
					{Code: aws.String("ConditionalCheckFailed")},
				},
			},
			wantCondition: true,
		},
		{
			name: "TransactionConflict at index 0 -- concurrent Link/Unlink, must NOT be reported as not-found",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: aws.String("TransactionConflict")},
					{Code: aws.String("None")},
					{Code: aws.String("None")},
				},
			},
			wantCondition: false,
		},
		{
			name:          "not a TransactionCanceledException at all",
			err:           errors.New("some other dynamodb error"),
			wantCondition: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapProjectTransactionCanceledError(tc.err, "p1", "project", "link")
			isCondition := errors.Is(got, ErrConditionFailed)
			if isCondition != tc.wantCondition {
				t.Fatalf("mapProjectTransactionCanceledError(%v) -> %v; errors.Is(ErrConditionFailed)=%v, want %v", tc.err, got, isCondition, tc.wantCondition)
			}
		})
	}
}
