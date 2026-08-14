# ADR-015: Account as a First-Class Shared Entity

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — implemented on `feat/account-foundation` (Plan 1 of 6). `CreateAccount`, `GetAccount`, `ListAccounts`, `AddMember`, and `ResolveAccountByAlias` are live behind `/api/accounts`.

## Context
TTOBAK started as a personal meeting tool: every item (meeting, attachment, share) was keyed to a single `USER#`. As it grew into an SA (Solutions Architect) assistant, the natural organizing unit became the **customer account** ("하나은행", "Hana Bank"), not the individual user. Multiple teammates (AM/TAM/SSA) work the same account and need a shared, durable place to accumulate meetings and insights — something a per-user share-list cannot express.

We needed an entity that: (1) is shared across a team with roles, (2) lives in the existing single-table DynamoDB design without a new table, (3) supports reverse lookup ("which accounts am I a member of?"), and (4) can be referenced by a human-friendly tag/alias from chat and MCP.

## Decision
Introduce **Account** as a first-class entity in the `ttobak-main` single table:

- **Account META**: `PK=ACCOUNT#{accountId}`, `SK=META` — name, aliases, domains, industry, owner.
- **Account member**: `PK=ACCOUNT#{accountId}`, `SK=MEMBER#{userId}` with `GSI1PK=USER#{userId}`, `GSI1SK=ACCOUNT#{accountId}` for reverse lookup. Roles: `owner` (creator only), plus the assignable set `AM`, `TAM`, `SSA`, `SA`, `SA Manager`, `AM Manager` — the allowlist lives in one place, `model.AssignableRoles` (`backend/internal/model/account.go`).
- **Creation is atomic**: the META item and the owner member are written in a single `TransactWriteItems` so an account never exists without its owner (a missing owner would make `GetMember` deny everyone, orphaning it).
- **Authorization is membership-derived**: every account read re-checks `GetMember` (strongly consistent); non-members get `ErrForbidden`, missing accounts get `ErrNotFound` (the two are deliberately distinguished).
- **Alias resolution**: `ResolveAccountByAlias` maps a tag → the caller's single matching account; ambiguity is rejected (never auto-picked).

## Consequences

### Positive
- One shared, team-scoped home for account knowledge — the substrate for [ADR-016](ADR-016-meeting-account-linking-and-sharing.md) (sharing) and the insight accumulation that follows.
- No new table or GSI: reuses GSI1 (membership rows isolated from meeting date-rows via `begins_with("ACCOUNT#")`).
- Reverse lookup ("my accounts") is a single GSI1 query.

### Negative
- Member emails (PII) are stored in the table; the table uses AWS-owned default encryption, not a customer-managed KMS key (accepted trade-off; revisit if compliance requires).
- `ListAccounts`/`ResolveAccountByAlias` do N+1 `GetAccount` reads per membership (fine at small N).

### Risks
- Roles are coarse (owner vs assignable). Finer per-resource permissions are out of scope for v1. The assignable set was widened from `AM`/`TAM`/`SSA` to also include `SA`/`SA Manager`/`AM Manager` — purely a label addition, no new authorization behavior.
- GSI1 is shared with meeting date-sorting rows; the `begins_with("ACCOUNT#")` guard must hold (timestamps never collide with the `ACCOUNT#` prefix).

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| Account as first-class single-table entity (chosen) | No new infra, atomic create, GSI1 reverse lookup | PII in shared table; coarse roles |
| Separate `ttobak-accounts` table | Clean isolation | Cross-table transactions, more infra, duplicate access plumbing |
| Reuse existing per-meeting `Share` only | Nothing new to build | No durable team home; can't accumulate account-level insights |

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — `feat/account-foundation`에서 구현(Plan 1/6). `CreateAccount`/`GetAccount`/`ListAccounts`/`AddMember`/`ResolveAccountByAlias`가 `/api/accounts`로 동작 중.

## 맥락
TTOBAK은 개인 회의 도구로 시작해 모든 항목이 `USER#` 기준이었다. SA 비서로 확장되면서 자연스러운 조직 단위는 개별 사용자가 아니라 **고객 Account**("하나은행")가 되었다. 한 Account를 여러 팀원(AM/TAM/SSA)이 함께 다루므로, 회의·인사이트를 적립할 **공유되고 영속적인 공간**이 필요했고 이는 사용자별 공유 목록으로는 표현할 수 없다.

요구사항: (1) 역할을 가진 팀 공유 엔티티, (2) 새 테이블 없이 기존 단일 테이블 설계 내 수용, (3) 역방향 조회("내가 속한 Account"), (4) 챗·MCP에서 사람이 읽는 태그/별칭으로 참조 가능.

## 결정
`ttobak-main` 단일 테이블에 **Account**를 1급 엔티티로 도입:

- **Account META**: `PK=ACCOUNT#{accountId}`, `SK=META` — 이름·별칭·도메인·산업·소유자.
- **Account 멤버**: `PK=ACCOUNT#{accountId}`, `SK=MEMBER#{userId}`, 역방향 조회용 `GSI1PK=USER#{userId}`/`GSI1SK=ACCOUNT#{accountId}`. 역할: `owner`(생성자 전용), 그리고 할당 가능 집합 `AM`, `TAM`, `SSA`, `SA`, `SA Manager`, `AM Manager` — 허용 목록은 `model.AssignableRoles`(`backend/internal/model/account.go`) 한 곳에만 존재.
- **생성은 원자적**: META와 owner 멤버를 단일 `TransactWriteItems`로 기록 — owner 없는 Account가 생기면 `GetMember`가 전원을 거부해 고아가 되므로 방지.
- **권한은 멤버십 기반**: 모든 Account 조회 시 `GetMember`(강한 일관성) 재확인. 비멤버는 `ErrForbidden`, 없는 Account는 `ErrNotFound`로 구분.
- **별칭 해석**: `ResolveAccountByAlias`가 태그 → 호출자의 단일 매칭 Account로 해석하며, 모호하면 자동 선택 없이 거부.

## 결과

### 긍정
- 팀 범위의 단일 지식 거점 확보 — [ADR-016](ADR-016-meeting-account-linking-and-sharing.md)(공유)과 이후 인사이트 적립의 기반.
- 새 테이블/GSI 불필요: GSI1 재사용(멤버 행은 `begins_with("ACCOUNT#")`로 미팅 날짜 행과 분리).
- "내 Account" 역방향 조회는 GSI1 단일 쿼리.

### 부정
- 멤버 이메일(PII)이 테이블에 저장됨. 고객 관리형 KMS가 아닌 AWS 소유 기본 암호화 사용(수용된 트레이드오프, 컴플라이언스 요구 시 재검토).
- `ListAccounts`/`ResolveAccountByAlias`는 멤버십당 N+1 `GetAccount` 조회(소규모에선 무방).

### 위험
- 역할이 거칠다(owner vs 할당가능). 리소스별 세분 권한은 v1 범위 밖. 할당 가능 집합은 `AM`/`TAM`/`SSA`에서 `SA`/`SA Manager`/`AM Manager`까지 확장됨 — 순수 레이블 추가이며 새 권한 동작은 없음.
- GSI1을 미팅 날짜 정렬 행과 공유 — `begins_with("ACCOUNT#")` 가드가 항상 성립해야 함.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| 단일 테이블 1급 엔티티(채택) | 신규 인프라 없음, 원자적 생성, GSI1 역방향 조회 | 공유 테이블 PII, 거친 역할 |
| 별도 `ttobak-accounts` 테이블 | 깔끔한 격리 | 교차 테이블 트랜잭션, 인프라/접근 배선 중복 |
| 기존 미팅 `Share`만 재사용 | 새로 만들 것 없음 | 영속적 팀 거점 없음, Account 단위 인사이트 적립 불가 |
