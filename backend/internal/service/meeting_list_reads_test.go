package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// listWire is an in-memory DynamoDB endpoint for the GET /api/meetings read
// path. It serves the real repository over HTTP, applies projections, and
// records each operation with its serial depth: a call's depth is one more
// than the deepest call that had finished when it started. Every call holds
// for delay so calls issued together overlap and share a depth.
type listWire struct {
	t      *testing.T
	delay  time.Duration
	rows   map[[2]string]map[string]types.AttributeValue
	owned  []map[string]types.AttributeValue // GSI1 rows, newest first
	shares []map[string]types.AttributeValue
	fail   map[string]error // operation name -> injected failure

	mu       sync.Mutex
	finished int
	calls    []listWireCall
}

type listWireCall struct {
	op         string
	depth      int
	bytes      int
	consistent bool
	projection string
}

func newListWire(t *testing.T, delay time.Duration) *listWire {
	return &listWire{t: t, delay: delay, rows: map[[2]string]map[string]types.AttributeValue{}, fail: map[string]error{}}
}

func (w *listWire) put(t *testing.T, v any) map[string]types.AttributeValue {
	t.Helper()
	item, err := attributevalue.MarshalMap(v)
	if err != nil {
		t.Fatal(err)
	}
	pk := item["PK"].(*types.AttributeValueMemberS).Value
	sk := item["SK"].(*types.AttributeValueMemberS).Value
	w.rows[[2]string{pk, sk}] = item
	return item
}

func (w *listWire) repo() *repository.DynamoDBRepository {
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: w})
	return repository.NewDynamoDBRepository(client, "test")
}

func (w *listWire) Do(req *http.Request) (*http.Response, error) {
	op := strings.TrimPrefix(req.Header.Get("X-Amz-Target"), "DynamoDB_20120810.")
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		w.t.Errorf("decode %s: %v", op, err)
	}
	w.mu.Lock()
	depth := w.finished + 1
	w.mu.Unlock()
	select {
	case <-time.After(w.delay):
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	name, status, out := w.serve(op, body)
	raw, _ := json.Marshal(out)
	w.mu.Lock()
	w.finished = max(w.finished, depth)
	consistent, _ := body["ConsistentRead"].(bool)
	projection := wireProjection(body)
	if items, ok := body["RequestItems"].(map[string]any); ok {
		batch, _ := items["test"].(map[string]any)
		consistent, _ = batch["ConsistentRead"].(bool)
		projection = wireProjection(batch)
	}
	w.calls = append(w.calls, listWireCall{op: name, depth: depth, bytes: len(raw), consistent: consistent, projection: projection})
	w.mu.Unlock()
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: req}, nil
}

// serve names each read by the repository operation that issues it.
func (w *listWire) serve(op string, body map[string]any) (string, int, any) {
	name := op
	var out any
	switch op {
	case "GetItem":
		key := wireKey(body["Key"])
		switch {
		case key[1] == model.PrefixProfile:
			name = "GetItem:profile"
		case strings.HasPrefix(key[1], model.PrefixMember):
			name = "GetItem:member"
		case strings.HasPrefix(key[1], model.PrefixMeeting):
			name = "GetItem:meeting"
		case strings.HasPrefix(key[1], model.PrefixShare):
			name = "GetItem:share"
		}
		result := map[string]any{}
		if row := w.rows[key]; row != nil {
			result["Item"] = wireItem(project(row, wireProjection(body)))
		}
		out = result
	case "Query":
		values := fmt.Sprint(body["ExpressionAttributeValues"])
		items := []map[string]types.AttributeValue{}
		var last map[string]types.AttributeValue
		switch {
		case strings.Contains(values, model.PrefixPendingShare):
			name = "Query:pendingShares"
		case body["IndexName"] == "GSI1":
			name = "Query:ownedGSI1"
			limit, _ := strconv.Atoi(fmt.Sprint(body["Limit"]))
			items = w.owned
			if limit > 0 && len(items) > limit {
				items, last = items[:limit], items[limit-1]
			}
		case strings.Contains(values, model.PrefixShare):
			name = "Query:shares"
			items = w.shares
		default:
			w.t.Errorf("unexpected query %v", body)
		}
		wire := []any{}
		for _, item := range items {
			wire = append(wire, wireItem(project(item, wireProjection(body))))
		}
		result := map[string]any{"Items": wire, "Count": len(wire)}
		if last != nil {
			result["LastEvaluatedKey"] = wireItem(project(last, "PK, SK, GSI1PK, GSI1SK"))
		}
		out = result
	case "BatchGetItem":
		batch := body["RequestItems"].(map[string]any)["test"].(map[string]any)
		items := []any{}
		for _, raw := range batch["Keys"].([]any) {
			if row := w.rows[wireKey(raw)]; row != nil {
				items = append(items, wireItem(project(row, wireProjection(batch))))
			}
		}
		out = map[string]any{"Responses": map[string]any{"test": items}}
	case "PutItem":
		out = map[string]any{}
	default:
		w.t.Errorf("unexpected operation %s", op)
	}
	if err := w.fail[name]; err != nil {
		return name, 400, map[string]any{"__type": "com.amazon.coral.validate#ValidationException", "message": err.Error()}
	}
	return name, 200, out
}

func (w *listWire) counts() (map[string]int, int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	counts := map[string]int{}
	depth, bytes := 0, 0
	for _, call := range w.calls {
		counts[call.op]++
		depth = max(depth, call.depth)
		bytes += call.bytes
	}
	return counts, depth, bytes
}

func (w *listWire) byOp(op string) []listWireCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []listWireCall
	for _, call := range w.calls {
		if call.op == op {
			out = append(out, call)
		}
	}
	return out
}

func wireKey(raw any) [2]string {
	key := raw.(map[string]any)
	return [2]string{key["PK"].(map[string]any)["S"].(string), key["SK"].(map[string]any)["S"].(string)}
}

// wireProjection resolves expression attribute names into a plain list.
func wireProjection(body map[string]any) string {
	projection, _ := body["ProjectionExpression"].(string)
	names, _ := body["ExpressionAttributeNames"].(map[string]any)
	parts := strings.Split(projection, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if name, ok := names[part].(string); ok {
			part = name
		}
		parts[i] = part
	}
	return strings.Join(parts, ", ")
}

func project(item map[string]types.AttributeValue, projection string) map[string]types.AttributeValue {
	if strings.TrimSpace(projection) == "" {
		return item
	}
	out := map[string]types.AttributeValue{}
	for _, name := range strings.Split(projection, ",") {
		if v, ok := item[strings.TrimSpace(name)]; ok {
			out[strings.TrimSpace(name)] = v
		}
	}
	return out
}

func wireItem(item map[string]types.AttributeValue) map[string]any {
	out := make(map[string]any, len(item))
	for k, v := range item {
		out[k] = wireValue(v)
	}
	return out
}

func wireValue(v types.AttributeValue) any {
	switch v := v.(type) {
	case *types.AttributeValueMemberS:
		return map[string]any{"S": v.Value}
	case *types.AttributeValueMemberN:
		return map[string]any{"N": v.Value}
	case *types.AttributeValueMemberBOOL:
		return map[string]any{"BOOL": v.Value}
	case *types.AttributeValueMemberNULL:
		return map[string]any{"NULL": true}
	case *types.AttributeValueMemberSS:
		return map[string]any{"SS": v.Value}
	case *types.AttributeValueMemberNS:
		return map[string]any{"NS": v.Value}
	case *types.AttributeValueMemberL:
		list := make([]any, len(v.Value))
		for i, e := range v.Value {
			list[i] = wireValue(e)
		}
		return map[string]any{"L": list}
	case *types.AttributeValueMemberM:
		return map[string]any{"M": wireItem(v.Value)}
	}
	panic(fmt.Sprintf("unsupported attribute %T", v))
}

// listReadsFixture is the production first page: a full owned GSI1 page with
// a continuation, two direct shares and two account-origin shares in two
// accounts. Shared rows carry large transcripts and notes the list never
// returns, as in the slow production traces.
func listReadsFixture(t *testing.T, delay time.Duration) *listWire {
	w := newListWire(t, delay)
	big := strings.Repeat("가", 30000) // 90 KB of UTF-8 per field
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w.put(t, model.User{PK: model.PrefixUser + "member", SK: model.PrefixProfile, UserID: "member", Email: "member@example.com", EntityType: "USER"})
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("own-%02d", i)
		date := created.Add(-time.Duration(i) * time.Hour)
		w.owned = append(w.owned, w.put(t, model.Meeting{PK: model.PrefixUser + "member", SK: model.PrefixMeeting + id, MeetingID: id, UserID: "member",
			Title: id, Date: date, Status: model.StatusDone, Content: "notes " + id, CreatedAt: date, UpdatedAt: date,
			GSI1PK: model.PrefixUser + "member", GSI1SK: date.Format(time.RFC3339), EntityType: "MEETING"}))
	}
	for _, account := range []string{"acc-1", "acc-2"} {
		w.put(t, model.AccountMember{PK: model.PrefixAccount + account, SK: model.PrefixMember + "member", AccountID: account, UserID: "member", Role: model.RoleSA, EntityType: model.EntityTypeAccountMember})
	}
	shared := []struct {
		id, account, permission, origin string
		team                            bool
	}{
		{"sh-direct-edit", "", model.PermissionEdit, "", false},
		{"sh-team-1", "acc-1", model.PermissionRead, model.ShareOriginAccount, true},
		{"sh-direct-read", "", model.PermissionRead, "", false},
		{"sh-team-2", "acc-2", model.PermissionRead, model.ShareOriginAccount, true},
	}
	for i, s := range shared {
		date := created.Add(-time.Duration(30+i) * time.Hour)
		w.put(t, model.Meeting{PK: model.PrefixUser + "owner", SK: model.PrefixMeeting + s.id, MeetingID: s.id, UserID: "owner",
			Title: "title " + s.id, Date: date, Status: model.StatusDone, Content: strings.Repeat("요약", 150) + s.id,
			Participants: []string{"a", "b"}, Tags: []string{"tag"}, Sentiment: "positive", Duration: 600 + i,
			TranscriptA: big, TranscriptB: big, TranscriptSegments: big, Notes: big, LiveSummary: big,
			AccountID: s.account, SharedToAccount: s.team, CreatedAt: date, UpdatedAt: date.Add(time.Minute),
			GSI1PK: model.PrefixUser + "owner", GSI1SK: date.Format(time.RFC3339), EntityType: "MEETING"})
		w.shares = append(w.shares, w.put(t, model.Share{PK: model.PrefixUser + "member", SK: model.PrefixShare + s.id, MeetingID: s.id,
			OwnerID: "owner", OwnerEmail: "owner@example.com", SharedToID: "member", Email: "member@example.com", Permission: s.permission, Origin: s.origin}))
	}
	return w
}

// legacyListService is the pre-change production wiring: eventually
// consistent full-row BatchGetMeetings plus per-share canonical GetItem.
func legacyListService(repo *repository.DynamoDBRepository) *MeetingService {
	return &MeetingService{repo: repo, listMetadata: repo.MetadataView()}
}

func runListRequest(svc *MeetingService) (*model.MeetingListResponse, error) {
	ctx := context.Background()
	joined := svc.EnsureProfileAndMaterializePendingShares(ctx, "member", "member@example.com", "Member", true)
	return svc.ListMeetings(ctx, "member", "all", "", "", 20, joined...)
}

func formatCounts(counts map[string]int) string {
	ops := make([]string, 0, len(counts))
	for op, n := range counts {
		ops = append(ops, fmt.Sprintf("%s=%d", op, n))
	}
	sort.Strings(ops)
	return strings.Join(ops, " ")
}

// TestMeetingListReadShape pins the DynamoDB request shape of the first page
// for the legacy wiring (the production baseline) and the current wiring.
func TestMeetingListReadShape(t *testing.T) {
	for _, tc := range []struct {
		name       string
		build      func(*repository.DynamoDBRepository) *MeetingService
		counts     map[string]int
		depth      int
		maxBytes   int
		consistent bool
	}{
		// Baseline before this change: 7 GetItem, 3 Query, 1 BatchGetItem at
		// depth 8. The legacy service wiring keeps its per-share re-reads; the
		// profile/pending and owned/shares overlaps apply to both wirings.
		{"legacy wiring", legacyListService, map[string]int{
			"GetItem:profile": 1, "Query:pendingShares": 1, "Query:ownedGSI1": 1, "Query:shares": 1,
			"BatchGetItem": 1, "GetItem:meeting": 2, "GetItem:member": 4,
		}, 6, 0, false},
		{"projected consistent batch", NewMeetingService, map[string]int{
			"GetItem:profile": 1, "Query:pendingShares": 1, "Query:ownedGSI1": 1, "Query:shares": 1,
			"BatchGetItem": 1, "GetItem:member": 4,
		}, 5, 64 * 1024, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := listReadsFixture(t, 20*time.Millisecond)
			resp, err := runListRequest(tc.build(w.repo()))
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Meetings) != 24 || resp.NextCursor == nil {
				t.Fatalf("meetings=%d next=%v, want 24 with a continuation", len(resp.Meetings), resp.NextCursor)
			}
			counts, depth, bytes := w.counts()
			t.Logf("%s: depth=%d bytes=%d [%s]", tc.name, depth, bytes, formatCounts(counts))
			if formatCounts(counts) != formatCounts(tc.counts) {
				t.Errorf("reads = [%s], want [%s]", formatCounts(counts), formatCounts(tc.counts))
			}
			if depth != tc.depth {
				t.Errorf("serial depth = %d, want %d", depth, tc.depth)
			}
			if tc.maxBytes > 0 && bytes > tc.maxBytes {
				t.Errorf("response bytes = %d, want <= %d", bytes, tc.maxBytes)
			}
			for _, call := range w.byOp("BatchGetItem") {
				if call.consistent != tc.consistent {
					t.Errorf("BatchGetItem consistent=%v, want %v", call.consistent, tc.consistent)
				}
				if tc.consistent && call.projection != strings.Join(repository.MeetingListRowFields, ", ") {
					t.Errorf("BatchGetItem projection = %q", call.projection)
				}
			}
		})
	}
}

// TestMeetingListResponseUnchangedByProjection compares the full JSON
// response, cursor included, of the legacy and projected wirings.
func TestMeetingListResponseUnchangedByProjection(t *testing.T) {
	legacy, err := runListRequest(legacyListService(listReadsFixture(t, 0).repo()))
	if err != nil {
		t.Fatal(err)
	}
	current, err := runListRequest(NewMeetingService(listReadsFixture(t, 0).repo()))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(legacy)
	b, _ := json.Marshal(current)
	if string(a) != string(b) {
		t.Fatalf("response changed:\nlegacy  %s\ncurrent %s", a, b)
	}
	want := []string{"sh-direct-edit", "sh-team-1", "sh-direct-read", "sh-team-2"}
	if got := meetingIDs(current.Meetings[20:]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("shared order = %v, want %v", got, want)
	}
}

// fullListMeeting populates every attribute a list row can expose plus the
// large fields the projection must drop.
func fullListMeeting() model.Meeting {
	content := strings.Repeat("요약 ", 120)
	at := time.Date(2026, 10, 2, 3, 4, 5, 0, time.UTC)
	return model.Meeting{PK: model.PrefixUser + "owner", SK: model.PrefixMeeting + "m-1", EntityType: "MEETING",
		MeetingID: "m-1", UserID: "owner", AccountID: "acc-1", SharedToAccount: true, Title: "title", Date: at,
		Status: model.StatusDone, Content: content, Participants: []string{"a"}, Tags: []string{"t"}, Sentiment: "neutral",
		Duration: 61, CreatedAt: at, UpdatedAt: at.Add(time.Hour),
		SummaryPrivacy: &model.SummaryPrivacy{Version: model.SummaryPrivacyVersion, State: model.SummaryPrivacySafe, GenerationID: "gen",
			ContentSHA256: model.SummaryPrivacyHash(content)},
		SummaryAccountApproval: &model.SummaryAccountApproval{Version: 1, OwnerUserID: "owner", AccountID: "acc-1", SummaryGeneration: "gen",
			ContentSHA256: model.SummaryPrivacyHash(content), ApprovedAt: "2026-10-02T03:04:05Z"},
		TranscriptA: "transcript", TranscriptB: "transcript", TranscriptSegments: "[]", Notes: "notes", LiveSummary: "live",
		ActionItems: "[]", Insights: "[]", SpeakerMap: map[string]string{"spk_0": "A"}}
}

// listRowView is everything the list path derives from a meeting row: the
// response item and every field its access and privacy checks read.
func listRowView(m *model.Meeting) string {
	permission, sharedBy := model.PermissionEdit, "owner@example.com"
	item := model.ToMeetingListItem(m, true, &sharedBy, &permission)
	projected := projectAccountReadMeeting(m, &accountReadGrant{AccountID: "acc-1", Member: &model.AccountMember{AccountID: "acc-1", UserID: "member"}})
	approval := ""
	if m.SummaryAccountApproval != nil {
		approval = m.SummaryAccountApproval.ApprovedAt
	}
	view, _ := json.Marshal([]any{item, model.ToMeetingListItem(projected, true, nil, &permission), m.UserID, m.MeetingID, m.AccountID,
		m.SharedToAccount, m.AccountSummaryShareable(), m.SummaryPrivacyState(), m.SummaryGeneration(), approval, m.PK, m.SK, m.EntityType})
	return string(view)
}

func projectedRow(t *testing.T, m model.Meeting, fields []string) *model.Meeting {
	t.Helper()
	item, err := attributevalue.MarshalMap(m)
	if err != nil {
		t.Fatal(err)
	}
	var out model.Meeting
	if err := attributevalue.UnmarshalMap(project(item, strings.Join(fields, ",")), &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

// TestMeetingListRowFieldsCoverResponse fails when a field the list returns
// or checks is missing from MeetingListRowFields, and when the fixture stops
// exercising any projected field.
func TestMeetingListRowFieldsCoverResponse(t *testing.T) {
	full := fullListMeeting()
	raw, _ := json.Marshal(model.ToMeetingListItem(&full, true, new(string), new(string)))
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for _, name := range []string{"meetingId", "accountId", "title", "date", "status", "summary", "participants", "tags", "sentiment", "duration", "isShared", "ownerUserId", "sharedBy", "permission", "createdAt", "updatedAt"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("fixture leaves list field %q empty", name)
		}
	}
	want := listRowView(&full)
	got := projectedRow(t, full, repository.MeetingListRowFields)
	if view := listRowView(got); view != want {
		t.Fatalf("projection changes the list view:\n got %s\nwant %s", view, want)
	}
	if got.TranscriptA != "" || got.TranscriptB != "" || got.TranscriptSegments != "" || got.Notes != "" ||
		got.LiveSummary != "" || got.ActionItems != "" || got.Insights != "" || got.SpeakerMap != nil {
		t.Fatalf("projection kept unbounded fields: %+v", got)
	}
	for i, field := range repository.MeetingListRowFields {
		t.Run("without "+field, func(t *testing.T) {
			fewer := append(append([]string{}, repository.MeetingListRowFields[:i]...), repository.MeetingListRowFields[i+1:]...)
			if listRowView(projectedRow(t, full, fewer)) == want {
				t.Fatalf("dropping %q goes unnoticed; extend fullListMeeting or listRowView", field)
			}
		})
	}
}

// TestMeetingListSharedInclusion lists shared and account rows that must be
// included or withheld, and compares the projected and legacy wirings.
func TestMeetingListSharedInclusion(t *testing.T) {
	meetingRow := func(id string) [2]string { return [2]string{model.PrefixUser + "owner", model.PrefixMeeting + id} }
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *listWire)
		want   []string
	}{
		{"all shared rows", func(*testing.T, *listWire) {}, []string{"sh-direct-edit", "sh-team-1", "sh-direct-read", "sh-team-2"}},
		{"removed member loses account-origin row", func(_ *testing.T, w *listWire) {
			delete(w.rows, [2]string{model.PrefixAccount + "acc-2", model.PrefixMember + "member"})
		}, []string{"sh-direct-edit", "sh-team-1", "sh-direct-read"}},
		{"unpublished meeting loses account-origin row", func(t *testing.T, w *listWire) {
			var m model.Meeting
			_ = attributevalue.UnmarshalMap(w.rows[meetingRow("sh-team-1")], &m)
			m.SharedToAccount = false
			w.put(t, m)
		}, []string{"sh-direct-edit", "sh-direct-read", "sh-team-2"}},
		{"deleted meeting is skipped", func(_ *testing.T, w *listWire) {
			delete(w.rows, meetingRow("sh-direct-read"))
		}, []string{"sh-direct-edit", "sh-team-1", "sh-team-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outputs []string
			for _, build := range []func(*repository.DynamoDBRepository) *MeetingService{legacyListService, NewMeetingService} {
				w := listReadsFixture(t, 0)
				tc.mutate(t, w)
				resp, err := runListRequest(build(w.repo()))
				if err != nil {
					t.Fatal(err)
				}
				if got := meetingIDs(resp.Meetings[20:]); strings.Join(got, ",") != strings.Join(tc.want, ",") {
					t.Fatalf("shared = %v, want %v", got, tc.want)
				}
				raw, _ := json.Marshal(resp)
				outputs = append(outputs, string(raw))
			}
			if outputs[0] != outputs[1] {
				t.Fatalf("wirings differ:\nlegacy  %s\ncurrent %s", outputs[0], outputs[1])
			}
		})
	}
}

// TestMeetingListReadErrors checks first-error propagation through the
// overlapped and batched reads.
func TestMeetingListReadErrors(t *testing.T) {
	boom := errors.New("injected")
	for _, tc := range []struct {
		name    string
		fail    []string
		mutate  func(*listWire)
		wantErr string // empty: the request succeeds
		is      error
		shared  int
	}{
		{name: "owned query wins over share query", fail: []string{"Query:ownedGSI1", "Query:shares"}, wantErr: "failed to query meetings"},
		{name: "share query", fail: []string{"Query:shares"}, wantErr: "failed to list shares"},
		{name: "batch read", fail: []string{"BatchGetItem"}, wantErr: "failed to load shared meetings"},
		{name: "unfiltered membership errors skip account rows", fail: []string{"GetItem:member"}, shared: 2},
		{name: "profile failure skips pending shares", fail: []string{"GetItem:profile"}, shared: 4},
		{name: "pending share failure is logged", fail: []string{"Query:pendingShares"}, shared: 4},
		{name: "account row identity mismatch", mutate: func(w *listWire) {
			key := [2]string{model.PrefixUser + "owner", model.PrefixMeeting + "sh-team-2"}
			var m model.Meeting
			_ = attributevalue.UnmarshalMap(w.rows[key], &m)
			m.UserID = "someone-else"
			item, _ := attributevalue.MarshalMap(m)
			w.rows[key] = item
		}, wantErr: ErrReadingData.Error(), is: ErrReadingData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := listReadsFixture(t, 0)
			for _, op := range tc.fail {
				w.fail[op] = boom
			}
			if tc.mutate != nil {
				tc.mutate(w)
			}
			resp, err := runListRequest(NewMeetingService(w.repo()))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || tc.is != nil && !errors.Is(err, tc.is) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := len(resp.Meetings) - 20; got != tc.shared {
				t.Fatalf("shared rows = %d, want %d", got, tc.shared)
			}
		})
	}
}

// listRowsMock serves BatchGetMeetingListRows from the mock repository's
// canonical meetings, as a consistent batch read would.
type listRowsMock struct {
	*latencyMeetingRepo
	err error
}

func (r *listRowsMock) BatchGetMeetingListRows(ctx context.Context, keys []repository.MeetingKey) (map[repository.MeetingKey]*model.Meeting, error) {
	if err := r.wait(ctx, "BatchGetMeetingListRows"); err != nil {
		return nil, err
	}
	if r.err != nil {
		return nil, r.err
	}
	out := map[repository.MeetingKey]*model.Meeting{}
	for _, key := range keys {
		if m := r.mockMeetingRepo.meetings[meetingKey(key.OwnerID, key.MeetingID)]; m != nil {
			cp := *m
			out[key] = &cp
		}
	}
	return out, nil
}

// TestTeamPageUsesConsistentBatchRows covers the team stream: batch rows
// replace every per-reference canonical GetItem without changing results.
func TestTeamPageUsesConsistentBatchRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*mockMeetingRepo)
		batch  error
		skip   string
	}{
		{name: "all team meetings"},
		{name: "unpublished meeting is withheld", mutate: func(m *mockMeetingRepo) { m.meetings[meetingKey("owner", "t-2-1")].SharedToAccount = false }, skip: "t-2-1"},
		{name: "moved meeting is withheld", mutate: func(m *mockMeetingRepo) { m.meetings[meetingKey("owner", "t-3-4")].AccountID = "acc-9" }, skip: "t-3-4"},
		{name: "individual share is left to the regular stream", mutate: func(m *mockMeetingRepo) {
			m.shares[shareKey("member", "t-1-2")] = &model.Share{MeetingID: "t-1-2", OwnerID: "owner", SharedToID: "member", Permission: model.PermissionEdit}
		}, skip: "t-1-2"},
		{name: "batch error", batch: errors.New("batch down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outputs []string
			for _, batched := range []bool{false, true} {
				base := teamTabFixture()
				if tc.mutate != nil {
					tc.mutate(base)
				}
				repo := &listRowsMock{latencyMeetingRepo: newLatencyMeetingRepo(base, 0), err: tc.batch}
				svc := NewMeetingServiceForTest(repo)
				if batched {
					svc.listRows = repo
				} else {
					base.batchMeetingsErr = tc.batch
				}
				resp, err := svc.ListMeetings(context.Background(), "member", "", "", "", 20)
				if tc.batch != nil {
					if !errors.Is(err, tc.batch) || !strings.Contains(err.Error(), "load team meetings") {
						t.Fatalf("err = %v, want the batch error", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range resp.Meetings {
					if item.MeetingID == tc.skip {
						t.Fatalf("%s listed", tc.skip)
					}
				}
				if batched && (repo.calls["GetMeeting"] != 0 || repo.calls["BatchGetMeetings"] != 0) {
					t.Fatalf("batched wiring still re-reads: [%s]", repo.summary())
				}
				raw, _ := json.Marshal(resp)
				outputs = append(outputs, string(raw))
			}
			if len(outputs) == 2 && outputs[0] != outputs[1] {
				t.Fatalf("wirings differ:\nlegacy  %s\ncurrent %s", outputs[0], outputs[1])
			}
		})
	}
}

// TestEnsureProfileOverlapsPendingShares: the profile and pending reads share
// a depth for an existing profile; a created profile lists again afterwards.
func TestEnsureProfileOverlapsPendingShares(t *testing.T) {
	w := listReadsFixture(t, 20*time.Millisecond)
	NewMeetingService(w.repo()).EnsureProfileAndMaterializePendingShares(context.Background(), "member", "member@example.com", "M", true)
	if counts, depth, _ := w.counts(); depth != 1 || counts["Query:pendingShares"] != 1 || counts["GetItem:profile"] != 1 {
		t.Fatalf("existing profile: depth=%d [%s], want both reads at depth 1", depth, formatCounts(counts))
	}
	fresh := newListWire(t, 20*time.Millisecond)
	NewMeetingService(fresh.repo()).EnsureProfileAndMaterializePendingShares(context.Background(), "new-user", "new@example.com", "N", true)
	pending, put := fresh.byOp("Query:pendingShares"), fresh.byOp("PutItem")
	if len(pending) != 2 || len(put) != 1 || pending[1].depth <= put[0].depth {
		t.Fatalf("a created profile must list pending shares after the create: %+v", fresh.calls)
	}
}
