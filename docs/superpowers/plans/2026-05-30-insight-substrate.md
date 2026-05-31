# Insight Substrate — Implementation Plan (Plan 3 of 6)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans. Steps use `- [ ]` checkboxes.

**Goal:** 미팅에서 8유형 인사이트를 Bedrock으로 추출해 `Meeting.Insights`(JSON)에 저장하고, 미팅이 Account에 공유될 때(또는 이미 공유된 미팅이 요약 완료될 때) 그 인사이트를 Account 파티션에 `INSIGHT#{occurredAt}#...` 아이템으로 팬아웃한다. Account+기간+유형으로 조회한다.

**Architecture:** `ExtractInsights`(BedrockService, Haiku)는 `ExtractActionItems`를 미러링해 JSON 문자열을 반환 → summarize 파이프라인이 `Meeting.Insights`에 저장. 순수 함수 `service.BuildAccountInsights`가 `Meeting.Insights`를 파싱해 `AccountInsight` 아이템(PK `ACCOUNT#{id}`, SK `INSIGHT#{occurredAt}#{meetingId}#{idx}`)을 만들고, `ShareMeetingToAccount`와 summarize 양쪽이 이를 써서 팬아웃한다. 조회는 `begins_with(INSIGHT#)` 쿼리 후 서비스에서 기간·유형 필터(스펙 §6.3: 1차는 client-side).

**Tech Stack:** Go 1.25, stdlib testing, CDK 변경 없음(Haiku는 summarize Bedrock 정책에 이미 포함, 신규 GSI 불필요).

**선행:** Plan 1·2 완료, branch `feat/account-foundation`.

## ⚠️ 이름 충돌 주의
`internal/service/insights.go`의 `InsightsService` + `model.InsightsResponse`/`InsightDetailResponse`/`CrawledDocument`는 **크롤러 뉴스 도메인**이라 건드리지 말 것. Plan 3는 새 타입 `MeetingInsight`/`AccountInsight`/`AccountInsightDTO`와 `AccountService.ListAccountInsights`(다른 리시버라 충돌 없음)를 쓴다.

## File Structure (Plan 3)
| 파일 | 변경 |
|---|---|
| `model/meeting.go` | `Insights string` 필드 |
| `model/account.go` | 8유형 상수 + `IsValidInsightType` + `PrefixInsight`/`EntityTypeInsight` + `MeetingInsight`/`AccountInsight`/`AccountInsightDTO` |
| `service/bedrock.go` | `parseMeetingInsights` + `ExtractInsights` |
| `service/bedrock_test.go` | `parseMeetingInsights` 테스트 (신규 파일) |
| `service/meeting.go` | `meetingRepo`에 `PutAccountInsights` 추가 + `BuildAccountInsights` + `ShareMeetingToAccount` 팬아웃 |
| `service/meeting_test.go` | mock 동기화 + `BuildAccountInsights`/팬아웃 테스트 |
| `service/account.go` | `accountRepo`에 `ListInsightsForAccount` 추가 + `ListAccountInsights` |
| `service/account_test.go` | mock 동기화 + `ListAccountInsights` 테스트 |
| `repository/account.go` | `PutAccountInsights` + `ListInsightsForAccount` |
| `handler/account.go` | `ListAccountInsights` 핸들러 |
| `handler/*_test.go` | mock 동기화 |
| `cmd/summarize/main.go` | `ExtractInsights` 호출 + 저장 + 조건부 팬아웃 |
| `cmd/api/main.go` | insights 라우트 |
| `docs/API-SPEC.md` | 엔드포인트 |

---

## Task 1: 모델 — 인사이트 타입·구조체

**Files:**
- Modify: `backend/internal/model/meeting.go`, `backend/internal/model/account.go`

- [ ] **Step 1: `Meeting`에 `Insights` 필드 추가** (`meeting.go`, `Sentiment` 줄 다음)

```go
	Insights           string            `dynamodbav:"insights,omitempty"` // JSON []MeetingInsight
```

- [ ] **Step 2: `account.go` 끝에 인사이트 타입·모델 추가**

```go
// Insight types (Plan 3). Feeds SIFT / 2by2 / Player Card raw material.
const (
	InsightTrend       = "trend"
	InsightNeed        = "need"
	InsightCompetitive = "competitive"
	InsightRisk        = "risk"
	InsightOpportunity = "opportunity"
	InsightTech        = "tech"
	InsightStakeholder = "stakeholder"
	InsightAction      = "action"
)

const (
	PrefixInsight     = "INSIGHT#"
	EntityTypeInsight = "ACCOUNT_INSIGHT"
)

// IsValidInsightType reports whether t is one of the 8 recognized insight types.
func IsValidInsightType(t string) bool {
	switch t {
	case InsightTrend, InsightNeed, InsightCompetitive, InsightRisk,
		InsightOpportunity, InsightTech, InsightStakeholder, InsightAction:
		return true
	default:
		return false
	}
}

// MeetingInsight is one typed insight extracted from a meeting (stored as JSON in Meeting.Insights).
type MeetingInsight struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Text     string   `json:"text"`
	TsMarker string   `json:"tsMarker,omitempty"` // [TS:NNN] transcript deep link
	Entities []string `json:"entities,omitempty"`
}

// AccountInsight is a persisted insight item in the account partition.
// PK: ACCOUNT#{accountId}, SK: INSIGHT#{occurredAt}#{meetingId}#{index}
type AccountInsight struct {
	PK           string    `dynamodbav:"PK"`
	SK           string    `dynamodbav:"SK"`
	AccountID    string    `dynamodbav:"accountId"`
	InsightID    string    `dynamodbav:"insightId"`
	Type         string    `dynamodbav:"type"`
	Text         string    `dynamodbav:"text"`
	SourceType   string    `dynamodbav:"sourceType"` // "meeting" | "news" | "ingest"
	SourceID     string    `dynamodbav:"sourceId"`
	SourceUserID string    `dynamodbav:"sourceUserId,omitempty"`
	OccurredAt   time.Time `dynamodbav:"occurredAt"`
	TsMarker     string    `dynamodbav:"tsMarker,omitempty"`
	Entities     []string  `dynamodbav:"entities,omitempty"`
	CreatedAt    time.Time `dynamodbav:"createdAt"`
	EntityType   string    `dynamodbav:"entityType"` // "ACCOUNT_INSIGHT"
}

type AccountInsightDTO struct {
	Type       string    `json:"type"`
	Text       string    `json:"text"`
	SourceType string    `json:"sourceType"`
	SourceID   string    `json:"sourceId"`
	OccurredAt time.Time `json:"occurredAt"`
	TsMarker   string    `json:"tsMarker,omitempty"`
	Entities   []string  `json:"entities,omitempty"`
}
```

- [ ] **Step 3: 빌드 확인**

Run: `cd backend && /usr/local/go/bin/go build ./internal/model/`
Expected: 성공.

- [ ] **Step 4: Commit**

```bash
cd backend && git add internal/model/meeting.go internal/model/account.go
git commit -m "feat(insight): insight types + MeetingInsight/AccountInsight models"
```

---

## Task 2: parseMeetingInsights + ExtractInsights (Bedrock)

`ExtractInsights`는 Bedrock 호출이라 단위테스트 불가(기존 추출기들과 동일). 순수 파서 `parseMeetingInsights`를 분리해 그것만 테스트한다.

**Files:**
- Modify: `backend/internal/service/bedrock.go`
- Create: `backend/internal/service/bedrock_test.go`

- [ ] **Step 1: 실패 테스트 작성** (`bedrock_test.go`)

```go
package service

import (
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestParseMeetingInsights_KeepsValidDropsInvalid(t *testing.T) {
	raw := "```json\n" + `[
	  {"type":"risk","text":"PoC 일정 지연 가능"},
	  {"type":"opportunity","text":"ROSA 확대 여지","entities":["ROSA"]},
	  {"type":"bogus","text":"버려야 함"},
	  {"type":"need","text":"   "}
	]` + "\n```"
	got, err := parseMeetingInsights(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 valid insights, got %d (%+v)", len(got), got)
	}
	if got[0].Type != model.InsightRisk || got[1].Type != model.InsightOpportunity {
		t.Errorf("unexpected types: %+v", got)
	}
	if got[0].ID == "" || got[1].ID == "" {
		t.Error("expected IDs assigned")
	}
}

func TestParseMeetingInsights_BadJSON(t *testing.T) {
	_, err := parseMeetingInsights("not json")
	if err == nil {
		t.Error("expected error on bad JSON")
	}
}

func TestParseMeetingInsights_Empty(t *testing.T) {
	got, err := parseMeetingInsights("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestParseMeetingInsights ./internal/service/`
Expected: FAIL — `parseMeetingInsights undefined`.

- [ ] **Step 3: `parseMeetingInsights` + `ExtractInsights` 구현** (`bedrock.go`)

```go
// parseMeetingInsights strips code fences, unmarshals, drops invalid-type or
// empty-text entries, and assigns stable IDs. Pure (unit-testable).
func parseMeetingInsights(raw string) ([]model.MeetingInsight, error) {
	raw = stripCodeFences(raw)
	var items []model.MeetingInsight
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}
	out := make([]model.MeetingInsight, 0, len(items))
	for _, it := range items {
		if !model.IsValidInsightType(it.Type) || strings.TrimSpace(it.Text) == "" {
			continue
		}
		it.ID = fmt.Sprintf("ins_%d", len(out)+1)
		out = append(out, it)
	}
	return out, nil
}

// ExtractInsights classifies a meeting into the 8 typed insights using Claude Haiku.
// Mirrors ExtractActionItems. Returns a JSON array string ("[]" on parse failure).
func (s *BedrockService) ExtractInsights(ctx context.Context, meetingID string, userID ...string) (string, error) {
	var meeting *model.Meeting
	var err error
	if len(userID) > 0 && userID[0] != "" {
		meeting, err = s.repo.GetMeeting(ctx, userID[0], meetingID)
	} else {
		meeting, err = s.repo.GetMeetingByID(ctx, meetingID)
	}
	if err != nil {
		return "", fmt.Errorf("failed to get meeting: %w", err)
	}
	if meeting == nil {
		return "", fmt.Errorf("meeting not found: %s", meetingID)
	}

	source := meeting.Content
	if source == "" {
		source = meeting.TranscriptA
		if meeting.SelectedTranscript == "B" && meeting.TranscriptB != "" {
			source = meeting.TranscriptB
		} else if source == "" && meeting.TranscriptB != "" {
			source = meeting.TranscriptB
		}
	}
	if source == "" {
		return "[]", nil
	}

	systemPrompt := `회의 요약/트랜스크립트에서 영업·고객 인사이트를 추출해 분류하세요.
각 인사이트: { "type": <유형>, "text": <한국어 설명>, "entities": [관련 고유명사들] }
유형(type)은 반드시 다음 8가지 중 하나:
- trend: 고객/시장 트렌드 (예: 그룹사 클라우드 전환 가속)
- need: 고객 니즈/요구사항 (예: DR 금융보안 컴플라이언스)
- competitive: 경쟁 정보 (예: 타사 견적 진행)
- risk: 리스크 (예: PoC 일정 지연 가능성)
- opportunity: 기회 (예: 워크로드 확대 여지)
- tech: 기술 주제/워크로드 (예: EKS, PrivateLink)
- stakeholder: 이해관계자 변화 (예: 신임 CTO 부임)
- action: 우리측 다음 액션 (예: 다음주 아키텍처 리뷰)
해당 유형이 명확한 항목만 추출하세요. 유효한 JSON 배열만 반환하고, 없으면 []를 반환하세요.
예시: [{"type":"risk","text":"PoC 일정 2개월 지연 가능","entities":["PoC"]}]`

	userPrompt := fmt.Sprintf("다음 회의 내용에서 인사이트를 추출하세요:\n\n%s", source)

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        1536,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: userPrompt}}},
		},
	}

	response, err := s.invokeClaudeModelWithID(ctx, request, ClaudeHaikuModelID)
	if err != nil {
		return "", fmt.Errorf("failed to extract insights: %w", err)
	}

	items, err := parseMeetingInsights(response)
	if err != nil {
		return "[]", nil
	}
	result, err := json.Marshal(items)
	if err != nil {
		return "[]", nil
	}
	return string(result), nil
}
```

> `bedrock.go`는 이미 `encoding/json`, `fmt`, `strings`를 import한다(확인). 없으면 추가.

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestParseMeetingInsights ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/bedrock.go internal/service/bedrock_test.go
git commit -m "feat(insight): ExtractInsights (Haiku) + pure parseMeetingInsights"
```

---

## Task 3: BuildAccountInsights (순수 빌더)

`Meeting.Insights`(JSON) → `[]AccountInsight` 아이템. `ShareMeetingToAccount`와 summarize 양쪽이 공유.

**Files:**
- Modify: `backend/internal/service/meeting.go`, `meeting_test.go`

- [ ] **Step 1: 실패 테스트 작성** (`meeting_test.go`)

```go
func TestBuildAccountInsights(t *testing.T) {
	when := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	meeting := &model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Date: when,
		Insights: `[{"id":"ins_1","type":"risk","text":"일정 지연","entities":["PoC"]},{"id":"ins_2","type":"opportunity","text":"확대 여지"}]`,
	}
	items, err := BuildAccountInsights("acc-1", meeting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2, got %d", len(items))
	}
	got := items[0]
	if got.PK != model.PrefixAccount+"acc-1" {
		t.Errorf("bad PK: %s", got.PK)
	}
	wantSK := model.PrefixInsight + when.Format(time.RFC3339) + "#m-1#0"
	if got.SK != wantSK {
		t.Errorf("bad SK: got %s want %s", got.SK, wantSK)
	}
	if got.Type != "risk" || got.SourceType != "meeting" || got.SourceID != "m-1" || got.SourceUserID != "owner-1" {
		t.Errorf("bad fields: %+v", got)
	}
	if !got.OccurredAt.Equal(when) {
		t.Errorf("bad occurredAt: %v", got.OccurredAt)
	}
}

func TestBuildAccountInsights_Empty(t *testing.T) {
	items, err := BuildAccountInsights("acc-1", &model.Meeting{MeetingID: "m-1"})
	if err != nil || items != nil {
		t.Errorf("expected nil,nil for no insights; got %v, %v", items, err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestBuildAccountInsights ./internal/service/`
Expected: FAIL — `BuildAccountInsights undefined`.

- [ ] **Step 3: 구현** (`meeting.go`)

```go
// BuildAccountInsights parses a meeting's stored insights JSON and builds
// account-partition AccountInsight items. SK = INSIGHT#{occurredAt}#{meetingId}#{index}
// is deterministic per (meeting,index), so re-running overwrites the same items
// (idempotent). Returns nil if the meeting has no insights yet.
func BuildAccountInsights(accountID string, meeting *model.Meeting) ([]model.AccountInsight, error) {
	if meeting == nil || strings.TrimSpace(meeting.Insights) == "" {
		return nil, nil
	}
	var parsed []model.MeetingInsight
	if err := json.Unmarshal([]byte(meeting.Insights), &parsed); err != nil {
		return nil, err
	}
	occurred := meeting.Date.UTC().Format(time.RFC3339)
	now := time.Now().UTC()
	out := make([]model.AccountInsight, 0, len(parsed))
	for i, p := range parsed {
		out = append(out, model.AccountInsight{
			PK:           model.PrefixAccount + accountID,
			SK:           fmt.Sprintf("%s%s#%s#%d", model.PrefixInsight, occurred, meeting.MeetingID, i),
			AccountID:    accountID,
			InsightID:    fmt.Sprintf("%s_%d", meeting.MeetingID, i),
			Type:         p.Type,
			Text:         p.Text,
			SourceType:   "meeting",
			SourceID:     meeting.MeetingID,
			SourceUserID: meeting.UserID,
			OccurredAt:   meeting.Date,
			TsMarker:     p.TsMarker,
			Entities:     p.Entities,
			CreatedAt:    now,
			EntityType:   model.EntityTypeInsight,
		})
	}
	return out, nil
}
```

> `meeting.go`는 이미 `encoding/json`(toRawJSON), `fmt`, `strings`, `time`을 import한다(확인). 없으면 추가.

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestBuildAccountInsights ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/meeting.go internal/service/meeting_test.go
git commit -m "feat(insight): BuildAccountInsights (meeting insights -> account items)"
```

---

## Task 4: 인터페이스 + mock 확장

`meetingRepo` += `PutAccountInsights`, `accountRepo` += `ListInsightsForAccount`. 4개 mock 동기화.

**Files:**
- Modify: `service/meeting.go`, `meeting_test.go`, `service/account.go`, `account_test.go`, `handler/meeting_test.go`, `handler/account_test.go`

- [ ] **Step 1: 인터페이스 추가**

`service/meeting.go` `meetingRepo` 끝에:

```go
	PutAccountInsights(ctx context.Context, insights []model.AccountInsight) error
```

`service/account.go` `accountRepo` 끝에:

```go
	ListInsightsForAccount(ctx context.Context, accountID string) ([]model.AccountInsight, error)
```

- [ ] **Step 2: `mockMeetingRepo`(service/meeting_test.go) 동기화**

struct에 추가: `accountInsights []model.AccountInsight`
생성자에 초기화 불필요(슬라이스 nil append 가능)이지만 명시하려면 생략 가능.
메서드 추가:

```go
func (m *mockMeetingRepo) PutAccountInsights(_ context.Context, insights []model.AccountInsight) error {
	m.accountInsights = append(m.accountInsights, insights...)
	return nil
}
```

- [ ] **Step 3: `mockHandlerMeetingRepo`(handler/meeting_test.go) 동기화**

struct에 `accountInsights []model.AccountInsight` 추가, 동일 `PutAccountInsights` 메서드를 `(m *mockHandlerMeetingRepo)` 리시버로 추가.

- [ ] **Step 4: `mockAccountRepo`(service/account_test.go) 동기화**

struct에 추가: `insightsByAccount map[string][]model.AccountInsight`
생성자에 추가: `insightsByAccount: make(map[string][]model.AccountInsight),`
메서드:

```go
func (m *mockAccountRepo) ListInsightsForAccount(_ context.Context, accountID string) ([]model.AccountInsight, error) {
	return append([]model.AccountInsight(nil), m.insightsByAccount[accountID]...), nil
}
```

- [ ] **Step 5: `mockHandlerAccountRepo`(handler/account_test.go) 동기화**

struct에 `insightsByAccount map[string][]model.AccountInsight` 추가, 생성자 init 추가, 동일 `ListInsightsForAccount`를 `(m *mockHandlerAccountRepo)` 리시버로 추가.

- [ ] **Step 6: 빌드 + 회귀 테스트**

Run: `cd backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...`
Expected: 성공. (Task 5 repo 메서드가 없어 `*DynamoDBRepository`가 인터페이스를 못 채워 빌드 실패하면 — Task 5를 먼저 작성하라. 컴파일 의존성, 최종 상태 동일.)

- [ ] **Step 7: Commit**

```bash
cd backend && git add internal/service/meeting.go internal/service/account.go internal/service/meeting_test.go internal/service/account_test.go internal/handler/meeting_test.go internal/handler/account_test.go
git commit -m "feat(insight): extend repo interfaces + mocks for account insights"
```

---

## Task 5: Repository — PutAccountInsights + ListInsightsForAccount

**Files:**
- Modify: `backend/internal/repository/account.go`

- [ ] **Step 1: 메서드 추가**

```go
// PutAccountInsights writes account insight items (caller builds PK/SK). The
// loop of PutItems is idempotent per item key (deterministic SK), so re-fanning
// a meeting's insights overwrites in place.
func (r *DynamoDBRepository) PutAccountInsights(ctx context.Context, insights []model.AccountInsight) error {
	for i := range insights {
		item, err := attributevalue.MarshalMap(&insights[i])
		if err != nil {
			return fmt.Errorf("marshal account insight: %w", err)
		}
		if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(r.tableName),
			Item:      item,
		}); err != nil {
			return fmt.Errorf("put account insight: %w", err)
		}
	}
	return nil
}

// ListInsightsForAccount returns all INSIGHT# items for an account (newest first).
// Period/type filtering is done by the service layer (spec §6.3: client-side for v1).
func (r *DynamoDBRepository) ListInsightsForAccount(ctx context.Context, accountID string) ([]model.AccountInsight, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixInsight))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build insights query: %w", err)
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query insights: %w", err)
	}
	insights := []model.AccountInsight{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &insights); err != nil {
		return nil, fmt.Errorf("unmarshal insights: %w", err)
	}
	return insights, nil
}
```

- [ ] **Step 2: 빌드 + 테스트**

Run: `cd backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...`
Expected: 성공.

- [ ] **Step 3: Commit**

```bash
cd backend && git add internal/repository/account.go
git commit -m "feat(insight): repo PutAccountInsights + ListInsightsForAccount"
```

---

## Task 6: ShareMeetingToAccount 인사이트 팬아웃

**Files:**
- Modify: `backend/internal/service/meeting.go`, `meeting_test.go`

- [ ] **Step 1: 실패 테스트 작성** (`meeting_test.go`)

```go
func TestShareMeetingToAccount_FansOutInsights(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Title: "ROSA", Status: model.StatusDone,
		Date:     time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC),
		Insights: `[{"type":"risk","text":"지연"},{"type":"opportunity","text":"확대"}]`,
	})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)

	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.accountInsights) != 2 {
		t.Fatalf("expected 2 fanned-out insights, got %d", len(repo.accountInsights))
	}
	if repo.accountInsights[0].AccountID != "acc-1" || repo.accountInsights[0].SourceID != "m-1" {
		t.Errorf("unexpected fanned insight: %+v", repo.accountInsights[0])
	}
}

func TestShareMeetingToAccount_NoInsightsNoFanout(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	if _, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.accountInsights) != 0 {
		t.Errorf("expected no fanout for meeting without insights, got %d", len(repo.accountInsights))
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestShareMeetingToAccount_FansOut ./internal/service/`
Expected: FAIL — `accountInsights`에 아무것도 안 들어옴(팬아웃 미구현).

- [ ] **Step 3: 구현** — `ShareMeetingToAccount`의 `PutMeetingRef` 직후(멤버 share 루프 앞)에 추가:

```go
	if items, berr := BuildAccountInsights(accountID, meeting); berr == nil && len(items) > 0 {
		if err := s.repo.PutAccountInsights(ctx, items); err != nil {
			return nil, err
		}
	}
```

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestShareMeetingToAccount ./internal/service/`
Expected: PASS (기존 Plan 2 공유 테스트 포함 전부).

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/meeting.go internal/service/meeting_test.go
git commit -m "feat(insight): fan out meeting insights to account on share"
```

---

## Task 7: AccountService.ListAccountInsights

**Files:**
- Modify: `backend/internal/service/account.go`, `account_test.go`

- [ ] **Step 1: 실패 테스트 작성** (`account_test.go`)

```go
func TestListAccountInsights_FilterByType(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	repo.insightsByAccount[acc.AccountID] = []model.AccountInsight{
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "지연", OccurredAt: d, SourceID: "m-1", SourceType: "meeting"},
		{AccountID: acc.AccountID, Type: model.InsightTech, Text: "EKS", OccurredAt: d, SourceID: "m-1", SourceType: "meeting"},
	}
	got, err := svc.ListAccountInsights(context.Background(), "owner-1", acc.AccountID, time.Time{}, time.Time{}, []string{model.InsightRisk})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != model.InsightRisk {
		t.Errorf("expected only risk, got %+v", got)
	}
}

func TestListAccountInsights_FilterByPeriod(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.insightsByAccount[acc.AccountID] = []model.AccountInsight{
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "4월", OccurredAt: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "5월", OccurredAt: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)},
	}
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 31, 23, 59, 59, 0, time.UTC)
	got, err := svc.ListAccountInsights(context.Background(), "owner-1", acc.AccountID, from, to, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Text != "5월" {
		t.Errorf("expected only May insight, got %+v", got)
	}
}

func TestListAccountInsights_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.ListAccountInsights(context.Background(), "stranger-9", acc.AccountID, time.Time{}, time.Time{}, nil)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}
```

> `account_test.go`에 `"time"` import가 없으면 추가.

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestListAccountInsights ./internal/service/`
Expected: FAIL — `svc.ListAccountInsights undefined`.

- [ ] **Step 3: 구현** (`account.go`)

```go
// ListAccountInsights returns insight raw material for an account, filtered by
// optional period [from,to] and optional types. Member-gated. (spec §6.3: filter
// client-side for v1.)
func (s *AccountService) ListAccountInsights(ctx context.Context, userID, accountID string, from, to time.Time, types []string) ([]model.AccountInsightDTO, error) {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		account, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	insights, err := s.repo.ListInsightsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	typeSet := make(map[string]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}
	out := make([]model.AccountInsightDTO, 0, len(insights))
	for _, ins := range insights {
		if !from.IsZero() && ins.OccurredAt.Before(from) {
			continue
		}
		if !to.IsZero() && ins.OccurredAt.After(to) {
			continue
		}
		if len(typeSet) > 0 && !typeSet[ins.Type] {
			continue
		}
		out = append(out, model.AccountInsightDTO{
			Type:       ins.Type,
			Text:       ins.Text,
			SourceType: ins.SourceType,
			SourceID:   ins.SourceID,
			OccurredAt: ins.OccurredAt,
			TsMarker:   ins.TsMarker,
			Entities:   ins.Entities,
		})
	}
	return out, nil
}
```

> `account.go`에 `"time"` import가 없으면 추가.

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestListAccountInsights ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(insight): ListAccountInsights (member-gated, period+type filter)"
```

---

## Task 8: summarize 파이프라인 연결

**Files:**
- Modify: `backend/cmd/summarize/main.go`

- [ ] **Step 1: `generateSummary`의 sentiment 저장 블록 직후(KB Export 앞)에 추가**

```go
	insights, err := bedrockService.ExtractInsights(ctx, meetingID, userID)
	if err != nil {
		log.Printf("Failed to extract insights (non-fatal): %v", err)
	} else if insights != "" && insights != "[]" {
		if err := repo.UpdateMeetingFields(ctx, userID, meetingID, map[string]interface{}{
			"insights": insights,
		}); err != nil {
			log.Printf("Failed to save insights: %v", err)
		} else {
			meeting.Insights = insights
			// If already shared to an account, fan out now (covers share-before-summarize ordering).
			if meeting.SharedToAccount && meeting.AccountID != "" {
				if items, berr := service.BuildAccountInsights(meeting.AccountID, meeting); berr == nil && len(items) > 0 {
					if perr := repo.PutAccountInsights(ctx, items); perr != nil {
						log.Printf("Failed to fan out insights to account (non-fatal): %v", perr)
					}
				}
			}
		}
	}
```

> `cmd/summarize/main.go`는 이미 `service`·`log` 패키지를 import한다(GenerateMeetingDocument 호출). `repo`/`bedrockService`/`meeting`은 `generateSummary` 스코프에 존재.

- [ ] **Step 2: 빌드**

Run: `cd backend && /usr/local/go/bin/go build ./cmd/summarize/`
Expected: 성공.

- [ ] **Step 3: Commit**

```bash
cd backend && git add cmd/summarize/main.go
git commit -m "feat(insight): extract insights in summarize pipeline + conditional fanout"
```

---

## Task 9: 핸들러 + 라우트 + API-SPEC + 최종 빌드

**Files:**
- Modify: `backend/internal/handler/account.go`, `account_test.go`
- Modify: `backend/cmd/api/main.go`, `docs/API-SPEC.md`

- [ ] **Step 1: 핸들러 작성** (`handler/account.go` 끝)

```go
func (h *AccountHandler) ListAccountInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var from, to time.Time
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'from' (RFC3339)")
			return
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'to' (RFC3339)")
			return
		}
		to = t
	}
	var types []string
	if v := r.URL.Query().Get("types"); v != "" {
		types = strings.Split(v, ",")
	}
	list, err := h.accountService.ListAccountInsights(ctx, userID, accountID, from, to, types)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"insights": list})
}
```

> `handler/account.go`에 `"time"`, `"strings"` import 추가 필요. `errors`/`service`/`chi`/`middleware`/`model`은 이미 있음.

- [ ] **Step 2: 핸들러 테스트** (`handler/account_test.go`)

```go
func TestHandlerListAccountInsights_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/insights", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.ListAccountInsights(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 3: 라우트 등록** (`cmd/api/main.go`, account 라우트 옆)

```go
		r.Get("/api/accounts/{accountId}/insights", accountHandler.ListAccountInsights)
```

- [ ] **Step 4: 전체 빌드 + 테스트 + ARM64**

```bash
cd backend && /usr/local/go/bin/go build ./... \
  && /usr/local/go/bin/go test ./internal/... \
  && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api \
  && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/summarize/bootstrap ./cmd/summarize
```
Expected: 모두 성공. (bootstrap 바이너리는 gitignore — 커밋하지 말 것.)

- [ ] **Step 5: API-SPEC 갱신**

`GET /api/accounts/{accountId}/insights?from=<RFC3339>&to=<RFC3339>&types=risk,opportunity` → `{insights:[{type,text,sourceType,sourceId,occurredAt,tsMarker?,entities?}]}` (403 비멤버, 404 없음, 400 잘못된 날짜). 8유형 목록 명시.

- [ ] **Step 6: Commit**

```bash
cd backend && git add internal/handler/account.go internal/handler/account_test.go cmd/api/main.go && git -C .. add docs/API-SPEC.md
git commit -m "feat(insight): GET /api/accounts/{id}/insights endpoint + API-SPEC"
```

---

## CDK 메모
인프라 변경 없음. `ExtractInsights`는 summarize의 기존 Bedrock(Haiku) 정책 재사용; `INSIGHT#`는 Account 파티션 내 SK 쿼리(신규 GSI 불필요); `grantReadWriteData`가 전체 접근 부여.

## Self-Review (작성자 체크)
- **Spec 커버리지(Plan 3):** §7 8유형 taxonomy ✅(상수+IsValidInsightType); §8 추출 파이프라인(ExtractInsights, best-effort, summarize 연결) ✅; §8 멱등(결정적 SK 덮어쓰기) ✅(단 재추출 시 항목 수 감소하면 잔여 — 아래 후속); §6.1 `INSIGHT#{occurredAt}` ✅; §6.3 기간/유형 client-side 필터 ✅; §9 `get_insights` 서빙용 데이터(REST) ✅(MCP 래퍼는 Plan 4); 멤버 게이트 ✅; `tsMarker` 딥링크 보존 ✅.
- **Placeholder:** 없음. 모든 스텝 실제 코드.
- **타입 일관성:** `ExtractInsights(ctx, meetingID, userID ...string) (string, error)`(형제와 동일), `BuildAccountInsights(accountID, *Meeting) ([]AccountInsight, error)`, `ListAccountInsights(ctx, userID, accountID, from, to time.Time, types []string) ([]AccountInsightDTO, error)`, repo `PutAccountInsights([]AccountInsight) error`/`ListInsightsForAccount(accountID) ([]AccountInsight, error)`. 인터페이스/4 mock/서비스/핸들러/테스트 일치. 이름 충돌 회피(MeetingInsight/AccountInsight). ✅
- **순서 보정:** Task 4 빌드는 Task 5 repo 메서드 필요(컴파일 의존) — Step 6 노트 명시.
- **알려진 후속(차단 아님):** (1) 재추출로 인사이트 개수가 줄면 이전 고인덱스 `INSIGHT#...#{idx}` 잔여 — 정밀 교체(sourceId 기준 삭제 후 재기록)는 후속. (2) 뉴스/인제스트 출처 인사이트(sourceType news/ingest)는 Plan 4/5에서. (3) summarize 측 팬아웃은 package main이라 미단위테스트(서비스 경로로 커버).
- **확인 필요(구현자):** `bedrock.go`/`meeting.go`/`handler account.go`의 import(`encoding/json`,`fmt`,`strings`,`time`) 유무 확인 후 보강. `cmd/summarize/main.go`의 `meeting` 변수가 `generateSummary` 스코프에서 `AccountID`/`SharedToAccount`를 담는지 확인(업스트림 GetMeeting 읽기).
