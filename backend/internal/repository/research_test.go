package repository

import (
	"testing"

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
