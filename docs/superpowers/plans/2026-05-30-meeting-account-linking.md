# Meeting ↔ Account Linking & Sharing — Implementation Plan (Plan 2 of 6)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 미팅을 Account에 연결(분류)하고, "Account 그룹에 공유"하면 그 Account 멤버 전원에게 read 권한을 부여하면서 Account 파티션에 `MEETINGREF#`를 적립한다. 더불어 태그 별칭 → Account 해석을 제공한다.

**Architecture:** `Meeting`에 `accountId`/`sharedToAccount` 속성을 추가한다. 공유 액션은 기존 `Share`(read) 메커니즘을 재사용해 Account 멤버마다 share를 생성하고, `ACCOUNT#{accountId}` 파티션에 `MEETINGREF#{occurredAt}#{meetingId}` 아이템을 적립한다(팀이 cross-partition 스캔 없이 공유 미팅 메타를 나열). 미팅 필드 갱신은 기존 `UpdateMeeting`(전체 구조체 저장)을 재사용해 `meetingRepo` 인터페이스 변경을 최소화한다.

**Tech Stack:** Go 1.25 (AWS SDK v2), stdlib `testing`(testify 없음), CDK 변경 없음(신규 GSI 불필요 — `MEETINGREF#`는 Account 파티션 내 SK range 쿼리).

**선행:** Plan 1(Account 엔티티) 완료, branch `feat/account-foundation`.

---

## 핵심 설계 결정 (요약)

- **두 액션 구분:** `Link`(accountId만 설정, 비공개=_Private) vs `Share`(accountId + sharedToAccount=true + 멤버별 Share read + MEETINGREF 적립). 스펙 §6.2.
- **공유 = 인사이트/메타 풀에 기여 + 원문 read 부여.** 멤버별 `CreateShare(... PermissionRead)` (owner 자신은 제외).
- **필드 갱신은 `UpdateMeeting`(전체 구조체) 재사용** → `meetingRepo`에 `UpdateMeetingFields` 추가 불필요. 추가하는 메서드는 `GetMember`, `ListAccountMembers`, `PutMeetingRef` 3개뿐.
- **MEETINGREF SK = `MEETINGREF#{date RFC3339}#{meetingId}`** (occurredAt 정렬 — Plan 3 인사이트 키 전략과 동일 원칙).
- **권한:** 공유/연결은 미팅 owner만; 그리고 owner는 그 Account의 멤버여야 함. 비멤버는 `ErrForbidden`.

## File Structure (Plan 2)

| 파일 | 변경 |
|---|---|
| `backend/internal/model/account.go` | `MeetingRef` + `PrefixMeetingRef`/`EntityTypeMeetingRef`, meeting-account DTO/요청 추가 |
| `backend/internal/model/meeting.go` | `Meeting`에 `AccountID`/`SharedToAccount` 필드 추가 |
| `backend/internal/service/meeting.go` | `meetingRepo`에 3메서드 추가 + `LinkMeetingToAccount`/`ShareMeetingToAccount` |
| `backend/internal/service/meeting_test.go` | `mockMeetingRepo`에 3메서드+필드, 테스트 |
| `backend/internal/service/account.go` | `accountRepo`에 1메서드, `ResolveAccountByAlias`/`ListAccountMeetings`, `ErrAmbiguousAlias` |
| `backend/internal/service/account_test.go` | `mockAccountRepo`에 1메서드+필드, 테스트 |
| `backend/internal/repository/account.go` | `PutMeetingRef`/`ListMeetingRefsForAccount` |
| `backend/internal/handler/meeting.go` | `LinkToAccount` 핸들러 |
| `backend/internal/handler/share.go` | `ShareToAccount` 핸들러 |
| `backend/internal/handler/account.go` | `ListAccountMeetings` 핸들러 |
| `backend/internal/handler/*_test.go` | 핸들러 테스트 + mock 3메서드/1메서드 동기화 |
| `backend/cmd/api/main.go` | 라우트 등록 |
| `docs/API-SPEC.md` | 신규 엔드포인트 |

---

## Task 1: 모델 — Meeting 필드 + MeetingRef + DTO + 센티넬

**Files:**
- Modify: `backend/internal/model/meeting.go`
- Modify: `backend/internal/model/account.go`

- [ ] **Step 1: `Meeting`에 필드 추가**

`meeting.go`의 `Meeting` struct에서 `LinkedMeetingIDs` 줄 바로 다음에 추가:

```go
	AccountID          string            `dynamodbav:"accountId,omitempty"`       // linked Account
	SharedToAccount    bool              `dynamodbav:"sharedToAccount,omitempty"` // published to account team
```

- [ ] **Step 2: `account.go`에 MeetingRef + DTO + 요청 타입 추가**

`account.go` 끝에 추가:

```go
const (
	PrefixMeetingRef     = "MEETINGREF#"
	EntityTypeMeetingRef = "MEETING_REF"
)

// MeetingRef is a lightweight reference to a meeting shared into an account,
// stored in the account partition so members can list shared meetings without
// a cross-partition scan. PK: ACCOUNT#{accountId}, SK: MEETINGREF#{occurredAt}#{meetingId}.
type MeetingRef struct {
	PK          string    `dynamodbav:"PK"`
	SK          string    `dynamodbav:"SK"`
	AccountID   string    `dynamodbav:"accountId"`
	MeetingID   string    `dynamodbav:"meetingId"`
	OwnerUserID string    `dynamodbav:"ownerUserId"`
	Title       string    `dynamodbav:"title,omitempty"`
	Date        time.Time `dynamodbav:"date"`
	EntityType  string    `dynamodbav:"entityType"` // "MEETING_REF"
}

// --- meeting↔account request/response DTOs ---

type LinkAccountRequest struct {
	AccountID string `json:"accountId"`
}

type ShareToAccountRequest struct {
	AccountID string `json:"accountId"`
}

type ShareToAccountResult struct {
	AccountID  string `json:"accountId"`
	SharedWith int    `json:"sharedWith"` // number of account members granted read
}

type MeetingRefDTO struct {
	MeetingID   string    `json:"meetingId"`
	OwnerUserID string    `json:"ownerUserId"`
	Title       string    `json:"title"`
	Date        time.Time `json:"date"`
}
```

- [ ] **Step 3: 빌드 확인**

Run: `cd backend && /usr/local/go/bin/go build ./internal/model/`
Expected: 성공.

- [ ] **Step 4: Commit**

```bash
cd backend && git add internal/model/meeting.go internal/model/account.go
git commit -m "feat(account): Meeting account fields + MeetingRef model + DTOs"
```

---

## Task 2: 인터페이스 + mock 확장

`meetingRepo`에 `GetMember`/`ListAccountMembers`/`PutMeetingRef` 추가, `accountRepo`에 `ListMeetingRefsForAccount` 추가. 4개 mock(서비스/핸들러 × 미팅/계정)을 동기화하지 않으면 컴파일 실패한다.

**Files:**
- Modify: `backend/internal/service/meeting.go`, `meeting_test.go`
- Modify: `backend/internal/service/account.go`, `account_test.go`
- Modify: `backend/internal/handler/meeting_test.go`, `account_test.go`

- [ ] **Step 1: `meetingRepo` 인터페이스에 3메서드 추가**

`service/meeting.go`의 `meetingRepo` 인터페이스 끝(`DeleteShare` 다음 줄)에 추가:

```go
	GetMember(ctx context.Context, accountID, userID string) (*model.AccountMember, error)
	ListAccountMembers(ctx context.Context, accountID string) ([]model.AccountMember, error)
	PutMeetingRef(ctx context.Context, ref *model.MeetingRef) error
```

- [ ] **Step 2: `accountRepo` 인터페이스에 1메서드 추가**

`service/account.go`의 `accountRepo` 인터페이스 끝(`GetUserByEmail` 다음 줄)에 추가:

```go
	ListMeetingRefsForAccount(ctx context.Context, accountID string) ([]model.MeetingRef, error)
```

- [ ] **Step 3: `mockMeetingRepo`(service/meeting_test.go) 동기화**

struct 정의에 필드 2개 추가:

```go
	members     map[string]*model.AccountMember // "accountID|userID"
	meetingRefs map[string][]model.MeetingRef   // accountID -> refs
```

`newMockMeetingRepo()` 초기화에 추가:

```go
		members:     make(map[string]*model.AccountMember),
		meetingRefs: make(map[string][]model.MeetingRef),
```

파일 끝에 메서드 3개 추가:

```go
func (m *mockMeetingRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	mem, ok := m.members[accountID+"|"+userID]
	if !ok {
		return nil, nil
	}
	cp := *mem
	return &cp, nil
}

func (m *mockMeetingRepo) ListAccountMembers(_ context.Context, accountID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, mem := range m.members {
		if mem.AccountID == accountID {
			out = append(out, *mem)
		}
	}
	return out, nil
}

func (m *mockMeetingRepo) PutMeetingRef(_ context.Context, ref *model.MeetingRef) error {
	m.meetingRefs[ref.AccountID] = append(m.meetingRefs[ref.AccountID], *ref)
	return nil
}
```

- [ ] **Step 4: `mockHandlerMeetingRepo`(handler/meeting_test.go) 동기화**

struct에 동일 필드 2개 추가, `newMockHandlerMeetingRepo()`에 동일 초기화 2줄 추가, 파일 끝에 위 3개 메서드를 `(m *mockHandlerMeetingRepo)` 리시버로 동일하게 추가(본문 동일).

- [ ] **Step 5: `mockAccountRepo`(service/account_test.go) 동기화**

struct에 추가:

```go
	meetingRefs map[string][]model.MeetingRef // accountID -> refs
```

`newMockAccountRepo()`에 추가:

```go
		meetingRefs: make(map[string][]model.MeetingRef),
```

파일 끝에 메서드 추가:

```go
func (m *mockAccountRepo) ListMeetingRefsForAccount(_ context.Context, accountID string) ([]model.MeetingRef, error) {
	return append([]model.MeetingRef(nil), m.meetingRefs[accountID]...), nil
}
```

- [ ] **Step 6: `mockHandlerAccountRepo`(handler/account_test.go) 동기화**

struct에 `meetingRefs map[string][]model.MeetingRef` 추가, 생성자에 init 추가, 파일 끝에 위 `ListMeetingRefsForAccount`를 `(m *mockHandlerAccountRepo)` 리시버로 동일하게 추가.

- [ ] **Step 7: 빌드 + 기존 테스트 회귀 확인**

Run: `cd backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...`
Expected: 성공(기존 Plan 1 테스트 전부 통과; 컴파일 OK).

> 만약 `*repository.DynamoDBRepository`가 `PutMeetingRef`/`ListMeetingRefsForAccount`를 아직 구현하지 않아 빌드가 깨지면 — Task 7의 repo 메서드를 먼저 작성하라(컴파일 의존성). Plan 1에서와 동일한 순서 보정이며 최종 상태는 동일하다.

- [ ] **Step 8: Commit**

```bash
cd backend && git add internal/service/meeting.go internal/service/account.go internal/service/meeting_test.go internal/service/account_test.go internal/handler/meeting_test.go internal/handler/account_test.go
git commit -m "feat(account): extend repo interfaces + mocks for meeting-account linking"
```

---

## Task 3: AccountService.ResolveAccountByAlias

태그(별칭)로 Account를 찾는다. 0개 → `(nil, ErrNotFound)`, 1개 → 그 Account, 2개+ → `ErrAmbiguousAlias`(스펙 §12 충돌은 자동 선택 금지).

**Files:**
- Modify: `backend/internal/service/account.go`, `account_test.go`

- [ ] **Step 1: 실패 테스트 작성**

`account_test.go`에 추가:

```go
func TestResolveAccountByAlias_Unique(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "하나은행", Aliases: []string{"하나은행", "Hana Bank"}})

	got, err := svc.ResolveAccountByAlias(context.Background(), "u1", "Hana Bank")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.AccountID != acc.AccountID {
		t.Errorf("expected to resolve to %s, got %+v", acc.AccountID, got)
	}
}

func TestResolveAccountByAlias_NotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, _ = svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "하나은행", Aliases: []string{"하나은행"}})
	_, err := svc.ResolveAccountByAlias(context.Background(), "u1", "없는태그")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveAccountByAlias_Ambiguous(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, _ = svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "A", Aliases: []string{"공통"}})
	_, _ = svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "B", Aliases: []string{"공통"}})
	_, err := svc.ResolveAccountByAlias(context.Background(), "u1", "공통")
	if !errors.Is(err, ErrAmbiguousAlias) {
		t.Errorf("expected ErrAmbiguousAlias, got %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestResolveAccountByAlias ./internal/service/`
Expected: FAIL — `svc.ResolveAccountByAlias undefined`, `ErrAmbiguousAlias undefined`.

- [ ] **Step 3: 구현**

`account.go`의 센티넬 블록에 추가:

```go
	ErrAmbiguousAlias = errors.New("alias maps to multiple accounts")
```

메서드 추가(대소문자 무시 비교):

```go
// ResolveAccountByAlias finds, among the user's accounts, the one whose aliases
// (or name) match the given tag. Returns ErrNotFound if none, ErrAmbiguousAlias
// if more than one (never auto-pick).
func (s *AccountService) ResolveAccountByAlias(ctx context.Context, userID, alias string) (*model.Account, error) {
	memberships, err := s.repo.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimSpace(alias))
	var matches []*model.Account
	for _, m := range memberships {
		account, err := s.repo.GetAccount(ctx, m.AccountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue
		}
		if strings.ToLower(account.Name) == target {
			matches = append(matches, account)
			continue
		}
		for _, a := range account.Aliases {
			if strings.ToLower(strings.TrimSpace(a)) == target {
				matches = append(matches, account)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return nil, ErrAmbiguousAlias
	}
}
```

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestResolveAccountByAlias ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(account): ResolveAccountByAlias (tag→account, ambiguity guarded)"
```

---

## Task 4: MeetingService.LinkMeetingToAccount

미팅을 Account에 연결(분류)만 한다. owner 전용 + owner가 해당 Account 멤버여야 함.

**Files:**
- Modify: `backend/internal/service/meeting.go`, `meeting_test.go`

- [ ] **Step 1: 실패 테스트 작성**

`meeting_test.go`에 헬퍼와 테스트 추가:

```go
func (m *mockMeetingRepo) addMember(accountID, userID, role string) {
	m.members[accountID+"|"+userID] = &model.AccountMember{AccountID: accountID, UserID: userID, Role: role}
}

func TestLinkMeetingToAccount_OwnerMember(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "T", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)

	if err := svc.LinkMeetingToAccount(context.Background(), "owner-1", "m-1", "acc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := repo.meetings[meetingKey("owner-1", "m-1")]
	if got.AccountID != "acc-1" {
		t.Errorf("expected accountId acc-1, got %s", got.AccountID)
	}
	if got.SharedToAccount {
		t.Error("link must not set SharedToAccount")
	}
}

func TestLinkMeetingToAccount_NotMemberForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	// owner-1 is NOT a member of acc-1
	err := svc.LinkMeetingToAccount(context.Background(), "owner-1", "m-1", "acc-1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestLinkMeetingToAccount_NotOwner(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	repo.addMember("acc-1", "intruder-9", model.RoleSSA)
	err := svc.LinkMeetingToAccount(context.Background(), "intruder-9", "m-1", "acc-1")
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrNotFound/ErrForbidden for non-owner, got %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestLinkMeetingToAccount ./internal/service/`
Expected: FAIL — `svc.LinkMeetingToAccount undefined`.

- [ ] **Step 3: 구현**

`meeting.go`에 추가:

```go
// LinkMeetingToAccount classifies a meeting under an account (no sharing).
// Only the meeting owner who is a member of the account may do this.
func (s *MeetingService) LinkMeetingToAccount(ctx context.Context, ownerID, meetingID, accountID string) error {
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		if existing, _ := s.repo.GetMeetingByID(ctx, meetingID); existing != nil {
			return ErrForbidden
		}
		return ErrNotFound
	}
	member, err := s.repo.GetMember(ctx, accountID, ownerID)
	if err != nil {
		return err
	}
	if member == nil {
		return ErrForbidden
	}
	meeting.AccountID = accountID
	return s.repo.UpdateMeeting(ctx, meeting)
}
```

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestLinkMeetingToAccount ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/meeting.go internal/service/meeting_test.go
git commit -m "feat(account): LinkMeetingToAccount (owner+member only, classify private)"
```

---

## Task 5: MeetingService.ShareMeetingToAccount

owner 전용 + owner가 멤버. `accountId`+`sharedToAccount=true` 설정 → 멤버별 `Share(read)` 생성(owner 제외) → `MEETINGREF#` 적립.

**Files:**
- Modify: `backend/internal/service/meeting.go`, `meeting_test.go`

- [ ] **Step 1: 실패 테스트 작성**

```go
func TestShareMeetingToAccount_GrantsAndRefs(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Title: "ROSA 리뷰", Status: model.StatusDone})
	repo.addMember("acc-1", "owner-1", model.RoleOwner)
	repo.addMember("acc-1", "tam-1", model.RoleTAM)
	repo.addMember("acc-1", "ssa-1", model.RoleSSA)

	res, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SharedWith != 2 { // tam-1, ssa-1 (owner excluded)
		t.Errorf("expected 2 shares, got %d", res.SharedWith)
	}
	got := repo.meetings[meetingKey("owner-1", "m-1")]
	if got.AccountID != "acc-1" || !got.SharedToAccount {
		t.Errorf("expected accountId+sharedToAccount set, got %+v", got)
	}
	if repo.shares[shareKey("tam-1", "m-1")] == nil || repo.shares[shareKey("ssa-1", "m-1")] == nil {
		t.Error("expected shares created for tam-1 and ssa-1")
	}
	if repo.shares[shareKey("owner-1", "m-1")] != nil {
		t.Error("owner must not be shared to themselves")
	}
	if len(repo.meetingRefs["acc-1"]) != 1 || repo.meetingRefs["acc-1"][0].MeetingID != "m-1" {
		t.Errorf("expected 1 meeting ref for acc-1, got %+v", repo.meetingRefs["acc-1"])
	}
}

func TestShareMeetingToAccount_NotMemberForbidden(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	_, err := svc.ShareMeetingToAccount(context.Background(), "owner-1", "o@x.com", "m-1", "acc-1")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}
```

> `mockMeetingRepo.CreateShare`는 이미 `m.shares[shareKey(sharedToID, meetingID)]`에 기록한다(서비스 mock 기존 구현). 확인만 하라.

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestShareMeetingToAccount ./internal/service/`
Expected: FAIL — `svc.ShareMeetingToAccount undefined`.

- [ ] **Step 3: 구현**

`meeting.go`에 추가:

```go
// ShareMeetingToAccount publishes a meeting to an account team: sets
// accountId+sharedToAccount, grants read Share to every account member
// (except the owner), and writes a MeetingRef into the account partition.
func (s *MeetingService) ShareMeetingToAccount(ctx context.Context, ownerID, ownerEmail, meetingID, accountID string) (*model.ShareToAccountResult, error) {
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		if existing, _ := s.repo.GetMeetingByID(ctx, meetingID); existing != nil {
			return nil, ErrForbidden
		}
		return nil, ErrNotFound
	}
	owner, err := s.repo.GetMember(ctx, accountID, ownerID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, ErrForbidden
	}

	meeting.AccountID = accountID
	meeting.SharedToAccount = true
	if err := s.repo.UpdateMeeting(ctx, meeting); err != nil {
		return nil, err
	}

	members, err := s.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	shared := 0
	for _, m := range members {
		if m.UserID == ownerID {
			continue
		}
		if _, err := s.repo.CreateShare(ctx, meetingID, ownerID, ownerEmail, m.UserID, m.Email, model.PermissionRead); err != nil {
			return nil, err
		}
		shared++
	}

	ref := &model.MeetingRef{
		PK:          model.PrefixAccount + accountID,
		SK:          model.PrefixMeetingRef + meeting.Date.UTC().Format(time.RFC3339) + "#" + meetingID,
		AccountID:   accountID,
		MeetingID:   meetingID,
		OwnerUserID: ownerID,
		Title:       meeting.Title,
		Date:        meeting.Date,
		EntityType:  model.EntityTypeMeetingRef,
	}
	if err := s.repo.PutMeetingRef(ctx, ref); err != nil {
		return nil, err
	}

	return &model.ShareToAccountResult{AccountID: accountID, SharedWith: shared}, nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestShareMeetingToAccount ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/meeting.go internal/service/meeting_test.go
git commit -m "feat(account): ShareMeetingToAccount (member shares + MeetingRef)"
```

---

## Task 6: AccountService.ListAccountMeetings

Account 멤버가 그 Account의 공유 미팅(MeetingRef) 목록을 조회. 비멤버 → `ErrForbidden`.

**Files:**
- Modify: `backend/internal/service/account.go`, `account_test.go`

- [ ] **Step 1: 실패 테스트 작성**

```go
func TestListAccountMeetings_MemberOnly(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{
		{AccountID: acc.AccountID, MeetingID: "m-1", OwnerUserID: "owner-1", Title: "ROSA"},
	}

	list, err := svc.ListAccountMeetings(context.Background(), "owner-1", acc.AccountID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].MeetingID != "m-1" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestListAccountMeetings_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.ListAccountMeetings(context.Background(), "stranger-9", acc.AccountID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestListAccountMeetings ./internal/service/`
Expected: FAIL — `svc.ListAccountMeetings undefined`.

- [ ] **Step 3: 구현**

`account.go`에 추가:

```go
// ListAccountMeetings returns the shared-meeting references for an account.
// Only members may read; non-members get ErrForbidden, missing account ErrNotFound.
func (s *AccountService) ListAccountMeetings(ctx context.Context, userID, accountID string) ([]model.MeetingRefDTO, error) {
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
	refs, err := s.repo.ListMeetingRefsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]model.MeetingRefDTO, 0, len(refs))
	for _, r := range refs {
		out = append(out, model.MeetingRefDTO{MeetingID: r.MeetingID, OwnerUserID: r.OwnerUserID, Title: r.Title, Date: r.Date})
	}
	return out, nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestListAccountMeetings ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd backend && git add internal/service/account.go internal/service/account_test.go
git commit -m "feat(account): ListAccountMeetings (member-gated MeetingRef list)"
```

---

## Task 7: Repository — PutMeetingRef + ListMeetingRefsForAccount

**Files:**
- Modify: `backend/internal/repository/account.go`

- [ ] **Step 1: 메서드 추가**

`repository/account.go` 끝에 추가:

```go
// PutMeetingRef writes a MeetingRef item (caller builds PK/SK).
func (r *DynamoDBRepository) PutMeetingRef(ctx context.Context, ref *model.MeetingRef) error {
	item, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return fmt.Errorf("marshal meeting ref: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("put meeting ref: %w", err)
	}
	return nil
}

// ListMeetingRefsForAccount queries the account partition for MEETINGREF# items
// (sorted by occurredAt via the SK prefix).
func (r *DynamoDBRepository) ListMeetingRefsForAccount(ctx context.Context, accountID string) ([]model.MeetingRef, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixAccount + accountID)).
		And(expression.Key("SK").BeginsWith(model.PrefixMeetingRef))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build meeting refs query: %w", err)
	}
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false), // newest first
	})
	if err != nil {
		return nil, fmt.Errorf("query meeting refs: %w", err)
	}
	refs := []model.MeetingRef{}
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &refs); err != nil {
		return nil, fmt.Errorf("unmarshal meeting refs: %w", err)
	}
	return refs, nil
}
```

- [ ] **Step 2: 전체 빌드 + 테스트**

Run: `cd backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...`
Expected: 성공(이제 concrete repo가 `meetingRepo`/`accountRepo` 확장을 충족).

- [ ] **Step 3: Commit**

```bash
cd backend && git add internal/repository/account.go
git commit -m "feat(account): repo PutMeetingRef + ListMeetingRefsForAccount"
```

---

## Task 8: 핸들러 + 라우트 + 핸들러 테스트

**Files:**
- Modify: `backend/internal/handler/meeting.go`, `share.go`, `account.go`
- Modify: `backend/internal/handler/meeting_test.go`, `account_test.go`

- [ ] **Step 1: `MeetingHandler.LinkToAccount` 작성** (`handler/meeting.go` 끝, 같은 파일의 `writeError`/`writeJSON` 재사용)

```go
func (h *MeetingHandler) LinkToAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}
	var req model.LinkAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	if err := h.meetingService.LinkMeetingToAccount(ctx, userID, meetingID, req.AccountID); err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"accountId": req.AccountID})
}
```

> `handler/meeting.go`가 `errors`/`service` 패키지를 이미 import하는지 확인하고 없으면 추가.

- [ ] **Step 2: `ShareHandler.ShareToAccount` 작성** (`handler/share.go` 끝)

```go
func (h *ShareHandler) ShareToAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	userEmail := middleware.GetUserEmail(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	if meetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}
	var req model.ShareToAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "accountId is required")
		return
	}
	res, err := h.meetingService.ShareMeetingToAccount(ctx, userID, userEmail, meetingID, req.AccountID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, res)
}
```

> `handler/share.go`가 `encoding/json`/`errors`/`service` import를 갖는지 확인하고 없으면 추가.

- [ ] **Step 3: `AccountHandler.ListAccountMeetings` 작성** (`handler/account.go` 끝)

```go
func (h *AccountHandler) ListAccountMeetings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	list, err := h.accountService.ListAccountMeetings(ctx, userID, accountID)
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"meetings": list})
}
```

- [ ] **Step 4: 핸들러 테스트 작성**

`handler/account_test.go`에 추가(공유 미팅 목록 멤버 게이트):

```go
func TestHandlerListAccountMeetings_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/meetings", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.ListAccountMeetings(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}
```

`handler/meeting_test.go`에 추가(연결 owner 검증) — `newStubMeetingHandler()` 재사용:

```go
func TestHandlerLinkToAccount_OK(t *testing.T) {
	h, repo := newStubMeetingHandler()
	repo.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone})
	repo.members["acc-1|owner-1"] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	body, _ := json.Marshal(model.LinkAccountRequest{AccountID: "acc-1"})
	r := httptest.NewRequest(http.MethodPost, "/api/meetings/m-1/account", bytes.NewReader(body))
	r = withUserCtx(r, "owner-1")
	r = withChiParam(r, "meetingId", "m-1")
	w := httptest.NewRecorder()

	h.LinkToAccount(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	if repo.meetings[hKey("owner-1", "m-1")].AccountID != "acc-1" {
		t.Error("accountId not set on meeting")
	}
}
```

> `mockHandlerMeetingRepo`는 Task 2에서 `members` 필드 + `GetMember`를 이미 가졌다. `withUserCtx`는 email을 비우지만 Link는 email이 필요 없으니 OK.

- [ ] **Step 5: 핸들러 패키지 테스트**

Run: `cd backend && /usr/local/go/bin/go test ./internal/handler/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd backend && git add internal/handler/meeting.go internal/handler/share.go internal/handler/account.go internal/handler/meeting_test.go internal/handler/account_test.go
git commit -m "feat(account): link/share-to-account + list-account-meetings handlers"
```

---

## Task 9: 라우트 등록 + 빌드 + API-SPEC

**Files:**
- Modify: `backend/cmd/api/main.go`
- Modify: `docs/API-SPEC.md`

- [ ] **Step 1: 라우트 추가** (인증 그룹 내, Account/Share 라우트 옆)

```go
		r.Post("/api/meetings/{meetingId}/account", meetingHandler.LinkToAccount)
		r.Post("/api/meetings/{meetingId}/share-account", shareHandler.ShareToAccount)
		r.Get("/api/accounts/{accountId}/meetings", accountHandler.ListAccountMeetings)
```

- [ ] **Step 2: 전체 빌드 + 테스트 + ARM64**

```bash
cd backend && /usr/local/go/bin/go build ./... \
  && /usr/local/go/bin/go test ./internal/... \
  && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```
Expected: 모두 성공.

- [ ] **Step 3: API-SPEC 갱신**

`docs/API-SPEC.md`에 3개 엔드포인트 추가(표준 에러 포맷):
- `POST /api/meetings/{meetingId}/account` body `{accountId}` → 200 `{accountId}` (403/404)
- `POST /api/meetings/{meetingId}/share-account` body `{accountId}` → 200 `{accountId,sharedWith}` (403/404)
- `GET /api/accounts/{accountId}/meetings` → `{meetings:[{meetingId,ownerUserId,title,date}]}` (403/404)

- [ ] **Step 4: Commit**

```bash
cd backend && git add cmd/api/main.go && git -C .. add docs/API-SPEC.md
git commit -m "feat(account): register meeting-account routes + API-SPEC"
```

---

## CDK 메모
인프라 변경 없음. `MEETINGREF#`는 Account 파티션 내 SK range 쿼리(신규 GSI 불필요). `grantReadWriteData`가 이미 전체 접근 부여.

## Self-Review (작성자 체크)
- **Spec 커버리지(Plan 2):** §5.2 태그 매핑 → `ResolveAccountByAlias` ✅; §6.1 `MEETINGREF#` ✅; §6.2 "공유=accountId+sharedToAccount+멤버 share+ref" ✅(ShareMeetingToAccount); §5.1 "내 미팅은 내 것"=owner만 공유, 원문은 Share로만 노출 ✅; §12 별칭 충돌 자동선택 금지 → `ErrAmbiguousAlias` ✅; 멤버 게이트 ✅.
- **Placeholder:** 없음. 모든 스텝 실제 코드.
- **타입 일관성:** `LinkMeetingToAccount(ctx, ownerID, meetingID, accountID) error`, `ShareMeetingToAccount(ctx, ownerID, ownerEmail, meetingID, accountID) (*ShareToAccountResult, error)`, `ResolveAccountByAlias(ctx, userID, alias) (*Account, error)`, `ListAccountMeetings(ctx, userID, accountID) ([]MeetingRefDTO, error)` — 인터페이스/mock/핸들러/테스트 일치. 신규 인터페이스 메서드 `GetMember`/`ListAccountMembers`/`PutMeetingRef`(meetingRepo), `ListMeetingRefsForAccount`(accountRepo) — Task 2에서 4개 mock 모두 동기화. ✅
- **순서 보정:** Task 2 빌드는 Task 7 repo 메서드가 있어야 통과(컴파일 의존) — Task 2 Step 7 노트에 명시.
- **확인 필요(구현자):** `handler/meeting.go`/`share.go`의 import에 `errors`/`service`/`encoding/json` 유무 확인 후 보강. `mockMeetingRepo.UpdateMeeting`이 `m.meetings[meetingKey(meeting.UserID, meeting.MeetingID)]`에 저장하는지 확인(테스트가 그 가정에 의존).
