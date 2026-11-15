package repository

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// exprNumbers returns the set of numeric expression values, so a test can
// assert the delta and bound an expression carries.
func exprNumbers(t *testing.T, values map[string]types.AttributeValue) map[int]bool {
	t.Helper()
	out := map[int]bool{}
	for _, v := range values {
		if n, ok := v.(*types.AttributeValueMemberN); ok {
			i, err := strconv.Atoi(n.Value)
			if err != nil {
				t.Fatalf("bad number %q", n.Value)
			}
			out[i] = true
		}
	}
	return out
}

func exprNameSet(names map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func checkCounterKey(t *testing.T, u *types.Update) {
	t.Helper()
	if u == nil {
		t.Fatal("counter update missing")
	}
	if feedAttrS(t, u.Key, "PK") != "USER#u1" || feedAttrS(t, u.Key, "SK") != model.SKUpdateSourceCount {
		t.Fatalf("counter key = %v", u.Key)
	}
	if strings.HasPrefix(model.SKUpdateSourceCount, model.PrefixUpdateSource) {
		t.Fatal("the counter row must not match the source prefix query")
	}
}

func TestCreateSourceItems(t *testing.T) {
	src := &model.UpdateSource{SourceID: "s1", Kind: model.UpdateSourceKindRSS, Label: "B", URL: "https://example.com/feed", Status: model.UpdateSourceActive}
	tests := []struct {
		name  string
		sub   *model.UpdateFeedSub
		items int
	}{
		{"feed source", feedTestSub(), 3},
		{"slack source", nil, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := createSourceItems("tbl", "u1", src, tt.sub, 30)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != tt.items {
				t.Fatalf("items = %d, want %d", len(items), tt.items)
			}
			srcPut := items[0].Put
			if srcPut == nil || feedAttrS(t, srcPut.Item, "PK") != "USER#u1" || feedAttrS(t, srcPut.Item, "SK") != "CUSRC#s1" {
				t.Fatalf("source put = %+v", items[0])
			}
			if srcPut.ConditionExpression == nil || !strings.Contains(*srcPut.ConditionExpression, "attribute_not_exists") {
				t.Fatalf("source put must be attribute_not_exists, got %v", srcPut.ConditionExpression)
			}
			if tt.sub != nil {
				subPut := items[1].Put
				if subPut == nil || feedAttrS(t, subPut.Item, "PK") != "CUFEEDS" || feedAttrS(t, subPut.Item, "SK") != "fh1#u1#s1" ||
					feedAttrS(t, subPut.Item, "entityType") != "UPDATE_FEED_SUB" {
					t.Fatalf("registry put = %+v", items[1])
				}
				for _, k := range []string{"feedHash", "url", "kind", "userId", "sourceId", "status", "fannedAt"} {
					if _, ok := subPut.Item[k]; !ok {
						t.Errorf("registry row lacks %s", k)
					}
				}
			}
			counter := items[len(items)-1].Update
			checkCounterKey(t, counter)
			cond := aws.ToString(counter.ConditionExpression)
			if !strings.Contains(cond, "attribute_exists") || !strings.Contains(cond, "<") || strings.Contains(cond, "attribute_not_exists") {
				t.Fatalf("counter condition must require a seeded count below the cap: %s", cond)
			}
			if !strings.HasPrefix(aws.ToString(counter.UpdateExpression), "ADD ") || !exprNameSet(counter.ExpressionAttributeNames)["sourceCount"] {
				t.Fatalf("counter update = %s %v", aws.ToString(counter.UpdateExpression), counter.ExpressionAttributeNames)
			}
			if nums := exprNumbers(t, counter.ExpressionAttributeValues); !nums[1] || !nums[30] || len(nums) != 2 {
				t.Fatalf("counter values must be +1 and the cap 30: %v", counter.ExpressionAttributeValues)
			}
		})
	}
}

func TestCreateSourceItemsRejectsBadInput(t *testing.T) {
	src := &model.UpdateSource{SourceID: "s1"}
	if _, err := createSourceItems("tbl", "u1", src, nil, 0); err == nil {
		t.Fatal("a non-positive cap must be rejected")
	}
	if _, err := createSourceItems("tbl", "u1", src, &model.UpdateFeedSub{}, 30); err == nil {
		t.Fatal("an incomplete registry row must be rejected")
	}
}

func TestRemoveSourceItems(t *testing.T) {
	tests := []struct {
		name     string
		feedHash string
		items    int
	}{
		{"feed source", "fh1", 3},
		{"slack source", "", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := removeSourceItems("tbl", "u1", "s1", tt.feedHash)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != tt.items {
				t.Fatalf("items = %d, want %d", len(items), tt.items)
			}
			del := items[0].Delete
			if del == nil || feedAttrS(t, del.Key, "SK") != "CUSRC#s1" ||
				!strings.Contains(aws.ToString(del.ConditionExpression), "attribute_exists") {
				t.Fatalf("source delete must require the row: %+v", items[0])
			}
			if tt.feedHash != "" {
				reg := items[1].Delete
				if reg == nil || feedAttrS(t, reg.Key, "SK") != "fh1#u1#s1" || reg.ConditionExpression != nil {
					t.Fatalf("registry delete must be unconditional: %+v", items[1])
				}
			}
			counter := items[len(items)-1].Update
			checkCounterKey(t, counter)
			if cond := aws.ToString(counter.ConditionExpression); !strings.Contains(cond, ">") {
				t.Fatalf("decrement must require a positive count: %s", cond)
			}
			if nums := exprNumbers(t, counter.ExpressionAttributeValues); !nums[-1] || !nums[0] || len(nums) != 2 {
				t.Fatalf("counter values must be -1 and 0: %v", counter.ExpressionAttributeValues)
			}
		})
	}
}

func TestSourceCountSeedInput(t *testing.T) {
	in, err := sourceCountSeedInput("tbl", "u1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if feedAttrS(t, in.Key, "PK") != "USER#u1" || feedAttrS(t, in.Key, "SK") != model.SKUpdateSourceCount {
		t.Fatalf("key = %v", in.Key)
	}
	if cond := aws.ToString(in.ConditionExpression); !strings.Contains(cond, "attribute_not_exists") {
		t.Fatalf("seed must only write an absent count: %s", cond)
	}
	if nums := exprNumbers(t, in.ExpressionAttributeValues); !nums[7] || len(nums) != 1 {
		t.Fatalf("seed values = %v", in.ExpressionAttributeValues)
	}
	if _, err := sourceCountSeedInput("tbl", "u1", -1); err == nil {
		t.Fatal("a negative seed must be rejected")
	}
}

func TestCountSourcesInputIsConsistent(t *testing.T) {
	in, err := countSourcesInput("tbl", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !aws.ToBool(in.ConsistentRead) || in.Select != types.SelectCount || in.Limit != nil {
		t.Fatalf("count query must be a consistent, unbounded COUNT: %+v", in)
	}
	if !strings.Contains(aws.ToString(in.KeyConditionExpression), "begins_with") {
		t.Fatalf("key condition = %s", aws.ToString(in.KeyConditionExpression))
	}
	if !aws.ToBool(sourceCountGetInput("tbl", "u1").ConsistentRead) {
		t.Fatal("count read must be strongly consistent")
	}
}

func sourceTxCancelled(codes ...string) error {
	reasons := make([]types.CancellationReason, len(codes))
	for i, c := range codes {
		reasons[i] = types.CancellationReason{Code: aws.String(c)}
	}
	return &types.TransactionCanceledException{Message: aws.String("cancelled"), CancellationReasons: reasons}
}

func TestSourceTransactErrorMapping(t *testing.T) {
	ccf, none, tc := "ConditionalCheckFailed", "None", "TransactionConflict"
	other := errors.New("throttled")
	seedConflict := &types.TransactionConflictException{Message: aws.String("conflict")}
	seedCCF := &types.ConditionalCheckFailedException{Message: aws.String("exists")}
	tests := []struct {
		name    string
		op      string // create, remove or seed
		err     error
		want    error
		cond    bool // also matches ErrConditionFailed
		wrapped error
	}{
		{"create duplicate", "create", sourceTxCancelled(ccf, none, none), ErrUpdateSourceExists, true, nil},
		{"create duplicate wins over cap", "create", sourceTxCancelled(ccf, none, ccf), ErrUpdateSourceExists, true, nil},
		{"create cap", "create", sourceTxCancelled(none, none, ccf), ErrUpdateSourceCapReached, true, nil},
		{"create slack cap", "create", sourceTxCancelled(none, ccf), ErrUpdateSourceCapReached, true, nil},
		{"create counter conflict", "create", sourceTxCancelled(none, none, tc), ErrUpdateSourceConflict, false, nil},
		{"create registry conflict", "create", sourceTxCancelled(none, tc, none), ErrUpdateSourceConflict, false, nil},
		{"create cap wins over conflict", "create", sourceTxCancelled(tc, none, ccf), ErrUpdateSourceCapReached, true, nil},
		{"create other cancellation", "create", sourceTxCancelled(none, "ValidationError"), nil, false, nil},
		{"create other", "create", other, nil, false, other},
		{"remove missing", "remove", sourceTxCancelled(ccf, none, none), ErrUpdateSourceMissing, true, nil},
		{"remove floor", "remove", sourceTxCancelled(none, none, ccf), ErrUpdateSourceCountFloor, true, nil},
		{"remove slack floor", "remove", sourceTxCancelled(none, ccf), ErrUpdateSourceCountFloor, true, nil},
		{"remove conflict", "remove", sourceTxCancelled(tc, none), ErrUpdateSourceConflict, false, nil},
		{"remove other", "remove", other, nil, false, other},
		{"seed already seeded", "seed", seedCCF, nil, true, nil},
		{"seed conflict", "seed", seedConflict, ErrUpdateSourceConflict, false, nil},
		{"seed other", "seed", other, nil, false, other},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got error
			switch tt.op {
			case "create":
				got = sourceCreateError(tt.err)
			case "remove":
				got = sourceRemoveError(tt.err)
			default:
				got = sourceSeedError(tt.err)
			}
			if got == nil {
				t.Fatal("an error must stay an error")
			}
			if tt.want != nil && !errors.Is(got, tt.want) {
				t.Fatalf("err = %v, want %v", got, tt.want)
			}
			if errors.Is(got, ErrConditionFailed) != tt.cond {
				t.Fatalf("ErrConditionFailed match = %v for %v", !tt.cond, got)
			}
			for _, s := range []error{ErrUpdateSourceExists, ErrUpdateSourceCapReached, ErrUpdateSourceMissing, ErrUpdateSourceCountFloor, ErrUpdateSourceConflict} {
				if s != tt.want && errors.Is(got, s) {
					t.Fatalf("err %v must not match %v", got, s)
				}
			}
			if tt.wrapped != nil && !errors.Is(got, tt.wrapped) {
				t.Fatalf("err %v must wrap %v", got, tt.wrapped)
			}
		})
	}
}
