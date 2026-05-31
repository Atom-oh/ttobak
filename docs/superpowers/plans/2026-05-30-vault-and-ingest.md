# Obsidian Vault Export + Inbound Ingest — Implementation Plan (Plan 5 of 6)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans. Steps use `- [ ]` checkboxes.

**Goal:** (A) 로컬 vault 문서를 TTOBAK으로 인제스트(`put_document`, 출처 규칙 루프 차단, 멤버 게이트)해 팀이 TTOBAK에서 열람. (B) 미팅 코퍼스를 Obsidian 친화 Markdown(YAML frontmatter)으로 export하되 `Accounts/{name}/`(공유) vs `_Private/Meetings/`(비공개) 디렉터리로 배치.

**Architecture:** 인바운드 문서는 Account 파티션의 `DOC#{docId}` 아이템에 **인라인 마크다운**으로 저장(신규 인프라 0, ≤300KB). Export는 새 `VaultService`가 사용자 미팅을 열거(`ListMeetings` tab=all 페이지네이션)→각 미팅 전체 재조회(`GetMeeting`, 리스트 projection이 insights/account를 누락하므로)→frontmatter+`GenerateMeetingDocument`로 `[]VaultFile{Path,Markdown}` 생성. Account MOC는 v1 생략(frontmatter의 `account: "[[name]]"`가 Obsidian 그래프로 대체).

**Tech Stack:** Go 1.25(stdlib testing) + TS(tsc). CDK 변경 없음(인라인 DynamoDB + 인메모리 생성).

**선행:** Plan 1-4 완료, branch `feat/account-foundation`.

## File Structure (Plan 5)
| 파일 | 변경 |
|---|---|
| `model/account.go` | `AccountDocument`+DTO+`PutDocumentRequest`, `VaultFile`, `EntityTypeAccountDoc` |
| `service/account.go` | `ErrLoopGuard`, `hasTtobakOriginMarker`, `PutDocument`/`ListAccountDocuments`/`GetAccountDocument`; accountRepo += 3 |
| `service/account_test.go` | mock 3메서드 + 테스트(loop guard, member gate) |
| `service/vault.go` | `VaultService` + `vaultRepo` + `ExportVault` + frontmatter/path 헬퍼 (신규) |
| `service/vault_test.go` | `mockVaultRepo` + ExportVault/frontmatter/path 테스트 (신규) |
| `repository/account.go` | `PutAccountDocument`/`ListAccountDocuments`/`GetAccountDocument` |
| `handler/account.go` | `PutDocument`/`ListDocuments`/`GetDocument` |
| `handler/vault.go` | `VaultHandler.ExportVault` (신규) |
| `handler/*_test.go` | mock 동기화 + 테스트 |
| `cmd/api/main.go` | VaultService/Handler 와이어링 + 라우트 4개 |
| `mcp-server/src/{api.ts,index.ts}`, `README.md` | put/list/get_document + export_vault 도구 4개 |
| `docs/API-SPEC.md` | 엔드포인트 |

---

## Task 1: 모델 — AccountDocument + VaultFile

**Files:** Modify `backend/internal/model/account.go`

- [ ] **Step 1: 추가** (account.go 끝)

```go
const EntityTypeAccountDoc = "ACCOUNT_DOC" // SK uses existing model.PrefixDoc ("DOC#")

// AccountDocument is a locally-authored (email/calendar/prep) doc ingested into
// an account so non-Obsidian teammates can read it in TTOBAK. PK: ACCOUNT#{id},
// SK: DOC#{docId}. Content is inline markdown (<=300KB). Loop guard: only
// local-origin docs (no ttobak_id frontmatter) are accepted, so TtobakOrigin=false.
type AccountDocument struct {
	PK           string    `dynamodbav:"PK"`
	SK           string    `dynamodbav:"SK"`
	AccountID    string    `dynamodbav:"accountId"`
	DocID        string    `dynamodbav:"docId"`
	Title        string    `dynamodbav:"title"`
	DocType      string    `dynamodbav:"docType,omitempty"` // "prep" | "reference" | ...
	Path         string    `dynamodbav:"path,omitempty"`    // original vault path
	Content      string    `dynamodbav:"content"`           // inline markdown
	SourceUserID string    `dynamodbav:"sourceUserId"`
	TtobakOrigin bool      `dynamodbav:"ttobakOrigin"`
	CreatedAt    time.Time `dynamodbav:"createdAt"`
	UpdatedAt    time.Time `dynamodbav:"updatedAt"`
	EntityType   string    `dynamodbav:"entityType"`
}

type PutDocumentRequest struct {
	Title    string `json:"title"`
	DocType  string `json:"docType,omitempty"`
	Path     string `json:"path,omitempty"`
	Markdown string `json:"markdown"`
}

type AccountDocumentDTO struct {
	DocID        string    `json:"docId"`
	Title        string    `json:"title"`
	DocType      string    `json:"docType,omitempty"`
	Path         string    `json:"path,omitempty"`
	SourceUserID string    `json:"sourceUserId"`
	CreatedAt    time.Time `json:"createdAt"`
}

type AccountDocumentDetail struct {
	AccountDocumentDTO
	Content string `json:"content"`
}

// VaultFile is one Obsidian note in an export bundle.
type VaultFile struct {
	Path     string `json:"path"`
	Markdown string `json:"markdown"`
}
```

- [ ] **Step 2: 빌드** `cd backend && /usr/local/go/bin/go build ./internal/model/` → 성공.
- [ ] **Step 3: Commit**
```bash
cd backend && git add internal/model/account.go
git commit -m "feat(vault): AccountDocument + VaultFile models"
```

---

## Task 2: 루프 차단 순수 헬퍼 + 테스트

**Files:** Modify `service/account.go`, create test in `service/account_test.go`

- [ ] **Step 1: 실패 테스트** (`account_test.go`)

```go
func TestHasTtobakOriginMarker(t *testing.T) {
	ttobakDoc := "---\naccount: \"[[하나은행]]\"\nttobak_id: m-123\n---\n\n# 회의록"
	if !hasTtobakOriginMarker(ttobakDoc) {
		t.Error("expected true for doc with ttobak_id frontmatter")
	}
	localDoc := "---\ntitle: Email notes\ntags: [prep]\n---\n\n# Prep"
	if hasTtobakOriginMarker(localDoc) {
		t.Error("expected false for local doc without ttobak_id")
	}
	noFront := "# Just markdown, no frontmatter"
	if hasTtobakOriginMarker(noFront) {
		t.Error("expected false when no frontmatter")
	}
}
```

- [ ] **Step 2: 실패 확인** `go test -run TestHasTtobakOriginMarker ./internal/service/` → FAIL (undefined).

- [ ] **Step 3: 구현** (`account.go`; 센티넬 + 상수 + 헬퍼)

센티넬 블록에 추가: `ErrLoopGuard = errors.New("document originated from ttobak (loop guard)")`
파일에 추가:

```go
const maxInlineDocBytes = 300 * 1024 // mirror repo transcript inline threshold

// hasTtobakOriginMarker reports whether markdown carries a ttobak_id key in a
// leading YAML frontmatter block (i.e. TTOBAK-origin → must not be re-ingested).
func hasTtobakOriginMarker(markdown string) bool {
	s := strings.TrimSpace(markdown)
	if !strings.HasPrefix(s, "---") {
		return false
	}
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ttobak_id:") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 통과** `go test -run TestHasTtobakOriginMarker ./internal/service/` → PASS.
- [ ] **Step 5: Commit**
```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(vault): loop-guard helper + ErrLoopGuard sentinel"
```

---

## Task 3: accountRepo 확장 + mock + repo 구현

**Files:** `service/account.go`(인터페이스), `service/account_test.go`+`handler/account_test.go`(mock), `repository/account.go`(impl)

- [ ] **Step 1: accountRepo 인터페이스에 추가** (account.go)

```go
	PutAccountDocument(ctx context.Context, doc *model.AccountDocument) error
	ListAccountDocuments(ctx context.Context, accountID string) ([]model.AccountDocument, error)
	GetAccountDocument(ctx context.Context, accountID, docID string) (*model.AccountDocument, error)
```

- [ ] **Step 2: repo 구현** (`repository/account.go` 끝)

```go
func (r *DynamoDBRepository) PutAccountDocument(ctx context.Context, doc *model.AccountDocument) error {
	item, err := attributevalue.MarshalMap(doc)
	if err != nil {
		return fmt.Errorf("marshal account doc: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(r.tableName), Item: item}); err != nil {
		return fmt.Errorf("put account doc: %w", err)
	}
	return nil
}

func (r *DynamoDBRepository) ListAccountDocuments(ctx context.Context, accountID string) ([]model.AccountDocument, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixDoc))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build docs query: %w", err)
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("query docs: %w", err)
	}
	docs := []model.AccountDocument{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &docs); err != nil {
		return nil, fmt.Errorf("unmarshal docs: %w", err)
	}
	return docs, nil
}

func (r *DynamoDBRepository) GetAccountDocument(ctx context.Context, accountID, docID string) (*model.AccountDocument, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixDoc + docID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get account doc: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var doc model.AccountDocument
	if err := attributevalue.UnmarshalMap(result.Item, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal account doc: %w", err)
	}
	return &doc, nil
}
```

- [ ] **Step 3: mock 2곳 동기화** — `mockAccountRepo`(service/account_test.go) + `mockHandlerAccountRepo`(handler/account_test.go) 각각:
  - struct 필드: `documents map[string][]model.AccountDocument` (accountID→docs)
  - 생성자 init: `documents: make(map[string][]model.AccountDocument),`
  - 메서드:
```go
func (m *mockAccountRepo) PutAccountDocument(_ context.Context, doc *model.AccountDocument) error {
	m.documents[doc.AccountID] = append(m.documents[doc.AccountID], *doc)
	return nil
}
func (m *mockAccountRepo) ListAccountDocuments(_ context.Context, accountID string) ([]model.AccountDocument, error) {
	return append([]model.AccountDocument(nil), m.documents[accountID]...), nil
}
func (m *mockAccountRepo) GetAccountDocument(_ context.Context, accountID, docID string) (*model.AccountDocument, error) {
	for _, d := range m.documents[accountID] {
		if d.DocID == docID {
			cp := d
			return &cp, nil
		}
	}
	return nil, nil
}
```
  (handler mock: 동일 본문, 리시버 `*mockHandlerAccountRepo`.)

- [ ] **Step 4: 빌드 + 회귀** `cd backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...` → 성공. (인터페이스 확장은 repo 구현이 있어야 컴파일 — 위 Step 2가 채움.)
- [ ] **Step 5: Commit**
```bash
cd backend && git add internal/service/account.go internal/repository/account.go internal/service/account_test.go internal/handler/account_test.go
git commit -m "feat(vault): account document repo methods + interface + mocks"
```

---

## Task 4: AccountService 문서 메서드 (TDD)

**Files:** `service/account.go`, `account_test.go`

- [ ] **Step 1: 실패 테스트** (`account_test.go`)

```go
func TestPutDocument_MemberStores(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	dto, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Email notes", DocType: "prep", Markdown: "# Prep\ncontent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.DocID == "" || dto.Title != "Email notes" {
		t.Errorf("unexpected dto: %+v", dto)
	}
	docs, _ := repo.ListAccountDocuments(context.Background(), acc.AccountID)
	if len(docs) != 1 || docs[0].Content != "# Prep\ncontent" || docs[0].TtobakOrigin {
		t.Errorf("doc not stored correctly: %+v", docs)
	}
}

func TestPutDocument_RejectsTtobakOrigin(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "echo", Markdown: "---\nttobak_id: m-1\n---\n# loop"})
	if !errors.Is(err, ErrLoopGuard) {
		t.Errorf("expected ErrLoopGuard, got %v", err)
	}
}

func TestPutDocument_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "stranger-9", acc.AccountID, &model.PutDocumentRequest{Title: "t", Markdown: "x"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestGetAccountDocument_ReturnsContent(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	dto, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "T", Markdown: "body"})
	detail, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, dto.DocID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Content != "body" {
		t.Errorf("expected content body, got %q", detail.Content)
	}
}
```

- [ ] **Step 2: 실패 확인** `go test -run 'TestPutDocument|TestGetAccountDocument' ./internal/service/` → FAIL.

- [ ] **Step 3: 구현** (`account.go`). `uuid` import 확인(account.go는 이미 uuid 사용).

```go
func (s *AccountService) requireMember(ctx context.Context, userID, accountID string) error {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return err
	}
	if member != nil {
		return nil
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return ErrNotFound
	}
	return ErrForbidden
}

func (s *AccountService) PutDocument(ctx context.Context, userID, accountID string, req *model.PutDocumentRequest) (*model.AccountDocumentDTO, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Markdown) == "" {
		return nil, ErrInvalidInput
	}
	if hasTtobakOriginMarker(req.Markdown) {
		return nil, ErrLoopGuard
	}
	if len(req.Markdown) > maxInlineDocBytes {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	docID := uuid.NewString()
	doc := &model.AccountDocument{
		PK: model.PrefixAccount + accountID, SK: model.PrefixDoc + docID,
		AccountID: accountID, DocID: docID, Title: strings.TrimSpace(req.Title),
		DocType: req.DocType, Path: req.Path, Content: req.Markdown,
		SourceUserID: userID, TtobakOrigin: false,
		CreatedAt: now, UpdatedAt: now, EntityType: model.EntityTypeAccountDoc,
	}
	if err := s.repo.PutAccountDocument(ctx, doc); err != nil {
		return nil, err
	}
	return &model.AccountDocumentDTO{DocID: docID, Title: doc.Title, DocType: doc.DocType, Path: doc.Path, SourceUserID: userID, CreatedAt: now}, nil
}

func (s *AccountService) ListAccountDocuments(ctx context.Context, userID, accountID, docType string) ([]model.AccountDocumentDTO, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	docs, err := s.repo.ListAccountDocuments(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]model.AccountDocumentDTO, 0, len(docs))
	for _, d := range docs {
		if docType != "" && d.DocType != docType {
			continue
		}
		out = append(out, model.AccountDocumentDTO{DocID: d.DocID, Title: d.Title, DocType: d.DocType, Path: d.Path, SourceUserID: d.SourceUserID, CreatedAt: d.CreatedAt})
	}
	return out, nil
}

func (s *AccountService) GetAccountDocument(ctx context.Context, userID, accountID, docID string) (*model.AccountDocumentDetail, error) {
	if err := s.requireMember(ctx, userID, accountID); err != nil {
		return nil, err
	}
	doc, err := s.repo.GetAccountDocument(ctx, accountID, docID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, ErrNotFound
	}
	return &model.AccountDocumentDetail{
		AccountDocumentDTO: model.AccountDocumentDTO{DocID: doc.DocID, Title: doc.Title, DocType: doc.DocType, Path: doc.Path, SourceUserID: doc.SourceUserID, CreatedAt: doc.CreatedAt},
		Content:            doc.Content,
	}, nil
}
```

- [ ] **Step 4: 통과** `go test -run 'TestPutDocument|TestGetAccountDocument' ./internal/service/` → PASS.
- [ ] **Step 5: Commit**
```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(vault): PutDocument(loop-guard,member-gate)/List/Get account documents"
```

---

## Task 5: VaultService + ExportVault (TDD)

**Files:** create `service/vault.go`, `service/vault_test.go`

- [ ] **Step 1: VaultService 골격 + 헬퍼 + 인터페이스** (`vault.go`)

```go
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type vaultRepo interface {
	ListMeetings(ctx context.Context, params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error)
	GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error)
	ListAttachments(ctx context.Context, meetingID string) ([]model.Attachment, error)
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
}

// VaultRepo is the exported alias for cross-package tests.
type VaultRepo = vaultRepo

type VaultService struct {
	repo vaultRepo
}

func NewVaultService(repo *repository.DynamoDBRepository) *VaultService { return &VaultService{repo: repo} }
func newVaultServiceWithRepo(repo vaultRepo) *VaultService            { return &VaultService{repo: repo} }
func NewVaultServiceForTest(repo VaultRepo) *VaultService             { return &VaultService{repo: repo} }

var fnameReplacer = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "", "\"", "'", "<", "(", ">", ")", "|", "-", "\n", " ")

func sanitizeFilename(s string) string { return strings.TrimSpace(fnameReplacer.Replace(s)) }

// insightCountsLine returns a deterministic `risk: 1, opportunity: 2` style string.
func insightCountsLine(insightsJSON string) string {
	if strings.TrimSpace(insightsJSON) == "" {
		return ""
	}
	var items []model.MeetingInsight
	if json.Unmarshal([]byte(insightsJSON), &items) != nil || len(items) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Type]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

func buildFrontmatter(meeting *model.Meeting, accountName string) string {
	var b strings.Builder
	b.WriteString("---\n")
	if accountName != "" {
		b.WriteString(fmt.Sprintf("account: \"[[%s]]\"\n", accountName))
	}
	b.WriteString(fmt.Sprintf("date: %s\n", meeting.Date.Format("2006-01-02")))
	if len(meeting.Participants) > 0 {
		b.WriteString(fmt.Sprintf("participants: [%s]\n", strings.Join(meeting.Participants, ", ")))
	}
	tags := append([]string{"meeting"}, meeting.Tags...)
	b.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(tags, ", ")))
	b.WriteString(fmt.Sprintf("status: %s\n", meeting.Status))
	if line := insightCountsLine(meeting.Insights); line != "" {
		b.WriteString(fmt.Sprintf("insights: {%s}\n", line))
	}
	b.WriteString(fmt.Sprintf("ttobak_id: %s\n", meeting.MeetingID))
	b.WriteString("---\n\n")
	return b.String()
}

func vaultPath(meeting *model.Meeting, accountName string) string {
	title := sanitizeFilename(meeting.Title)
	if title == "" {
		title = meeting.MeetingID
	}
	fname := fmt.Sprintf("%s %s.md", meeting.Date.Format("2006-01-02"), title)
	if meeting.SharedToAccount && accountName != "" {
		return fmt.Sprintf("Accounts/%s/%s", sanitizeFilename(accountName), fname)
	}
	return "_Private/Meetings/" + fname
}
```

- [ ] **Step 2: 실패 테스트** (`vault_test.go`)

```go
package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type mockVaultRepo struct {
	owned    []model.Meeting
	full     map[string]*model.Meeting
	accounts map[string]*model.Account
}

func (m *mockVaultRepo) ListMeetings(_ context.Context, _ repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	return &repository.ListMeetingsResult{Meetings: m.owned, NextCursor: nil}, nil
}
func (m *mockVaultRepo) GetMeeting(_ context.Context, _ , meetingID string) (*model.Meeting, error) {
	return m.full[meetingID], nil
}
func (m *mockVaultRepo) ListAttachments(_ context.Context, _ string) ([]model.Attachment, error) {
	return nil, nil
}
func (m *mockVaultRepo) GetAccount(_ context.Context, accountID string) (*model.Account, error) {
	return m.accounts[accountID], nil
}

func TestExportVault_PlacesSharedAndPrivate(t *testing.T) {
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	shared := model.Meeting{MeetingID: "m-1", UserID: "u1", Title: "ROSA 리뷰", Date: d, Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true, Insights: `[{"type":"risk","text":"x"}]`}
	priv := model.Meeting{MeetingID: "m-2", UserID: "u1", Title: "개인 메모", Date: d, Status: model.StatusDone}
	repo := &mockVaultRepo{
		owned: []model.Meeting{shared, priv},
		full:  map[string]*model.Meeting{"m-1": &shared, "m-2": &priv},
		accounts: map[string]*model.Account{"acc-1": {AccountID: "acc-1", Name: "하나은행"}},
	}
	svc := newVaultServiceWithRepo(repo)

	files, err := svc.ExportVault(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.Markdown
	}
	sharedMd, ok := byPath["Accounts/하나은행/2026-05-12 ROSA 리뷰.md"]
	if !ok {
		t.Fatalf("shared meeting not placed under account folder; paths=%v", byPath)
	}
	if !strings.Contains(sharedMd, `account: "[[하나은행]]"`) || !strings.Contains(sharedMd, "ttobak_id: m-1") || !strings.Contains(sharedMd, "insights: {risk: 1}") {
		t.Errorf("frontmatter missing fields:\n%s", sharedMd)
	}
	if !strings.Contains(sharedMd, "# ROSA 리뷰") {
		t.Error("expected GenerateMeetingDocument body (H1 title)")
	}
	if _, ok := byPath["_Private/Meetings/2026-05-12 개인 메모.md"]; !ok {
		t.Fatalf("private meeting not under _Private; paths=%v", byPath)
	}
}
```

- [ ] **Step 3: 실패 확인** `go test -run TestExportVault ./internal/service/` → FAIL (`svc.ExportVault undefined`).

- [ ] **Step 4: ExportVault 구현** (`vault.go`)

```go
// ExportVault renders the user's owned meetings as Obsidian-ready markdown files,
// placed under Accounts/{name}/ (if shared to an account) or _Private/Meetings/.
// Account names are resolved once and cached. (Account MOC files are a follow-up;
// the `account: "[[name]]"` frontmatter already drives Obsidian's graph.)
func (s *VaultService) ExportVault(ctx context.Context, userID string) ([]model.VaultFile, error) {
	files := []model.VaultFile{}
	nameCache := map[string]string{}
	cursor := ""
	for {
		res, err := s.repo.ListMeetings(ctx, repository.ListMeetingsParams{UserID: userID, Tab: "all", Cursor: cursor, Limit: 100})
		if err != nil {
			return nil, err
		}
		for i := range res.Meetings {
			full, err := s.repo.GetMeeting(ctx, userID, res.Meetings[i].MeetingID)
			if err != nil {
				return nil, err
			}
			if full == nil {
				continue
			}
			accountName := ""
			if full.SharedToAccount && full.AccountID != "" {
				name, ok := nameCache[full.AccountID]
				if !ok {
					if acc, err := s.repo.GetAccount(ctx, full.AccountID); err == nil && acc != nil {
						name = acc.Name
					}
					nameCache[full.AccountID] = name
				}
				accountName = name
			}
			attachments, _ := s.repo.ListAttachments(ctx, full.MeetingID)
			md := buildFrontmatter(full, accountName) + GenerateMeetingDocument(full, attachments)
			files = append(files, model.VaultFile{Path: vaultPath(full, accountName), Markdown: md})
		}
		if res.NextCursor == nil {
			break
		}
		cursor = *res.NextCursor
	}
	return files, nil
}
```

- [ ] **Step 5: 통과** `go test -run TestExportVault ./internal/service/` → PASS.
- [ ] **Step 6: Commit**
```bash
cd backend && git add internal/service/vault.go internal/service/vault_test.go
git commit -m "feat(vault): VaultService.ExportVault (frontmatter + shared/private placement)"
```

---

## Task 6: 핸들러 + 라우트 + 와이어링

**Files:** `handler/account.go`, `account_test.go`, create `handler/vault.go`, `cmd/api/main.go`

- [ ] **Step 1: 문서 핸들러 3개** (`handler/account.go` 끝) — 표준 errors.Is 래더 사용

```go
func (h *AccountHandler) PutDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var req model.PutDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.PutDocument(ctx, userID, accountID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		case errors.Is(err, service.ErrLoopGuard):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Document originated from TTOBAK (loop guard)")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "title and markdown required (<=300KB)")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (h *AccountHandler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	list, err := h.accountService.ListAccountDocuments(ctx, userID, accountID, r.URL.Query().Get("docType"))
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"documents": list})
}

func (h *AccountHandler) GetDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	docID := chi.URLParam(r, "docId")
	if accountID == "" || docID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID and Document ID are required")
		return
	}
	detail, err := h.accountService.GetAccountDocument(ctx, userID, accountID, docID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Document not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
```

- [ ] **Step 2: VaultHandler** (`handler/vault.go` 신규)

```go
package handler

import (
	"errors"
	"net/http"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type VaultHandler struct {
	vaultService *service.VaultService
}

func NewVaultHandler(vaultService *service.VaultService) *VaultHandler {
	return &VaultHandler{vaultService: vaultService}
}

func (h *VaultHandler) ExportVault(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	files, err := h.vaultService.ExportVault(ctx, userID)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"files": files})
}
```

- [ ] **Step 3: 핸들러 테스트** (`handler/account_test.go`) — 문서 put 멤버 게이트(403)

```go
func TestHandlerPutDocument_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	body, _ := json.Marshal(model.PutDocumentRequest{Title: "t", Markdown: "x"})
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/acc-1/documents", bytes.NewReader(body))
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()
	h.PutDocument(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 4: 와이어링 + 라우트** (`cmd/api/main.go`)
  - 서비스: `vaultService := service.NewVaultService(repo)`
  - 핸들러: `vaultHandler := handler.NewVaultHandler(vaultService)`
  - 라우트(인증 그룹, account 라우트 옆):
```go
		r.Post("/api/accounts/{accountId}/documents", accountHandler.PutDocument)
		r.Get("/api/accounts/{accountId}/documents", accountHandler.ListDocuments)
		r.Get("/api/accounts/{accountId}/documents/{docId}", accountHandler.GetDocument)
		r.Get("/api/vault/export", vaultHandler.ExportVault)
```

- [ ] **Step 5: 빌드 + 테스트 + ARM64**
```bash
cd backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/... \
  && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```
Expected: 성공(bootstrap 커밋 금지).

- [ ] **Step 6: Commit**
```bash
cd backend && git add internal/handler/account.go internal/handler/vault.go internal/handler/account_test.go cmd/api/main.go
git commit -m "feat(vault): document + vault-export handlers, routes, wiring"
```

---

## Task 7: MCP 도구 4개 + README + API-SPEC + 최종 검증

**Files:** `mcp-server/src/api.ts`, `index.ts`, `README.md`, `docs/API-SPEC.md`

- [ ] **Step 1: api.ts 메서드 추가**

```ts
  async exportVault() {
    return this.get('/api/vault/export');
  }

  async putDocument(
    accountId: string,
    doc: { title: string; markdown: string; docType?: string; path?: string },
  ) {
    return this.post(`/api/accounts/${accountId}/documents`, doc);
  }

  async listDocuments(accountId: string, docType?: string) {
    const q = new URLSearchParams();
    if (docType) q.set('docType', docType);
    const qs = q.toString();
    return this.get(`/api/accounts/${accountId}/documents${qs ? '?' + qs : ''}`);
  }

  async getDocument(accountId: string, docId: string) {
    return this.get(`/api/accounts/${accountId}/documents/${docId}`);
  }
```

- [ ] **Step 2: index.ts 도구 정의 4개** (`ttobak_get_account_brief` 다음)

```ts
    {
      name: 'ttobak_export_vault',
      description: 'Export your meetings as Obsidian-ready markdown files [{path, markdown}], placed under Accounts/{name}/ (shared) or _Private/Meetings/. Write each to your local vault.',
      inputSchema: { type: 'object' as const, properties: {} },
    },
    {
      name: 'ttobak_put_document',
      description: 'Ingest a locally-authored document (email/calendar/prep notes) into an account so teammates can read it in TTOBAK. Rejects docs that originated from TTOBAK (loop guard).',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          title: { type: 'string', description: 'Document title' },
          markdown: { type: 'string', description: 'Markdown content (<=300KB)' },
          docType: { type: 'string', description: 'Optional: prep | reference | ...' },
          path: { type: 'string', description: 'Optional: original vault path' },
        },
        required: ['accountId', 'title', 'markdown'],
      },
    },
    {
      name: 'ttobak_list_documents',
      description: 'List ingested documents for an account (docId, title, docType).',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          docType: { type: 'string', description: 'Optional docType filter' },
        },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_document',
      description: 'Get an ingested document with full content.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          docId: { type: 'string', description: 'Document ID' },
        },
        required: ['accountId', 'docId'],
      },
    },
```

- [ ] **Step 3: index.ts switch case 4개** (`ttobak_get_account_brief` case 다음)

```ts
      case 'ttobak_export_vault': {
        const result = await api.exportVault();
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_put_document': {
        const { accountId, title, markdown, docType, path } = args as {
          accountId: string; title: string; markdown: string; docType?: string; path?: string;
        };
        if (!accountId) return error('accountId is required');
        if (!title) return error('title is required');
        if (!markdown) return error('markdown is required');
        const result = await api.putDocument(accountId, { title, markdown, docType, path });
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_list_documents': {
        const { accountId, docType } = args as { accountId: string; docType?: string };
        if (!accountId) return error('accountId is required');
        const result = await api.listDocuments(accountId, docType);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_document': {
        const { accountId, docId } = args as { accountId: string; docId: string };
        if (!accountId) return error('accountId is required');
        if (!docId) return error('docId is required');
        const result = await api.getDocument(accountId, docId);
        return text(JSON.stringify(result, null, 2));
      }
```

- [ ] **Step 4: README** — 도구 표 2곳(EN/KR) + `/mcp` 목록 2곳에 `ttobak_export_vault, ttobak_put_document, ttobak_list_documents, ttobak_get_document` 추가(기존 포맷에 맞춤).

- [ ] **Step 5: API-SPEC** — 4개 엔드포인트:
  - `POST /api/accounts/{accountId}/documents` body `{title,markdown,docType?,path?}` → 201 doc DTO (400 loop-guard/invalid, 403/404)
  - `GET /api/accounts/{accountId}/documents?docType=` → `{documents:[...]}`
  - `GET /api/accounts/{accountId}/documents/{docId}` → doc detail+content (404)
  - `GET /api/vault/export` → `{files:[{path,markdown}]}`

- [ ] **Step 6: 최종 검증**
```bash
cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...
cd /home/ec2-user/ttobak/mcp-server && npm run build
```
Expected: 모두 성공.

- [ ] **Step 7: Commit**
```bash
cd /home/ec2-user/ttobak/mcp-server && git add src/api.ts src/index.ts README.md && git -C .. add docs/API-SPEC.md
git commit -m "feat(vault): MCP export_vault + document tools + API-SPEC"
```

---

## CDK 메모
인프라 변경 없음. 문서는 인라인 DynamoDB DOC# 아이템(≤300KB), export는 인메모리 생성. `grantReadWriteData`가 전체 접근 부여.

## Self-Review (작성자 체크)
- **Spec 커버리지(Plan 5):** §10 export_vault(Accounts/_Private 2계층 + frontmatter `account/date/tags/status/insights/ttobak_id`) ✅; §5.5 출처 규칙 루프 차단(`hasTtobakOriginMarker`→ErrLoopGuard) ✅; 인바운드 `put_document`(멤버 게이트, docType:"prep") ✅; `list_documents`/`get_document` ✅; §9 MCP 도구 11번째군 완성 ✅. (Account MOC·뉴스/인제스트 인사이트 추출·S3 스필은 후속.)
- **Placeholder:** README는 근사 라인+구체 도구명. 코드 스텝 전부 실제 코드.
- **타입 일관성:** `PutDocument(ctx,userID,accountID,*PutDocumentRequest)(*AccountDocumentDTO,err)`, `ListAccountDocuments(...,docType string)([]AccountDocumentDTO)`, `GetAccountDocument(...)(*AccountDocumentDetail)`, `ExportVault(ctx,userID)([]VaultFile)`. accountRepo += 3(2 mock 동기화), vaultRepo 신규(mockVaultRepo). 라우트/도구명 api/index/README 일치.
- **순서:** Task 3에서 인터페이스+repo구현+mock을 한 커밋으로 묶어 컴파일 의존성 회피(Plan 2-4의 분리-후-보정과 달리 이번엔 동시).
- **확인 필요(구현자):** `mockVaultRepo`의 `GetMeeting(_,_, meetingID)` 시그니처가 `vaultRepo.GetMeeting(ctx,userID,meetingID)`와 일치하는지(언더스코어 2개). `handler/account.go`에 `encoding/json` import 있음. `cmd/api/main.go` 와이어링 위치(meeting/account 서비스·핸들러 옆).
