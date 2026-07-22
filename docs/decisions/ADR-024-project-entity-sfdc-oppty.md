# ADR-024: Project (SFDC Opportunity) Entity — Graph-Reference Linking, Hybrid Membership, Read-Time Insight Aggregation

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
Meetings, research, and insights currently live under Account — a first-class, team-shared entity representing one customer. In practice, a single sales opportunity (SFDC Opportunity) often spans a distinct engagement — its own set of engaged people, its own set of meeting notes — and can involve more than one Account (a partner account plus the end-customer account, for example). Account's model doesn't fit this: Account membership is the unit of both identity and access, and Account↔Meeting is a singular field (`Meeting.AccountID`), not a graph.

We needed a new entity — Project — that:
1. Groups meetings, research, and insights by opportunity rather than by customer.
2. Can link to more than one Account (many-to-many).
3. Can be populated externally via MCP (an SFDC MCP client reads an Opportunity and calls `ttobak_create_project`) — no direct SFDC API integration in this codebase, metadata fields only.
4. Reuses proven patterns rather than inventing new ones, since Research↔Account linking (added earlier, then hardened across several review rounds — see AGENTS.md/CLAUDE.md's "Research↔Account linking" section) already solved the exact class of problem (graph-style linking with atomic set mutation and fail-closed reads) this entity needs.

## Decision

### 1. Graph-reference architecture, not a new pattern
Project↔Account is many-to-many via `Project.AccountIDs` (DynamoDB String Set) plus a reverse-index item (`ACCOUNT#{accountId}/PROJECTREF#{projectId}`) — the same shape as `Research.AccountIDs`/`ResearchRef`. Project↔Meeting and Project↔Research are the same shape (`Meeting.ProjectIDs`, `Research.ProjectIDs`, `PROJECT#{id}/MEETINGREF#{date}#{meetingId}`, `PROJECT#{id}/RESEARCHREF#{researchId}`).

**Link/unlink mutations are a single `TransactWriteItems` call** (`ProjectAccountLinkTransactional`, `MeetingProjectLinkTransactional`, `ResearchProjectLinkTransactional` and their unlink counterparts), mirroring `ResearchRepository.LinkAccountTransactional`/`UnlinkAccountTransactional` — not two separate requests. Two separate requests (an early implementation of this PR's own review cycle used exactly this shape before being corrected) reopen the interleaving gap the transactional pattern exists to close: a concurrent link/unlink pair for the same (project, account) could leave the canonical set linked but the reverse-index ref deleted, permanently hiding a real link from `ListAccountProjects`.

**Read paths fail closed**: every ref-driven list (`ListProjectMeetings`, `ListProjectResearch`, `GetProjectInsights`, `ListAccountProjects`) treats the reverse-index ref as a candidate only, re-verifying membership in the canonical set before returning a result. A stale ref left behind by any failure mode is invisible to the caller, never a false positive.

**`ProjectMeetingRef`'s SK embeds `Meeting.Date`** (mutable) for sort order — this created a duplicate-ref bug (a re-link after Date changed would Put a second ref at a different SK instead of updating the original) that later rounds of review caught and fixed: `LinkMeeting`'s idempotent branch and `UnlinkMeeting` both now look up the ref's actual existing SK via `existingProjectMeetingRefSK` instead of recomputing it from the meeting's current Date, and every ref-driven meeting list also dedups by `MeetingID` as a second line of defense.

**`DeleteProject` rejects deletion while any relation exists** (linked accounts, meetings, research, or direct members) rather than cascading. A cascade delete would need to remove an unbounded number of MEMBER#/ref items across multiple partitions — for a project with enough relations, that could exceed `TransactWriteItems`' 100-item cap (the same limit `ShareMeetingToAccount` already works around by not using a single transaction for per-member shares), and a partial non-transactional cascade risks leaving a half-cleaned-up mess on failure. Requiring the caller to unlink everything first via the existing APIs is simpler and has no partial-failure state to reason about.

**`UpdateMeeting`'s existing whole-item `PutItem` re-fetches and preserves `ProjectIDs`** before writing. `UpdateMeeting` was already a full-item overwrite (not a partial `UpdateItem`) before this feature, used by the STT pipeline (transcribe/summarize) and most meeting mutations — every one of those call sites would otherwise silently revert (or, since the field is `omitempty`, delete) a `ProjectIDs` change made by a concurrent `LinkMeeting`/`UnlinkMeeting` call, defeating the very reason `ProjectIDs` is a String Set with atomic ADD/DELETE. A projected `GetItem` for just that one attribute, done immediately before the write, narrows the race window from "however long the caller held its in-memory copy" (potentially an entire STT run) to ordinary single-item read-then-write latency. This does not fix the underlying whole-item-write pattern for any other field — that's a pre-existing, broader issue tracked separately (see Consequences), not something this ADR's scope covers.

### 2. Hybrid membership: Account-inherited + direct members
`requireProjectAccess` grants access to: the project owner, a directly-invited member (`PROJECT#{id}/MEMBER#{userId}`, via `AddMember`/`RemoveMember`), or a member of **any** linked Account. This means linking an Account to a Project automatically extends project access to that Account's entire team, without needing individual invites — matching the real workflow (an opportunity's engaged team overlaps heavily with the customer account's existing team) while still allowing project-only specialists (e.g. a deal desk analyst with no Account membership) to be added directly.

**Unlinking an Account requires only project ownership, not current membership in that Account.** An earlier version required both, which created a revocation deadlock: if the project owner was later removed from the linked Account, `GetMember` would return nil and nobody could ever unlink that Account again — while every remaining member of that Account kept indefinite project access via the inheritance path above. The authority that created a link (project ownership) is sufficient to remove it.

**Project members can see linked meetings' titles, dates, and insights regardless of `Meeting.SharedToAccount`.** This is a deliberate, additional sharing channel layered on top of (not gated by) the existing Account-sharing publication step — a project groups an opportunity's engaged people around exactly the meetings relevant to that opportunity, which is a narrower and more deliberate act (the meeting's owner explicitly links it) than blanket Account sharing. It does mean that once a meeting is linked and an Account is linked, the exposure surface grows automatically as that Account's membership grows — an accepted tradeoff of the inheritance model above, not an oversight.

### 3. Read-time insight aggregation, no persistence
`GetProjectInsights` parses each linked meeting's `Meeting.Insights` JSON on every call and returns DTOs directly — it does not write persisted insight rows the way `BuildAccountInsights` does for Account. The tradeoff is deliberate: Account's persisted-insight model needs an explicit re-sync whenever a meeting is re-summarized (materialized at share time, going stale until the next share), while Project's read-time model can never go stale and needs no sync step at all, at the cost of parsing N meetings' JSON on every `GetProjectInsights`/`GetProjectBrief` call instead of one indexed query. Given a project's linked-meeting count is expected to stay in the tens, not thousands, this cost is accepted as negligible; if that assumption stops holding, the mitigation is caching or pagination on this read path, not switching to the persisted model (which would reintroduce the sync problem this design avoids).

### 4. Explicit scope exclusions (YAGNI)
- No direct SFDC API integration — `sfdcOpptyId`/`sfdcUrl` are opaque metadata fields populated by an external MCP client, not fetched by this codebase.
- No automatic research generation from project context — research↔project linking is manual only; automated triggering is a follow-up, not this feature.
- No per-insight linking — insights are visible only via the meeting-level link (a project can't "adopt" one insight out of an otherwise-unlinked meeting).
- No document↔project linking — only meetings, research, and (transitively, via meetings) insights.
- No MCP link/member-management tools — only create/read tools (`ttobak_create_project`, `ttobak_list_projects`, `ttobak_get_project`, `ttobak_get_project_brief`, `ttobak_get_project_insights`) are exposed via MCP; linking, unlinking, and member management are REST-only, since those are expected to happen from the ttobak UI, not from an external SFDC-reading agent.

## Consequences

### Positive
- Reuses a battle-tested pattern (Research↔Account's transactional link, fail-closed read) instead of inventing a new one — the correctness properties this design leans on were already found and fixed once, in a different entity pair.
- Hybrid membership matches how opportunity teams actually form (Account team + a few specialists) without requiring redundant invites for every Account member.
- Read-time insight aggregation has zero sync-drift risk by construction.

### Negative
- `GetProjectInsights`/`GetProjectBrief` cost scales with the project's linked-meeting count (JSON-parse per meeting per call) — acceptable at expected scale, a future concern if a project ever accumulates hundreds of linked meetings.
- `DeleteProject`'s reject-if-linked behavior pushes cleanup work onto the caller (unlink everything, then delete) rather than offering one-shot deletion — a minor UX cost traded for avoiding partial-cascade failure states.
- Project-mediated insight exposure bypassing `SharedToAccount` is a second, independent sharing channel a future reader of `service/meeting.go`'s account-sharing code might not expect — flagged here specifically so it isn't mistaken for an oversight later.

### Risks
- `UpdateMeeting`'s whole-item `PutItem` pattern remains unfixed for every field other than `ProjectIDs` (e.g. `AccountID`, `SharedToAccount`) — this ADR closes the race for the one field it introduces, not the underlying pattern. A broader fix (switching `UpdateMeeting` to a partial `UpdateItem`, or auditing every call site) is a pre-existing, cross-cutting issue tracked as a follow-up, not in this ADR's scope. **For `ProjectIDs` specifically, this is now closed, not narrowed**: `UpdateMeeting`'s `PutItem` carries a `ConditionExpression` asserting `projectIds` still equals what was just read, with a bounded (3-attempt) retry on `ConditionalCheckFailedException` — a plain re-read-then-write (an earlier revision's approach) only shrinks the window to ordinary read-then-write latency, it doesn't close it; detecting the race and retrying past it is what this project's own AGENTS.md rule against "a whole-item `PutItem` carrying a stale read-time snapshot" actually calls for.
- Unlink authorization is intentionally asymmetric with Link: `LinkMeeting`/`LinkResearch` require the caller to own the meeting/research being linked (enforced structurally — `GetMeeting` is scoped to the caller's own partition), but `UnlinkMeeting`/`UnlinkResearch` accept either that same resource ownership **or** project ownership, checked directly via `GetProject` rather than `requireProjectAccess` (which would reintroduce the exact problem this avoids — see below). A symmetric (resource-owner-only) Unlink check created a revocation deadlock: a non-owner member who linked their own meeting, then lost project access via `RemoveMember`, would leave nobody who could ever unlink it — not the former member (no longer passes `requireProjectAccess`), not the project owner (never owned the meeting). The project owner's authority to unlink anything on their own project closes this, mirroring `UnlinkAccount`'s "unlink needs less proof than link" pattern from Decision §2 above. (An earlier revision called `requireProjectAccess` unconditionally *before* checking resource ownership, recreating the identical deadlock in the opposite direction: the former member's own resource-ownership check was never reached, since `requireProjectAccess` itself denied them first. Both `UnlinkMeeting`/`UnlinkResearch` now check resource-or-project ownership directly against `GetProject`, without going through `requireProjectAccess`'s broader direct-member/account-inherited-member allowance at all — deliberately: an ordinary project member with no stake in a specific link still cannot unlink someone else's.)
- `DeleteProject`'s "reject if linked" check (Decision §1) re-verifies against the canonical `ProjectIDs`/`AccountIDs` sets (via `ListProjectMeetings`/`ListProjectResearch`, the same fail-closed logic reads use) rather than counting raw ref items — a `MEETINGREF#`/`RESEARCHREF#` ref can outlive the meeting/research it points at (e.g. deleted through the ordinary meeting-delete path, which has no knowledge of project refs) and become a permanent orphan; counting raw refs would let one such orphan block deletion of a project with nothing genuinely linked, forever.

Resolved during review (kept here for record, not because it's still open):
- `MeetingProjectLinkTransactional`/`ResearchProjectLinkTransactional` now include a `ConditionCheck` on the project CONFIG item's existence (`attribute_exists(PK)`) as a third item in the same `TransactWriteItems` call, closing the delete-vs-link race an earlier revision left open (linking against a project deleted in the narrow window between `requireProjectAccess` and the transaction's commit). `ProjectAccountLinkTransactional` already had the equivalent guarantee structurally (its `Update` targets the project CONFIG item directly).
- The error mapper for the above (`mapProjectTransactionCanceledError`) initially inspected only `CancellationReasons[0]`, but that `ConditionCheck` is `TransactItem` index 2, not 0 — a concurrent project deletion failing specifically that check produced `[None, None, ConditionalCheckFailed]`, which the index-0-only check missed, silently defeating the `ConditionCheck` it was meant to translate into `ErrConditionFailed`/404. It now scans every reason for the `ConditionalCheckFailed` code regardless of position.

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| Project as an Account-owned sub-entity (nested under one "owning" Account) | Simpler membership model (inherit from one parent) | Doesn't fit the real many-Account case (partner + end-customer); would need a separate cross-Account sharing mechanism anyway |
| Project members only (no Account inheritance) | Simplest access model | Requires re-inviting every Account team member individually for every project — heavy duplication for the common case |
| Persist insights at link time (mirror Account's `BuildAccountInsights`) | One indexed read instead of N JSON parses | Reintroduces the exact staleness problem Account already has — a re-summarized meeting's insights wouldn't refresh in the project's view without a re-sync step |
| Cascade-delete on `DeleteProject` | One-shot deletion, no caller-side cleanup burden | Unbounded item count across multiple partitions can exceed the 100-item `TransactWriteItems` cap; non-transactional cascade risks a half-cleaned-up state on partial failure |

## References
- CLAUDE.md's "Research↔Account linking" section — the pattern this ADR reuses and the source of the transactional-link, fail-closed-read, `attribute_exists(PK)` conventions.
- PR #128 (`feat/project-entity`) — implementation, including the review-cycle history that surfaced and fixed the non-transactional-link, member-visibility, revocation-deadlock, and duplicate-meeting-ref issues this ADR documents the corrected shape of.

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
현재 미팅노트·리서치·인사이트는 팀이 공유하는 1급 엔티티인 Account(고객사 1곳을 표현) 아래에 있다. 하지만 실제로는 하나의 영업 기회(SFDC Opportunity)가 그 자체로 독립된 인게이지먼트인 경우가 많다 — 별도의 관계자 집합, 별도의 미팅노트 집합을 갖고, 하나 이상의 Account(파트너사 + 엔드고객 등)에 걸치기도 한다. Account 모델은 이 형태에 맞지 않는다: Account 멤버십은 곧 신원과 접근권한의 단위이고, Account↔Meeting은 그래프가 아니라 단일 필드(`Meeting.AccountID`)다.

새 엔티티 Project가 필요했고, 다음을 만족해야 했다:
1. 고객사가 아니라 기회(Opportunity) 단위로 미팅·리서치·인사이트를 묶는다.
2. 둘 이상의 Account에 연결될 수 있다(다대다).
3. 외부에서 MCP로 채울 수 있다(SFDC MCP 클라이언트가 Opportunity를 읽어 `ttobak_create_project`를 호출) — 이 코드베이스에는 SFDC API 직접 연동이 없고 메타데이터 필드만 저장한다.
4. 새 패턴을 만들지 않고 이미 검증된 패턴을 재사용한다 — Research↔Account 연동(먼저 추가되었고, 여러 리뷰 라운드를 거쳐 강화됨 — AGENTS.md/CLAUDE.md의 "Research↔Account linking" 절 참고)이 이미 정확히 같은 종류의 문제(원자적 집합 변경과 fail-closed 읽기를 갖는 그래프식 연동)를 풀었다.

## 결정

### 1. 그래프 레퍼런스 아키텍처 — 새 패턴을 만들지 않음
Project↔Account는 `Project.AccountIDs`(DynamoDB String Set)와 역참조 아이템(`ACCOUNT#{accountId}/PROJECTREF#{projectId}`)으로 다대다를 구현한다 — `Research.AccountIDs`/`ResearchRef`와 동일한 형태다. Project↔Meeting, Project↔Research도 같은 형태(`Meeting.ProjectIDs`, `Research.ProjectIDs`, `PROJECT#{id}/MEETINGREF#{date}#{meetingId}`, `PROJECT#{id}/RESEARCHREF#{researchId}`)다.

**링크/언링크 변경은 단일 `TransactWriteItems` 호출**(`ProjectAccountLinkTransactional`, `MeetingProjectLinkTransactional`, `ResearchProjectLinkTransactional`과 각각의 unlink 버전)로 이루어지며, `ResearchRepository.LinkAccountTransactional`/`UnlinkAccountTransactional`을 그대로 본떴다 — 두 번의 별도 요청이 아니다. 두 번의 별도 요청(이 PR 자체의 리뷰 사이클 초기 구현이 정확히 이 형태였다가 교정됨)은 트랜잭션 패턴이 막으려던 인터리빙 간극을 다시 열어버린다: 같은 (project, account) 쌍에 대한 동시 link/unlink가 canonical 집합은 연동된 상태인데 역참조는 삭제된 상태로 남을 수 있고, 이는 `ListAccountProjects`에서 실제 연동을 영구히 숨긴다.

**읽기 경로는 fail-closed**: 역참조 기반 목록(`ListProjectMeetings`, `ListProjectResearch`, `GetProjectInsights`, `ListAccountProjects`) 전부가 역참조를 후보로만 취급하고, 결과를 반환하기 전에 canonical 집합에 여전히 포함되는지 재검증한다. 어떤 실패 모드로든 남은 stale ref는 호출자에게 보이지 않는다 — 오탐(false positive)이 되는 경우는 없다.

**`ProjectMeetingRef`의 SK는 정렬을 위해 (mutable한) `Meeting.Date`를 포함**하는데, 이것이 중복 ref 버그를 낳았다(Date가 바뀐 뒤 재링크하면 원본을 갱신하는 대신 다른 SK에 두 번째 ref를 Put) — 이후 리뷰 라운드가 이를 발견해 수정했다: `LinkMeeting`의 idempotent 분기와 `UnlinkMeeting` 모두 현재 Date로 SK를 재계산하는 대신 `existingProjectMeetingRefSK`로 실제 존재하는 ref의 SK를 조회해 재사용하고, 미팅 관련 목록 전부가 `MeetingID` 기준 중복 제거도 2차 방어로 적용한다.

**`DeleteProject`는 관계가 하나라도 남아있으면 삭제를 거부**한다(캐스케이드 삭제 대신). 캐스케이드 삭제는 여러 파티션에 걸친 개수 제한 없는 MEMBER#/ref 아이템을 지워야 하는데, 관계가 충분히 많은 프로젝트라면 `TransactWriteItems`의 100개 제한을 넘을 수 있고(`ShareMeetingToAccount`가 멤버별 공유에 단일 트랜잭션을 쓰지 않는 이유와 같은 제약), 트랜잭션이 아닌 캐스케이드는 중간 실패 시 절반만 정리된 상태를 남길 위험이 있다. 호출자가 기존 API로 먼저 전부 해제하도록 요구하는 것이 더 단순하고, 부분 실패 상태를 고민할 필요가 없다.

**기존 `UpdateMeeting`의 whole-item `PutItem`이 쓰기 직전에 `ProjectIDs`를 다시 조회해 보존**하도록 했다. `UpdateMeeting`은 이 기능 이전부터 이미 전체 아이템을 덮어쓰는 방식(부분 `UpdateItem`이 아님)이었고, STT 파이프라인(transcribe/summarize)을 비롯한 대부분의 미팅 변경 경로가 이를 호출한다 — 그대로였다면 이 호출들 전부가 동시에 실행된 `LinkMeeting`/`UnlinkMeeting`의 `ProjectIDs` 변경을 조용히 되돌리거나(필드가 `omitempty`라 아예 삭제) 했을 것이고, `ProjectIDs`를 원자적 ADD/DELETE가 가능한 String Set으로 만든 이유 자체가 무력화된다. 쓰기 직전에 그 속성 하나만 projection으로 `GetItem`하면 race 윈도우가 "호출자가 메모리 복사본을 들고 있던 시간"(STT 실행 전체가 될 수 있음)에서 보통의 단일 아이템 read-then-write 지연 수준으로 좁혀진다. 다른 필드에 대한 whole-item-write 패턴 자체를 고치는 것은 아니다 — 이는 기존부터 있던 더 넓은 범위의 이슈로 별도 트래킹한다(Consequences 참고).

### 2. 하이브리드 멤버십: Account 상속 + 직접 멤버
`requireProjectAccess`는 다음에게 접근을 허용한다: 프로젝트 owner, 직접 초대된 멤버(`PROJECT#{id}/MEMBER#{userId}`, `AddMember`/`RemoveMember`로 관리), 또는 연동된 Account **중 하나라도**의 멤버. 즉 Account를 프로젝트에 연동하면 그 Account 팀 전체가 개별 초대 없이 자동으로 프로젝트 접근권한을 얻는다 — 실제 워크플로(기회의 관계자는 고객사 기존 팀과 크게 겹침)에 맞으면서도, Account 멤버가 아닌 프로젝트 전용 인력(예: 딜 데스크 분석가)을 직접 추가하는 것도 가능하다.

**Account 연동 해제는 project ownership만 요구하며, 현재 그 Account의 멤버일 필요는 없다.** 초기 구현은 둘 다 요구했는데, 이는 회수(revocation) 데드락을 만들었다: 프로젝트 owner가 나중에 해당 Account에서 제거되면 `GetMember`가 nil을 반환해 아무도 그 Account를 다시 해제할 수 없게 되는데, 그 Account의 남은 멤버 전원은 위의 상속 경로로 프로젝트 접근권한을 계속 유지한다. 링크를 만든 권한(project ownership)이면 해제에도 충분하다.

**프로젝트 멤버는 `Meeting.SharedToAccount` 여부와 무관하게 연동된 미팅의 제목·날짜·인사이트를 볼 수 있다.** 이는 기존 Account 공유 게시 단계 위에 얹힌(그 게이트를 거치지 않는) 의도적인 별도 공유 채널이다 — 프로젝트는 그 기회에 실제로 관련된 미팅만을 중심으로 관계자를 묶는데, 이는 (미팅 owner가 명시적으로 링크하는) Account 공유 전체보다 더 좁고 의도적인 행위다. 다만 미팅이 링크되고 Account도 링크된 상태에서 그 Account 멤버가 늘어나면 노출 범위가 자동으로 커진다는 뜻이기도 하다 — 위 상속 모델의 받아들인 트레이드오프이며 실수가 아니다.

### 3. 읽기 시점 인사이트 집계, 저장하지 않음
`GetProjectInsights`는 매 호출마다 연동된 각 미팅의 `Meeting.Insights` JSON을 파싱해 DTO를 직접 반환한다 — Account의 `BuildAccountInsights`처럼 영속 인사이트 row를 쓰지 않는다. 이 트레이드오프는 의도된 것이다: Account의 영속 인사이트 모델은 미팅이 재요약될 때마다 명시적 재동기화가 필요한 반면(공유 시점에 스냅샷을 뜨고 다음 공유 전까지 stale), Project의 읽기 시점 모델은 절대 stale해지지 않고 동기화 단계 자체가 없다 — 대가는 한 번의 인덱스 조회 대신 매 `GetProjectInsights`/`GetProjectBrief` 호출마다 N개 미팅의 JSON을 파싱하는 것이다. 프로젝트당 연동 미팅 수가 수천이 아니라 수십 단위로 유지될 것으로 예상되므로 이 비용은 무시할 만하다고 판단했다 — 이 가정이 깨지면 완화책은 이 읽기 경로에 캐싱이나 페이지네이션을 추가하는 것이지, 영속 모델로 전환하는 것이 아니다(그러면 이 설계가 피하려던 동기화 문제가 다시 생긴다).

### 4. 명시적 스코프 제외 (YAGNI)
- SFDC API 직접 연동 없음 — `sfdcOpptyId`/`sfdcUrl`은 외부 MCP 클라이언트가 채우는 불투명한 메타데이터 필드일 뿐, 이 코드베이스가 직접 가져오지 않는다.
- 프로젝트 컨텍스트로부터의 자동 리서치 생성 없음 — 리서치↔프로젝트 연동은 수동뿐이며, 자동 트리거는 이 기능이 아니라 후속 작업이다.
- 인사이트 개별 연동 없음 — 인사이트는 미팅 단위 링크를 통해서만 보인다(프로젝트가 링크되지 않은 미팅에서 인사이트 하나만 "가져올" 수는 없다).
- 문서↔프로젝트 연동 없음 — 미팅·리서치, 그리고 (미팅을 통해 전이적으로) 인사이트만 대상이다.
- MCP 연동/멤버관리 도구 없음 — MCP로는 생성/조회 도구(`ttobak_create_project`, `ttobak_list_projects`, `ttobak_get_project`, `ttobak_get_project_brief`, `ttobak_get_project_insights`)만 노출하며, 링크·언링크·멤버관리는 REST 전용이다 — 이런 조작은 SFDC를 읽어오는 외부 에이전트가 아니라 TTOBAK UI에서 일어날 것으로 예상하기 때문이다.

## 결과

### 긍정적
- 새 패턴을 만드는 대신 이미 검증된 패턴(Research↔Account의 트랜잭션 링크, fail-closed 읽기)을 재사용한다 — 이 설계가 의존하는 정합성 속성은 다른 엔티티 쌍에서 한 번 발견되고 이미 고쳐진 것이다.
- 하이브리드 멤버십이 실제 기회 팀 구성 방식(Account 팀 + 소수 전문가)에 맞아, Account 멤버 전원에게 중복 초대를 요구하지 않는다.
- 읽기 시점 인사이트 집계는 구조적으로 동기화 드리프트 위험이 없다.

### 부정적
- `GetProjectInsights`/`GetProjectBrief`의 비용은 프로젝트의 연동 미팅 수에 비례한다(호출마다 미팅별 JSON 파싱) — 예상 규모에서는 문제없지만, 프로젝트가 수백 개의 연동 미팅을 쌓으면 향후 고려사항이 된다.
- `DeleteProject`의 "링크 있으면 거부" 동작은 정리 작업을 호출자에게 넘긴다(전부 해제 후 삭제) — 부분 캐스케이드 실패 상태를 피하는 대가로 감내하는 작은 UX 비용이다.
- `SharedToAccount`를 우회하는 프로젝트 경유 인사이트 노출은, `service/meeting.go`의 계정 공유 코드를 나중에 읽는 사람이 예상하지 못할 수 있는 두 번째 독립 공유 채널이다 — 나중에 실수로 오해하지 않도록 여기서 명시적으로 짚어둔다.

### 위험
- `UpdateMeeting`의 whole-item `PutItem` 패턴은 `ProjectIDs` 외 다른 필드(`AccountID`, `SharedToAccount` 등)에 대해서는 여전히 고쳐지지 않았다 — 이 ADR은 이 기능이 도입한 필드 하나의 race만 닫았을 뿐, 근본 패턴을 고치지 않았다. 더 넓은 범위의 수정(`UpdateMeeting`을 부분 `UpdateItem`으로 바꾸거나 모든 호출부를 감사하는 것)은 기존부터 있던 범-엔티티 이슈로 이 ADR 스코프 밖에서 후속 트래킹한다. **`ProjectIDs`에 한해서는 이제 좁힌 것이 아니라 완전히 닫혔다**: `UpdateMeeting`의 `PutItem`이 방금 읽은 값과 `projectIds`가 여전히 같은지 확인하는 `ConditionExpression`을 달고, `ConditionalCheckFailedException` 발생 시 제한된(3회) 재시도를 한다 — 단순 재조회-후-쓰기(이전 버전의 방식)는 창을 보통의 read-then-write 지연 수준으로 줄일 뿐 닫지는 못한다; race를 감지해서 재시도로 통과하는 것이 이 프로젝트의 AGENTS.md 규칙("stale read-time snapshot을 담은 whole-item PutItem")이 실제로 요구하는 바다.
- Unlink 권한은 Link와 의도적으로 비대칭이다: `LinkMeeting`/`LinkResearch`는 호출자가 링크할 미팅/리서치의 owner일 것을 요구하지만(`GetMeeting`이 호출자 자신의 파티션으로 스코프되어 구조적으로 강제됨), `UnlinkMeeting`/`UnlinkResearch`는 그 리소스 owner이거나 **프로젝트 owner**면 충분하며, 이는 (`requireProjectAccess`가 아니라) `GetProject`로 직접 확인한다 — `requireProjectAccess`를 쓰면 아래에서 설명하는 문제를 다시 만든다. 대칭적인(리소스 owner만 허용하는) Unlink 체크는 회수 데드락을 만들었다: 자기 미팅을 링크한 비-owner 멤버가 이후 `RemoveMember`로 제거되면, 그 누구도 언링크할 수 없게 된다 — 제거된 멤버는 더 이상 `requireProjectAccess`를 통과하지 못하고, 프로젝트 owner는 그 미팅을 소유한 적이 없다. 프로젝트 owner가 자기 프로젝트의 무엇이든 해제할 수 있는 권한이 이를 막는다 — 위 결정 §2의 `UnlinkAccount` "해제는 링크보다 적은 증명을 요구한다" 패턴과 같은 원리다. (이전 버전은 리소스 소유권 체크보다 `requireProjectAccess`를 무조건 먼저 호출해, 정확히 같은 데드락을 반대 방향으로 재현했다 — 제거된 멤버 본인의 리소스 소유권 체크에 도달하기 전에 `requireProjectAccess` 자체가 그들을 막아버렸다. 이제 `UnlinkMeeting`/`UnlinkResearch` 둘 다 `requireProjectAccess`의 더 넓은 직접 멤버/account-상속 멤버 허용을 전혀 거치지 않고 `GetProject`로 리소스-또는-프로젝트 소유권만 직접 확인한다 — 의도적으로: 특정 링크에 아무 지분도 없는 일반 프로젝트 멤버는 여전히 남의 링크를 해제할 수 없다.)
- `DeleteProject`의 "링크 있으면 거부" 체크(결정 §1)는 raw ref 개수가 아니라 canonical `ProjectIDs`/`AccountIDs` 집합으로 재검증한다(읽기 경로가 쓰는 것과 동일한 `ListProjectMeetings`/`ListProjectResearch`의 fail-closed 로직 재사용) — `MEETINGREF#`/`RESEARCHREF#` ref는 가리키는 미팅/리서치보다 더 오래 살아남을 수 있고(예: project ref를 전혀 모르는 일반 미팅 삭제 경로로 삭제됨) 영구 고아가 될 수 있는데, raw ref 개수로 판정했다면 실제로는 아무것도 연동되지 않은 프로젝트의 삭제가 그런 고아 하나로 영구히 막혔을 것이다.

리뷰 중 해소됨(더 이상 열려있지 않지만 기록을 위해 남김, 계속):
- 위 `ConditionCheck`의 에러 매퍼(`mapProjectTransactionCanceledError`)가 처음엔 `CancellationReasons[0]`만 검사했는데, 그 `ConditionCheck`는 `TransactItem` index 2다(0이 아님) — 그 체크만 실패하는 동시 프로젝트 삭제는 `[None, None, ConditionalCheckFailed]`를 만들어내는데, index-0만 보는 검사는 이를 놓쳐 `ConditionCheck`가 `ErrConditionFailed`/404로 번역되어야 할 목적을 조용히 무력화했다. 이제 위치와 무관하게 모든 reason에서 `ConditionalCheckFailed` 코드를 검사한다.

리뷰 중 해소됨(더 이상 열려있지 않지만 기록을 위해 남김): `MeetingProjectLinkTransactional`/`ResearchProjectLinkTransactional`이 이제 같은 `TransactWriteItems` 호출의 세 번째 아이템으로 project CONFIG 아이템의 존재를 확인하는 `ConditionCheck`(`attribute_exists(PK)`)를 포함해, 이전 버전이 열어두었던 delete-vs-link race(`requireProjectAccess`와 트랜잭션 커밋 사이의 좁은 창에서 프로젝트가 삭제된 상태로 링크되는 경우)를 닫았다. `ProjectAccountLinkTransactional`은 구조적으로 이미 동일한 보장을 갖고 있었다(그 `Update`가 project CONFIG 아이템을 직접 대상으로 하기 때문).

## 검토한 대안
| 옵션 | 장점 | 단점 |
|--------|------|------|
| Project를 Account 소속 하위 엔티티로 (하나의 "소유" Account 아래 nested) | 멤버십 모델이 단순함(하나의 부모에서 상속) | 실제 다중-Account 케이스(파트너+엔드고객)에 맞지 않음; 어차피 별도 cross-Account 공유 메커니즘이 필요해짐 |
| 프로젝트 멤버만 (Account 상속 없음) | 가장 단순한 접근 모델 | 매 프로젝트마다 Account 팀 멤버 전원을 개별 재초대해야 함 — 흔한 케이스에서 중복이 심함 |
| 링크 시점에 인사이트 영속화 (Account의 `BuildAccountInsights` 본뜨기) | JSON 파싱 N번 대신 인덱스 조회 1번 | Account가 이미 겪는 stale 문제를 그대로 재도입 — 재요약된 미팅의 인사이트가 재동기화 없이는 프로젝트 뷰에 반영되지 않음 |
| `DeleteProject`에서 캐스케이드 삭제 | 한 번에 삭제, 호출자 쪽 정리 부담 없음 | 여러 파티션에 걸친 개수 제한 없는 아이템이 `TransactWriteItems`의 100개 제한을 넘을 수 있음; 트랜잭션 아닌 캐스케이드는 부분 실패 시 절반만 정리된 상태 위험 |

## 참고
- CLAUDE.md의 "Research↔Account linking" 절 — 이 ADR이 재사용하는 패턴이자 트랜잭션 링크·fail-closed 읽기·`attribute_exists(PK)` 컨벤션의 출처.
- PR #128(`feat/project-entity`) — 구현체. 이 ADR이 문서화하는 교정된 형태(non-transactional 링크, 멤버 가시성, 회수 데드락, 중복 미팅 ref 이슈)를 드러내고 고친 리뷰 사이클 이력을 포함.
