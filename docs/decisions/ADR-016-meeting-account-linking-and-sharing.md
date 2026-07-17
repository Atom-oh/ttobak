# ADR-016: Meeting↔Account Linking and Team Sharing via MeetingRef

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — implemented on `feat/account-foundation` (Plan 2 of 6). `LinkMeetingToAccount`, `ShareMeetingToAccount`, and `ListAccountMeetings` are live; routes `POST /api/meetings/{id}/account`, `POST /api/meetings/{id}/share-account`, `GET /api/accounts/{id}/meetings`.

## Context
With [ADR-015](ADR-015-account-first-class-shared-entity.md) establishing Account, meetings need to attach to an account in two distinct modes:

1. **Link (classify)** — the owner tags their meeting with an account for their own organization, without exposing it to the team.
2. **Share (publish)** — the owner publishes the meeting to the whole account team so every member can read it.

Listing "all meetings shared into this account" must not require a cross-partition scan of every member's meetings. And because DynamoDB has no multi-item atomic write beyond `TransactWriteItems` (100-item cap), a share that fans out to N members must degrade safely on partial failure.

## Decision
- **Link** sets `Meeting.AccountID` only (owner + account-member gated). No sharing, no team visibility.
- **Share** sets `AccountID` + `SharedToAccount=true`, writes a lightweight **`MeetingRef`** into the account partition (`PK=ACCOUNT#{id}`, `SK=MEETINGREF#{occurredAt}#{meetingId}`), then grants a read `Share` to every account member except the owner.
- **MeetingRef** lets `ListAccountMeetings` return the account's shared meetings with a single partition query (newest-first), no cross-partition scan.
- **Write order is deliberate and non-transactional**: `UpdateMeeting` → `PutMeetingRef` → (insight fan-out) → per-member `CreateShare`. The `MeetingRef` is written **before** the share loop so a list-visible record always exists even if a later `CreateShare` fails; every write targets a fixed key, so a client retry converges. `TransactWriteItems` was rejected because its 100-item limit would cap account team size.

## Consequences

### Positive
- Clean separation of private classification vs team publication.
- `ListAccountMeetings` is O(1 query) and member-gated.
- Partial-failure-safe: a crash mid-share leaves a visible ref (recoverable by retry), never a `sharedToAccount=true` meeting with no ref.

### Negative
- Non-transactional: a crash can leave some members without their read `Share` until retry (they see the ref but can't open the meeting yet).
- `MeetingRef.SK` embeds `meeting.Date`; if the date is later edited and the meeting re-shared, the old ref orphans (new SK differs).

### Risks
- ~~Removing a member from an account does not revoke previously granted per-meeting shares~~ — **superseded**: `RemoveMember` attempts a best-effort revocation of every `origin=="account"` Share for the removed member across all of the account's `MeetingRef`s, surfacing any per-meeting failure in the response (`cleanupFailedForMeetings`) instead of only in logs. But that revocation attempt is not what actually enforces access anymore: `checkAccess` (`meeting.go`) re-verifies live account membership at READ TIME whenever it sees an `origin=="account"` Share row, rather than trusting the row unconditionally. This means a removed member loses access immediately even if the cleanup delete never ran (transient DynamoDB error, or a `ShareMeetingToAccount` write racing the `RemoveMember` snapshot) — the best-effort cleanup is now a housekeeping optimization (avoids an extra `GetMember` call on every future read of an already-fully-cleaned-up share) rather than the sole enforcement mechanism, closing the permanent-access gap without needing a reconciliation job. New account-origin shares are created transactionally (`CreateShareIfMember`, closing the `AddMember`↔`ShareMeetingToAccount` TOCTOU gap). Pre-existing shares written before the `Origin` field existed are not automatically revocable (they collapse indistinguishably with direct shares — see `docs/API-SPEC.md`'s Known limitation note); `backend/cmd/backfill-share-origin` is a manually-run, one-account-at-a-time remediation CLI that tags them for accounts an operator has reviewed.

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| MeetingRef + per-member Share, ref-first ordering (chosen) | Single-query list, no team-size cap, safe partial failure | Not atomic; possible transient missing shares |
| `TransactWriteItems` for the whole share | Atomic | 100-item cap limits team size |
| No ref, scan members' meetings for `accountId` | No extra item | Cross-partition scan, O(members) per list |

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — `feat/account-foundation`에서 구현(Plan 2/6). `LinkMeetingToAccount`/`ShareMeetingToAccount`/`ListAccountMeetings` 동작 중. 라우트: `POST /api/meetings/{id}/account`, `POST /api/meetings/{id}/share-account`, `GET /api/accounts/{id}/meetings`.

## 맥락
[ADR-015](ADR-015-account-first-class-shared-entity.md)로 Account가 생기면서, 미팅을 Account에 붙이는 두 가지 모드가 필요했다:

1. **연결(분류)** — 소유자가 팀에 노출하지 않고 자기 정리용으로 Account 태그.
2. **공유(게시)** — 소유자가 Account 팀 전체에 게시해 모든 멤버가 읽도록.

"이 Account에 공유된 모든 미팅" 조회가 멤버별 미팅을 교차 파티션 스캔하지 않아야 한다. 또한 DynamoDB는 `TransactWriteItems`(100개 한도) 외 다중 항목 원자 쓰기가 없어, N명에게 팬아웃하는 공유는 부분 실패 시 안전하게 degrade해야 한다.

## 결정
- **연결**은 `Meeting.AccountID`만 설정(소유자 + Account 멤버 게이트). 공유·팀 가시성 없음.
- **공유**는 `AccountID` + `SharedToAccount=true` 설정 후, Account 파티션에 경량 **`MeetingRef`**(`PK=ACCOUNT#{id}`, `SK=MEETINGREF#{occurredAt}#{meetingId}`)를 기록하고, 소유자를 제외한 전 멤버에게 읽기 `Share` 부여.
- **`MeetingRef`** 덕분에 `ListAccountMeetings`가 단일 파티션 쿼리(최신순)로 공유 미팅을 반환, 교차 스캔 불필요.
- **쓰기 순서는 의도적·비트랜잭션**: `UpdateMeeting` → `PutMeetingRef` → (인사이트 팬아웃) → 멤버별 `CreateShare`. share 루프 **전에** `MeetingRef`를 먼저 기록 — 이후 `CreateShare`가 실패해도 목록에 보이는 레코드는 항상 존재. 모든 쓰기가 고정 키 대상이라 클라이언트 재시도로 수렴. `TransactWriteItems`는 100개 한도가 팀 크기를 제약해 기각.

## 결과

### 긍정
- 비공개 분류 vs 팀 게시의 명확한 분리.
- `ListAccountMeetings`는 단일 쿼리이며 멤버 게이트.
- 부분 실패 안전: 공유 중 중단돼도 보이는 ref는 남음(재시도 복구), ref 없는 `sharedToAccount=true` 미팅은 발생하지 않음.

### 부정
- 비트랜잭션: 중단 시 일부 멤버가 재시도 전까지 읽기 `Share`를 못 받음(ref는 보이나 미팅 열기 불가).
- `MeetingRef.SK`에 `meeting.Date` 포함 — 이후 날짜 수정 후 재공유 시 옛 ref가 고아가 됨.

### 위험
- ~~Account에서 멤버 제거 시 이미 부여된 미팅별 share는 회수되지 않음~~ — **대체됨**: `RemoveMember`가 제거된 멤버의 `origin=="account"` Share를 해당 Account의 모든 `MeetingRef`에 걸쳐 best-effort로 회수 시도하며, 미팅별 실패는 응답(`cleanupFailedForMeetings`)으로 노출된다. 하지만 실제 접근 통제는 이 회수 시도가 아니라 `checkAccess`(`meeting.go`)의 읽기 시점(read-time) 재검증이 담당한다 — `origin=="account"` Share row를 보면 그 row를 무조건 신뢰하지 않고 현재 account membership을 즉시 재확인한다. 따라서 cleanup의 삭제 자체가 실패해도(일시적 DynamoDB 오류, 또는 `RemoveMember`의 snapshot과 경쟁하는 `ShareMeetingToAccount` 쓰기 등) 제거된 멤버는 즉시 접근을 잃는다 — best-effort cleanup은 이제 유일한 집행 수단이 아니라 하우스키핑 최적화(이미 완전히 정리된 share에 대해 매 읽기마다 추가 `GetMember` 호출을 피함)일 뿐이며, reconciliation job 없이도 영구 접근 잔존 갭이 닫혔다. 신규 account-origin share는 트랜잭션(`CreateShareIfMember`)으로 생성돼 `AddMember`↔`ShareMeetingToAccount` 사이 TOCTOU 갭도 닫혔다. `Origin` 필드 도입 이전에 쓰인 기존 share는 direct share와 구분 불가능하게 겹쳐 자동 회수되지 않음(`docs/API-SPEC.md`의 Known limitation 참고) — `backend/cmd/backfill-share-origin`이 운영자가 검토한 계정을 한 번에 하나씩 태깅하는 수동 실행 remediation CLI다.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| MeetingRef + 멤버별 Share, ref 우선 순서(채택) | 단일 쿼리 목록, 팀 크기 무제한, 안전한 부분 실패 | 비원자적, 일시적 share 누락 가능 |
| 공유 전체를 `TransactWriteItems` | 원자적 | 100개 한도가 팀 크기 제약 |
| ref 없이 `accountId`로 멤버 미팅 스캔 | 추가 항목 없음 | 교차 파티션 스캔, 목록당 O(멤버 수) |
