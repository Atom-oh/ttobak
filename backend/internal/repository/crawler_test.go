package repository

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
)

// TestSourceFieldUnchangedCondition_EmptyValueAcceptsAbsentAttribute is a
// regression test: a legacy crawler source written before a given field
// existed (or one of the union fields Unsubscribe's last-subscriber branch
// clears to nil) has that attribute genuinely ABSENT/NULL in DynamoDB, not
// present-as-an-empty-list. Building the condition as a plain Equal against
// an empty list would then never match a real GetItem result for such an
// item, deterministically failing UpdateSourcePartial's condition (and
// exhausting the caller's bounded retry) even with no concurrent write at
// all. The fix ORs attribute_not_exists in for this case.
func TestSourceFieldUnchangedCondition_EmptyValueAcceptsAbsentAttribute(t *testing.T) {
	cond := sourceFieldUnchangedCondition("newsSources", []string(nil))

	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		t.Fatalf("failed to build expression: %v", err)
	}

	condStr := *expr.Condition()
	if !strings.Contains(condStr, "attribute_not_exists") {
		t.Errorf("expected condition to include attribute_not_exists for an empty value, got %q", condStr)
	}
	if !strings.Contains(condStr, "OR") {
		t.Errorf("expected condition to OR attribute_not_exists with an equality fallback, got %q", condStr)
	}
}

// TestSourceFieldUnchangedCondition_NonEmptyValueIsPlainEquality asserts the
// non-empty path stays a strict equality check (no OR/attribute_not_exists
// laxness) -- a genuine concurrent change to a populated field must still
// fail the condition.
func TestSourceFieldUnchangedCondition_NonEmptyValueIsPlainEquality(t *testing.T) {
	cond := sourceFieldUnchangedCondition("newsSources", []string{"aws-blog"})

	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		t.Fatalf("failed to build expression: %v", err)
	}

	condStr := *expr.Condition()
	if strings.Contains(condStr, "attribute_not_exists") {
		t.Errorf("expected a plain equality condition for a non-empty value, got %q", condStr)
	}
	if strings.Contains(condStr, "OR") {
		t.Errorf("expected no OR clause for a non-empty value, got %q", condStr)
	}
}

// TestSourceFieldUnchangedCondition_ScalarField covers the status field's
// plain-Equal path (never omitempty, so never treated as an absent-list
// case even though it's passed through the same helper).
func TestSourceFieldUnchangedCondition_ScalarField(t *testing.T) {
	cond := sourceFieldUnchangedCondition("status", "idle")

	expr, err := expression.NewBuilder().WithCondition(cond).Build()
	if err != nil {
		t.Fatalf("failed to build expression: %v", err)
	}

	condStr := *expr.Condition()
	if strings.Contains(condStr, "attribute_not_exists") || strings.Contains(condStr, "OR") {
		t.Errorf("expected a plain equality condition for a scalar field, got %q", condStr)
	}
}
