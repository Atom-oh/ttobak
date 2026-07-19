package repository

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestIsConditionalCheckFailedTransaction(t *testing.T) {
	code := func(s string) *string { return &s }

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "single condition failure",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("ConditionalCheckFailed")},
				},
			},
			want: true,
		},
		{
			name: "condition failure alongside untouched items (None)",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("None")},
					{Code: code("ConditionalCheckFailed")},
				},
			},
			want: true,
		},
		{
			name: "throttling mixed in must not be classified as benign",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("ConditionalCheckFailed")},
					{Code: code("ProvisionedThroughputExceeded")},
				},
			},
			want: false,
		},
		{
			name: "transaction conflict alone must not be classified as benign",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("TransactionConflict")},
				},
			},
			want: false,
		},
		{
			name: "no cancellation reasons",
			err:  &types.TransactionCanceledException{},
			want: false,
		},
		{
			name: "unrelated error type",
			err:  errors.New("network timeout"),
			want: false,
		},
		{
			name: "wrapped TransactionCanceledException still unwraps via errors.As",
			err: errWrap{err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("ConditionalCheckFailed")},
				},
			}},
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isConditionalCheckFailedTransaction(c.err)
			if got != c.want {
				t.Errorf("isConditionalCheckFailedTransaction(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// errWrap wraps an error for errors.As unwrapping in the "wrapped" test case.
type errWrap struct{ err error }

func (e errWrap) Error() string { return e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }
