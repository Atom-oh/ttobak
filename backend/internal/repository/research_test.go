package repository

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// TestAccountLinkExpression_EmitsStringSetOperand verifies, without any AWS
// network call, that the exact expression.Value(&types.AttributeValueMemberSS{...})
// pattern AddAccountLink/RemoveAccountLink use actually produces a String Set
// (SS) AttributeValue in the built expression -- not, say, a nested Map (M)
// from expression.Value falling back to reflection-based marshaling of the
// struct. A round-4 PR review raised this as a disputed MAJOR (models
// disagreed, and the mock-based service tests bypass the real expression
// build entirely) since a wrong operand type would silently break every
// research<->account link/unlink at runtime with no test catching it.
func TestAccountLinkExpression_EmitsStringSetOperand(t *testing.T) {
	for _, tc := range []struct {
		name string
		expr expression.UpdateBuilder
	}{
		{"Add", expression.Add(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{"acc-1"}}))},
		{"Delete", expression.Delete(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{"acc-1"}}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, err := expression.NewBuilder().WithUpdate(tc.expr).Build()
			if err != nil {
				t.Fatalf("build expression: %v", err)
			}
			values := built.Values()
			if len(values) != 1 {
				t.Fatalf("expected exactly 1 expression value, got %d: %v", len(values), values)
			}
			for placeholder, av := range values {
				ss, ok := av.(*types.AttributeValueMemberSS)
				if !ok {
					t.Fatalf("expected placeholder %s to be *types.AttributeValueMemberSS, got %T: %#v", placeholder, av, av)
				}
				if len(ss.Value) != 1 || ss.Value[0] != "acc-1" {
					t.Fatalf("expected SS value [acc-1], got %v", ss.Value)
				}
			}
		})
	}
}

// TestMapTransactionCanceledError verifies, without any AWS network call,
// that mapTransactionCanceledError only maps a TransactWriteItems
// cancellation to ErrConditionFailed ("research not found") when item 0
// (the CONFIG Update) specifically failed its condition -- NOT for every
// TransactionCanceledException. A round-10 PR review caught this as a real
// bug: TransactionCanceledException also fires for TransactionConflict (a
// concurrent transaction on the same item -- exactly what racing
// LinkAccount/UnlinkAccount calls for the same research produce) and
// throttling, neither of which mean the research doesn't exist. The old
// unconditional mapping turned a transient, retryable conflict into a
// wrong 404 for a caller who did nothing wrong.
func TestMapTransactionCanceledError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantCondition bool // true: expect ErrConditionFailed; false: expect the original error propagated (retryable)
	}{
		{
			name: "item 0 ConditionalCheckFailed -- research genuinely gone",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: aws.String("ConditionalCheckFailed")},
					{Code: aws.String("None")},
				},
			},
			wantCondition: true,
		},
		{
			name: "item 0 TransactionConflict -- concurrent Link/Unlink, must NOT be reported as not-found",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: aws.String("TransactionConflict")},
					{Code: aws.String("None")},
				},
			},
			wantCondition: false,
		},
		{
			name: "item 1 failed (the ref write), item 0 None -- research exists, must NOT be reported as not-found",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: aws.String("None")},
					{Code: aws.String("ConditionalCheckFailed")},
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
			got := mapTransactionCanceledError(tc.err, "r1", "link")
			isCondition := errors.Is(got, ErrConditionFailed)
			if isCondition != tc.wantCondition {
				t.Fatalf("mapTransactionCanceledError(%v) -> %v; errors.Is(ErrConditionFailed)=%v, want %v", tc.err, got, isCondition, tc.wantCondition)
			}
		})
	}
}
