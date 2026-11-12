package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type briefBindingSealer struct{ binding map[string]string }

func (s *briefBindingSealer) Seal(_ context.Context, p []byte, b map[string]string) (string, error) {
	s.binding = b
	return "sealed:" + string(p), nil
}
func (s *briefBindingSealer) Open(_ context.Context, sealed string, b map[string]string) ([]byte, error) {
	s.binding = b
	return []byte(strings.TrimPrefix(sealed, "sealed:")), nil
}

// briefTransaction returns the update and the keys of the condition checks.
func briefTransaction(t *testing.T, target string, body map[string]any) (map[string]any, []string) {
	t.Helper()
	if !strings.HasSuffix(target, ".TransactWriteItems") {
		t.Fatalf("brief write is not a transaction: %s", target)
	}
	var update map[string]any
	var checked []string
	for _, op := range body["TransactItems"].([]any) {
		op := op.(map[string]any)
		if u, ok := op["Update"].(map[string]any); ok {
			update = u
			continue
		}
		key := op["ConditionCheck"].(map[string]any)["Key"].(map[string]any)
		checked = append(checked, key["SK"].(map[string]any)["S"].(string))
	}
	if update == nil {
		t.Fatal("transaction has no update")
	}
	return update, checked
}

func TestCustomerBriefQueueRequiresMembershipAndClearsExpiredBrief(t *testing.T) {
	now := time.Now().UTC()
	next := &model.CustomerBriefState{AccountID: "acct", UserID: "user", RunID: "run2", RequestedAt: now, UpdatedAt: now, LeaseUntil: now.Add(time.Minute).UnixMilli(), ExpiresAt: now.Add(time.Hour).Unix()}
	kept := *next
	kept.BriefExpiresAt = now.Add(time.Minute).Unix()
	for name, tc := range map[string]struct {
		prior   *model.CustomerBriefState
		next    *model.CustomerBriefState
		clear   bool
		require []string
	}{
		"absent":  {nil, next, false, []string{"attribute_not_exists"}},
		"pending": {&model.CustomerBriefState{AccountID: "acct", UserID: "user", RunID: "run1", Status: model.AnalysisQueued, LeaseUntil: 5}, next, false, []string{"run1", "pendingShareExpiresAt"}},
		"kept":    {&model.CustomerBriefState{AccountID: "acct", UserID: "user", RunID: "run1", Status: model.AnalysisSucceeded}, &kept, false, []string{"run1"}},
		"expired": {&model.CustomerBriefState{AccountID: "acct", UserID: "user", RunID: "run1", Status: model.AnalysisSucceeded}, next, true, []string{"run1", "succeeded"}},
	} {
		t.Run(name, func(t *testing.T) {
			repo := actionAnalysisWire(t, func(target string, body map[string]any) {
				update, checked := briefTransaction(t, target, body)
				if strings.Join(checked, ",") != "META,MEMBER#user" {
					t.Fatalf("access checks: %v", checked)
				}
				if update["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "CUSTBRIEF#user" {
					t.Fatal("wrong row")
				}
				condition := analysisCondition(t, update)
				for _, want := range tc.require {
					if !strings.Contains(condition, want) {
						t.Fatalf("missing %s: %s", want, condition)
					}
				}
				expression := update["UpdateExpression"].(string)
				if tc.clear != strings.Contains(expression, "REMOVE") {
					t.Fatalf("content removal mismatch: %s", expression)
				}
				names := update["ExpressionAttributeNames"].(map[string]any)
				pinned := false
				for _, name := range names {
					pinned = pinned || name == "briefExpiresAt"
				}
				// Clearing removes briefExpiresAt; keeping pins it; otherwise untouched.
				if pinned != (tc.clear || tc.next.BriefExpiresAt != 0) {
					t.Fatalf("briefExpiresAt handling: %s %v", expression, names)
				}
			}, 200, `{}`)
			if err := repo.QueueCustomerBrief(context.Background(), tc.prior, tc.next, tc.clear); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCustomerBriefCompleteSealsAndPublishesAtomically(t *testing.T) {
	now := time.Now().UTC()
	state := &model.CustomerBriefState{AccountID: "acct", UserID: "user", RunID: "run", Status: model.AnalysisRunning, InputHash: "hash", LeaseUntil: now.Add(time.Minute).UnixMilli()}
	repo := actionAnalysisWire(t, func(string, map[string]any) { t.Fatal("must fail closed before DynamoDB") }, 200, `{}`)
	if err := repo.CompleteCustomerBrief(context.Background(), state, []byte(`{}`), "r", "m", now); !errors.Is(err, ErrContactEncryptionUnavailable) {
		t.Fatalf("missing sealer: %v", err)
	}
	if err := repo.CompleteCustomerBrief(context.Background(), state, make([]byte, model.CustomerBriefMaxBytes+1), "r", "m", now); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("oversized content: %v", err)
	}
	sealer := &briefBindingSealer{}
	repo = actionAnalysisWire(t, func(target string, body map[string]any) {
		update, checked := briefTransaction(t, target, body)
		if strings.Join(checked, ",") != "META,MEMBER#user" {
			t.Fatalf("access checks: %v", checked)
		}
		condition := analysisCondition(t, update)
		for _, want := range []string{"run", "running", "hash", "leaseUntil"} {
			if !strings.Contains(condition, want) {
				t.Fatalf("missing %s: %s", want, condition)
			}
		}
		values := update["ExpressionAttributeValues"].(map[string]any)
		found := false
		for _, v := range values {
			if s, ok := v.(map[string]any)["S"].(string); ok && s == `sealed:{"situation":null}` {
				found = true
			}
		}
		ttl, briefExpiry := false, false
		for _, name := range update["ExpressionAttributeNames"].(map[string]any) {
			ttl = ttl || name == model.PendingShareTTLAttribute
			briefExpiry = briefExpiry || name == "briefExpiresAt"
		}
		if !found || !ttl || !briefExpiry {
			t.Fatal("sealed content or expiry not written")
		}
	}, 200, `{}`)
	repo.SetRecordSealer(sealer)
	if err := repo.CompleteCustomerBrief(context.Background(), state, []byte(`{"situation":null}`), "r", "m", now); err != nil {
		t.Fatal(err)
	}
	if sealer.binding["purpose"] != "ttobak-customer-brief" || sealer.binding["accountId"] != "acct" || sealer.binding["userId"] != "user" {
		t.Fatalf("binding: %v", sealer.binding)
	}
}

func TestCustomerBriefFailIsSingleConditionalUpdate(t *testing.T) {
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		if !strings.HasSuffix(target, ".UpdateItem") {
			t.Fatalf("fail must not need membership: %s", target)
		}
		update := analysisCondition(t, body)
		if !strings.Contains(update, "run") || strings.Contains(body["UpdateExpression"].(string), "sealed") {
			t.Fatalf("fail must be run-conditioned and keep the brief: %s", body["UpdateExpression"])
		}
	}, 200, `{}`)
	state := &model.CustomerBriefState{AccountID: "acct", UserID: "user", RunID: "run", Status: model.AnalysisRunning}
	if err := repo.FailCustomerBrief(context.Background(), state, "X", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestCustomerBriefGetRejectsForeignRows(t *testing.T) {
	repo := actionAnalysisWire(t, nil, 200, `{"Item":{"PK":{"S":"ACCOUNT#acct"},"SK":{"S":"CUSTBRIEF#other"},"accountId":{"S":"acct"},"userId":{"S":"other"}}}`)
	if _, err := repo.GetCustomerBrief(context.Background(), "acct", "user"); !errors.Is(err, ErrCustomerBriefUnreadable) {
		t.Fatalf("foreign row: %v", err)
	}
}
