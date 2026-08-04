# Pending Email Invites Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Inviting/sharing to an email that hasn't signed up yet succeeds immediately (instead of 404 "user not found") and materializes into a real grant automatically the moment that person's account provisions.

**Architecture:** One new item shape, `PendingInvite` (PK `PENDINGINVITE#{lowercased email}`, SK `{TYPE}#{entityId}`), written by each of 5 email-based grant flows (meeting share, doc share, research share, account member, project member) instead of erroring on an unresolved email. Reconciled in exactly one place — `DynamoDBRepository.GetOrCreateUser`'s existing "brand-new profile" branch — which materializes every pending row for that email into the real `Share`/`AccountMember`/`ProjectMember` via the same repo methods the live path already uses, then deletes the pending row.

**Tech Stack:** Go (backend/Lambda, DynamoDB single-table), Next.js/TypeScript (frontend).

## Global Constraints

- Go binary path: `/usr/local/go/bin/go` (not just `go`).
- Build/test from repo root: `cd backend && /usr/local/go/bin/go test ./internal/... -run <Test> -v` for a single test, or `/usr/local/go/bin/go build -tags lambda.norpc ./...` to check compilation.
- Every DynamoDB item needs `EntityType` set and must go through the expression builder for queries, not raw strings (see backend/CLAUDE.md conventions).
- API errors follow `{ "error": { "code": "...", "message": "..." } }` with codes `BAD_REQUEST`/`UNAUTHORIZED`/`FORBIDDEN`/`NOT_FOUND`/`INTERNAL_ERROR` — this plan doesn't add new error codes, it removes an existing 404 path.
- Design doc: `docs/superpowers/specs/2026-08-04-pending-email-invites-design.md` — read it once before Task 1; it has the full rationale each task below only summarizes.

---

### Task 1: `PendingInvite` model + `Pending` response fields

**Files:**
- Create: `backend/internal/model/pending_invite.go`
- Modify: `backend/internal/model/meeting.go:80-106` (`Share` struct)
- Modify: `backend/internal/model/account.go:82-86` (`AccountMemberDTO`)
- Modify: `backend/internal/model/project.go:140-143` (`ProjectMemberDTO`)
- Modify: `backend/internal/model/request.go:189-193` (`ShareResponse`)

**Interfaces:**
- Produces: `model.PendingInvite` struct; `model.PrefixPendingInvite`, `model.EntityTypePendingInvite` constants; `model.PendingInviteMeetingShare`, `model.PendingInviteDocShare`, `model.PendingInviteResearchShare`, `model.PendingInviteAccountMember`, `model.PendingInviteProjectMember` type-string constants. `model.Share.Pending bool` (not persisted). `model.AccountMemberDTO.Pending`, `model.ProjectMemberDTO.Pending`, `model.ShareResponse.Pending` (all `json:"pending,omitempty"`).

- [ ] **Step 1: Create the `PendingInvite` model file**

```go
package model

import "time"

// Key prefixes for the pending-invite partition. PENDINGINVITE#{email} is
// its own partition (not nested under an existing entity) because at write
// time the target user doesn't exist yet -- there's nothing else to key it
// under. SK: {Type}#{EntityID}, so re-inviting the same email to the same
// entity overwrites the existing row instead of duplicating it, while
// distinct grants to one email (two different meetings, or a meeting plus
// an account) coexist as separate items in the same partition.
const (
	PrefixPendingInvite     = "PENDINGINVITE#"
	EntityTypePendingInvite = "PENDING_INVITE"
)

// PendingInvite.Type values -- one per grant flow that can target an
// unregistered email.
const (
	PendingInviteMeetingShare  = "MEETING_SHARE"
	PendingInviteDocShare      = "DOC_SHARE"
	PendingInviteResearchShare = "RESEARCH_SHARE"
	PendingInviteAccountMember = "ACCOUNT_MEMBER"
	PendingInviteProjectMember = "PROJECT_MEMBER"
)

// PendingInvite queues a share/membership grant for an email address that
// hasn't signed up yet. Materialized into the real Share/AccountMember/
// ProjectMember row by DynamoDBRepository's reconcilePendingInvites, called
// once from GetOrCreateUser the moment a brand-new user profile is created
// -- see docs/superpowers/specs/2026-08-04-pending-email-invites-design.md.
type PendingInvite struct {
	PK string `dynamodbav:"PK"` // PENDINGINVITE#{lowercased email}
	SK string `dynamodbav:"SK"` // {Type}#{EntityID}
	// Email is stored verbatim (not lowercased) so the eventual grant shows
	// the inviter's original casing; PK is what's actually queried on.
	Email string `dynamodbav:"email"`
	Type  string `dynamodbav:"type"`
	// EntityID is the meetingId/docId/researchId/accountId/projectId being
	// granted, depending on Type.
	EntityID string `dynamodbav:"entityId"`
	OwnerID  string `dynamodbav:"ownerId"`
	// OwnerEmail is set for the three Share-shaped types (unused for
	// ACCOUNT_MEMBER/PROJECT_MEMBER, which have no ownerEmail concept).
	OwnerEmail string `dynamodbav:"ownerEmail,omitempty"`
	// Permission carries the meeting/research share's "read"/"edit", or the
	// account member's role (RoleAM/RoleTAM/...); unused for
	// PROJECT_MEMBER and DOC_SHARE, which have no permission/role concept.
	Permission string    `dynamodbav:"permission,omitempty"`
	CreatedAt  time.Time `dynamodbav:"createdAt"`
	EntityType string    `dynamodbav:"entityType"` // "PENDING_INVITE"
}
```

- [ ] **Step 2: Add `Pending` to `model.Share`**

In `backend/internal/model/meeting.go`, inside the `Share` struct (after the `EntityType` field, i.e. after line 105's `EntityType string \`dynamodbav:"entityType"\` // "SHARE"`), add:

```go
	// Pending is set only on the synthetic Share a *ByEmail function returns
	// when the invited email hasn't registered yet -- never persisted
	// (dynamodbav:"-"), just a signal for the handler to surface in the API
	// response. A real Share row is never created in this case.
	Pending bool `dynamodbav:"-"`
```

- [ ] **Step 3: Add `Pending` to `model.AccountMemberDTO`, `model.ProjectMemberDTO`, `model.ShareResponse`**

In `backend/internal/model/account.go`, change:

```go
type AccountMemberDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role"`
}
```

to:

```go
type AccountMemberDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role"`
	// Pending is true when this member's invite is queued for an email that
	// hasn't signed up yet -- UserID is empty in that case.
	Pending bool `json:"pending,omitempty"`
}
```

In `backend/internal/model/project.go`, change:

```go
type ProjectMemberDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
}
```

to:

```go
type ProjectMemberDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
	// Pending is true when this member's invite is queued for an email that
	// hasn't signed up yet -- UserID is empty in that case.
	Pending bool `json:"pending,omitempty"`
}
```

In `backend/internal/model/request.go`, change:

```go
type ShareResponse struct {
	UserID     string `json:"userId"`
	Email      string `json:"email"`
	Permission string `json:"permission"`
}
```

to:

```go
type ShareResponse struct {
	UserID     string `json:"userId"`
	Email      string `json:"email"`
	Permission string `json:"permission"`
	// Pending is true when this share is queued for an email that hasn't
	// signed up yet -- UserID is empty in that case.
	Pending bool `json:"pending,omitempty"`
}
```

- [ ] **Step 4: Verify it compiles**

Run: `cd backend && /usr/local/go/bin/go build -tags lambda.norpc ./internal/model/...`
Expected: no output (success).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/model/pending_invite.go backend/internal/model/meeting.go backend/internal/model/account.go backend/internal/model/project.go backend/internal/model/request.go
git commit -m "feat: add PendingInvite model and Pending response fields"
```

---

### Task 2: Repository — `PendingInvite` CRUD + reconciliation on signup

**Files:**
- Create: `backend/internal/repository/pending_invite.go`
- Create: `backend/internal/repository/pending_invite_test.go`
- Modify: `backend/internal/repository/dynamodb.go:1796-1846` (`GetOrCreateUser`)

**Interfaces:**
- Consumes: `model.PendingInvite` (Task 1); existing `(*DynamoDBRepository).CreateShare`, `.CreateDocShare`, `.CreateResearchShare`, `.PutMember`, `.PutProjectMember` (all pre-existing, unchanged signatures).
- Produces: `(*DynamoDBRepository) CreatePendingInvite(ctx, invite *model.PendingInvite) error`, `.ListPendingInvites(ctx, email string) ([]model.PendingInvite, error)`, `.DeletePendingInvite(ctx, pk, sk string) error` — these three are what Task 3-6's service interfaces will add. `reconcilePendingInvites` is package-private, called only from `GetOrCreateUser`.

- [ ] **Step 1: Write the failing repository test**

Create `backend/internal/repository/pending_invite_test.go`. This test needs a live or fake DynamoDB — check how existing repository tests in this package are set up:

```bash
grep -rn "func Test" backend/internal/repository/*_test.go | head -5
grep -n "func newTestRepo\|func setupTest\|dynamodb-local\|localstack" backend/internal/repository/*_test.go | head -10
```

Run the above to find the existing test-repo bootstrap helper (e.g. a `newTestRepository(t)` that points at a local DynamoDB endpoint or a table-per-test setup). Use that exact helper — do not invent a new one. Write:

```go
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestPendingInviteCreateListDelete(t *testing.T) {
	repo := newTestRepository(t) // replace with whatever helper the grep above found
	ctx := context.Background()

	invite := &model.PendingInvite{
		Email: "New.User@Example.com", Type: model.PendingInviteMeetingShare,
		EntityID: "meeting-1", OwnerID: "owner-1", OwnerEmail: "owner@example.com",
		Permission: "read", CreatedAt: time.Now().UTC(),
	}
	if err := repo.CreatePendingInvite(ctx, invite); err != nil {
		t.Fatalf("CreatePendingInvite: %v", err)
	}

	// Lookup is case-insensitive on email.
	got, err := repo.ListPendingInvites(ctx, "new.user@example.com")
	if err != nil {
		t.Fatalf("ListPendingInvites: %v", err)
	}
	if len(got) != 1 || got[0].EntityID != "meeting-1" {
		t.Fatalf("expected 1 invite for meeting-1, got %+v", got)
	}

	if err := repo.DeletePendingInvite(ctx, got[0].PK, got[0].SK); err != nil {
		t.Fatalf("DeletePendingInvite: %v", err)
	}
	got, err = repo.ListPendingInvites(ctx, "new.user@example.com")
	if err != nil {
		t.Fatalf("ListPendingInvites after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 invites after delete, got %+v", got)
	}
}

func TestPendingInviteReconciledOnFirstSignup(t *testing.T) {
	repo := newTestRepository(t) // replace with whatever helper the grep above found
	ctx := context.Background()

	if err := repo.CreatePendingInvite(ctx, &model.PendingInvite{
		Email: "invitee@example.com", Type: model.PendingInviteAccountMember,
		EntityID: "acct-1", OwnerID: "owner-1", Permission: model.RoleAM,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed pending invite: %v", err)
	}

	user, err := repo.GetOrCreateUser(ctx, "new-user-id", "invitee@example.com", "Invitee")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if user.UserID != "new-user-id" {
		t.Fatalf("expected new-user-id, got %s", user.UserID)
	}

	member, err := repo.GetMember(ctx, "acct-1", "new-user-id")
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if member == nil || member.Role != model.RoleAM {
		t.Fatalf("expected member with role AM, got %+v", member)
	}

	remaining, err := repo.ListPendingInvites(ctx, "invitee@example.com")
	if err != nil {
		t.Fatalf("ListPendingInvites after reconcile: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected pending invite consumed, got %+v", remaining)
	}

	// A second GetOrCreateUser call for the SAME (already-existing) user must
	// not error or re-run reconciliation (no pending invites left to find,
	// this just proves the existing-user branch is untouched).
	if _, err := repo.GetOrCreateUser(ctx, "new-user-id", "invitee@example.com", "Invitee"); err != nil {
		t.Fatalf("second GetOrCreateUser: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && /usr/local/go/bin/go test ./internal/repository/... -run TestPendingInvite -v`
Expected: FAIL — `repo.CreatePendingInvite undefined` (method doesn't exist yet).

- [ ] **Step 3: Implement `pending_invite.go`**

```go
package repository

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func pendingInvitePK(email string) string {
	return model.PrefixPendingInvite + strings.ToLower(strings.TrimSpace(email))
}

// CreatePendingInvite queues a grant for an email that isn't registered yet.
// Overwrites any existing pending invite of the same Type+EntityID for the
// same email -- re-inviting is idempotent, not an error.
func (r *DynamoDBRepository) CreatePendingInvite(ctx context.Context, invite *model.PendingInvite) error {
	invite.PK = pendingInvitePK(invite.Email)
	invite.SK = invite.Type + "#" + invite.EntityID
	invite.EntityType = model.EntityTypePendingInvite
	item, err := attributevalue.MarshalMap(invite)
	if err != nil {
		return fmt.Errorf("failed to marshal pending invite: %w", err)
	}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("failed to put pending invite: %w", err)
	}
	return nil
}

// ListPendingInvites returns every pending grant queued for email
// (case-insensitive).
func (r *DynamoDBRepository) ListPendingInvites(ctx context.Context, email string) ([]model.PendingInvite, error) {
	keyEx := expression.Key("PK").Equal(expression.Value(pendingInvitePK(email)))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return nil, fmt.Errorf("build pending invites query: %w", err)
	}
	items, err := r.queryAllPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return nil, fmt.Errorf("query pending invites: %w", err)
	}
	invites := []model.PendingInvite{}
	if err := attributevalue.UnmarshalListOfMaps(items, &invites); err != nil {
		return nil, fmt.Errorf("unmarshal pending invites: %w", err)
	}
	return invites, nil
}

// DeletePendingInvite removes one materialized (or abandoned) pending row.
func (r *DynamoDBRepository) DeletePendingInvite(ctx context.Context, pk, sk string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: sk},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete pending invite: %w", err)
	}
	return nil
}

// reconcilePendingInvites materializes every PendingInvite queued for a
// brand-new user's email into its real Share/AccountMember/ProjectMember
// row, then deletes the pending row. Called once, from GetOrCreateUser,
// right after a new profile is created -- never on an existing user's
// lookup. A single row's materialization failing (e.g. its underlying
// meeting/doc/research/account/project was deleted while the invite sat
// pending) is logged and skipped rather than failing signup -- a user must
// never fail to provision because a stale invite couldn't be replayed.
func (r *DynamoDBRepository) reconcilePendingInvites(ctx context.Context, user *model.User) {
	invites, err := r.ListPendingInvites(ctx, user.Email)
	if err != nil {
		log.Printf("reconcilePendingInvites: list failed for %s: %v", user.Email, err)
		return
	}
	for _, inv := range invites {
		var matErr error
		switch inv.Type {
		case model.PendingInviteMeetingShare:
			_, matErr = r.CreateShare(ctx, inv.EntityID, inv.OwnerID, inv.OwnerEmail, user.UserID, user.Email, inv.Permission, "")
		case model.PendingInviteDocShare:
			_, matErr = r.CreateDocShare(ctx, inv.EntityID, inv.OwnerID, inv.OwnerEmail, user.UserID, user.Email)
		case model.PendingInviteResearchShare:
			_, matErr = r.CreateResearchShare(ctx, inv.EntityID, inv.OwnerID, inv.OwnerEmail, user.UserID, user.Email, inv.Permission)
		case model.PendingInviteAccountMember:
			matErr = r.PutMember(ctx, &model.AccountMember{
				PK: model.PrefixAccount + inv.EntityID, SK: model.PrefixMember + user.UserID,
				AccountID: inv.EntityID, UserID: user.UserID, Email: user.Email, Role: inv.Permission,
				AddedAt: time.Now().UTC(), GSI1PK: model.PrefixUser + user.UserID,
				GSI1SK: model.PrefixAccount + inv.EntityID, EntityType: model.EntityTypeAccountMember,
			})
		case model.PendingInviteProjectMember:
			matErr = r.PutProjectMember(ctx, &model.ProjectMember{
				PK: model.PrefixProject + inv.EntityID, SK: model.PrefixProjectMember + user.UserID,
				ProjectID: inv.EntityID, UserID: user.UserID, Email: user.Email,
				AddedAt: time.Now().UTC(), GSI1PK: model.PrefixUser + user.UserID,
				GSI1SK: model.PrefixProject + inv.EntityID, EntityType: model.EntityTypeProjectMember,
			})
		default:
			log.Printf("reconcilePendingInvites: unknown type %q for %s, skipping", inv.Type, user.Email)
			continue
		}
		if matErr != nil {
			log.Printf("reconcilePendingInvites: materialize %s (%s) for %s failed: %v", inv.Type, inv.EntityID, user.Email, matErr)
			continue
		}
		if err := r.DeletePendingInvite(ctx, inv.PK, inv.SK); err != nil {
			log.Printf("reconcilePendingInvites: delete pending row for %s failed: %v", user.Email, err)
		}
	}
}
```

- [ ] **Step 4: Wire reconciliation into `GetOrCreateUser`**

In `backend/internal/repository/dynamodb.go`, find the `GetOrCreateUser` function (around line 1796). It currently ends with:

```go
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to put user: %w", err)
	}

	return user, nil
}
```

Change it to:

```go
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to put user: %w", err)
	}

	r.reconcilePendingInvites(ctx, user)

	return user, nil
}
```

(This only runs in the "new user" branch of the function, not the earlier `if result.Item != nil { ... return &user, nil }` branch — reconciliation must never re-run for an existing user.)

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd backend && /usr/local/go/bin/go test ./internal/repository/... -run TestPendingInvite -v`
Expected: PASS (both `TestPendingInviteCreateListDelete` and `TestPendingInviteReconciledOnFirstSignup`).

- [ ] **Step 6: Run the full repository test suite to check for regressions**

Run: `cd backend && /usr/local/go/bin/go test ./internal/repository/... -v`
Expected: all PASS, including every pre-existing `GetOrCreateUser`-related test.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/repository/pending_invite.go backend/internal/repository/pending_invite_test.go backend/internal/repository/dynamodb.go
git commit -m "feat: reconcile pending email invites on first signup"
```

---

### Task 3: Account service — pending invites for member-add and doc-share-by-email

**Files:**
- Modify: `backend/internal/service/account.go:73-103` (`accountRepo` interface), `:254-292` (`AddMember`), `:1178-1193` (`ShareUserDocumentByEmail`)
- Modify: `backend/internal/service/account_test.go` (mock repo + new tests)

**Interfaces:**
- Consumes: `model.PendingInvite`, `model.PendingInviteAccountMember`, `model.PendingInviteDocShare` (Task 1); `repo.CreatePendingInvite` (Task 2, added to `accountRepo` below).
- Produces: `AccountService.AddMember` returns `*model.AccountMemberDTO{Pending: true}` (no error) when the email is unregistered. `AccountService.ShareUserDocumentByEmail` returns `*model.Share{Pending: true}` (no error) likewise.

- [ ] **Step 1: Write the failing tests**

In `backend/internal/service/account_test.go`, add:

```go
func TestAddMember_PendingInviteForUnregisteredEmail(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	seedUser(repo, "owner-1", "owner@example.com")
	acc := &model.Account{AccountID: "acct-1", OwnerUserID: "owner-1"}
	repo.accounts["acct-1"] = acc
	repo.PutMember(context.Background(), &model.AccountMember{AccountID: "acct-1", UserID: "owner-1", Role: model.RoleOwner})

	dto, err := svc.AddMember(context.Background(), "owner-1", "acct-1", &model.AddMemberRequest{Email: "unregistered@example.com", Role: model.RoleAM})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !dto.Pending {
		t.Fatalf("expected Pending=true, got %+v", dto)
	}
	if dto.Email != "unregistered@example.com" || dto.UserID != "" {
		t.Fatalf("unexpected dto: %+v", dto)
	}

	invites, _ := repo.pendingInvites["PENDINGINVITE#unregistered@example.com"]
	if len(invites) != 1 || invites[0].Type != model.PendingInviteAccountMember || invites[0].EntityID != "acct-1" || invites[0].Permission != model.RoleAM {
		t.Fatalf("expected one pending ACCOUNT_MEMBER invite for acct-1, got %+v", invites)
	}
}

func TestShareUserDocumentByEmail_PendingInviteForUnregisteredEmail(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	repo.documents[model.PrefixUser+"owner-1"] = []model.AccountDocument{
		{DocID: "doc-1", Title: "note", EntityType: model.EntityTypeUserDoc},
	}

	share, err := svc.ShareUserDocumentByEmail(context.Background(), "owner-1", "owner@example.com", "doc-1", "unregistered@example.com")
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !share.Pending || share.Email != "unregistered@example.com" {
		t.Fatalf("unexpected share: %+v", share)
	}

	invites := repo.pendingInvites["PENDINGINVITE#unregistered@example.com"]
	if len(invites) != 1 || invites[0].Type != model.PendingInviteDocShare || invites[0].EntityID != "doc-1" {
		t.Fatalf("expected one pending DOC_SHARE invite for doc-1, got %+v", invites)
	}
}

func TestShareUserDocumentByEmail_SelfInviteStillRejectedWhenPending(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	repo.documents[model.PrefixUser+"owner-1"] = []model.AccountDocument{
		{DocID: "doc-1", Title: "note", EntityType: model.EntityTypeUserDoc},
	}

	_, err := svc.ShareUserDocumentByEmail(context.Background(), "owner-1", "owner@example.com", "doc-1", "Owner@Example.com")
	if !errors.Is(err, ErrSelfShare) {
		t.Fatalf("expected ErrSelfShare, got %v", err)
	}
}
```

Add the `pendingInvites` map to `mockAccountRepo` and its `CreatePendingInvite` method — in `backend/internal/service/account_test.go`, add `pendingInvites map[string][]model.PendingInvite // "PENDINGINVITE#{email}" -> invites` to the `mockAccountRepo` struct fields (alongside `docShares` etc.), initialize it in `newMockAccountRepo()` (`pendingInvites: make(map[string][]model.PendingInvite)`), and add:

```go
func (m *mockAccountRepo) CreatePendingInvite(_ context.Context, invite *model.PendingInvite) error {
	key := "PENDINGINVITE#" + strings.ToLower(invite.Email)
	m.pendingInvites[key] = append(m.pendingInvites[key], *invite)
	return nil
}
```

(Add `"strings"` to the test file's imports if not already present — check with `grep -n '"strings"' backend/internal/service/account_test.go` first.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestAddMember_PendingInvite -v` and `-run TestShareUserDocumentByEmail_Pending`
Expected: FAIL — compile error, `accountRepo` has no method `CreatePendingInvite` / `AddMember` returns `ErrUserNotFound` instead of a pending DTO.

- [ ] **Step 3: Add `CreatePendingInvite` to the `accountRepo` interface**

In `backend/internal/service/account.go`, in the `accountRepo` interface (ends at line 103 with `ListDocSharesForDoc(...)`), add:

```go
	CreatePendingInvite(ctx context.Context, invite *model.PendingInvite) error
```

- [ ] **Step 4: Update `AddMember`**

In `backend/internal/service/account.go`, find:

```go
	user, err := s.repo.GetUserByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
```

(inside `AddMember`, around line 265) and change to:

```go
	user, err := s.repo.GetUserByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		if err := s.repo.CreatePendingInvite(ctx, &model.PendingInvite{
			Email: req.Email, Type: model.PendingInviteAccountMember, EntityID: accountID,
			OwnerID: requesterUserID, Permission: req.Role, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
		return &model.AccountMemberDTO{Email: req.Email, Role: req.Role, Pending: true}, nil
	}
```

(`time` is already imported in `account.go` — confirm with `grep -n '"time"' backend/internal/service/account.go`.)

- [ ] **Step 5: Update `ShareUserDocumentByEmail`**

In `backend/internal/service/account.go`, find:

```go
	targetUser, err := s.repo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		return nil, ErrUserNotFound
	}
	if targetUser.UserID == ownerID {
		return nil, ErrSelfShare
	}
	return s.repo.CreateDocShare(ctx, docID, ownerID, ownerEmail, targetUser.UserID, targetEmail)
```

(inside `ShareUserDocumentByEmail`, around line 1182) and change to:

```go
	targetUser, err := s.repo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		if strings.EqualFold(strings.TrimSpace(targetEmail), strings.TrimSpace(ownerEmail)) {
			return nil, ErrSelfShare
		}
		if err := s.repo.CreatePendingInvite(ctx, &model.PendingInvite{
			Email: targetEmail, Type: model.PendingInviteDocShare, EntityID: docID,
			OwnerID: ownerID, OwnerEmail: ownerEmail, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
		return &model.Share{Email: targetEmail, Pending: true}, nil
	}
	if targetUser.UserID == ownerID {
		return nil, ErrSelfShare
	}
	return s.repo.CreateDocShare(ctx, docID, ownerID, ownerEmail, targetUser.UserID, targetEmail)
```

(`strings` is already imported in `account.go`.)

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run 'TestAddMember_PendingInvite|TestShareUserDocumentByEmail_' -v`
Expected: PASS.

- [ ] **Step 7: Run the full account service test suite to check for regressions**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestAddMember -v` and `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestShareUserDocumentByEmail -v`
Expected: all PASS, including pre-existing `TestAddMember_...` (forbidden/invalid-role/already-exists) and `TestShareUserDocumentByEmail_...` cases.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/service/account.go backend/internal/service/account_test.go
git commit -m "feat(account): pending invite for member-add and doc-share on unregistered email"
```

---

### Task 4: Project service — pending invite for member-add

**Files:**
- Modify: `backend/internal/service/project.go:29-59` (`projectRepo` interface), `:353-386` (`AddMember`)
- Modify: `backend/internal/service/project_test.go` (mock repo + new test)

**Interfaces:**
- Consumes: `model.PendingInvite`, `model.PendingInviteProjectMember` (Task 1); `repo.CreatePendingInvite` (added to `projectRepo` below).
- Produces: `ProjectService.AddMember` returns `*model.ProjectMemberDTO{Pending: true}` (no error) when the email is unregistered.

- [ ] **Step 1: Write the failing test**

In `backend/internal/service/project_test.go`, add:

```go
func TestAddMember_PendingInviteForUnregisteredEmail(t *testing.T) {
	repo := newMockProjectRepo()
	svc := newProjectServiceWithRepo(repo)
	repo.projects["proj-1"] = &model.Project{ProjectID: "proj-1", OwnerUserID: "owner-1"}

	dto, err := svc.AddMember(context.Background(), "owner-1", "proj-1", &model.AddProjectMemberRequest{Email: "unregistered@example.com"})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !dto.Pending || dto.Email != "unregistered@example.com" || dto.UserID != "" {
		t.Fatalf("unexpected dto: %+v", dto)
	}

	invites := repo.pendingInvites["PENDINGINVITE#unregistered@example.com"]
	if len(invites) != 1 || invites[0].Type != model.PendingInviteProjectMember || invites[0].EntityID != "proj-1" {
		t.Fatalf("expected one pending PROJECT_MEMBER invite for proj-1, got %+v", invites)
	}
}
```

Run `grep -n "func newProjectServiceWithRepo\|func NewProjectService\b" backend/internal/service/project.go` first and use whichever constructor the file actually exposes for same-package tests (mirrors Task 3's `newAccountServiceWithRepo` pattern — if no such helper exists yet for project, check how other `project_test.go` tests construct `*ProjectService` and copy that exactly).

Add `pendingInvites map[string][]model.PendingInvite` to `mockProjectRepo`'s struct fields, initialize it in `newMockProjectRepo()`, and add:

```go
func (m *mockProjectRepo) CreatePendingInvite(_ context.Context, invite *model.PendingInvite) error {
	key := "PENDINGINVITE#" + strings.ToLower(invite.Email)
	m.pendingInvites[key] = append(m.pendingInvites[key], *invite)
	return nil
}
```

(Add `"strings"` import if missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestAddMember_PendingInviteForUnregisteredEmail -v`
Expected: FAIL — compile error (`projectRepo` has no `CreatePendingInvite`) or `ErrUserNotFound` returned.

- [ ] **Step 3: Add `CreatePendingInvite` to the `projectRepo` interface**

In `backend/internal/service/project.go`, in the `projectRepo` interface (ends at line 59 with `BatchGetResearchByIDs(...)`), add:

```go
	CreatePendingInvite(context.Context, *model.PendingInvite) error
```

(Match the interface's existing positional-args style, not named params — see the other entries.)

- [ ] **Step 4: Update `AddMember`**

In `backend/internal/service/project.go`, find:

```go
	user, err := s.repo.GetUserByEmail(ctx, strings.TrimSpace(req.Email))
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
```

(inside `AddMember`, around line 367) and change to:

```go
	email := strings.TrimSpace(req.Email)
	user, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		if err := s.repo.CreatePendingInvite(ctx, &model.PendingInvite{
			Email: email, Type: model.PendingInviteProjectMember, EntityID: projectID,
			OwnerID: requesterUserID, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
		return &model.ProjectMemberDTO{Email: email, Pending: true}, nil
	}
```

Check whether `time` is already imported in `project.go` (`grep -n '"time"' backend/internal/service/project.go`); add it to the import block if missing.

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestAddMember_PendingInviteForUnregisteredEmail -v`
Expected: PASS.

- [ ] **Step 6: Run the full project service test suite to check for regressions**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestAddMember -v`
Expected: all PASS (both account's and project's `TestAddMember_*`).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/service/project.go backend/internal/service/project_test.go
git commit -m "feat(project): pending invite for member-add on unregistered email"
```

---

### Task 5: Meeting service — pending invite for meeting share

**Files:**
- Modify: `backend/internal/service/meeting.go:56-77` (`meetingRepo` interface), `:579-605` (`ShareMeetingByEmail`)
- Modify: `backend/internal/service/meeting_test.go` (mock repo + new tests)

**Interfaces:**
- Consumes: `model.PendingInvite`, `model.PendingInviteMeetingShare` (Task 1); `repo.CreatePendingInvite` (added to `meetingRepo` below).
- Produces: `MeetingService.ShareMeetingByEmail` returns `*model.Share{Pending: true}` (no error) when the email is unregistered.

- [ ] **Step 1: Write the failing tests**

In `backend/internal/service/meeting_test.go`, add (check the exact same-package constructor name first: `grep -n "func newMeetingServiceWithRepo" backend/internal/service/meeting.go` — it's `newMeetingServiceWithRepo` per the interface block seen during planning):

```go
func TestShareMeetingByEmail_PendingInviteForUnregisteredEmail(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.meetings["owner-1|meeting-1"] = &model.Meeting{MeetingID: "meeting-1", UserID: "owner-1"}

	share, err := svc.ShareMeetingByEmail(context.Background(), "owner-1", "owner@example.com", "meeting-1", "unregistered@example.com", "read")
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !share.Pending || share.Email != "unregistered@example.com" || share.Permission != "read" {
		t.Fatalf("unexpected share: %+v", share)
	}

	invites := repo.pendingInvites["PENDINGINVITE#unregistered@example.com"]
	if len(invites) != 1 || invites[0].Type != model.PendingInviteMeetingShare || invites[0].EntityID != "meeting-1" || invites[0].Permission != "read" {
		t.Fatalf("expected one pending MEETING_SHARE invite for meeting-1, got %+v", invites)
	}
}

func TestShareMeetingByEmail_SelfInviteStillRejectedWhenPending(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := newMeetingServiceWithRepo(repo)
	repo.meetings["owner-1|meeting-1"] = &model.Meeting{MeetingID: "meeting-1", UserID: "owner-1"}

	_, err := svc.ShareMeetingByEmail(context.Background(), "owner-1", "owner@example.com", "meeting-1", "Owner@Example.com", "read")
	if !errors.Is(err, ErrSelfShare) {
		t.Fatalf("expected ErrSelfShare, got %v", err)
	}
}
```

Check the exact key format `mockMeetingRepo.meetings` uses (the struct comment says `"userID|meetingID" -> meeting`, matching what's used above) and how `GetMeeting` reads it — confirm with `grep -n "func (m \*mockMeetingRepo) GetMeeting" -A 8 backend/internal/service/meeting_test.go` before relying on the `"owner-1|meeting-1"` key literal.

Add `pendingInvites map[string][]model.PendingInvite` to `mockMeetingRepo`'s struct fields, initialize it wherever the mock is constructed, and add:

```go
func (m *mockMeetingRepo) CreatePendingInvite(_ context.Context, invite *model.PendingInvite) error {
	key := "PENDINGINVITE#" + strings.ToLower(invite.Email)
	m.pendingInvites[key] = append(m.pendingInvites[key], *invite)
	return nil
}
```

(Add `"strings"` import if missing.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run 'TestShareMeetingByEmail_' -v`
Expected: FAIL — compile error or `ErrUserNotFound`/no `ErrSelfShare`.

- [ ] **Step 3: Add `CreatePendingInvite` to the `meetingRepo` interface**

In `backend/internal/service/meeting.go`, in the `meetingRepo` interface (ends at line 77 with `PutAccountInsights(...)`), add:

```go
	CreatePendingInvite(ctx context.Context, invite *model.PendingInvite) error
```

- [ ] **Step 4: Update `ShareMeetingByEmail`**

In `backend/internal/service/meeting.go`, find:

```go
	// Find user by email
	targetUser, err := s.repo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		return nil, fmt.Errorf("user not found")
	}

	// Cannot share with self
	if ownerID == targetUser.UserID {
		return nil, fmt.Errorf("cannot share with yourself")
	}

	return s.repo.CreateShare(ctx, meetingID, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission, "")
```

(inside `ShareMeetingByEmail`, around line 590) and change to:

```go
	// Find user by email
	targetUser, err := s.repo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		if strings.EqualFold(strings.TrimSpace(targetEmail), strings.TrimSpace(ownerEmail)) {
			return nil, ErrSelfShare
		}
		if err := s.repo.CreatePendingInvite(ctx, &model.PendingInvite{
			Email: targetEmail, Type: model.PendingInviteMeetingShare, EntityID: meetingID,
			OwnerID: ownerID, OwnerEmail: ownerEmail, Permission: permission, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
		return &model.Share{Email: targetEmail, Permission: permission, Pending: true}, nil
	}

	// Cannot share with self
	if ownerID == targetUser.UserID {
		return nil, ErrSelfShare
	}

	return s.repo.CreateShare(ctx, meetingID, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission, "")
```

Note this also switches the self-share error from the ad-hoc `fmt.Errorf("cannot share with yourself")` to the existing `ErrSelfShare` sentinel (already defined at `meeting.go:52` and already what `handler/share.go`'s `case "cannot share with yourself":` string-matches against — `ErrSelfShare.Error()` is exactly that string, so the handler's existing string-switch keeps working unchanged). `strings` needs to be added to `meeting.go`'s imports if not already present (`grep -n '"strings"' backend/internal/service/meeting.go`).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run 'TestShareMeetingByEmail_' -v`
Expected: PASS.

- [ ] **Step 6: Run the full meeting service test suite to check for regressions**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestShareMeetingByEmail -v`
Expected: all PASS, including any pre-existing `TestShareMeetingByEmail_*` cases (e.g. forbidden/not-found).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/service/meeting.go backend/internal/service/meeting_test.go
git commit -m "feat(meeting): pending invite for meeting share on unregistered email"
```

---

### Task 6: Research service — pending invite for research share

**Files:**
- Modify: `backend/internal/service/research.go:25-31` (`researchMainRepo` interface), `:476-497` (`ShareResearchByEmail`)
- Modify: `backend/internal/service/research_test.go` (`mockResearchMainRepo` + new tests)

**Interfaces:**
- Consumes: `model.PendingInvite`, `model.PendingInviteResearchShare` (Task 1); `repo.CreatePendingInvite` (added to `researchMainRepo` below).
- Produces: `ResearchService.ShareResearchByEmail` returns `*model.Share{Pending: true}` (no error) when the email is unregistered.

- [ ] **Step 1: Write the failing tests**

`mockResearchMainRepo.GetUserByEmail` currently always returns `nil, nil` (no `users` map exists yet — grep confirms no test in this file exercises `ShareResearchByEmail` today). In `backend/internal/service/research_test.go`, first give `mockResearchMainRepo` a real `users` map: add `users map[string]*model.User // email -> user` to its struct fields, initialize it in `newMockResearchMainRepo()` (`users: make(map[string]*model.User)`), replace the body of `GetUserByEmail`:

```go
func (m *mockResearchMainRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}
```

and add a `pendingInvites map[string][]model.PendingInvite` field (initialized the same way) plus:

```go
func (m *mockResearchMainRepo) CreatePendingInvite(_ context.Context, invite *model.PendingInvite) error {
	key := "PENDINGINVITE#" + strings.ToLower(invite.Email)
	m.pendingInvites[key] = append(m.pendingInvites[key], *invite)
	return nil
}
```

(Add `"strings"` import if missing — check `grep -n '"strings"' backend/internal/service/research_test.go` first.)

Then add the tests (find the existing research-owning setup helper by checking how other research tests seed a `model.Research` and construct `*ResearchService` — grep `func newResearchServiceWithRepo\|func TestShareResearch` in `research.go`/`research_test.go` first, since none currently exist for share):

```go
func TestShareResearchByEmail_PendingInviteForUnregisteredEmail(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newResearchServiceWithRepo(repo, mainRepo)
	repo.byID["research-1"] = &model.Research{ResearchID: "research-1", UserID: "owner-1"}

	share, err := svc.ShareResearchByEmail(context.Background(), "owner-1", "owner@example.com", "research-1", "unregistered@example.com", "read")
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !share.Pending || share.Email != "unregistered@example.com" || share.Permission != "read" {
		t.Fatalf("unexpected share: %+v", share)
	}

	invites := mainRepo.pendingInvites["PENDINGINVITE#unregistered@example.com"]
	if len(invites) != 1 || invites[0].Type != model.PendingInviteResearchShare || invites[0].EntityID != "research-1" {
		t.Fatalf("expected one pending RESEARCH_SHARE invite for research-1, got %+v", invites)
	}
}

func TestShareResearchByEmail_SelfInviteStillRejectedWhenPending(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newResearchServiceWithRepo(repo, mainRepo)
	repo.byID["research-1"] = &model.Research{ResearchID: "research-1", UserID: "owner-1"}

	_, err := svc.ShareResearchByEmail(context.Background(), "owner-1", "owner@example.com", "research-1", "Owner@Example.com", "read")
	if !errors.Is(err, ErrSelfShare) {
		t.Fatalf("expected ErrSelfShare, got %v", err)
	}
}
```

Check `mockResearchRepo.research`'s exact field name/type via `grep -n "type mockResearchRepo struct" -A 15 backend/internal/service/research_test.go` before relying on `repo.research["research-1"] = ...` — adjust the field name if it differs.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run 'TestShareResearchByEmail_' -v`
Expected: FAIL — compile error (`researchMainRepo` has no `CreatePendingInvite`) or `ErrUserNotFound`.

- [ ] **Step 3: Add `CreatePendingInvite` to the `researchMainRepo` interface**

In `backend/internal/service/research.go`, in the `researchMainRepo` interface (ends around line 31 with `ListResearchRefsForAccount(...)`), add:

```go
	CreatePendingInvite(ctx context.Context, invite *model.PendingInvite) error
```

- [ ] **Step 4: Update `ShareResearchByEmail`**

In `backend/internal/service/research.go`, find:

```go
	targetUser, err := s.mainRepo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		return nil, ErrUserNotFound
	}

	if ownerID == targetUser.UserID {
		return nil, ErrSelfShare
	}

	return s.repo.CreateResearchShare(ctx, researchId, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission)
```

(inside `ShareResearchByEmail`, around line 488) and change to:

```go
	targetUser, err := s.mainRepo.GetUserByEmail(ctx, targetEmail)
	if err != nil {
		return nil, err
	}
	if targetUser == nil {
		if strings.EqualFold(strings.TrimSpace(targetEmail), strings.TrimSpace(ownerEmail)) {
			return nil, ErrSelfShare
		}
		if err := s.mainRepo.CreatePendingInvite(ctx, &model.PendingInvite{
			Email: targetEmail, Type: model.PendingInviteResearchShare, EntityID: researchId,
			OwnerID: ownerID, OwnerEmail: ownerEmail, Permission: permission, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
		return &model.Share{Email: targetEmail, Permission: permission, Pending: true}, nil
	}

	if ownerID == targetUser.UserID {
		return nil, ErrSelfShare
	}

	return s.repo.CreateResearchShare(ctx, researchId, ownerID, ownerEmail, targetUser.UserID, targetEmail, permission)
```

`"strings"` needs adding to `research.go`'s import block (it currently imports `context`, `crypto/rand`, `encoding/hex`, `encoding/json`, `fmt`, `errors`, `io`, `log`, `time` — no `strings` yet).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run 'TestShareResearchByEmail_' -v`
Expected: PASS.

- [ ] **Step 6: Run the full research service test suite to check for regressions**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestShareResearch -v` (and, more broadly, `-run TestResearch` if that's the existing naming convention for this file)
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/service/research.go backend/internal/service/research_test.go
git commit -m "feat(research): pending invite for research share on unregistered email"
```

---

### Task 7: Handlers — surface `Pending` in the three share responses

**Files:**
- Modify: `backend/internal/handler/share.go:79-87` (`ShareMeeting`)
- Modify: `backend/internal/handler/document.go:171-173` (`ShareWithUser`)
- Modify: `backend/internal/handler/research_share.go:76-80` (research share handler)

**Interfaces:**
- Consumes: `model.Share.Pending`, `model.ShareResponse.Pending` (Task 1); `MeetingService.ShareMeetingByEmail`, `AccountService.ShareUserDocumentByEmail`, `ResearchService.ShareResearchByEmail` (Tasks 3/5/6, unchanged signatures, now sometimes returning `Pending: true` with no error).

No handler test file changes are needed here — these are pure pass-through field additions with no new branching; existing handler tests (if any) that assert on the non-pending success shape are unaffected since `pending` is `omitempty` and defaults to `false`/absent.

- [ ] **Step 1: Update `ShareMeeting` handler**

In `backend/internal/handler/share.go`, change:

```go
	response := model.SharedWithResponse{
		SharedWith: model.ShareResponse{
			UserID:     share.SharedToID,
			Email:      share.Email,
			Permission: share.Permission,
		},
	}
```

to:

```go
	response := model.SharedWithResponse{
		SharedWith: model.ShareResponse{
			UserID:     share.SharedToID,
			Email:      share.Email,
			Permission: share.Permission,
			Pending:    share.Pending,
		},
	}
```

- [ ] **Step 2: Update `ShareWithUser` (document) handler**

In `backend/internal/handler/document.go`, change:

```go
	writeJSON(w, http.StatusCreated, model.SharedWithResponse{SharedWith: model.ShareResponse{
		UserID: share.SharedToID, Email: share.Email, Permission: share.Permission,
	}})
```

to:

```go
	writeJSON(w, http.StatusCreated, model.SharedWithResponse{SharedWith: model.ShareResponse{
		UserID: share.SharedToID, Email: share.Email, Permission: share.Permission, Pending: share.Pending,
	}})
```

- [ ] **Step 3: Update the research share handler**

In `backend/internal/handler/research_share.go`, find (around line 76):

```go
	response := model.SharedWithResponse{
		SharedWith: model.ShareResponse{
			UserID:     share.SharedToID,
			Email:      share.Email,
```

Read the full literal (it continues past what was seen during planning — open the file to get the exact remaining fields) and add `Pending: share.Pending,` alongside `Permission: share.Permission,` in that same struct literal, matching the pattern from Steps 1-2.

- [ ] **Step 4: Verify it compiles**

Run: `cd backend && /usr/local/go/bin/go build -tags lambda.norpc ./internal/handler/...`
Expected: no output (success).

- [ ] **Step 5: Run the full backend test suite**

Run: `cd backend && /usr/local/go/bin/go build -tags lambda.norpc ./... && /usr/local/go/bin/go test ./... `
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/handler/share.go backend/internal/handler/document.go backend/internal/handler/research_share.go
git commit -m "feat: surface pending flag in meeting/doc/research share API responses"
```

---

### Task 8: ADR-030 + docs sync

**Files:**
- Create: `docs/decisions/ADR-030-pending-email-invites.md`
- Modify: `CLAUDE.md` (root) — add a subsection under "Architecture" (or extend "Document sharing & public links") documenting the mechanism, per the Auto-Sync Rules ("New decisions": add ADR; architecture changes update `docs/architecture.md`)
- Modify: `docs/architecture.md` — one-paragraph mention of the pending-invite mechanism and its reconciliation hook.
- Modify: `docs/API-SPEC.md` — document the new `pending` field on the 5 affected endpoints' responses.

**Interfaces:**
- Consumes: nothing new — this task documents Tasks 1-7's shipped behavior.

- [ ] **Step 1: Write the ADR**

Create `docs/decisions/ADR-030-pending-email-invites.md`:

```markdown
# ADR-030: Pending invites for not-yet-registered emails

## Status
Accepted

## Context
Five email-based grant flows (meeting share, personal doc share, research
share, account member invite, project member invite) all resolved the
target email via `GetUserByEmail` and hard-failed with `ErrUserNotFound`
(404) when it didn't match a registered user. An inviter had no way to
grant access to someone who simply hadn't signed up yet — they had to wait
for that person to register, then repeat the invite by hand.

## Decision
Each of the 5 flows, on an unresolved email, now writes a `PendingInvite`
item (PK `PENDINGINVITE#{lowercased email}`, SK `{Type}#{EntityID}`) instead
of erroring, and returns a success response with `pending: true` (200/201,
not 404). `DynamoDBRepository.GetOrCreateUser` — the existing lazy
user-provisioning hook, called on a user's first authenticated request
after Cognito signup — materializes every pending row queued for that email
into the real `Share`/`AccountMember`/`ProjectMember` row (via the same
repo methods the live-invite path already uses) immediately after creating
a brand-new profile, then deletes the pending row. This runs exactly once
per invite, self-cleans, and needs no new Cognito trigger.

## Consequences
- A pending grant has no reverse index by entity (meeting/account/etc.) —
  only by email. It can't be listed, browsed, or canceled once sent; it
  either materializes on signup or sits inert forever if that email never
  registers. Frontend surfaces `pending: true` as a one-time confirmation
  at invite time, not a persisted badge on the member/share list (see
  `docs/superpowers/specs/2026-08-04-pending-email-invites-design.md`).
- Materialization failure for one pending row (its underlying
  meeting/doc/research/account/project was deleted while the invite sat
  pending) is logged and skipped, never fails the user's signup.
- A user who signs up (Cognito) but never opens the app never triggers
  reconciliation — acceptable, since there's nothing to show them either
  way until they do.
```

- [ ] **Step 2: Update `docs/architecture.md`**

Read `docs/architecture.md` first (`grep -n "^##" docs/architecture.md` to find the right section — likely near wherever Account/Project sharing is described) and add one paragraph: pending invites exist for the 5 email-based grant flows, keyed by email, reconciled via `GetOrCreateUser`; reference ADR-030.

- [ ] **Step 3: Update `docs/API-SPEC.md`**

Find the 5 endpoints' documented response shapes (`grep -n "share\|addMember\|members" docs/API-SPEC.md`) and add a `pending` (boolean, optional) field to each, with a one-line note: "true if the invited email hasn't registered yet; the grant activates automatically on signup (ADR-030)."

- [ ] **Step 4: Update root `CLAUDE.md`**

Add a short subsection (near "Research↔Account linking" or as its own entry) summarizing: the 5 email-based grant flows now support inviting an unregistered email via a `PendingInvite` item, reconciled on first signup via `GetOrCreateUser`; reference ADR-030 and the design spec path.

- [ ] **Step 5: Commit**

```bash
git add docs/decisions/ADR-030-pending-email-invites.md docs/architecture.md docs/API-SPEC.md CLAUDE.md
git commit -m "docs: ADR-030 and doc sync for pending email invites"
```

---

### Task 9: Frontend types — `pending` field

**Files:**
- Modify: `frontend/src/types/meeting.ts:71-77` (`SharedUser`), `:271-275` (`AccountMember`), `:312-315` (`ProjectMember`)

**Interfaces:**
- Produces: `SharedUser.pending?: boolean`, `AccountMember.pending?: boolean`, `ProjectMember.pending?: boolean` — consumed by Tasks 10-11.

- [ ] **Step 1: Add the field to all three types**

In `frontend/src/types/meeting.ts`, change:

```typescript
export interface SharedUser {
  userId: string;
  email: string;
  name?: string;
  permission: 'read' | 'edit';
  sharedAt: string;
}
```

to:

```typescript
export interface SharedUser {
  userId: string;
  email: string;
  name?: string;
  permission: 'read' | 'edit';
  sharedAt: string;
  /** True when this share is queued for an email that hasn't signed up yet. */
  pending?: boolean;
}
```

Change:

```typescript
export interface AccountMember {
  userId: string;
  email?: string;
  role: string;
}
```

to:

```typescript
export interface AccountMember {
  userId: string;
  email?: string;
  role: string;
  /** True when this invite is queued for an email that hasn't signed up yet. */
  pending?: boolean;
}
```

Change:

```typescript
export interface ProjectMember {
  userId: string;
  email?: string;
}
```

to:

```typescript
export interface ProjectMember {
  userId: string;
  email?: string;
  /** True when this invite is queued for an email that hasn't signed up yet. */
  pending?: boolean;
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd frontend && npm run build 2>&1 | tail -30` (a full static-export build is the fastest way to catch a type error here since there's no standalone `tsc --noEmit` script — check `cat frontend/package.json | grep -A3 '"scripts"'` if you want a faster check and one exists)
Expected: build succeeds (these are additive optional fields, nothing else references them yet, so no new errors).

- [ ] **Step 3: Commit**

```bash
git add frontend/src/types/meeting.ts
git commit -m "feat(frontend): add pending field to SharedUser/AccountMember/ProjectMember types"
```

---

### Task 10: Frontend — email-invite fallback in `MemberPicker` and `ShareButton`

**Files:**
- Modify: `frontend/src/components/MemberPicker.tsx`
- Modify: `frontend/src/components/ShareButton.tsx:165-184` (`handleShare`), `:263-297` (search results render)

**Interfaces:**
- Consumes: `User` type (`frontend/src/types/meeting.ts:101-106`), `pending` field (Task 9).
- Produces: `MemberPicker`'s `onPick` now also fires for a typed-but-unregistered email (as a synthetic `{userId: '', email, name: undefined}`); `ShareButton`'s `handleShare` now reads the actual API response instead of only echoing back the clicked `User`, so it can surface `pending`.

- [ ] **Step 1: Add the email-invite fallback to `MemberPicker`**

In `frontend/src/components/MemberPicker.tsx`, add a helper above the component:

```typescript
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
```

Then, in the render block, change:

```tsx
      {query.length >= 2 && (
        <div className="mt-1 max-h-48 overflow-y-auto rounded-lg border border-slate-200 dark:border-white/10">
          {isSearching ? (
            <div className="p-3 text-center">
              <div className="animate-spin rounded-full h-4 w-4 border-2 border-primary border-t-transparent mx-auto" />
            </div>
          ) : results.length > 0 ? (
            results.map((user) => (
              <button
                key={user.userId}
                type="button"
                onClick={() => {
                  onPick(user);
                  setQuery('');
                  setResults([]);
                }}
                className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-slate-50 dark:hover:bg-white/5"
              >
                <div className="w-6 h-6 rounded-full bg-primary/10 flex items-center justify-center text-primary text-xs font-bold shrink-0">
                  {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm text-slate-900 dark:text-text-main truncate">{user.name || user.email}</p>
                  {user.name && <p className="text-xs text-slate-500 dark:text-text-muted truncate">{user.email}</p>}
                </div>
                <span className="material-symbols-outlined text-primary text-lg">add</span>
              </button>
            ))
          ) : (
            <div className="p-3 text-center text-slate-400 dark:text-text-muted text-sm">No users found</div>
          )}
        </div>
      )}
```

to:

```tsx
      {query.length >= 2 && (
        <div className="mt-1 max-h-48 overflow-y-auto rounded-lg border border-slate-200 dark:border-white/10">
          {isSearching ? (
            <div className="p-3 text-center">
              <div className="animate-spin rounded-full h-4 w-4 border-2 border-primary border-t-transparent mx-auto" />
            </div>
          ) : results.length > 0 ? (
            results.map((user) => (
              <button
                key={user.userId}
                type="button"
                onClick={() => {
                  onPick(user);
                  setQuery('');
                  setResults([]);
                }}
                className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-slate-50 dark:hover:bg-white/5"
              >
                <div className="w-6 h-6 rounded-full bg-primary/10 flex items-center justify-center text-primary text-xs font-bold shrink-0">
                  {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm text-slate-900 dark:text-text-main truncate">{user.name || user.email}</p>
                  {user.name && <p className="text-xs text-slate-500 dark:text-text-muted truncate">{user.email}</p>}
                </div>
                <span className="material-symbols-outlined text-primary text-lg">add</span>
              </button>
            ))
          ) : EMAIL_RE.test(query) ? (
            <button
              type="button"
              onClick={() => {
                onPick({ userId: '', email: query });
                setQuery('');
                setResults([]);
              }}
              className="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-slate-50 dark:hover:bg-white/5"
            >
              <div className="w-6 h-6 rounded-full bg-amber-100 dark:bg-amber-900/30 flex items-center justify-center text-amber-600 dark:text-amber-400 text-xs font-bold shrink-0">
                @
              </div>
              <p className="flex-1 min-w-0 text-sm text-slate-900 dark:text-text-main truncate">이 이메일로 초대: {query}</p>
              <span className="material-symbols-outlined text-primary text-lg">add</span>
            </button>
          ) : (
            <div className="p-3 text-center text-slate-400 dark:text-text-muted text-sm">No users found</div>
          )}
        </div>
      )}
```

- [ ] **Step 2: Add the same fallback to `ShareButton`'s search results**

In `frontend/src/components/ShareButton.tsx`, add the same `EMAIL_RE` constant near the top of the file (after the imports), and change the search-results block (around lines 269-295):

```tsx
              ) : searchResults.length > 0 ? (
                searchResults.map((user) => (
                  <button
                    key={user.userId}
                    onClick={() => handleShare(user)}
                    disabled={isSharing}
                    className="w-full flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
                  >
                    <div className="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center text-primary text-sm font-bold">
                      {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1 text-left">
                      <p className="text-sm font-medium text-slate-900 dark:text-white">
                        {user.name || user.email}
                      </p>
                      {user.name && (
                        <p className="text-xs text-slate-500">{user.email}</p>
                      )}
                    </div>
                    <span className="material-symbols-outlined text-primary">add</span>
                  </button>
                ))
              ) : (
                <div className="p-4 text-center text-slate-500 text-sm">
                  No users found
                </div>
              )}
```

to:

```tsx
              ) : searchResults.length > 0 ? (
                searchResults.map((user) => (
                  <button
                    key={user.userId}
                    onClick={() => handleShare(user)}
                    disabled={isSharing}
                    className="w-full flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
                  >
                    <div className="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center text-primary text-sm font-bold">
                      {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1 text-left">
                      <p className="text-sm font-medium text-slate-900 dark:text-white">
                        {user.name || user.email}
                      </p>
                      {user.name && (
                        <p className="text-xs text-slate-500">{user.email}</p>
                      )}
                    </div>
                    <span className="material-symbols-outlined text-primary">add</span>
                  </button>
                ))
              ) : EMAIL_RE.test(searchQuery) ? (
                <button
                  onClick={() => handleShare({ userId: '', email: searchQuery })}
                  disabled={isSharing}
                  className="w-full flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
                >
                  <div className="w-8 h-8 rounded-full bg-amber-100 dark:bg-amber-900/30 flex items-center justify-center text-amber-600 dark:text-amber-400 text-sm font-bold">
                    @
                  </div>
                  <p className="flex-1 text-left text-sm font-medium text-slate-900 dark:text-white">
                    이 이메일로 초대: {searchQuery}
                  </p>
                  <span className="material-symbols-outlined text-primary">add</span>
                </button>
              ) : (
                <div className="p-4 text-center text-slate-500 text-sm">
                  No users found
                </div>
              )}
```

- [ ] **Step 3: Make `handleShare` surface the actual `pending` response instead of echoing the input**

`handleShare` currently discards `shareApi`'s response and hand-builds the `SharedUser` passed to `onShare` from the clicked `User` alone — which can't know about `pending` (only the API response does), and for the email-invite fallback also has no `name`. Change (around line 165):

```tsx
  const handleShare = async (user: User) => {
    setIsSharing(true);
    try {
      const permission = readOnly ? 'read' : selectedPermission;
      await shareApi(entityId, { email: user.email, permission });
      onShare?.({
        userId: user.userId,
        email: user.email,
        name: user.name,
        permission: readOnly ? 'read' : selectedPermission,
        sharedAt: new Date().toISOString(),
      });
      setSearchQuery('');
      setSearchResults([]);
    } catch (err) {
      console.error('Failed to share:', err);
    } finally {
      setIsSharing(false);
    }
  };
```

to:

```tsx
  const handleShare = async (user: User) => {
    setIsSharing(true);
    try {
      const permission = readOnly ? 'read' : selectedPermission;
      const result = await shareApi(entityId, { email: user.email, permission }) as {
        sharedWith: { userId: string; email: string; permission: string; pending?: boolean };
      };
      onShare?.({
        userId: result.sharedWith.userId,
        email: result.sharedWith.email,
        name: user.name,
        permission: readOnly ? 'read' : selectedPermission,
        sharedAt: new Date().toISOString(),
        pending: result.sharedWith.pending,
      });
      setSearchQuery('');
      setSearchResults([]);
    } catch (err) {
      console.error('Failed to share:', err);
    } finally {
      setIsSharing(false);
    }
  };
```

This narrows `shareApi`'s declared return type too: change the `ShareButtonProps.shareApi` field type from `Promise<unknown>` to the shape above, and update `DocumentShareButton`'s inline `shareApi={(id, { email }) => docApi.share(id, { email })}` call if its return type no longer structurally matches (check `docApi.share`'s return type in `frontend/src/lib/api.ts:507-510` — it already returns `{ sharedWith: { userId, email, permission } }`, which is missing `pending` at the type level even though the runtime JSON will have it once Task 7 ships; add `pending?: boolean` to that inline type, and to `meetingsApi.share`'s and `researchApi.share`'s inline return types at `api.ts:157-158` and `api.ts:378-379`, so `shareApi`'s prop type lines up with all three call sites without an `as` cast at the prop-passing boundary — only the `as` cast inside `handleShare` itself remains, needed because `shareApi`'s prop type is deliberately the narrow shape `ShareButtonProps` declares, not each individual API function's literal return type).

- [ ] **Step 4: Render the pending badge in the "Shared with" list**

In `frontend/src/components/ShareButton.tsx`, change (around line 305-320):

```tsx
                {sharedWith.map((user) => (
                  <div
                    key={user.userId}
                    className="flex items-center gap-3 px-4 py-2"
                  >
                    <div className="w-8 h-8 rounded-full bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-slate-500 text-sm font-bold">
                      {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-slate-900 dark:text-white truncate">
                        {user.name || user.email}
                      </p>
                      <p className="text-xs text-slate-500">
                        {user.permission === 'edit' ? 'Can edit' : 'Can view'}
                      </p>
                    </div>
```

to:

```tsx
                {sharedWith.map((user) => (
                  <div
                    key={user.userId || user.email}
                    className="flex items-center gap-3 px-4 py-2"
                  >
                    <div className="w-8 h-8 rounded-full bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-slate-500 text-sm font-bold">
                      {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-slate-900 dark:text-white truncate">
                        {user.name || user.email}
                      </p>
                      <p className="text-xs text-slate-500">
                        {user.pending
                          ? '초대됨 · 가입 대기중'
                          : user.permission === 'edit' ? 'Can edit' : 'Can view'}
                      </p>
                    </div>
```

(`key={user.userId}` becomes `key={user.userId || user.email}` since a pending entry has an empty `userId`.)

- [ ] **Step 5: Manually verify in dev**

Run: `cd frontend && npm run dev`, open a meeting, click Share, type an email that has never signed up (e.g. `nobody-yet@example.com`), confirm the "이 이메일로 초대" row appears and, after clicking it, the shared-with list shows it with the "초대됨 · 가입 대기중" label instead of erroring.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/MemberPicker.tsx frontend/src/components/ShareButton.tsx frontend/src/lib/api.ts
git commit -m "feat(frontend): invite-by-email fallback + pending badge in ShareButton/MemberPicker"
```

---

### Task 11: Frontend — pending confirmation for account/project member invites

**Files:**
- Modify: `frontend/src/components/AccountDetailClient.tsx:90-101` (`handlePickMember`), member-list render (`:225-257`)
- Modify: `frontend/src/components/ProjectDetailClient.tsx:115-130` (`handleInvite`), member-list render (find via `grep -n "members.map" frontend/src/components/ProjectDetailClient.tsx`)

**Interfaces:**
- Consumes: `AccountMember.pending`/`ProjectMember.pending` (Task 9); `accountApi.addMember`/`projectApi.addMember`'s response (unchanged call signature, now sometimes carrying `pending: true`).

- [ ] **Step 1: Show a one-time confirmation in `AccountDetailClient`**

In `frontend/src/components/AccountDetailClient.tsx`, add a new piece of state near the other `useState` calls (e.g. next to `inviting`): `const [pendingNotice, setPendingNotice] = useState<string | null>(null);`

Change `handlePickMember` from:

```tsx
  const handlePickMember = async (picked: User) => {
    setInviting(true);
    setError(null);
    try {
      await accountApi.addMember(accountId, { email: picked.email, role: inviteRole });
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
    }
  };
```

to:

```tsx
  const handlePickMember = async (picked: User) => {
    setInviting(true);
    setError(null);
    setPendingNotice(null);
    try {
      const member = await accountApi.addMember(accountId, { email: picked.email, role: inviteRole });
      if (member.pending) {
        setPendingNotice(`${picked.email}님에게 초대장을 보냈습니다. 가입하면 자동으로 멤버가 됩니다.`);
      }
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
    }
  };
```

Then, in the render, right above the `{/* Members */}` section's `MemberPicker` row (around line 258-278), add a notice banner. Find:

```tsx
                {user?.userId === account.ownerUserId && (
                  <div className="flex gap-2 pt-2">
```

and insert directly before it:

```tsx
                {pendingNotice && (
                  <p className="text-xs text-amber-600 dark:text-amber-400 pt-2">{pendingNotice}</p>
                )}
                {user?.userId === account.ownerUserId && (
                  <div className="flex gap-2 pt-2">
```

- [ ] **Step 2: Show a one-time confirmation in `ProjectDetailClient`**

Read the member-list render section first: `grep -n "members.map\|handleInvite\|inviteEmail" frontend/src/components/ProjectDetailClient.tsx` to find the exact surrounding JSX (it wasn't fully read during planning — locate the form element that calls `handleInvite` and the notice's insertion point relative to it).

Add `const [pendingNotice, setPendingNotice] = useState<string | null>(null);` near the other state.

Change `handleInvite` from:

```tsx
  const handleInvite = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const email = inviteEmail.trim();
    if (!email) return;
    setInviting(true);
    setError(null);
    try {
      await projectApi.addMember(projectId, { email });
      setInviteEmail('');
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
    }
  };
```

to:

```tsx
  const handleInvite = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const email = inviteEmail.trim();
    if (!email) return;
    setInviting(true);
    setError(null);
    setPendingNotice(null);
    try {
      const member = await projectApi.addMember(projectId, { email });
      if (member.pending) {
        setPendingNotice(`${email}님에게 초대장을 보냈습니다. 가입하면 자동으로 멤버가 됩니다.`);
      }
      setInviteEmail('');
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
    }
  };
```

Then render `{pendingNotice && <p className="text-xs text-amber-600 dark:text-amber-400">{pendingNotice}</p>}` directly below the invite `<form>` element found via the grep above, following whatever indentation/structure that JSX block already uses.

- [ ] **Step 3: Verify it compiles**

Run: `cd frontend && npm run build 2>&1 | tail -30`
Expected: build succeeds.

- [ ] **Step 4: Manually verify in dev**

Run: `cd frontend && npm run dev`, open an account you own, invite a never-registered email via the member picker, confirm the amber notice appears and no error is shown. Repeat on a project you own using the free-text invite field.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/AccountDetailClient.tsx frontend/src/components/ProjectDetailClient.tsx
git commit -m "feat(frontend): show pending-invite confirmation for account/project member invites"
```

---

### Task 12: End-to-end regression pass

**Files:** none created/modified — verification only.

**Interfaces:** none — this task exercises Tasks 1-11 together.

- [ ] **Step 1: Full backend build + test**

Run: `cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb cmd/research-worker cmd/websocket cmd/ws-authorizer; do GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir; done`
Expected: all 8 binaries build with no errors.

Run: `cd backend && /usr/local/go/bin/go test ./... -v 2>&1 | tail -80`
Expected: all PASS, zero regressions in any package touched by Tasks 1-8.

- [ ] **Step 2: Full frontend build + lint**

Run: `cd frontend && npm run lint`
Expected: no new errors.

Run: `cd frontend && npm run build`
Expected: static export succeeds.

- [ ] **Step 3: Manual end-to-end walkthrough (dev servers)**

With `cd frontend && npm run dev` running against a real (or already-deployed) backend:
1. Share a meeting to a never-registered email → confirm the "초대됨 · 가입 대기중" row and no error.
2. Invite a never-registered email as an account member → confirm the amber pending notice.
3. Invite a never-registered email as a project member → confirm the amber pending notice.
4. Share a personal document with a never-registered email via `DocumentShareButton` → confirm the pending row.
5. Share a research report with a never-registered email → confirm the pending row.
6. Have that invited email actually sign up (or, if self-signup is disabled per the concurrent ADR-007 work, have an admin `AdminCreateUser` it and complete the temp-password login) and load the app — confirm all 5 grants above are now live: the meeting/doc/research appear in that user's respective lists, and they show up as a real (non-pending) account/project member.

- [ ] **Step 4: No commit for this task** — it's verification-only. If Step 3's walkthrough surfaces a bug, fix it as a small follow-up commit referencing which numbered task it belongs to.
