package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type batchWireKey struct{ PK, SK string }

// batchWire serves BatchGetItem from rows and records each request's keys.
// unprocessed returns the keys to report unprocessed for a given request.
type batchWire struct {
	mu          sync.Mutex
	rows        map[batchWireKey]map[string]any
	requests    [][]batchWireKey
	consistent  []bool
	projections []string
	unprocessed func(n int, keys []batchWireKey) []batchWireKey
	extra       map[string]any
	query       func(map[string]any) (int, string)
}

func (w *batchWire) repo(t *testing.T) *DynamoDBRepository {
	return projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		if strings.HasSuffix(target, ".Query") && w.query != nil {
			return w.query(body)
		}
		if !strings.HasSuffix(target, ".BatchGetItem") {
			t.Fatalf("unexpected call %s", target)
		}
		request := body["RequestItems"].(map[string]any)["test"].(map[string]any)
		keys := []batchWireKey{}
		for _, raw := range request["Keys"].([]any) {
			key := raw.(map[string]any)
			keys = append(keys, batchWireKey{
				PK: key["PK"].(map[string]any)["S"].(string),
				SK: key["SK"].(map[string]any)["S"].(string),
			})
		}
		w.mu.Lock()
		n := len(w.requests)
		w.requests = append(w.requests, keys)
		w.consistent = append(w.consistent, request["ConsistentRead"] == true)
		projection, _ := request["ProjectionExpression"].(string)
		if names, ok := request["ExpressionAttributeNames"].(map[string]any); ok {
			// Replace whole aliases so #1 never rewrites part of #13.
			parts := strings.Split(projection, ", ")
			for i, part := range parts {
				if name, ok := names[part].(string); ok {
					parts[i] = name
				}
			}
			projection = strings.Join(parts, ", ")
		}
		w.projections = append(w.projections, projection)
		w.mu.Unlock()
		skip := map[batchWireKey]bool{}
		unprocessed := []any{}
		if w.unprocessed != nil {
			for _, key := range w.unprocessed(n, keys) {
				skip[key] = true
				unprocessed = append(unprocessed, map[string]any{"PK": map[string]any{"S": key.PK}, "SK": map[string]any{"S": key.SK}})
			}
		}
		items := []any{}
		for _, key := range keys {
			if row := w.rows[key]; row != nil && !skip[key] {
				items = append(items, row)
			}
		}
		if w.extra != nil {
			items = append(items, w.extra)
		}
		out := map[string]any{"Responses": map[string]any{"test": items}}
		if len(unprocessed) != 0 {
			out["UnprocessedKeys"] = map[string]any{"test": map[string]any{"Keys": unprocessed}}
		}
		raw, _ := json.Marshal(out)
		return 200, string(raw)
	})
}

func batchAccountRow(id string) map[string]any {
	return map[string]any{"PK": map[string]any{"S": "ACCOUNT#" + id}, "SK": map[string]any{"S": "META"},
		"accountId": map[string]any{"S": id}, "name": map[string]any{"S": "Name " + id}}
}

func batchMemberRow(id, user string) map[string]any {
	return map[string]any{"PK": map[string]any{"S": "ACCOUNT#" + id}, "SK": map[string]any{"S": "MEMBER#" + user},
		"accountId": map[string]any{"S": id}, "userId": map[string]any{"S": user}, "role": map[string]any{"S": model.RoleAM}}
}

func batchProjectRow(id string) map[string]any {
	return map[string]any{"PK": map[string]any{"S": "PROJECT#" + id}, "SK": map[string]any{"S": "CONFIG"},
		"projectId": map[string]any{"S": id}, "name": map[string]any{"S": "Project " + id}}
}

func TestBatchGetAccountRows(t *testing.T) {
	defer func(previous time.Duration) { batchReadBackoff = previous }(batchReadBackoff)
	batchReadBackoff = time.Millisecond
	accountKeys := func(n int) ([]AccountRowKey, map[batchWireKey]map[string]any) {
		keys := []AccountRowKey{}
		rows := map[batchWireKey]map[string]any{}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("a-%03d", i)
			keys = append(keys, AccountRowKey{AccountID: id}, AccountRowKey{AccountID: id, UserID: "reader"})
			if i%3 != 0 { // every third account has no membership row
				rows[batchWireKey{"ACCOUNT#" + id, "MEMBER#reader"}] = batchMemberRow(id, "reader")
			}
			rows[batchWireKey{"ACCOUNT#" + id, "META"}] = batchAccountRow(id)
		}
		return keys, rows
	}
	cases := []struct {
		name        string
		accounts    int
		unprocessed func(int, []batchWireKey) []batchWireKey
		extra       map[string]any
		wantErr     string
		requests    int
	}{
		{name: "empty", accounts: 0, requests: 0},
		{name: "single request", accounts: 10, requests: 1},
		{name: "more than 100 keys", accounts: 120, requests: 3},
		{name: "unprocessed keys are retried", accounts: 10, requests: 3,
			unprocessed: func(n int, keys []batchWireKey) []batchWireKey {
				if n < 2 {
					return keys[:2]
				}
				return nil
			}},
		{name: "progressing partial responses succeed past the stall limit", accounts: 10, requests: 20,
			unprocessed: func(_ int, keys []batchWireKey) []batchWireKey { return keys[1:] }},
		{name: "stalls after progress fail closed", accounts: 10, wantErr: "unprocessed keys remain", requests: 5,
			unprocessed: func(_ int, keys []batchWireKey) []batchWireKey { return keys[:1] }},
		{name: "never progressing fails closed", accounts: 10, wantErr: "unprocessed keys remain", requests: 4,
			unprocessed: func(_ int, keys []batchWireKey) []batchWireKey { return keys }},
		{name: "response outside the selection fails closed", accounts: 2, wantErr: "unexpected batch read response", requests: 1,
			extra: batchAccountRow("intruder")},
		{name: "malformed row fails closed", accounts: 2, wantErr: "unmarshal account", requests: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys, rows := accountKeys(tc.accounts)
			if tc.name == "malformed row fails closed" {
				rows[batchWireKey{"ACCOUNT#a-001", "META"}]["name"] = map[string]any{"L": []any{}}
			}
			w := &batchWire{rows: rows, unprocessed: tc.unprocessed, extra: tc.extra}
			got, err := w.repo(t).BatchGetAccountRows(context.Background(), append(keys, keys...))
			if len(w.requests) != tc.requests {
				t.Fatalf("requests = %d, want %d", len(w.requests), tc.requests)
			}
			for i, request := range w.requests {
				if len(request) > 100 || !w.consistent[i] {
					t.Fatalf("request %d: %d keys, consistent=%v", i, len(request), w.consistent[i])
				}
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || got != nil {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Accounts) != tc.accounts {
				t.Fatalf("accounts = %d", len(got.Accounts))
			}
			for i := 0; i < tc.accounts; i++ {
				id := fmt.Sprintf("a-%03d", i)
				if a := got.Accounts[id]; a == nil || a.AccountID != id || a.Name != "Name "+id {
					t.Fatalf("account %s = %+v", id, a)
				}
				m := got.Members[AccountRowKey{AccountID: id, UserID: "reader"}]
				if (i%3 == 0) != (m == nil) || (m != nil && (m.AccountID != id || m.Role != model.RoleAM)) {
					t.Fatalf("member %s = %+v", id, m)
				}
			}
		})
	}
}

func TestBatchGetAccountRowsRejectsInvalidKeys(t *testing.T) {
	w := &batchWire{}
	if _, err := w.repo(t).BatchGetAccountRows(context.Background(), []AccountRowKey{{UserID: "reader"}}); err == nil {
		t.Fatal("empty account ID must be rejected")
	}
	if len(w.requests) != 0 {
		t.Fatal("invalid selection must not be read")
	}
}

func TestListProjectsForUserBatchesCanonicalRows(t *testing.T) {
	rows := map[batchWireKey]map[string]any{}
	index := []any{}
	for i := 0; i < 150; i++ {
		id := fmt.Sprintf("p-%03d", i)
		index = append(index, map[string]any{"PK": map[string]any{"S": "USER#u"}, "SK": map[string]any{"S": "PROJECT#" + id}, "projectId": map[string]any{"S": id}})
		if i != 7 { // deleted project with a stale owner index row
			rows[batchWireKey{"PROJECT#" + id, "CONFIG"}] = batchProjectRow(id)
		}
	}
	queries := 0
	w := &batchWire{rows: rows, query: func(map[string]any) (int, string) {
		queries++
		items := []any{}
		if queries == 1 {
			items = index
		}
		raw, _ := json.Marshal(map[string]any{"Items": items})
		return 200, string(raw)
	}}
	r := w.repo(t)
	projects, err := r.ListProjectsForUser(context.Background(), "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 149 || projects[0].ProjectID != "p-000" || projects[7].ProjectID != "p-008" || projects[148].ProjectID != "p-149" {
		t.Fatalf("projects lost index order: %d", len(projects))
	}
	if len(w.requests) != 2 || queries != 2 {
		t.Fatalf("BatchGetItem requests = %d, queries = %d", len(w.requests), queries)
	}
}

func TestBatchGetProjectListRowsProjectsAwayLargeFields(t *testing.T) {
	row := batchProjectRow("p")
	row["description"] = map[string]any{"S": strings.Repeat("x", 4096)}
	row["accountIds"] = map[string]any{"SS": []any{"a"}}
	w := &batchWire{rows: map[batchWireKey]map[string]any{{"PROJECT#p", "CONFIG"}: row}}
	got, err := w.repo(t).BatchGetProjectListRows(context.Background(), []string{"p", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.projections) != 1 || strings.Contains(w.projections[0], "description") {
		t.Fatalf("projection = %q", w.projections)
	}
	for _, field := range ProjectListRowFields {
		if !strings.Contains(w.projections[0], field) {
			t.Fatalf("projection %q lacks %s", w.projections[0], field)
		}
	}
	if p := got["p"]; p == nil || p.Name != "Project p" || len(p.AccountIDs) != 1 || got["missing"] != nil {
		t.Fatalf("rows = %+v", got)
	}
}

func TestBatchReadDeadlineFailsClosed(t *testing.T) {
	defer func(backoff, deadline time.Duration) { batchReadBackoff, batchReadDeadline = backoff, deadline }(batchReadBackoff, batchReadDeadline)
	batchReadBackoff, batchReadDeadline = time.Second, 20*time.Millisecond
	w := &batchWire{rows: map[batchWireKey]map[string]any{}, unprocessed: func(_ int, keys []batchWireKey) []batchWireKey { return keys }}
	start := time.Now()
	_, err := w.repo(t).BatchGetAccountRows(context.Background(), []AccountRowKey{{AccountID: "a"}})
	if err == nil || !strings.Contains(err.Error(), "deadline") || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}
