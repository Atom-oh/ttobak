package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	chiadapter "github.com/awslabs/aws-lambda-go-api-proxy/chi"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type readingHTTP func(*http.Request) (*http.Response, error)

func (f readingHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

type readingFixture struct {
	meeting     *model.Meeting
	state       *model.ActionItemsAnalysis
	user        string
	share       *model.Share
	member      *model.AccountMember
	staleIndex  *model.Meeting
	afterStatus func()
	failS3      bool
	failStatus  bool
	objects     map[string]string
	s3Keys      []string
	reads       int
	repo        *repository.DynamoDBRepository
	adapter     *chiadapter.ChiLambda
}

func newReadingFixture(t *testing.T) *readingFixture {
	t.Helper()
	f := &readingFixture{user: "owner", meeting: &model.Meeting{
		PK: "USER#owner", SK: "MEETING#m1", UserID: "owner", MeetingID: "m1",
		EntityType: "MEETING", Status: model.StatusDone, Title: "회의", Notes: "작은 메모😀",
		SelectedTranscript: "A", UpdatedAt: time.Date(2026, 9, 12, 1, 2, 3, 0, time.UTC),
		TranscriptA: "s3://reading-fixture/transcripts/m1/transcriptA.txt",
		TranscriptB: "s3://reading-fixture/transcripts/m1/transcriptB.txt",
	}, objects: map[string]string{
		"transcripts/m1/transcriptA.txt": strings.Repeat("A", 4*1024*1024),
		"transcripts/m1/transcriptB.txt": strings.Repeat("B", 3*1024*1024),
	}}
	wire := readingHTTP(func(req *http.Request) (*http.Response, error) {
		var payload any = map[string]any{}
		switch req.Header.Get("X-Amz-Target") {
		case "DynamoDB_20120810.GetItem":
			f.reads++
			// The SDK attribute-value interface needs the small wire shape here.
			var raw struct {
				Key            map[string]struct{ S string }
				ConsistentRead bool
			}
			if err := json.NewDecoder(req.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			if !raw.ConsistentRead {
				t.Fatal("reading must use consistent primary reads")
			}
			var item any
			if raw.Key["PK"].S == "USER#owner" && raw.Key["SK"].S == "MEETING#m1" && f.meeting != nil {
				item = f.meeting
			}
			if raw.Key["SK"].S == model.ActionAnalysisSK {
				if f.failStatus {
					return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(`{"__type":"AccessDeniedException","message":"synthetic status failure"}`))}, nil
				}
				if f.state != nil {
					item = f.state
				}
				if f.afterStatus != nil {
					f.afterStatus()
					f.afterStatus = nil
				}
			}
			if raw.Key["PK"].S == "USER#viewer" && raw.Key["SK"].S == "SHARED#m1" && f.share != nil {
				item = f.share
			}
			if raw.Key["PK"].S == "ACCOUNT#acct" && raw.Key["SK"].S == "MEMBER#viewer" && f.member != nil {
				item = f.member
			}
			if item != nil {
				attrs, err := attributevalue.MarshalMap(item)
				if err != nil {
					t.Fatal(err)
				}
				// Encode DynamoDB's wire format rather than Go interface wrappers.
				payload = map[string]any{"Item": readingAttributeMap(attrs)}
			}
		case "DynamoDB_20120810.Query":
			item := f.meeting
			if f.staleIndex != nil {
				item = f.staleIndex
			}
			rows := []any{}
			if item != nil {
				attrs, err := attributevalue.MarshalMap(item)
				if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, readingAttributeMap(attrs))
			}
			payload = map[string]any{"Items": rows}
		default:
			if req.Method != http.MethodGet {
				t.Fatalf("unexpected storage write: %s", req.Method)
			}
			key := strings.TrimPrefix(req.URL.Path, "/")
			f.s3Keys = append(f.s3Keys, key)
			if f.failS3 {
				return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`<Error><Code>AccessDenied</Code></Error>`))}, nil
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(f.objects[key]))}, nil
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	f.repo = repository.NewDynamoDBRepositoryWithS3(
		dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: wire}),
		"reading-fixture", s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: wire}), "reading-fixture")
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); id != "" {
				r = r.WithContext(context.WithValue(r.Context(), middleware.UserIDKey, id))
			}
			next.ServeHTTP(w, r)
		})
	})
	analysis := service.NewActionItemsAnalysisService(f.repo, service.NewMeetingService(f.repo), nil, nil)
	router.Get("/api/meetings/{meetingId}/reading", NewMeetingReadingHandler(service.NewMeetingReadingService(f.repo, analysis)).Get)
	f.adapter = chiadapter.New(router)
	return f
}

func readingAttributeMap(attrs map[string]dynamodbtypes.AttributeValue) map[string]any {
	out := map[string]any{}
	for k, v := range attrs {
		switch x := v.(type) {
		case *dynamodbtypes.AttributeValueMemberS:
			out[k] = map[string]any{"S": x.Value}
		case *dynamodbtypes.AttributeValueMemberN:
			out[k] = map[string]any{"N": x.Value}
		case *dynamodbtypes.AttributeValueMemberBOOL:
			out[k] = map[string]any{"BOOL": x.Value}
		case *dynamodbtypes.AttributeValueMemberNULL:
			out[k] = map[string]any{"NULL": x.Value}
		case *dynamodbtypes.AttributeValueMemberL:
			list := []any{}
			for _, value := range x.Value {
				list = append(list, readingAttributeMap(map[string]dynamodbtypes.AttributeValue{"v": value})["v"])
			}
			out[k] = map[string]any{"L": list}
		case *dynamodbtypes.AttributeValueMemberM:
			out[k] = map[string]any{"M": readingAttributeMap(x.Value)}
		case *dynamodbtypes.AttributeValueMemberSS:
			out[k] = map[string]any{"SS": x.Value}
		}
	}
	return out
}

func (f *readingFixture) call(t *testing.T, query map[string][]string) events.APIGatewayProxyResponse {
	t.Helper()
	single := map[string]string{}
	for key, values := range query {
		if len(values) > 0 {
			single[key] = values[len(values)-1]
		}
	}
	response, err := f.adapter.ProxyWithContext(context.Background(), events.APIGatewayProxyRequest{
		Path: "/api/meetings/m1/reading", HTTPMethod: "GET",
		Headers: map[string]string{"Authorization": "Bearer " + f.user}, MultiValueQueryStringParameters: query,
		QueryStringParameters: single,
		RequestContext:        events.APIGatewayProxyRequestContext{Path: "/api/meetings/m1/reading"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestReadingAPIHandlesOversizedMeetingWithoutNotesHydration(t *testing.T) {
	f := newReadingFixture(t)
	response := f.call(t, nil)
	if response.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	if len(response.Body) > 14000 {
		t.Fatalf("unbounded API Gateway v1 body: %d bytes", len(response.Body))
	}
	if !strings.HasSuffix(response.Body, "\n") {
		t.Fatal("JSON newline must be included in the budget")
	}
	envelope, _ := json.Marshal(response)
	if len(envelope) > 32000 {
		t.Fatalf("API Gateway envelope too large: %d", len(envelope))
	}
	mcp, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": response.Body}}})
	if len(mcp) > 32000 {
		t.Fatalf("MCP wrapper too large: %d", len(mcp))
	}
	if len(f.s3Keys) != 0 {
		t.Fatalf("notes hydrated transcripts: %v", f.s3Keys)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(response.Body), &result); err != nil {
		t.Fatal(err)
	}
	if result["notes"] != f.meeting.Notes {
		t.Fatalf("lost notes: %+v", result)
	}
	for _, field := range []string{"transcriptA", "transcriptB", "transcription", "speakerMap"} {
		if _, ok := result[field]; ok {
			t.Fatalf("unrequested field: %s", field)
		}
	}
}

func readingResult(t *testing.T, r events.APIGatewayProxyResponse) map[string]any {
	t.Helper()
	if r.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", r.StatusCode, r.Body)
	}
	if len(r.Body) > 14000 {
		t.Fatalf("oversized reading body: %d", len(r.Body))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadingRepositoryViewDoesNotChangeNormalHydration(t *testing.T) {
	f := newReadingFixture(t)
	view := f.repo.MetadataView()
	m, err := view.GetMeeting(context.Background(), "owner", "m1")
	if err != nil || !strings.HasPrefix(m.TranscriptA, "s3://") || len(f.s3Keys) != 0 {
		t.Fatalf("metadata view hydrated: %v %v", err, f.s3Keys)
	}
	m, err = f.repo.GetMeeting(context.Background(), "owner", "m1")
	if err != nil || len(m.TranscriptA) != 4*1024*1024 || len(m.TranscriptB) != 3*1024*1024 || len(f.s3Keys) != 2 {
		t.Fatalf("view changed original reader: %v %v", err, f.s3Keys)
	}
}

func TestReadingAPIHydratesOnlyRequestedFieldAfterAuthorization(t *testing.T) {
	f := newReadingFixture(t)
	readingResult(t, f.call(t, map[string][]string{"kind": {"transcript"}, "pageSize": {"8000"}}))
	if strings.Join(f.s3Keys, ",") != "transcripts/m1/transcriptA.txt" {
		t.Fatalf("unselected hydration: %v", f.s3Keys)
	}
	f.s3Keys = nil
	result := readingResult(t, f.call(t, map[string][]string{"kind": {"transcript"}, "source": {"B"}}))
	if result["source"] != "B" || result["mode"] != "text" || strings.Join(f.s3Keys, ",") != "transcripts/m1/transcriptB.txt" {
		t.Fatalf("wrong explicit source: %v %v", result["source"], f.s3Keys)
	}
}

func TestReadingAPIUnicodeContinuationAndStaleness(t *testing.T) {
	f := newReadingFixture(t)
	text := strings.Repeat("예산😀\n확인합니다. ", 600)
	f.meeting.TranscriptA = text
	query := map[string][]string{"kind": {"transcript"}, "pageSize": {"137"}}
	got := ""
	first := ""
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatal("no pagination progress")
		}
		result := readingResult(t, f.call(t, query))
		page := result["page"].(map[string]any)
		if int(page["startOffset"].(float64)) != utf8.RuneCountInString(got) {
			t.Fatal("page overlap/gap")
		}
		for _, raw := range result["chunks"].([]any) {
			chunk := raw.(map[string]any)
			if _, ok := chunk["segment"]; ok {
				t.Fatal("invented timing")
			}
			part := chunk["text"].(string)
			if !utf8.ValidString(part) || strings.ContainsRune(part, '�') {
				t.Fatal("split Unicode")
			}
			got += part
		}
		if page["nextCursor"] == nil {
			if page["complete"] != true {
				t.Fatal("inconsistent completeness")
			}
			break
		}
		cursor := page["nextCursor"].(string)
		query["cursor"] = []string{cursor}
		if first == "" {
			first = cursor
		}
	}
	if got != text {
		t.Fatal("text omitted or duplicated")
	}
	f.meeting.TranscriptA += "정정"
	query["cursor"] = []string{first}
	if r := f.call(t, query); r.StatusCode != 409 || !strings.Contains(r.Body, "STALE_CURSOR") {
		t.Fatalf("stale cursor accepted: %d %s", r.StatusCode, r.Body)
	}
	if len(f.s3Keys) != 0 {
		t.Fatal("inline source used S3")
	}
}

func TestReadingAPIQueriesFailBeforeStorage(t *testing.T) {
	f := newReadingFixture(t)
	for _, query := range []map[string][]string{
		{"unexpected": {"1"}}, {"kind": {"meeting", "transcript"}}, {"kind": {"wrong"}},
		{"kind": {"transcript"}, "section": {"notes"}}, {"source": {"A"}}, {"pageSize": {"8001"}},
		{"kind": {"transcript"}, "source": {"C"}}, {"section": {"wrong"}},
		{"pageSize": {"0"}}, {"pageSize": {"1.5"}}, {"cursor": {"!"}}, {"cursor": {"e30"}},
		{"kind": {"transcript"}, "startTime": {"0"}},
		{"kind": {"transcript"}, "startTime": {"2"}, "endTime": {"1"}},
		{"kind": {"transcript"}, "startTime": {"NaN"}, "endTime": {"2"}},
		{"kind": {"transcript"}, "startTime": {"0"}, "endTime": {"+Inf"}},
	} {
		before := f.reads
		r := f.call(t, query)
		if r.StatusCode != 400 || f.reads != before || len(f.s3Keys) != 0 {
			t.Fatalf("invalid query reached storage: %v status=%d reads=%d", query, r.StatusCode, f.reads-before)
		}
	}
	f.user = ""
	if r := f.call(t, nil); r.StatusCode != 401 || f.reads != 0 {
		t.Fatalf("unauthenticated request read storage: %d", r.StatusCode)
	}
}

func TestReadingAPIAuthorizationRecheckedOnContinuation(t *testing.T) {
	for _, kind := range []string{"direct", "account"} {
		t.Run(kind, func(t *testing.T) {
			f := newReadingFixture(t)
			f.user = "viewer"
			f.share = &model.Share{OwnerID: "owner", MeetingID: "m1", Permission: "read"}
			if kind == "account" {
				f.share.Origin = model.ShareOriginAccount
				f.meeting.SharedToAccount = true
				f.meeting.AccountID = "acct"
				f.member = &model.AccountMember{AccountID: "acct", UserID: "viewer"}
			}
			result := readingResult(t, f.call(t, map[string][]string{"kind": {"transcript"}, "pageSize": {"10"}}))
			cursor := result["page"].(map[string]any)["nextCursor"].(string)
			f.s3Keys = nil
			if kind == "direct" {
				f.share = nil
			} else {
				f.member = nil
			}
			r := f.call(t, map[string][]string{"kind": {"transcript"}, "cursor": {cursor}})
			if r.StatusCode != 404 || len(f.s3Keys) != 0 {
				t.Fatalf("revoked access survived: %d %v", r.StatusCode, f.s3Keys)
			}
		})
	}
}

func TestReadingAPIStaleGSIAndForeignReferencesFailClosed(t *testing.T) {
	f := newReadingFixture(t)
	f.user = "viewer"
	f.share = &model.Share{OwnerID: "owner", MeetingID: "m1", Permission: "read"}
	f.staleIndex = f.meeting
	f.meeting = nil
	if r := f.call(t, nil); r.StatusCode != 404 || len(f.s3Keys) != 0 {
		t.Fatalf("stale index granted access: %d", r.StatusCode)
	}
	f = newReadingFixture(t)
	f.meeting.TranscriptA = "s3://reading-fixture/transcripts/another/transcriptA.txt"
	r := f.call(t, map[string][]string{"kind": {"transcript"}})
	if r.StatusCode != 500 || len(f.s3Keys) != 0 || strings.Contains(r.Body, "s3://") {
		t.Fatalf("foreign ref used/exposed: %d %s", r.StatusCode, r.Body)
	}
}

func TestReadingAPIAnalysisStateAndFreshItems(t *testing.T) {
	f := newReadingFixture(t)
	result := readingResult(t, f.call(t, nil))
	if result["actionItemsAnalysis"].(map[string]any)["status"] != "unknown" {
		t.Fatal("legacy absence implied success")
	}
	f.state = &model.ActionItemsAnalysis{Status: model.AnalysisFailed, ErrorCode: model.AnalysisProviderFailed, RunID: "run-1"}
	result = readingResult(t, f.call(t, nil))
	if result["actionItemsAnalysis"].(map[string]any)["errorCode"] != "PROVIDER_FAILED" {
		t.Fatal("failure code lost")
	}
	for _, status := range []string{model.AnalysisQueued, model.AnalysisRunning} {
		f.state = &model.ActionItemsAnalysis{Status: status, RunID: "pending", LeaseUntil: time.Now().Add(time.Hour).UnixMilli()}
		result = readingResult(t, f.call(t, nil))
		if result["actionItemsAnalysis"].(map[string]any)["status"] != status {
			t.Fatal("pending state lost")
		}
	}
	f.meeting.Content = "회의 요약"
	sum := sha256.Sum256([]byte(f.meeting.Content))
	f.state = &model.ActionItemsAnalysis{Status: model.AnalysisSucceeded, SourceHash: hex.EncodeToString(sum[:])}
	f.meeting.ActionItems = "[]"
	result = readingResult(t, f.call(t, nil))
	if result["actionItemsAnalysis"].(map[string]any)["status"] != "succeeded" || len(result["actionItems"].([]any)) != 0 {
		t.Fatal("successful empty extraction lost")
	}
	f.afterStatus = func() { f.meeting.ActionItems = `[{"id":"ai-1","text":"준비😀","completed":true}]` }
	result = readingResult(t, f.call(t, nil))
	items := result["actionItems"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["completed"] != true {
		t.Fatal("succeeded paired with older items")
	}
	f.meeting.Content = "summary changed"
	result = readingResult(t, f.call(t, nil))
	if result["actionItemsAnalysis"].(map[string]any)["errorCode"] != "SOURCE_CHANGED" {
		t.Fatal("stale success not identified")
	}
	if len(f.s3Keys) != 0 {
		t.Fatal("analysis description hydrated transcripts")
	}
	f.failStatus = true
	result = readingResult(t, f.call(t, nil))
	analysis := result["actionItemsAnalysis"].(map[string]any)
	if analysis["status"] != "unknown" || analysis["errorCode"] != "STATUS_UNAVAILABLE" {
		t.Fatal("status failure hidden")
	}
}

func TestReadingAPITimeWindowsAndSegmentLimit(t *testing.T) {
	f := newReadingFixture(t)
	segments := []map[string]any{}
	var text strings.Builder
	for i := 0; i < 120; i++ {
		line := fmt.Sprintf("항목%d 검토😀.", i)
		text.WriteString(line + "\n")
		segments = append(segments, map[string]any{"id": fmt.Sprint(i), "speaker": "김", "text": line, "startTime": i * 2, "endTime": i*2 + 1})
	}
	raw, _ := json.Marshal(segments)
	f.meeting.TranscriptA = text.String()
	f.meeting.TranscriptSegments = string(raw)
	result := readingResult(t, f.call(t, map[string][]string{"kind": {"transcript"}, "pageSize": {"8000"}}))
	if len(result["chunks"].([]any)) != 50 || result["page"].(map[string]any)["complete"] != false {
		t.Fatal("segment cap/completeness wrong")
	}
	query := map[string][]string{"kind": {"transcript"}, "startTime": {"0"}, "endTime": {"4"}, "pageSize": {"3"}}
	got := ""
	for i := 0; i < 100; i++ {
		result = readingResult(t, f.call(t, query))
		if result["completenessScope"] != "requested_time_range" {
			t.Fatal("range claimed whole-source completeness")
		}
		for _, v := range result["chunks"].([]any) {
			chunk := v.(map[string]any)
			segment := chunk["segment"].(map[string]any)
			if segment["startTime"].(float64) >= 4 || segment["timingScope"] != "whole_segment" {
				t.Fatal("invented/incorrect time range")
			}
			got += chunk["text"].(string)
		}
		page := result["page"].(map[string]any)
		if page["nextCursor"] == nil {
			break
		}
		query["cursor"] = []string{page["nextCursor"].(string)}
		if i == 99 {
			t.Fatal("cursor did not progress")
		}
	}
	if got != "항목0 검토😀.\n항목1 검토😀." {
		t.Fatalf("time window omitted/duplicated: %q", got)
	}
	f.meeting.TranscriptA = "unmatched current source"
	delete(query, "cursor")
	if r := f.call(t, query); r.StatusCode != 400 || !strings.Contains(r.Body, "TIME_RANGE_UNAVAILABLE") {
		t.Fatal("stale times exposed")
	}
	result = readingResult(t, f.call(t, map[string][]string{"kind": {"transcript"}}))
	if result["mode"] != "text" {
		t.Fatal("mismatched segments did not degrade to honest text")
	}
}

func TestReadingAPIHugeSegmentsMetadataAndActionJSON(t *testing.T) {
	f := newReadingFixture(t)
	text := strings.Repeat("가😀\x00\"\\", 1800)
	f.objects["transcripts/m1/transcriptA.txt"] = text
	raw, _ := json.Marshal([]map[string]any{{"id": strings.Repeat("id", 1000), "speaker": strings.Repeat("김", 1000), "text": text, "startTime": 0, "endTime": 60}})
	f.meeting.TranscriptSegments = "s3://reading-fixture/transcripts/m1/transcriptSegments.txt"
	f.objects["transcripts/m1/transcriptSegments.txt"] = string(raw)
	query := map[string][]string{"kind": {"transcript"}, "pageSize": {"8000"}}
	got := ""
	for i := 0; i < 100; i++ {
		result := readingResult(t, f.call(t, query))
		chunks := result["chunks"].([]any)
		if len(chunks) != 1 {
			t.Fatal("huge segment disappeared")
		}
		chunk := chunks[0].(map[string]any)
		if int(chunk["startOffset"].(float64)) != utf8.RuneCountInString(got) {
			t.Fatal("chunk gap/overlap")
		}
		segment := chunk["segment"].(map[string]any)
		if segment["startTime"].(float64) != 0 || segment["endTime"].(float64) != 60 {
			t.Fatal("invented subsegment times")
		}
		got += chunk["text"].(string)
		page := result["page"].(map[string]any)
		if page["nextCursor"] == nil {
			break
		}
		query["cursor"] = []string{page["nextCursor"].(string)}
		if i == 99 {
			t.Fatal("cursor did not progress")
		}
	}
	if got != text {
		t.Fatal("huge segment lost characters")
	}
	items := []service.ActionItem{{ID: "stable", Text: strings.Repeat("작업😀\x00\"\\", 2000), Completed: true}}
	raw, _ = json.Marshal(items)
	f.meeting.ActionItems = string(raw)
	f.meeting.Title = strings.Repeat("\x00\"\\😀", 3000)
	f.meeting.Participants = make([]string, 12)
	for i := range f.meeting.Participants {
		f.meeting.Participants[i] = f.meeting.Title
	}
	result := readingResult(t, f.call(t, nil))
	if result["actionItemsPreview"].(map[string]any)["complete"] != false {
		t.Fatal("truncated preview claimed completeness")
	}
	query = map[string][]string{"section": {"actionItems"}, "pageSize": {"799"}}
	serialized := ""
	for i := 0; i < 200; i++ {
		result = readingResult(t, f.call(t, query))
		page := result["page"].(map[string]any)
		if int(page["startOffset"].(float64)) != utf8.RuneCountInString(serialized) {
			t.Fatal("action JSON gap/overlap")
		}
		serialized += result["actionItemsJson"].(string)
		if page["nextCursor"] == nil {
			break
		}
		query["cursor"] = []string{page["nextCursor"].(string)}
		if i == 199 {
			t.Fatal("action JSON cursor did not progress")
		}
	}
	var restored []service.ActionItem
	if err := json.Unmarshal([]byte(serialized), &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0] != items[0] {
		t.Fatal("full action JSON lost data")
	}
}

func TestReadingAPINotesIgnoreBrokenTranscriptStorage(t *testing.T) {
	f := newReadingFixture(t)
	f.failS3 = true
	readingResult(t, f.call(t, nil))
	if len(f.s3Keys) != 0 {
		t.Fatal("notes touched broken S3")
	}
	r := f.call(t, map[string][]string{"kind": {"transcript"}})
	if r.StatusCode != 500 || strings.Contains(r.Body, "s3://") {
		t.Fatal("storage failure was hidden or leaked a reference")
	}
}

func TestReadingAPIPreservesActionJSONExtensions(t *testing.T) {
	f := newReadingFixture(t)
	items := []map[string]interface{}{
		{"id": "x", "text": "task", "completed": false,
			"evidence":  map[string]interface{}{"quote": strings.Repeat("근거😀", 7000), "source": map[string]interface{}{"page": float64(3), "labels": []interface{}{"원문", "확인"}}},
			"extension": strings.Repeat("large extension ", 3000)},
		{"text": "legacy", "done": true, "evidence": map[string]interface{}{"quote": "남길 근거"}},
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	f.meeting.ActionItems = string(raw)
	result := readingResult(t, f.call(t, nil))
	preview := result["actionItemsPreview"].(map[string]any)
	if preview["complete"] != false {
		t.Fatal("preview claims completeness while omitting evidence/extensions")
	}
	flags := preview["metadataTruncated"].([]any)
	found := false
	for _, flag := range flags {
		if flag == "actionItems[0].otherFields" {
			found = true
		}
	}
	if !found {
		t.Fatalf("extension omission not disclosed: %v", flags)
	}
	query := map[string][]string{"section": {"actionItems"}, "pageSize": {"8000"}}
	serialized := ""
	for n := 0; n < 200; n++ {
		result = readingResult(t, f.call(t, query))
		page := result["page"].(map[string]any)
		if int(page["startOffset"].(float64)) != utf8.RuneCountInString(serialized) {
			t.Fatal("full JSON gap/overlap")
		}
		serialized += result["actionItemsJson"].(string)
		if page["nextCursor"] == nil {
			break
		}
		query["cursor"] = []string{page["nextCursor"].(string)}
		if n == 199 {
			t.Fatal("JSON cursor did not finish")
		}
	}
	var restored []map[string]interface{}
	if err := json.Unmarshal([]byte(serialized), &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 || !reflect.DeepEqual(restored[0], items[0]) {
		t.Fatal("full JSON dropped or altered extension data")
	}
	if restored[1]["completed"] != true || restored[1]["done"] != true ||
		!strings.HasPrefix(restored[1]["id"].(string), "legacy-") ||
		!reflect.DeepEqual(restored[1]["evidence"], items[1]["evidence"]) {
		t.Fatal("legacy normalization lost fields")
	}
	first := readingResult(t, f.call(t, map[string][]string{"section": {"actionItems"}, "pageSize": {"20"}}))
	items[0]["evidence"].(map[string]interface{})["quote"] = "changed evidence only"
	raw, _ = json.Marshal(items)
	f.meeting.ActionItems = string(raw)
	r := f.call(t, map[string][]string{"section": {"actionItems"}, "cursor": {first["page"].(map[string]any)["nextCursor"].(string)}})
	if r.StatusCode != 409 || !strings.Contains(r.Body, "STALE_CURSOR") {
		t.Fatal("extension-only update did not invalidate the cursor")
	}
	if len(f.s3Keys) != 0 {
		t.Fatal("action JSON reading hydrated transcripts")
	}
}
