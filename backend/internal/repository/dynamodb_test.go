package repository

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func TestPaginateMeetingsPage(t *testing.T) {
	key := func(id string) map[string]types.AttributeValue {
		return map[string]types.AttributeValue{
			"PK":     &types.AttributeValueMemberS{Value: "USER#u1"},
			"SK":     &types.AttributeValueMemberS{Value: "MEETING#" + id},
			"GSI1PK": &types.AttributeValueMemberS{Value: "USER#u1"},
			"GSI1SK": &types.AttributeValueMemberS{Value: "2026-01-0" + id},
		}
	}
	meetings := func(n int) []model.Meeting {
		out := make([]model.Meeting, n)
		for i := range out {
			id := string(rune('1' + i))
			out[i] = model.Meeting{PK: "USER#u1", SK: "MEETING#" + id, GSI1PK: "USER#u1", GSI1SK: "2026-01-0" + id}
		}
		return out
	}

	t.Run("exactly filled with more remaining -- cursor must survive", func(t *testing.T) {
		// Regression: this is the case the bug lost. A page that lands
		// exactly on Limit, with the underlying query's LastEvaluatedKey
		// still non-nil (more items exist), used to have its cursor
		// forced to nil -- silently ending pagination for that user.
		page, resume := paginateMeetingsPage(meetings(3), 3, key("9"))
		if len(page) != 3 {
			t.Fatalf("expected 3 meetings, got %d", len(page))
		}
		if resume == nil {
			t.Fatal("expected the cursor to survive an exactly-filled page with more remaining")
		}
	})

	t.Run("exactly filled, partition exhausted -- no cursor", func(t *testing.T) {
		page, resume := paginateMeetingsPage(meetings(3), 3, nil)
		if len(page) != 3 || resume != nil {
			t.Fatalf("expected 3 meetings and nil cursor, got %d meetings, resume=%v", len(page), resume)
		}
	})

	t.Run("under-filled after maxPages, more remaining -- cursor must survive", func(t *testing.T) {
		// The other case the bug lost: the filter-loop's maxPages bound
		// hit before collecting a full page, but the partition isn't
		// actually exhausted.
		page, resume := paginateMeetingsPage(meetings(2), 3, key("9"))
		if len(page) != 2 || resume == nil {
			t.Fatalf("expected 2 meetings and a surviving cursor, got %d meetings, resume=%v", len(page), resume)
		}
	})

	t.Run("under-filled, partition exhausted -- no cursor", func(t *testing.T) {
		page, resume := paginateMeetingsPage(meetings(2), 3, nil)
		if len(page) != 2 || resume != nil {
			t.Fatalf("expected 2 meetings and nil cursor, got %d meetings, resume=%v", len(page), resume)
		}
	})

	t.Run("overshot -- truncates and resumes from the last kept item, not the raw LastEvaluatedKey", func(t *testing.T) {
		page, resume := paginateMeetingsPage(meetings(5), 3, key("99"))
		if len(page) != 3 {
			t.Fatalf("expected truncation to 3, got %d", len(page))
		}
		sk := resume["SK"].(*types.AttributeValueMemberS).Value
		if sk != "MEETING#3" {
			t.Fatalf("expected resume key for the 3rd kept item (MEETING#3), got %q", sk)
		}
	})
}
