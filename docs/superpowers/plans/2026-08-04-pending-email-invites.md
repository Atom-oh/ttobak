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
- **Pending invites need their own list/revoke path.** The existing revoke
  routes (`DELETE .../share/{userId}` 등) key on a `userId` that a pending
  invite doesn't have yet, so without a dedicated path a mistyped-email invite
  can't be cancelled until its TTL expires. Task 7 must include
  `GET /api/pending-invites?entityId=...` (owner-only) and
  `DELETE /api/pending-invites/{type}/{entityId}?email=...` (owner-only,
  deletes the `PENDINGINVITE#` row) alongside surfacing `Pending` in
  responses.

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
// Two rows per invite, written/deleted together via TransactWriteItems --
// same dual-row convention as the Research-Account RESEARCHREF# pattern:
//   canonical:   PK PENDINGINVITE#{lowercased email} / SK {Type}#{EntityID}
//                (what signup-time reconciliation queries by email)
//   reverse ref: PK PENDINGREF#{EntityID} / SK PENDINGINVITE#{lowercased email}#{Type}
//                (what Task 7's owner-side "list pending invites for my
//                entity" endpoint queries -- without it, entityId-scoped
//                lookup would require a table Scan)
// Both rows carry the same attributes below (incl. TTL) so either read is
// self-sufficient; the reverse ref is denormalized, and DeletePendingInvite
// removes both transactionally.
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
	// ExpiresAt maps to the table's ONE shared TTL attribute name "TTL"
	// (epoch seconds) -- DynamoDB allows a single TTL attribute per table,
	// and backend/python/qa/handler.py already writes "TTL" on its
	// session/feedback/cache rows (with its own lazy read-time expiry
	// filtering, so enabling real sweeping is compatible). Do NOT introduce
	// a second attribute name like "expiresAt"; enabling TTL on it would
	// permanently orphan the QA rows' field. A pending invite holds PII (an
	// email address) for someone who may never sign up -- the repo's
	// data-handling mandate requires PII rows in DynamoDB to carry a TTL,
	// so unclaimed invites self-expire (90 days). Reconciliation must treat
	// an expired-but-not-yet-swept row (TTL deletion is lazy) as absent:
	// skip when time.Now().Unix() > ExpiresAt.
	ExpiresAt  int64  `dynamodbav:"TTL"`
	EntityType string `dynamodbav:"entityType"` // "PENDING_INVITE"
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
- Modify: `infra/lib/storage-stack.ts` — the `ttobak-main` Table construct has
  no TTL configured and the live table's TTL status is DISABLED (verified via
  `aws dynamodb describe-time-to-live`); add `timeToLiveAttribute: 'TTL'` —
  the attribute name `backend/python/qa/handler.py` ALREADY writes (DynamoDB
  supports one TTL attribute per table, so pending invites reuse it rather
  than introducing a second name). Side effect to note in the PR: QA's
  session/feedback/KB-cache rows, which today rely only on lazy read-time
  filtering, will start being genuinely swept — compatible, since their
  readers already treat expired rows as absent. Without this CDK change the
  attribute is inert and the PII-TTL mandate is not met. (Deploy:
  `npx cdk deploy TtobakStorageStack --exclusively` — TTL enablement is an
  in-place UpdateTimeToLive call, no table replacement.)

**Interfaces:**
- Consumes: `model.PendingInvite` (Task 1); existing `(*DynamoDBRepository).CreateShare`, `.CreateDocShare`, `.CreateResearchShare`, `.PutMember`, `.PutProjectMember` (all pre-existing, unchanged signatures).
- Produces: `(*DynamoDBRepository) CreatePendingInvite(ctx, invite *model.PendingInvite) error`, `.ListPendingInvites(ctx, email string) ([]model.PendingInvite, error)`, `.ListPendingInvitesForEntity(ctx, entityID string) ([]model.PendingInvite, error)` (Task 7의 owner-side GET이 읽는 `PENDINGREF#{entityId}` 파티션 Query), `.DeletePendingInvite(ctx, email, invType, entityID string) error` (양쪽 행을 트랜잭션 삭제 — 키는 내부 파생) — the first/second/fourth are what Task 3-6's service interfaces will add; the third is consumed only by Task 7's pending-invite handler service. `reconcilePendingInvites` is package-private, called only from `GetOrCreateUser`.

- [ ] **Step 1: Write the failing repository test (pure functions only)**

Create `backend/internal/repository/pending_invite_test.go`. **This repo's
repository tests have NO DynamoDB-local/LocalStack bootstrap** — every
existing `*_test.go` in `backend/internal/repository/` tests pure decision
functions with no AWS client (see `dynamodb_test.go`'s
`classifyProjectIDsPutItemErr` tests, `research_test.go`'s
`mapTransactionCanceledError` tests). Follow that convention: extract the
key-derivation and expiry logic into pure functions and test those. The
DynamoDB round-trip behavior (create → list → reconcile → consumed) is
covered at the SERVICE layer with mock repos in Tasks 3–6, same as every
other feature. Structure the implementation as:

```go
// pendingInviteKeys derives all four key strings for an invite's two rows.
func pendingInviteKeys(email, invType, entityID string) (canonicalPK, canonicalSK, refPK, refSK string)

// pendingInviteExpired reports whether an invite's TTL has lapsed
// (TTL sweep is lazy, so reads must filter).
func pendingInviteExpired(inv *model.PendingInvite, now time.Time) bool
```

Then write:

```go
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestPendingInviteKeys(t *testing.T) {
	// Email is lowercased in BOTH rows' keys (lookup is case-insensitive),
	// and the two rows' keys mirror each other so a Create/Delete pair over
	// the same identity always touches the same two items.
	cpk, csk, rpk, rsk := pendingInviteKeys("New.User@Example.com", model.PendingInviteMeetingShare, "meeting-1")
	if cpk != "PENDINGINVITE#new.user@example.com" || csk != model.PendingInviteMeetingShare+"#meeting-1" {
		t.Fatalf("bad canonical keys: %s / %s", cpk, csk)
	}
	if rpk != "PENDINGREF#meeting-1" || rsk != "PENDINGINVITE#new.user@example.com#"+model.PendingInviteMeetingShare {
		t.Fatalf("bad ref keys: %s / %s", rpk, rsk)
	}
}

func TestPendingInviteExpired(t *testing.T) {
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	fresh := &model.PendingInvite{ExpiresAt: now.Add(time.Hour).Unix()}
	lapsed := &model.PendingInvite{ExpiresAt: now.Add(-time.Hour).Unix()}
	// zero ExpiresAt (a row written by code that skipped CreatePendingInvite's
	// central stamping) must read as expired, never as immortal.
	zero := &model.PendingInvite{}
	if pendingInviteExpired(fresh, now) {
		t.Fatal("fresh invite must not be expired")
	}
	if !pendingInviteExpired(lapsed, now) || !pendingInviteExpired(zero, now) {
		t.Fatal("lapsed and zero-TTL invites must both read as expired")
	}
}
```

The full round-trip (create pending → new-user signup materializes the grant
→ pending consumed, both rows gone; second signup call is a no-op) is
asserted at the service layer in Tasks 3–6 with mock repos — the mocks must
model BOTH rows (canonical + ref) so a service-layer regression that orphans
the ref row fails a test.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd backend && /usr/local/go/bin/go test ./internal/repository/... -run TestPendingInvite -v`
Expected: FAIL — `pendingInviteKeys`/`pendingInviteExpired` undefined (functions don't exist yet).

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
// pendingInviteTTL bounds how long an unclaimed invite (PII: an email
// address) may sit in the table before DynamoDB TTL sweeps it.
const pendingInviteTTL = 90 * 24 * time.Hour

func (r *DynamoDBRepository) CreatePendingInvite(ctx context.Context, invite *model.PendingInvite) error {
	invite.PK = pendingInvitePK(invite.Email)
	invite.SK = invite.Type + "#" + invite.EntityID
	invite.EntityType = model.EntityTypePendingInvite
	// Set centrally here, NOT at the five service call sites -- a call site
	// that forgot would store TTL: 0, which the reconciliation guard
	// (now > ExpiresAt) reads as already-expired and silently skips.
	invite.ExpiresAt = time.Now().Add(pendingInviteTTL).Unix()
	item, err := attributevalue.MarshalMap(invite)
	if err != nil {
		return fmt.Errorf("failed to marshal pending invite: %w", err)
	}
	// Denormalized reverse ref for owner-side entity-scoped listing
	// (GET /api/pending-invites) -- same attributes, entity-keyed.
	ref := *invite
	ref.PK = "PENDINGREF#" + invite.EntityID
	ref.SK = pendingInvitePK(invite.Email) + "#" + invite.Type
	refItem, err := attributevalue.MarshalMap(&ref)
	if err != nil {
		return fmt.Errorf("failed to marshal pending invite ref: %w", err)
	}
	// One transaction so the two rows can never diverge (mirrors the
	// Research-Account LinkAccountTransactional convention).
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: item}},
			{Put: &types.Put{TableName: aws.String(r.tableName), Item: refItem}},
		},
	}); err != nil {
		return fmt.Errorf("failed to put pending invite: %w", err)
	}
	return nil
}
// DeletePendingInvite must delete BOTH rows in one TransactWriteItems call
// (canonical + PENDINGREF#) -- reconciliation and the revoke endpoint both
// route through it.

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

// DeletePendingInvite removes one materialized (or abandoned) pending
// invite -- BOTH rows (canonical + PENDINGREF#) in one TransactWriteItems,
// matching how CreatePendingInvite wrote them. The signature takes the
// invite's identity (email, invType, entityID), not raw pk/sk: both keys
// are derived internally, so no caller can structurally delete only one
// row and orphan the other (a leftover ref would keep showing an
// already-consumed/cancelled invite in the owner-side GET for up to the
// 90-day TTL).
func (r *DynamoDBRepository) DeletePendingInvite(ctx context.Context, email, invType, entityID string) error {
	canonicalKey := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pendingInvitePK(email)},
		"SK": &types.AttributeValueMemberS{Value: invType + "#" + entityID},
	}
	refKey := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "PENDINGREF#" + entityID},
		"SK": &types.AttributeValueMemberS{Value: pendingInvitePK(email) + "#" + invType},
	}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: canonicalKey}},
			{Delete: &types.Delete{TableName: aws.String(r.tableName), Key: refKey}},
		},
	}); err != nil {
		return fmt.Errorf("failed to delete pending invite: %w", err)
	}
	return nil
}

// reconcilePendingInvites materializes every PendingInvite queued for a
// brand-new user's email into its real Share/AccountMember/ProjectMember
// row, then deletes the pending row. Called once, from GetOrCreateUser,
// right after a new profile is created -- never on an existing user's
// lookup. A single row's materialization failing is logged and skipped
// rather than failing signup -- a user must never fail to provision
// because a stale invite couldn't be replayed.
//
// IMPORTANT (zombie-grant prevention): before each materialization below,
// verify the target entity still exists (GetMeetingByID / getDoc /
// GetResearch / GetAccount / GetProject returning non-nil). When it
// doesn't, skip the materialization AND delete the pending row (fall
// through to DeletePendingInvite) so the dead invite doesn't replay
// forever. The Create*/Put* calls this loop uses are unconditional puts,
// so without that existence check an invite whose meeting/doc/account/
// project was deleted while it sat pending would silently create a grant
// row pointing at nothing -- the same zombie pattern the Research-Account
// link's attribute_exists(PK) transaction exists to prevent (see
// CLAUDE.md). The snippets below elide those checks for brevity; the
// implementation must include them (one existence read per invite,
// before the switch).
func (r *DynamoDBRepository) reconcilePendingInvites(ctx context.Context, user *model.User) {
	invites, err := r.ListPendingInvites(ctx, user.Email)
	if err != nil {
		log.Printf("reconcilePendingInvites: list failed for %s: %v", user.Email, err)
		return
	}
	for _, inv := range invites {
		// TTL deletion is lazy -- an expired row can linger for up to ~48h
		// before DynamoDB sweeps it. Treat it as already gone. (ExpiresAt is
		// always set: CreatePendingInvite stamps it centrally.)
		if time.Now().Unix() > inv.ExpiresAt {
			continue
		}
		var matErr error
		switch inv.Type {
		case model.PendingInviteMeetingShare:
			_, matErr = r.CreateShare(ctx, inv.EntityID, inv.OwnerID, inv.OwnerEmail, user.UserID, user.Email, inv.Permission, "")
		case model.PendingInviteDocShare:
			_, matErr = r.CreateDocShare(ctx, inv.EntityID, inv.OwnerID, inv.OwnerEmail, user.UserID, user.Email)
		case model.PendingInviteResearchShare:
			// CreateResearchShare lives on *ResearchRepository, NOT on
			// *DynamoDBRepository -- calling r.CreateResearchShare here would
			// not compile. Construct one over the same client/table
			// (NewResearchRepository(r.client, r.tableName) is stateless) or
			// hold it as a field initialized in the DynamoDBRepository
			// constructor; the snippet assumes a `researchRepo` field.
			_, matErr = r.researchRepo.CreateResearchShare(ctx, inv.EntityID, inv.OwnerID, inv.OwnerEmail, user.UserID, user.Email, inv.Permission)
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
		if err := r.DeletePendingInvite(ctx, inv.Email, inv.Type, inv.EntityID); err != nil {
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
	// attribute_not_exists makes "brand-new user" a fact the write itself
	// proves: two concurrent first-requests can both reach this branch off
	// the same empty read, but only one Put wins -- the loser gets
	// ConditionalCheckFailedException, re-reads the (now-existing) profile,
	// and skips reconciliation, so reconcile runs at most once per user
	// instead of racing twice over the same pending rows.
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return r.GetUser(ctx, userID) // lost the race; profile now exists
		}
		return nil, fmt.Errorf("failed to put user: %w", err)
	}

	r.reconcilePendingInvites(ctx, user)

	return user, nil
}
```

(This only runs in the "new user" branch of the function, not the earlier `if result.Item != nil { ... return &user, nil }` branch — reconciliation must never re-run for an existing user. Adjust the lost-race re-read to whatever user-fetch helper the function's earlier branch already uses.)

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd backend && /usr/local/go/bin/go test ./internal/repository/... -run TestPendingInvite -v`
Expected: PASS (both `TestPendingInviteCreateListDelete` and `TestPendingInviteReconciledOnFirstSignup`).

- [ ] **Step 6: Run the full repository test suite to check for regressions**

Run: `cd backend && /usr/local/go/bin/go test ./internal/repository/... -v`
Expected: all PASS, including every pre-existing `GetOrCreateUser`-related test.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/repository/pending_invite.go backend/internal/repository/pending_invite_test.go backend/internal/repository/dynamodb.go infra/lib/storage-stack.ts
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
func TestAccountAddMember_PendingInviteForUnregisteredEmail(t *testing.T) {
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

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestAccountAddMember_PendingInvite -v` and `-run TestShareUserDocumentByEmail_Pending`
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
		return &model.Share{Email: targetEmail, Permission: model.PermissionRead, Pending: true}, nil
	}
	if targetUser.UserID == ownerID {
		return nil, ErrSelfShare
	}
	return s.repo.CreateDocShare(ctx, docID, ownerID, ownerEmail, targetUser.UserID, targetEmail)
```

(`strings` is already imported in `account.go`.)

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run 'TestAccountAddMember_PendingInvite|TestShareUserDocumentByEmail_' -v`
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
func TestProjectAddMember_PendingInviteForUnregisteredEmail(t *testing.T) {
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

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestProjectAddMember_PendingInviteForUnregisteredEmail -v`
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

Run: `cd backend && /usr/local/go/bin/go test ./internal/service/... -run TestProjectAddMember_PendingInviteForUnregisteredEmail -v`
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

### Task 7: Handlers — surface `Pending` in share responses + pending list/revoke endpoints

**Files:**
- Modify: `backend/internal/handler/share.go:79-87` (`ShareMeeting`)
- Modify: `backend/internal/handler/document.go:171-173` (`ShareWithUser`)
- Modify: `backend/internal/handler/research_share.go:76-80` (research share handler)
- Create: `backend/internal/handler/pending_invite.go` — the owner-only
  list/revoke endpoints required by Global Constraints:
  `GET /api/pending-invites?entityId=...&type=...` (list this owner's pending
  invites for an entity they own — served by a Query on the
  `PENDINGREF#{entityId}` reverse-ref partition Task 1/2 define, no Scan) and
  `DELETE /api/pending-invites/{type}/{entityId}?email=...` (cancel a
  mistyped-email invite: verify caller owns the entity, then
  `DeletePendingInvite`, which removes canonical + ref rows transactionally).
  Register both in `cmd/api/main.go`'s authenticated subrouter.
- Create: `backend/internal/handler/pending_invite_test.go`

**Interfaces:**
- Consumes: `model.Share.Pending`, `model.ShareResponse.Pending` (Task 1); `MeetingService.ShareMeetingByEmail`, `AccountService.ShareUserDocumentByEmail`, `ResearchService.ShareResearchByEmail` (Tasks 3/5/6, unchanged signatures, now sometimes returning `Pending: true` with no error); `ListPendingInvites`/`DeletePendingInvite` (Task 2) via a small service wrapper that enforces entity ownership before touching the row.

The three `Pending` pass-throughs need no handler test changes (pure field
additions, `omitempty`); the NEW pending-invite endpoints DO need stdlib
handler tests (ownership rejection, happy-path list/delete) in
`pending_invite_test.go` per the repo's Go test-coverage expectation.

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
git add backend/internal/handler/share.go backend/internal/handler/document.go backend/internal/handler/research_share.go backend/internal/handler/pending_invite.go backend/internal/handler/pending_invite_test.go backend/cmd/api/main.go
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
- A pending invite is stored as two rows written in one transaction: the
  canonical row keyed by email (what signup reconciliation reads) and a
  `PENDINGREF#{entityId}` reverse-ref row (what the owner-only
  `GET /api/pending-invites?entityId=...` list reads — same dual-row
  convention as the Research↔Account `RESEARCHREF#` pattern, and for the
  same reason: an entity-scoped lookup on an email-keyed row would need a
  table Scan). `DELETE /api/pending-invites/{type}/{entityId}?email=...`
  cancels a mistyped-email invite by removing both rows transactionally.
  An unclaimed invite that is never cancelled self-expires via the 90-day
  DynamoDB TTL (shared table attribute `TTL`) instead of sitting inert
  forever.
  Frontend surfaces `pending: true` as a one-time confirmation at invite
  time, not a persisted badge on the member/share list (see
  `docs/superpowers/specs/2026-08-04-pending-email-invites-design.md`).
- Materialization failure for one pending row (its underlying
  meeting/doc/research/account/project was deleted while the invite sat
  pending) is logged and skipped, never fails the user's signup.
- Two accepted limitations, both bounded by fail-closed reads: (1) the
  existence check before materialization is check-then-act — an entity
  deleted in that window still gets a grant row, but grant rows confer no
  access by themselves (every read path re-verifies the target and 404s/
  skips missing entities, same as the repo's existing share/ref rows), so
  the residue is cosmetic, not a privilege. (2) Reconciliation is
  deliberately one-shot at signup; a transiently-failed row stays pending
  (not deleted) until its TTL. Recovery is the normal share/add-member
  flow — the recipient now exists as a real user, so the owner simply
  re-invites and the live path takes over. Neither warrants a retry
  daemon at this feature's scale.
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

- [ ] **Step 1b: Infra synth + tests (Task 2's storage-stack TTL change)**

Run: `cd infra && npx cdk synth && npm test`
Expected: synth succeeds and jest passes — this validates the
`timeToLiveAttribute: 'TTL'` addition compiled and didn't break existing
stack assertions.
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
