# Account Entity Foundation — Implementation Plan (Plan 1 of 6)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 고객사(Account)를 1급 엔티티로 만든다 — 명시 등록 + 팀 멤버십(AM/TAM/SSA/owner) + 권한 검사 + REST API. 나머지 모든 단계(미팅 연결·인사이트·MCP·Vault)의 토대.

**Architecture:** DynamoDB 단일 테이블 `ttobak-main`에 `ACCOUNT#{accountId}` 공유 파티션을 추가한다. Account META + `MEMBER#{userId}` 아이템으로 멤버십을 표현하고, 멤버 아이템의 기존 **GSI1**(`GSI1PK=USER#{userId}`, `GSI1SK=ACCOUNT#{accountId}`)로 "내 Account 목록"을 역조회한다(신규 GSI 불필요). 서비스 계층은 기존 `MeetingService` 패턴(unexported repo 인터페이스 + 손수 만든 mock + 센티넬 에러)을 그대로 따른다.

**Tech Stack:** Go 1.25 (AWS SDK v2 `attributevalue`/`expression`, chi v5), stdlib `testing`(testify 없음), CDK는 변경 없음(GSI1 재사용).

---

## 전체 로드맵 (6개 플랜)

이 스펙(`docs/superpowers/specs/2026-05-30-account-insight-substrate-design.md`)은 6개 순차 플랜으로 분해된다. 각 플랜은 독립적으로 빌드·테스트 가능하다.

1. **Account Entity Foundation** ← *이 문서*. Account+멤버십 CRUD API.
2. **Meeting ↔ Account 연결 & 공유.** `Meeting`에 `AccountID`/`SharedToAccount` 추가, 태그 별칭 해석, Account 그룹 공유(기존 `Share` 확장), `MEETINGREF#` 적립, 멤버의 공유 미팅 열람 권한.
3. **Insight Substrate.** `bedrock.go`에 `ExtractInsights`(8유형), `ACCOUNT#`/`INSIGHT#{occurredAt}` 적립, 기간 range 쿼리, summarize 파이프라인·크롤러 연결.
4. **MCP Back-Data 도구.** REST 엔드포인트(`get_insights`/`account_activity`/`account_brief`/`list_documents` 등) + `mcp-server/src` TS 래퍼 11종.
5. **Obsidian Vault Export + 인바운드 인제스트.** `export_vault`(Accounts/_Private 2계층) + `put_document`(출처 규칙 루프 차단).
6. **프런트엔드 표면.** Account 등록/멤버 초대, 미팅↔Account 연결 UI, Account 상세.

> 본 Plan 1 완료 시점의 검증 가능 산출물: `POST/GET /api/accounts`, `GET /api/accounts/{id}`, `POST /api/accounts/{id}/members`가 동작하고 `go test ./...`가 통과한다.

---

## File Structure (Plan 1)

| 파일 | 책임 | 신규/수정 |
|---|---|---|
| `backend/internal/model/account.go` | `Account`/`AccountMember` 엔티티, 프리픽스·역할 상수, 요청/응답 DTO | Create |
| `backend/internal/service/account.go` | `AccountService` + `accountRepo` 인터페이스 + 센티넬 에러 + 비즈니스 규칙 | Create |
| `backend/internal/service/account_test.go` | 서비스 단위 테스트 + `mockAccountRepo` | Create |
| `backend/internal/repository/account.go` | `DynamoDBRepository`의 Account 메서드(실 DynamoDB) | Create |
| `backend/internal/handler/account.go` | `AccountHandler`(CRUD + 멤버 추가), 에러 매핑 | Create |
| `backend/internal/handler/account_test.go` | 핸들러 테스트(httptest + mock repo) | Create |
| `backend/cmd/api/main.go` | 서비스·핸들러 와이어링 + 라우트 등록 | Modify (`init()` 그룹) |

---

## Task 1: Account 도메인 모델 & 상수

**Files:**
- Create: `backend/internal/model/account.go`

- [ ] **Step 1: 모델 파일 작성**

```go
package model

import "time"

// Key prefixes / constants for the Account partition.
// Account META:   PK: ACCOUNT#{accountId}, SK: META
// Account member: PK: ACCOUNT#{accountId}, SK: MEMBER#{userId}
//                 (GSI1PK: USER#{userId}, GSI1SK: ACCOUNT#{accountId} for reverse lookup)
const (
	PrefixAccount = "ACCOUNT#"
	SKAccountMeta = "META"
	PrefixMember  = "MEMBER#"

	EntityTypeAccount       = "ACCOUNT"
	EntityTypeAccountMember = "ACCOUNT_MEMBER"
)

// Account member roles. owner is assigned only to the creator.
const (
	RoleOwner = "owner"
	RoleAM    = "AM"
	RoleTAM   = "TAM"
	RoleSSA   = "SSA"
)

// Account is a first-class customer/company entity shared across a team.
type Account struct {
	PK              string    `dynamodbav:"PK"` // ACCOUNT#{accountId}
	SK              string    `dynamodbav:"SK"` // META
	AccountID       string    `dynamodbav:"accountId"`
	Name            string    `dynamodbav:"name"`
	Aliases         []string  `dynamodbav:"aliases,omitempty"` // tag mapping e.g. ["하나은행","Hana Bank"]
	Domains         []string  `dynamodbav:"domains,omitempty"`
	Industry        string    `dynamodbav:"industry,omitempty"`
	CrawlerSourceID string    `dynamodbav:"crawlerSourceId,omitempty"` // links CRAWLER#{sourceId} (Plan 3)
	OwnerUserID     string    `dynamodbav:"ownerUserId"`
	CreatedAt       time.Time `dynamodbav:"createdAt"`
	UpdatedAt       time.Time `dynamodbav:"updatedAt"`
	EntityType      string    `dynamodbav:"entityType"` // "ACCOUNT"
}

// AccountMember binds a user (with a role) to an account.
type AccountMember struct {
	PK         string    `dynamodbav:"PK"` // ACCOUNT#{accountId}
	SK         string    `dynamodbav:"SK"` // MEMBER#{userId}
	AccountID  string    `dynamodbav:"accountId"`
	UserID     string    `dynamodbav:"userId"`
	Email      string    `dynamodbav:"email,omitempty"`
	Role       string    `dynamodbav:"role"`
	AddedAt    time.Time `dynamodbav:"addedAt"`
	GSI1PK     string    `dynamodbav:"GSI1PK,omitempty"` // USER#{userId}
	GSI1SK     string    `dynamodbav:"GSI1SK,omitempty"` // ACCOUNT#{accountId}
	EntityType string    `dynamodbav:"entityType"`       // "ACCOUNT_MEMBER"
}

// --- Request / Response DTOs ---

type CreateAccountRequest struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Domains  []string `json:"domains,omitempty"`
	Industry string   `json:"industry,omitempty"`
}

type AddMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type AccountMemberDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role"`
}

type AccountResponse struct {
	AccountID   string             `json:"accountId"`
	Name        string             `json:"name"`
	Aliases     []string           `json:"aliases,omitempty"`
	Domains     []string           `json:"domains,omitempty"`
	Industry    string             `json:"industry,omitempty"`
	OwnerUserID string             `json:"ownerUserId"`
	Members     []AccountMemberDTO `json:"members"`
	CreatedAt   time.Time          `json:"createdAt"`
}

// AccountSummary is the list-view item for "my accounts".
type AccountSummary struct {
	AccountID string `json:"accountId"`
	Name      string `json:"name"`
	Role      string `json:"role"`
}
```

- [ ] **Step 2: 컴파일 확인**

Run: `cd backend && /usr/local/go/bin/go build ./internal/model/`
Expected: 에러 없이 성공.

- [ ] **Step 3: Commit**

```bash
cd backend && git add internal/model/account.go
git commit -m "feat(account): add Account and AccountMember domain models"
```

---

## Task 2: accountRepo 인터페이스 + mock + AccountService 골격

**Files:**
- Create: `backend/internal/service/account.go`
- Create: `backend/internal/service/account_test.go`

- [ ] **Step 1: 서비스 골격 + 인터페이스 + 센티넬 작성**

`backend/internal/service/account.go`:

```go
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Account-specific sentinel errors. (ErrForbidden, ErrNotFound, ErrUserNotFound
// are already declared in service/meeting.go in this same package — reuse them.)
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrMemberExists = errors.New("member already exists")
)

// accountRepo is the persistence seam for AccountService (mirrors meetingRepo).
type accountRepo interface {
	CreateAccount(ctx context.Context, account *model.Account, ownerMember *model.AccountMember) error
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	PutMember(ctx context.Context, member *model.AccountMember) error
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
}

// AccountRepo is the exported alias for cross-package (handler) tests.
type AccountRepo = accountRepo

type AccountService struct {
	repo accountRepo
}

func NewAccountService(repo *repository.DynamoDBRepository) *AccountService {
	return &AccountService{repo: repo}
}

// newAccountServiceWithRepo is for same-package (service) tests.
func newAccountServiceWithRepo(repo accountRepo) *AccountService {
	return &AccountService{repo: repo}
}

// NewAccountServiceForTest is for cross-package (handler) tests.
func NewAccountServiceForTest(repo AccountRepo) *AccountService {
	return &AccountService{repo: repo}
}

func isAssignableRole(role string) bool {
	switch role {
	case model.RoleAM, model.RoleTAM, model.RoleSSA:
		return true
	default:
		return false
	}
}

func toAccountResponse(a *model.Account, members []model.AccountMember) *model.AccountResponse {
	dtos := make([]model.AccountMemberDTO, 0, len(members))
	for _, m := range members {
		dtos = append(dtos, model.AccountMemberDTO{UserID: m.UserID, Email: m.Email, Role: m.Role})
	}
	return &model.AccountResponse{
		AccountID:   a.AccountID,
		Name:        a.Name,
		Aliases:     a.Aliases,
		Domains:     a.Domains,
		Industry:    a.Industry,
		OwnerUserID: a.OwnerUserID,
		Members:     dtos,
		CreatedAt:   a.CreatedAt,
	}
}

// methods added in Tasks 3-6
var _ = uuid.NewString
var _ = strings.TrimSpace
var _ = time.Now
```

> The three `var _ =` lines keep imports used while methods are stubbed out; remove them in Task 3 once the real methods reference these packages.

- [ ] **Step 2: mock repo + 컴파일 테스트 작성**

`backend/internal/service/account_test.go`:

```go
package service

import (
	"context"

	"github.com/ttobak/backend/internal/model"
)

// mockAccountRepo implements accountRepo with in-memory maps.
type mockAccountRepo struct {
	accounts map[string]*model.Account       // accountID
	members  map[string]*model.AccountMember // accountID|userID
	users    map[string]*model.User          // email
}

func newMockAccountRepo() *mockAccountRepo {
	return &mockAccountRepo{
		accounts: make(map[string]*model.Account),
		members:  make(map[string]*model.AccountMember),
		users:    make(map[string]*model.User),
	}
}

func memberKey(accountID, userID string) string { return accountID + "|" + userID }

func (m *mockAccountRepo) CreateAccount(_ context.Context, account *model.Account, ownerMember *model.AccountMember) error {
	cp := *account
	m.accounts[account.AccountID] = &cp
	mc := *ownerMember
	m.members[memberKey(ownerMember.AccountID, ownerMember.UserID)] = &mc
	return nil
}

func (m *mockAccountRepo) GetAccount(_ context.Context, accountID string) (*model.Account, error) {
	a, ok := m.accounts[accountID]
	if !ok {
		return nil, nil
	}
	cp := *a
	return &cp, nil
}

func (m *mockAccountRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	mem, ok := m.members[memberKey(accountID, userID)]
	if !ok {
		return nil, nil
	}
	cp := *mem
	return &cp, nil
}

func (m *mockAccountRepo) PutMember(_ context.Context, member *model.AccountMember) error {
	cp := *member
	m.members[memberKey(member.AccountID, member.UserID)] = &cp
	return nil
}

func (m *mockAccountRepo) ListAccountMembers(_ context.Context, accountID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, mem := range m.members {
		if mem.AccountID == accountID {
			out = append(out, *mem)
		}
	}
	return out, nil
}

func (m *mockAccountRepo) ListAccountsForUser(_ context.Context, userID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, mem := range m.members {
		if mem.UserID == userID {
			out = append(out, *mem)
		}
	}
	return out, nil
}

func (m *mockAccountRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}
```

- [ ] **Step 3: 빌드 확인**

Run: `cd backend && /usr/local/go/bin/go build ./internal/service/ && /usr/local/go/bin/go vet ./internal/service/`
Expected: 성공(아직 테스트 함수 없음).

- [ ] **Step 4: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(account): AccountService scaffold + accountRepo interface + mock"
```

---

## Task 3: CreateAccount

**Files:**
- Modify: `backend/internal/service/account.go` (add method)
- Modify: `backend/internal/service/account_test.go` (add tests)

- [ ] **Step 1: 실패하는 테스트 작성**

`account_test.go`에 추가:

```go
func TestCreateAccount_SetsOwnerMember(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)

	acc, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com",
		&model.CreateAccountRequest{Name: "하나은행", Aliases: []string{"Hana Bank"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.AccountID == "" {
		t.Fatal("expected generated accountId")
	}
	if acc.OwnerUserID != "user-1" {
		t.Errorf("expected owner user-1, got %s", acc.OwnerUserID)
	}
	if acc.EntityType != model.EntityTypeAccount {
		t.Errorf("expected entityType ACCOUNT, got %s", acc.EntityType)
	}
	// owner must exist as a member with role owner
	owner, _ := repo.GetMember(context.Background(), acc.AccountID, "user-1")
	if owner == nil || owner.Role != model.RoleOwner {
		t.Errorf("expected owner member with role owner, got %+v", owner)
	}
	// GSI1 keys for reverse lookup
	if owner.GSI1PK != model.PrefixUser+"user-1" || owner.GSI1SK != model.PrefixAccount+acc.AccountID {
		t.Errorf("owner GSI1 keys wrong: %s / %s", owner.GSI1PK, owner.GSI1SK)
	}
}

func TestCreateAccount_EmptyNameRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com",
		&model.CreateAccountRequest{Name: "   "})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}
```

추가로 `account_test.go` import 블록에 `"errors"`와 `"testing"`을 더한다:

```go
import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestCreateAccount ./internal/service/`
Expected: FAIL — `svc.CreateAccount undefined`.

- [ ] **Step 3: CreateAccount 구현**

`account.go`의 `var _ =` 세 줄을 삭제하고 메서드 추가:

```go
func (s *AccountService) CreateAccount(ctx context.Context, ownerUserID, ownerEmail string, req *model.CreateAccountRequest) (*model.Account, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	accountID := uuid.NewString()
	account := &model.Account{
		PK:          model.PrefixAccount + accountID,
		SK:          model.SKAccountMeta,
		AccountID:   accountID,
		Name:        strings.TrimSpace(req.Name),
		Aliases:     req.Aliases,
		Domains:     req.Domains,
		Industry:    req.Industry,
		OwnerUserID: ownerUserID,
		CreatedAt:   now,
		UpdatedAt:   now,
		EntityType:  model.EntityTypeAccount,
	}
	owner := &model.AccountMember{
		PK:         model.PrefixAccount + accountID,
		SK:         model.PrefixMember + ownerUserID,
		AccountID:  accountID,
		UserID:     ownerUserID,
		Email:      ownerEmail,
		Role:       model.RoleOwner,
		AddedAt:    now,
		GSI1PK:     model.PrefixUser + ownerUserID,
		GSI1SK:     model.PrefixAccount + accountID,
		EntityType: model.EntityTypeAccountMember,
	}
	if err := s.repo.CreateAccount(ctx, account, owner); err != nil {
		return nil, err
	}
	return account, nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestCreateAccount ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(account): CreateAccount sets creator as owner member"
```

---

## Task 4: GetAccount + 멤버십 권한

**Files:**
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/account_test.go`

- [ ] **Step 1: 실패하는 테스트 작성**

```go
func TestGetAccount_MemberSees(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com",
		&model.CreateAccountRequest{Name: "하나은행"})

	resp, err := svc.GetAccount(context.Background(), "owner-1", acc.AccountID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Name != "하나은행" {
		t.Errorf("expected name 하나은행, got %s", resp.Name)
	}
	if len(resp.Members) != 1 || resp.Members[0].Role != model.RoleOwner {
		t.Errorf("expected 1 owner member, got %+v", resp.Members)
	}
}

func TestGetAccount_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com",
		&model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.GetAccount(context.Background(), "stranger-9", acc.AccountID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestGetAccount_MissingNotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, err := svc.GetAccount(context.Background(), "user-1", "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestGetAccount ./internal/service/`
Expected: FAIL — `svc.GetAccount undefined`.

- [ ] **Step 3: GetAccount 구현**

```go
func (s *AccountService) GetAccount(ctx context.Context, userID, accountID string) (*model.AccountResponse, error) {
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		// Distinguish "no such account" (NotFound) from "exists but not a member" (Forbidden).
		account, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	members, err := s.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return toAccountResponse(account, members), nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestGetAccount ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(account): GetAccount with membership-based access control"
```

---

## Task 5: ListAccounts (내 Account 목록)

**Files:**
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/account_test.go`

- [ ] **Step 1: 실패하는 테스트 작성**

```go
func TestListAccounts_OnlyMine(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	a1, _ := svc.CreateAccount(context.Background(), "user-1", "u1@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, _ = svc.CreateAccount(context.Background(), "user-2", "u2@x.com", &model.CreateAccountRequest{Name: "삼성전자"})

	list, err := svc.ListAccounts(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 account for user-1, got %d", len(list))
	}
	if list[0].AccountID != a1.AccountID || list[0].Role != model.RoleOwner {
		t.Errorf("unexpected summary: %+v", list[0])
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestListAccounts ./internal/service/`
Expected: FAIL — `svc.ListAccounts undefined`.

- [ ] **Step 3: ListAccounts 구현**

```go
func (s *AccountService) ListAccounts(ctx context.Context, userID string) ([]model.AccountSummary, error) {
	memberships, err := s.repo.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]model.AccountSummary, 0, len(memberships))
	for _, m := range memberships {
		account, err := s.repo.GetAccount(ctx, m.AccountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue // membership dangling after account deletion
		}
		out = append(out, model.AccountSummary{AccountID: account.AccountID, Name: account.Name, Role: m.Role})
	}
	return out, nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestListAccounts ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(account): ListAccounts returns user's memberships"
```

---

## Task 6: AddMember (owner 전용)

**Files:**
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/account_test.go`

- [ ] **Step 1: 실패하는 테스트 작성**

```go
func seedUser(repo *mockAccountRepo, userID, email string) {
	repo.users[email] = &model.User{UserID: userID, Email: email}
}

func TestAddMember_OwnerAddsTAM(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")

	dto, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.UserID != "tam-1" || dto.Role != model.RoleTAM {
		t.Errorf("unexpected dto: %+v", dto)
	}
	mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1")
	if mem == nil || mem.GSI1PK != model.PrefixUser+"tam-1" {
		t.Errorf("member not persisted with GSI1 keys: %+v", mem)
	}
}

func TestAddMember_NonOwnerForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	// make tam-1 a non-owner member first
	_, _ = svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	seedUser(repo, "ssa-1", "ssa@x.com")

	_, err := svc.AddMember(context.Background(), "tam-1", acc.AccountID, &model.AddMemberRequest{Email: "ssa@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestAddMember_UnknownEmail(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "ghost@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestAddMember_DuplicateRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	_, _ = svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrMemberExists) {
		t.Errorf("expected ErrMemberExists, got %v", err)
	}
}

func TestAddMember_InvalidRole(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "x-1", "x@x.com")
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "x@x.com", Role: "owner"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput (owner not assignable), got %v", err)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestAddMember ./internal/service/`
Expected: FAIL — `svc.AddMember undefined`.

- [ ] **Step 3: AddMember 구현**

```go
func (s *AccountService) AddMember(ctx context.Context, requesterUserID, accountID string, req *model.AddMemberRequest) (*model.AccountMemberDTO, error) {
	requester, err := s.repo.GetMember(ctx, accountID, requesterUserID)
	if err != nil {
		return nil, err
	}
	if requester == nil || requester.Role != model.RoleOwner {
		return nil, ErrForbidden
	}
	if !isAssignableRole(req.Role) {
		return nil, ErrInvalidInput
	}
	user, err := s.repo.GetUserByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	existing, err := s.repo.GetMember(ctx, accountID, user.UserID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrMemberExists
	}
	member := &model.AccountMember{
		PK:         model.PrefixAccount + accountID,
		SK:         model.PrefixMember + user.UserID,
		AccountID:  accountID,
		UserID:     user.UserID,
		Email:      user.Email,
		Role:       req.Role,
		AddedAt:    time.Now().UTC(),
		GSI1PK:     model.PrefixUser + user.UserID,
		GSI1SK:     model.PrefixAccount + accountID,
		EntityType: model.EntityTypeAccountMember,
	}
	if err := s.repo.PutMember(ctx, member); err != nil {
		return nil, err
	}
	return &model.AccountMemberDTO{UserID: user.UserID, Email: user.Email, Role: req.Role}, nil
}
```

- [ ] **Step 4: 전체 서비스 테스트 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/`
Expected: PASS (모든 account 테스트 포함).

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(account): AddMember (owner-only) with validation"
```

---

## Task 7: Repository 구현 (실 DynamoDB)

서비스 테스트는 mock으로 끝났다. 이제 `NewAccountService(repo)`가 실제로 컴파일·동작하도록 `DynamoDBRepository`에 메서드를 구현한다. (이 저장소 계층은 기존 코드 관례대로 직접 단위 테스트하지 않는다 — DynamoDB Local/페이크가 없음. 빌드와 서비스 테스트로 검증.)

**Files:**
- Create: `backend/internal/repository/account.go`

- [ ] **Step 1: 저장소 메서드 작성**

```go
package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// CreateAccount writes the account META item and the owner member item.
func (r *DynamoDBRepository) CreateAccount(ctx context.Context, account *model.Account, ownerMember *model.AccountMember) error {
	accItem, err := attributevalue.MarshalMap(account)
	if err != nil {
		return fmt.Errorf("marshal account: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      accItem,
	}); err != nil {
		return fmt.Errorf("put account: %w", err)
	}
	memItem, err := attributevalue.MarshalMap(ownerMember)
	if err != nil {
		return fmt.Errorf("marshal owner member: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      memItem,
	}); err != nil {
		return fmt.Errorf("put owner member: %w", err)
	}
	return nil
}

func (r *DynamoDBRepository) GetAccount(ctx context.Context, accountID string) (*model.Account, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.SKAccountMeta},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var account model.Account
	if err := attributevalue.UnmarshalMap(result.Item, &account); err != nil {
		return nil, fmt.Errorf("unmarshal account: %w", err)
	}
	return &account, nil
}

func (r *DynamoDBRepository) GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		ConsistentRead: aws.Bool(true),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: model.PrefixAccount + accountID},
			"SK": &types.AttributeValueMemberS{Value: model.PrefixMember + userID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}
	var member model.AccountMember
	if err := attributevalue.UnmarshalMap(result.Item, &member); err != nil {
		return nil, fmt.Errorf("unmarshal member: %w", err)
	}
	return &member, nil
}

func (r *DynamoDBRepository) PutMember(ctx context.Context, member *model.AccountMember) error {
	item, err := attributevalue.MarshalMap(member)
	if err != nil {
		return fmt.Errorf("marshal member: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put member: %w", err)
	}
	return nil
}

// ListAccountMembers queries the account partition for MEMBER# items.
func (r *DynamoDBRepository) ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixMember))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build members query: %w", err)
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query members: %w", err)
	}
	members := []model.AccountMember{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal members: %w", err)
	}
	return members, nil
}

// ListAccountsForUser reverse-looks-up memberships via GSI1
// (GSI1PK=USER#{userId}, GSI1SK begins_with ACCOUNT#). GSI1 is shared with
// meeting date-sorting items whose GSI1SK is an RFC3339 timestamp, so the
// begins_with("ACCOUNT#") condition isolates membership rows.
func (r *DynamoDBRepository) ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error) {
	keyEx := expression.Key("GSI1PK").Equal(expression.Value(model.PrefixUser + userID)).
		And(expression.Key("GSI1SK").BeginsWith(model.PrefixAccount))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build accounts-for-user query: %w", err)
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("GSI1"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query accounts for user: %w", err)
	}
	members := []model.AccountMember{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &members); err != nil {
		return nil, fmt.Errorf("unmarshal memberships: %w", err)
	}
	return members, nil
}
```

> `GetUserByEmail` 는 이미 `dynamodb.go:1297`에 존재하므로 `accountRepo` 인터페이스를 자동 충족한다.

- [ ] **Step 2: 전체 백엔드 빌드 + 테스트**

Run: `cd backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...`
Expected: 성공. (`*DynamoDBRepository`가 `accountRepo`를 충족 → `NewAccountService(repo)` 컴파일.)

- [ ] **Step 3: Commit**

```bash
cd backend && git add internal/repository/account.go
git commit -m "feat(account): DynamoDBRepository account+member persistence (GSI1 reverse lookup)"
```

---

## Task 8: HTTP 핸들러 + 라우트 + 핸들러 테스트

**Files:**
- Create: `backend/internal/handler/account.go`
- Create: `backend/internal/handler/account_test.go`

- [ ] **Step 1: 핸들러 작성**

```go
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type AccountHandler struct {
	accountService *service.AccountService
}

func NewAccountHandler(accountService *service.AccountService) *AccountHandler {
	return &AccountHandler{accountService: accountService}
}

func (h *AccountHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	email := middleware.GetUserEmail(ctx)

	var req model.CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	acc, err := h.accountService.CreateAccount(ctx, userID, email, &req)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account name is required")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	// Return the freshly-created account as a response (owner is the only member).
	writeJSON(w, http.StatusCreated, model.AccountResponse{
		AccountID:   acc.AccountID,
		Name:        acc.Name,
		Aliases:     acc.Aliases,
		Domains:     acc.Domains,
		Industry:    acc.Industry,
		OwnerUserID: acc.OwnerUserID,
		Members:     []model.AccountMemberDTO{{UserID: userID, Email: email, Role: model.RoleOwner}},
		CreatedAt:   acc.CreatedAt,
	})
}

func (h *AccountHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	list, err := h.accountService.ListAccounts(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"accounts": list})
}

func (h *AccountHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	resp, err := h.accountService.GetAccount(ctx, userID, accountID)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AccountHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var req model.AddMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}
	dto, err := h.accountService.AddMember(ctx, userID, accountID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Only the owner can add members")
		case errors.Is(err, service.ErrUserNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No user with that email")
		case errors.Is(err, service.ErrMemberExists):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "User is already a member")
		case errors.Is(err, service.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid role (AM/TAM/SSA)")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}
```

- [ ] **Step 2: 핸들러 테스트 작성 (실패 먼저)**

`backend/internal/handler/account_test.go` — 기존 `withUserCtx`/`withChiParam` 헬퍼와 동일 패턴을 사용하되, account용 mock repo를 둔다. 단, 핸들러 테스트는 `email`이 필요하므로 user 컨텍스트에 email도 넣는 헬퍼를 추가한다.

```go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// withUserEmailCtx injects both userID and email into the request context.
func withUserEmailCtx(r *http.Request, userID, email string) *http.Request {
	ctx := context.WithValue(r.Context(), middleware.UserIDKey, userID)
	ctx = context.WithValue(ctx, middleware.UserEmailKey, email)
	return r.WithContext(ctx)
}

// mockHandlerAccountRepo implements service.AccountRepo for handler tests.
type mockHandlerAccountRepo struct {
	accounts map[string]*model.Account
	members  map[string]*model.AccountMember
	users    map[string]*model.User
}

func newMockHandlerAccountRepo() *mockHandlerAccountRepo {
	return &mockHandlerAccountRepo{
		accounts: make(map[string]*model.Account),
		members:  make(map[string]*model.AccountMember),
		users:    make(map[string]*model.User),
	}
}

func acctMemberKey(accountID, userID string) string { return accountID + "|" + userID }

func (m *mockHandlerAccountRepo) CreateAccount(_ context.Context, a *model.Account, owner *model.AccountMember) error {
	ac := *a
	m.accounts[a.AccountID] = &ac
	oc := *owner
	m.members[acctMemberKey(owner.AccountID, owner.UserID)] = &oc
	return nil
}
func (m *mockHandlerAccountRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	a, ok := m.accounts[id]
	if !ok {
		return nil, nil
	}
	c := *a
	return &c, nil
}
func (m *mockHandlerAccountRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	v, ok := m.members[acctMemberKey(accountID, userID)]
	if !ok {
		return nil, nil
	}
	c := *v
	return &c, nil
}
func (m *mockHandlerAccountRepo) PutMember(_ context.Context, member *model.AccountMember) error {
	c := *member
	m.members[acctMemberKey(member.AccountID, member.UserID)] = &c
	return nil
}
func (m *mockHandlerAccountRepo) ListAccountMembers(_ context.Context, accountID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, v := range m.members {
		if v.AccountID == accountID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (m *mockHandlerAccountRepo) ListAccountsForUser(_ context.Context, userID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, v := range m.members {
		if v.UserID == userID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (m *mockHandlerAccountRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	c := *u
	return &c, nil
}

func newStubAccountHandler() (*AccountHandler, *mockHandlerAccountRepo) {
	repo := newMockHandlerAccountRepo()
	svc := service.NewAccountServiceForTest(repo)
	return &AccountHandler{accountService: svc}, repo
}

func TestHandlerCreateAccount_Created(t *testing.T) {
	h, repo := newStubAccountHandler()
	body, _ := json.Marshal(model.CreateAccountRequest{Name: "하나은행"})
	r := httptest.NewRequest(http.MethodPost, "/api/accounts", bytes.NewReader(body))
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	w := httptest.NewRecorder()

	h.CreateAccount(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var resp model.AccountResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.AccountID == "" || resp.OwnerUserID != "owner-1" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if _, ok := repo.accounts[resp.AccountID]; !ok {
		t.Error("account not persisted")
	}
}

func TestHandlerGetAccount_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	// seed an account owned by someone else
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.GetAccount(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

var _ = chi.URLParam // ensure chi import is used if helpers are trimmed
```

> `withChiParam` 와 `middleware.UserEmailKey` 는 기존 `handler` 패키지/미들웨어에 이미 존재한다(`meeting_test.go`, `middleware/auth.go`). `var _ = chi.URLParam` 는 import 안전장치이며, 다른 테스트가 chi를 쓰면 제거해도 된다.

- [ ] **Step 3: 테스트 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run 'TestHandler(CreateAccount|GetAccount)' ./internal/handler/`
Expected: 처음엔 컴파일 에러(핸들러 미존재) → Step 1 작성 후엔 PASS. 핸들러를 먼저 썼다면 바로 PASS.

- [ ] **Step 4: 핸들러 패키지 전체 테스트**

Run: `cd backend && /usr/local/go/bin/go test ./internal/handler/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/handler/account.go internal/handler/account_test.go
git commit -m "feat(account): HTTP handler (create/get/list/add-member) + handler tests"
```

---

## Task 9: 라우트 등록 + 와이어링 + 바이너리 빌드

**Files:**
- Modify: `backend/cmd/api/main.go`

- [ ] **Step 1: `init()`에 서비스·핸들러 구성 추가**

`main.go`에서 `meetingService := ...` 인근(서비스 생성 구간)에 추가:

```go
	accountService := service.NewAccountService(repo)
```

핸들러 생성 구간(`meetingHandler := handler.NewMeetingHandler(...)` 인근)에 추가:

```go
	accountHandler := handler.NewAccountHandler(accountService)
```

- [ ] **Step 2: 인증 그룹에 라우트 등록**

`r.Group(func(r chi.Router){ r.Use(middleware.Auth); ... })` 블록 안, 미팅 라우트 옆에 추가:

```go
		r.Get("/api/accounts", accountHandler.ListAccounts)
		r.Post("/api/accounts", accountHandler.CreateAccount)
		r.Get("/api/accounts/{accountId}", accountHandler.GetAccount)
		r.Post("/api/accounts/{accountId}/members", accountHandler.AddMember)
```

- [ ] **Step 3: 전체 빌드 + 테스트 + api 바이너리 크로스컴파일**

```bash
cd backend && /usr/local/go/bin/go build ./... \
  && /usr/local/go/bin/go test ./internal/... \
  && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```
Expected: 모두 성공, `cmd/api/bootstrap` 생성.

- [ ] **Step 4: Commit**

```bash
cd backend && git add cmd/api/main.go cmd/api/bootstrap
git commit -m "feat(account): wire AccountService/Handler and register /api/accounts routes"
```

---

## Task 10: 문서 동기화 (API-SPEC)

CLAUDE.md Auto-Sync 규칙: 핸들러 변경 시 `docs/API-SPEC.md` 갱신.

**Files:**
- Modify: `docs/API-SPEC.md`

- [ ] **Step 1: Account 엔드포인트 섹션 추가**

`docs/API-SPEC.md`에 다음 4개 엔드포인트를 기존 포맷에 맞춰 기술한다:
- `GET /api/accounts` → `{ "accounts": [{accountId,name,role}] }`
- `POST /api/accounts` (body `{name,aliases?,domains?,industry?}`) → 201 `AccountResponse`
- `GET /api/accounts/{accountId}` → `AccountResponse` (403 비멤버, 404 없음)
- `POST /api/accounts/{accountId}/members` (body `{email,role}`) → 201 `{userId,email,role}` (403 owner아님, 404 이메일없음, 400 중복/잘못된역할)

에러는 표준 `{"error":{"code","message"}}` 포맷 사용.

- [ ] **Step 2: Commit**

```bash
git add docs/API-SPEC.md
git commit -m "docs(api): add /api/accounts endpoints to API-SPEC"
```

---

## CDK 메모 (이 플랜에서 인프라 변경 없음)

- 새 GSI 불필요: 멤버십 역조회는 기존 **GSI1**(`GSI1PK`/`GSI1SK`) 재사용 + `begins_with("ACCOUNT#")`.
- IAM 변경 불필요: `props.table.grantReadWriteData(this.apiRole)`(`ai-stack.ts:84`)가 GSI 포함 전체 접근을 이미 부여.
- 따라서 `cd infra && npm test`는 그대로 통과해야 한다(회귀 확인용으로 1회 실행 권장).

---

## Self-Review (작성자 체크)

- **Spec 커버리지(Plan 1 범위):** §5.1 공유 Account ✅(ACCOUNT# 파티션), §5.2 명시 등록 ✅(CreateAccount), §5.3 그룹=멤버십 ✅(MEMBER# + 역할), §6.1 META/MEMBER 아이템 ✅, §6.3 GSI1 역조회 ✅, §12 권한(멤버십 검사·owner 전용) ✅. (별칭→Account 해석은 Plan 2, 인사이트는 Plan 3로 이월 — 의도된 범위.)
- **Placeholder 스캔:** "TBD"/"적절히 처리" 없음. 모든 코드 스텝에 실제 코드 포함. ✅
- **타입 일관성:** `CreateAccount(ctx, ownerUserID, ownerEmail, *CreateAccountRequest)`, `GetMember(ctx, accountID, userID)`, `ListAccountsForUser(ctx, userID)`, `AddMember(...) (*AccountMemberDTO, error)` — 인터페이스·mock·서비스·핸들러·테스트 전반에서 시그니처 동일. 역할 상수 `RoleOwner/AM/TAM/SSA`, 센티넬 `ErrInvalidInput/ErrMemberExists`(+기존 `ErrForbidden/ErrNotFound/ErrUserNotFound`) 일관. ✅
- **확인 필요(구현자):** `middleware.UserEmailKey` 키 이름이 정확한지 `middleware/auth.go`에서 확인(없으면 동등 키 사용). `GetUserByEmail` 반환 `*model.User`에 `UserID`/`Email` 존재 — 확인됨(`meeting.go:85-95`).
