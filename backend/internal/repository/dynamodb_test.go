package repository

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
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

// TestTransactionItemFailed covers the per-index cancellation-reason
// inspection MaterializePendingAccountGrant/MaterializePendingMeetingGrant
// use to distinguish "this exact item's own condition failed" from "some
// other item in the same transaction failed" or "not a condition failure
// at all" -- getting this wrong either drops a live grant it shouldn't
// (checking the wrong index) or resurrects a dead one (treating an
// unrelated failure as if this item passed).
func TestTransactionItemFailed(t *testing.T) {
	code := func(s string) *string { return &s }

	tests := []struct {
		name       string
		err        error
		idx        int
		wantLen    int
		wantFailed bool
		wantOK     bool
	}{
		{
			name: "index 0 failed, others untouched",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("ConditionalCheckFailed")},
					{Code: code("None")},
					{Code: code("None")},
				},
			},
			idx: 0, wantLen: 3, wantFailed: true, wantOK: true,
		},
		{
			name: "index 1 failed -- asking about index 0 must report false, not the other item's failure",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("None")},
					{Code: code("ConditionalCheckFailed")},
					{Code: code("None")},
				},
			},
			idx: 0, wantLen: 3, wantFailed: false, wantOK: true,
		},
		{
			name: "same case, asking about the index that actually failed",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("None")},
					{Code: code("ConditionalCheckFailed")},
					{Code: code("None")},
				},
			},
			idx: 1, wantLen: 3, wantFailed: true, wantOK: true,
		},
		{
			name: "TransactionConflict (not a condition failure) must not be reported as failed",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("TransactionConflict")},
					{Code: code("None")},
					{Code: code("None")},
				},
			},
			idx: 0, wantLen: 3, wantFailed: false, wantOK: true,
		},
		{
			name: "wrong wantLen -- caller's item count doesn't match the exception's, must not guess",
			err: &types.TransactionCanceledException{
				CancellationReasons: []types.CancellationReason{
					{Code: code("ConditionalCheckFailed")},
					{Code: code("None")},
				},
			},
			idx: 0, wantLen: 3, wantFailed: false, wantOK: false,
		},
		{
			name: "not a TransactionCanceledException at all",
			err:  errors.New("some other dynamodb error"),
			idx:  0, wantLen: 3, wantFailed: false, wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failed, ok := transactionItemFailed(tc.err, tc.idx, tc.wantLen)
			if failed != tc.wantFailed || ok != tc.wantOK {
				t.Errorf("transactionItemFailed(idx=%d, wantLen=%d) = (%v, %v), want (%v, %v)",
					tc.idx, tc.wantLen, failed, ok, tc.wantFailed, tc.wantOK)
			}
		})
	}
}

// errWrap wraps an error for errors.As unwrapping in the "wrapped" test case.
type errWrap struct{ err error }

func (e errWrap) Error() string { return e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }

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

// TestClassifyProjectIDsPutItemErr covers UpdateMeeting's retry-loop
// decision logic in isolation: retry on ConditionalCheckFailedException
// while attempts remain, a distinct exhaustion error once they don't,
// success on nil, and pass-through for anything else -- without needing a
// live DynamoDB client.
func TestClassifyProjectIDsPutItemErr(t *testing.T) {
	const maxAttempts = 3

	t.Run("success", func(t *testing.T) {
		action, err := classifyProjectIDsPutItemErr(nil, 1, maxAttempts)
		if action != putItemRetryActionDone || err != nil {
			t.Fatalf("expected done/nil, got action=%v err=%v", action, err)
		}
	})

	t.Run("ConditionalCheckFailedException with attempts remaining -- retry", func(t *testing.T) {
		action, err := classifyProjectIDsPutItemErr(&types.ConditionalCheckFailedException{}, 1, maxAttempts)
		if action != putItemRetryActionRetry || err != nil {
			t.Fatalf("expected retry/nil, got action=%v err=%v", action, err)
		}
		action, err = classifyProjectIDsPutItemErr(&types.ConditionalCheckFailedException{}, maxAttempts-1, maxAttempts)
		if action != putItemRetryActionRetry || err != nil {
			t.Fatalf("expected retry/nil on the last remaining attempt, got action=%v err=%v", action, err)
		}
	})

	t.Run("ConditionalCheckFailedException on the final attempt -- exhaustion error, not a retry", func(t *testing.T) {
		action, err := classifyProjectIDsPutItemErr(&types.ConditionalCheckFailedException{}, maxAttempts, maxAttempts)
		if action != putItemRetryActionDone {
			t.Fatalf("expected done (no further retry) at attempt == maxAttempts, got %v", action)
		}
		if err == nil {
			t.Fatal("expected a terminal exhaustion error, got nil")
		}
	})

	t.Run("a ConditionalCheckFailedException past maxAttempts is still terminal, not a retry", func(t *testing.T) {
		// Defends against an off-by-one flip (e.g. <= instead of <) that
		// would retry forever past the intended cap.
		action, err := classifyProjectIDsPutItemErr(&types.ConditionalCheckFailedException{}, maxAttempts+1, maxAttempts)
		if action != putItemRetryActionDone || err == nil {
			t.Fatalf("expected done/non-nil past maxAttempts, got action=%v err=%v", action, err)
		}
	})

	t.Run("non-conditional error passes through, not retried", func(t *testing.T) {
		wrapped := errors.New("network timeout")
		action, err := classifyProjectIDsPutItemErr(wrapped, 1, maxAttempts)
		if action != putItemRetryActionDone {
			t.Fatalf("expected done (not retried) for a non-conditional error, got %v", action)
		}
		if err == nil || !errors.Is(err, wrapped) {
			t.Fatalf("expected the original error wrapped, got %v", err)
		}
	})

	t.Run("wrapped ConditionalCheckFailedException still retried via errors.As", func(t *testing.T) {
		wrapped := errWrap{err: &types.ConditionalCheckFailedException{}}
		action, err := classifyProjectIDsPutItemErr(wrapped, 1, maxAttempts)
		if action != putItemRetryActionRetry || err != nil {
			t.Fatalf("expected retry/nil for a wrapped CCFE, got action=%v err=%v", action, err)
		}
	})
}

// TestProjectIDsUnchangedCondition covers the two DynamoDB condition shapes
// UpdateMeeting's retry loop relies on to detect (not just narrow) a
// concurrent LinkMeeting/UnlinkMeeting: an empty projectIds set must assert
// the attribute is entirely absent (DynamoDB removes a String Set attribute
// once its last element is deleted -- it can never exist as an empty set),
// while a non-empty set must assert equality against the exact value just
// read.
func TestProjectIDsUnchangedCondition(t *testing.T) {
	t.Run("empty current -- asserts attribute_not_exists, not an empty-set equality", func(t *testing.T) {
		expr, err := projectIDsUnchangedCondition(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cond := *expr.Condition()
		if !strings.Contains(cond, "attribute_not_exists") {
			t.Fatalf("expected attribute_not_exists in condition, got %q", cond)
		}
	})

	t.Run("non-empty current -- asserts equality against the read value", func(t *testing.T) {
		expr, err := projectIDsUnchangedCondition([]string{"p1", "p2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cond := *expr.Condition()
		if !strings.Contains(cond, "=") {
			t.Fatalf("expected an equality condition, got %q", cond)
		}
		found := false
		for _, v := range expr.Values() {
			if ss, ok := v.(*types.AttributeValueMemberSS); ok {
				for _, id := range ss.Value {
					if id == "p1" || id == "p2" {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("expected the current projectIds to appear as the condition's String Set operand, got %v", expr.Values())
		}
	})
}

func TestTranscriptOverflowThreshold(t *testing.T) {
	// transcriptA/B keep the historical 300KB threshold. transcriptSegments
	// must spill to S3 much earlier: it coexists with transcriptA in the
	// same item, and two fields individually under 300KB can still add up
	// past DynamoDB's 400KB item limit (the 2026-08-31 meeting-797877d5
	// incident class: 6h recording -> segments JSON + transcript together
	// exceeded the item limit and flipped the meeting to error).
	cases := []struct {
		field string
		want  int
	}{
		{"transcriptA", 300 * 1024},
		{"transcriptB", 300 * 1024},
		{"transcriptSegments", 100 * 1024},
	}
	for _, c := range cases {
		if got := transcriptOverflowThreshold(c.field); got != c.want {
			t.Errorf("transcriptOverflowThreshold(%q) = %d, want %d", c.field, got, c.want)
		}
	}
}

func TestUtf16UnitCount(t *testing.T) {
	// This is the actual production conversion site (getStoredInlineSizes
	// calls utf16UnitCount directly) -- the astral-character fixture below
	// is what makes a regression back to a rune-counting implementation
	// (e.g. utf8.RuneCountInString) fail here rather than only against
	// Korean production data days later. See the 2026-09-01 incident in
	// CLAUDE.md Known Issues.
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"pure ASCII", "abc", 3},
		{"BMP-only (Korean)", "한글", 2},
		{"single astral character (emoji)", "😀", 2}, // U+1F600: 1 rune, 2 UTF-16 units
		{"mixed", "메모 😀", 5},                        // "메모"(2 units) + " "(1 unit) + emoji(2 units, 1 rune) = 5 units, 4 runes
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := utf16UnitCount(c.s); got != c.want {
				t.Errorf("utf16UnitCount(%q) = %d, want %d", c.s, got, c.want)
			}
			if got := utf16UnitCount(c.s); got == len([]rune(c.s)) && c.s == "😀" {
				t.Fatalf("utf16UnitCount(%q) matches rune count (%d) -- fixture no longer distinguishes UTF-16 units from runes", c.s, got)
			}
		})
	}
}

func TestSiblingSizeCondition(t *testing.T) {
	t.Run("no siblings when every guarded field is carried", func(t *testing.T) {
		_, ok := siblingSizeCondition(
			map[string]bool{
				"transcriptA": true, "transcriptB": true, "transcriptSegments": true,
				"content": true, "notes": true, "liveSummary": true, "actionItems": true,
			},
			map[string]inlineFieldSize{})
		if ok {
			t.Fatal("expected no condition when every guarded field is carried by the update")
		}
	})

	t.Run("non-spillable sibling growth is guarded too", func(t *testing.T) {
		cond, ok := siblingSizeCondition(
			map[string]bool{
				"transcriptA": true, "transcriptB": true, "transcriptSegments": true,
				"content": true, "notes": true, "actionItems": true,
			},
			map[string]inlineFieldSize{"liveSummary": {bytes: 5000, utf16Units: 5000}})
		if !ok {
			t.Fatal("expected a condition pinning the uncarried liveSummary")
		}
		expr, err := expression.NewBuilder().WithCondition(cond).Build()
		if err != nil {
			t.Fatalf("condition must build: %v", err)
		}
		found := false
		for _, v := range expr.Values() {
			if n, isN := v.(*types.AttributeValueMemberN); isN && n.Value == "5000" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected liveSummary's 5000 as a size() operand, got %v", expr.Values())
		}
	})

	t.Run("uses UTF-16 code unit count, not byte count, for the size() operand", func(t *testing.T) {
		// DynamoDB's size() on a String attribute counts UTF-16 code units,
		// confirmed by probing a live item (2026-09-01 incident, see CLAUDE.md
		// Known Issues): Korean text's byte length (UTF-8, ~3 bytes/syllable)
		// diverges sharply from its UTF-16 length, and a condition built from
		// bytes fails deterministically on every write for content containing
		// Korean -- not just under a genuine concurrent-writer race. bytes and
		// utf16Units are deliberately different here so a regression back to
		// storedSizes[field].bytes fails this test.
		cond, ok := siblingSizeCondition(
			map[string]bool{
				"transcriptA": true, "transcriptB": true, "transcriptSegments": true,
				"notes": true, "actionItems": true,
			},
			map[string]inlineFieldSize{
				"content":     {bytes: 18259, utf16Units: 9265},
				"liveSummary": {bytes: 4479, utf16Units: 2329},
			})
		if !ok {
			t.Fatal("expected a condition pinning content and liveSummary")
		}
		expr, err := expression.NewBuilder().WithCondition(cond).Build()
		if err != nil {
			t.Fatalf("condition must build: %v", err)
		}
		wantUnits := map[string]bool{"9265": false, "2329": false}
		for _, v := range expr.Values() {
			if n, isN := v.(*types.AttributeValueMemberN); isN {
				if _, want := wantUnits[n.Value]; want {
					wantUnits[n.Value] = true
				}
				if n.Value == "18259" || n.Value == "4479" {
					t.Fatalf("size() operand %s is the byte length, not the UTF-16 unit count -- "+
						"this would fail against a real DynamoDB item every time", n.Value)
				}
			}
		}
		for units, seen := range wantUnits {
			if !seen {
				t.Fatalf("expected UTF-16 unit count %s as a size() operand, got %v", units, expr.Values())
			}
		}
	})

	t.Run("astral characters need UTF-16 code units, not runes", func(t *testing.T) {
		// U+1F600 (😀) is a single Unicode rune but a UTF-16 surrogate PAIR --
		// 2 code units. Korean text alone can't catch a rune-count regression
		// (every Hangul syllable is in the Basic Multilingual Plane, so rune
		// count and UTF-16 unit count coincide for it); this is the case that
		// would silently reintroduce the same deterministic-failure bug via
		// unicode/utf8.RuneCountInString instead of unicode/utf16.Encode.
		text := "메모 😀"
		byteLen := len(text)
		utf16Len := len(utf16.Encode([]rune(text)))
		runeLen := len([]rune(text))
		if utf16Len == runeLen {
			t.Fatalf("test fixture doesn't actually distinguish UTF-16 units from runes (got %d == %d)", utf16Len, runeLen)
		}

		cond, ok := siblingSizeCondition(
			map[string]bool{
				"transcriptA": true, "transcriptB": true, "transcriptSegments": true,
				"liveSummary": true, "actionItems": true,
			},
			map[string]inlineFieldSize{
				"content": {bytes: byteLen, utf16Units: utf16Len},
			})
		if !ok {
			t.Fatal("expected a condition pinning content")
		}
		expr, err := expression.NewBuilder().WithCondition(cond).Build()
		if err != nil {
			t.Fatalf("condition must build: %v", err)
		}
		found := false
		for _, v := range expr.Values() {
			if n, isN := v.(*types.AttributeValueMemberN); isN {
				switch n.Value {
				case fmt.Sprint(utf16Len):
					found = true
				case fmt.Sprint(runeLen):
					t.Fatalf("size() operand %d is the rune count, not the UTF-16 unit count (%d) -- "+
						"would fail against a real DynamoDB item containing astral characters", runeLen, utf16Len)
				case fmt.Sprint(byteLen):
					t.Fatalf("size() operand %d is the byte length, not the UTF-16 unit count (%d)", byteLen, utf16Len)
				}
			}
		}
		if !found {
			t.Fatalf("expected UTF-16 unit count %d as a size() operand, got %v", utf16Len, expr.Values())
		}
	})

	t.Run("stored sibling pins size, absent sibling pins absent-or-ref", func(t *testing.T) {
		cond, ok := siblingSizeCondition(
			map[string]bool{"transcriptB": true},
			map[string]inlineFieldSize{"transcriptA": {bytes: 12345, utf16Units: 12345}})
		if !ok {
			t.Fatal("expected a condition")
		}
		expr, err := expression.NewBuilder().WithCondition(cond).Build()
		if err != nil {
			t.Fatalf("condition must build: %v", err)
		}
		foundSize := false
		for _, v := range expr.Values() {
			if n, isN := v.(*types.AttributeValueMemberN); isN && n.Value == "12345" {
				foundSize = true
			}
		}
		if !foundSize {
			t.Fatalf("expected the sizing read's 12345 to appear as a size() operand, got %v", expr.Values())
		}
		if !strings.Contains(*expr.Condition(), "attribute_not_exists") {
			t.Fatalf("expected absent sibling (transcriptSegments) to be pinned absent-or-ref, got %s", *expr.Condition())
		}
	})
}

func TestPickTranscriptToSpill(t *testing.T) {
	kb := func(n int) int { return n * 1024 }
	cases := []struct {
		name   string
		inline map[string]int
		fixed  int
		want   string
	}{
		{"all small fits", map[string]int{"transcriptA": kb(50), "transcriptSegments": kb(40)}, 0, ""},
		{"segments over own 100KB cap even when total fits", map[string]int{"transcriptA": kb(50), "transcriptSegments": kb(120)}, 0, "transcriptSegments"},
		{"A over own 300KB cap", map[string]int{"transcriptA": kb(310)}, 0, "transcriptA"},
		// The round-4 residual hole: A and B independent, each under 300KB,
		// combined past the budget — largest spills.
		{"A+B combined over budget spills largest", map[string]int{"transcriptA": kb(299), "transcriptB": kb(250)}, 0, "transcriptA"},
		// Partial update: incoming B alone is fine, but a stored inline A
		// (fixed baseline, unspillable here) pushes the total over — the
		// incoming field must spill.
		{"fixed baseline forces incoming spill", map[string]int{"transcriptB": kb(90)}, kb(250), "transcriptB"},
		{"fixed baseline alone over budget but nothing spillable", map[string]int{}, kb(400), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickTranscriptToSpill(c.inline, c.fixed); got != c.want {
				t.Fatalf("pickTranscriptToSpill(%v, %d) = %q, want %q", c.inline, c.fixed, got, c.want)
			}
		})
	}

	t.Run("fixpoint loop drains until fit", func(t *testing.T) {
		inline := map[string]int{"transcriptA": kb(200), "transcriptB": kb(200), "transcriptSegments": kb(90)}
		var spilled []string
		for len(inline) > 0 {
			pick := pickTranscriptToSpill(inline, 0)
			if pick == "" {
				break
			}
			spilled = append(spilled, pick)
			delete(inline, pick)
		}
		// 490KB total: spill one 200KB field -> 290KB remaining fits budget
		// and each remainder is under its own threshold.
		if len(spilled) != 1 {
			t.Fatalf("expected exactly 1 spill, got %v", spilled)
		}
	})
}

func TestValidateTranscriptRef(t *testing.T) {
	// The ref must match the ONE key storeTranscript writes for this
	// meeting+field. Anything else — another meeting's transcript in the
	// same bucket included — is a cross-tenant read primitive when combined
	// with the user-settable TranscriptA passthrough and the api Lambda's
	// bucket-wide grant, and must be rejected.
	const own = "ttobak-assets-test"
	cases := []struct {
		name    string
		field   string
		ref     string
		wantKey string
		wantErr bool
	}{
		{"own meeting transcriptA", "transcriptA", "s3://ttobak-assets-test/transcripts/m1/transcriptA.txt", "transcripts/m1/transcriptA.txt", false},
		{"own meeting segments", "transcriptSegments", "s3://ttobak-assets-test/transcripts/m1/transcriptSegments.txt", "transcripts/m1/transcriptSegments.txt", false},
		{"ANOTHER meeting's transcript rejected", "transcriptA", "s3://ttobak-assets-test/transcripts/victim-meeting/transcriptA.txt", "", true},
		{"field mismatch rejected", "transcriptA", "s3://ttobak-assets-test/transcripts/m1/transcriptB.txt", "", true},
		{"foreign bucket rejected", "transcriptA", "s3://attacker-bucket/transcripts/m1/transcriptA.txt", "", true},
		{"own bucket but audio prefix rejected", "transcriptA", "s3://ttobak-assets-test/audio/other-user/m2/rec.webm", "", true},
		{"path traversal rejected", "transcriptA", "s3://ttobak-assets-test/transcripts/../audio/other/rec.webm", "", true},
		{"suffix smuggling rejected", "transcriptA", "s3://ttobak-assets-test/transcripts/m1/transcriptA.txt.evil", "", true},
		{"malformed no key", "transcriptA", "s3://ttobak-assets-test", "", true},
		{"malformed empty", "transcriptA", "s3://", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bucket, key, err := validateTranscriptRef(own, "m1", c.field, c.ref)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got bucket=%q key=%q", c.ref, bucket, key)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", c.ref, err)
			}
			if bucket != own || key != c.wantKey {
				t.Fatalf("got bucket=%q key=%q, want bucket=%q key=%q", bucket, key, own, c.wantKey)
			}
		})
	}
}
