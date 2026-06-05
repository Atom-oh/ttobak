# ADR-018: Bidirectional MCP Back-Data Tools

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — implemented on `feat/account-foundation` (Plan 4 of 6). `mcp-server` exposes account back-data tools backed by `GET /api/accounts/{id}/brief`, `GET /api/vault/export`, and `POST /api/accounts/{id}/documents`. Extends, not replaces, [ADR-003](ADR-003-mcp-server-for-external-meeting-access.md).

## Context
[ADR-003](ADR-003-mcp-server-for-external-meeting-access.md) gave external agents **read-only** access to meetings over MCP. The Account substrate ([ADR-015](ADR-015-account-first-class-shared-entity.md)–[ADR-017](ADR-017-vault-export-and-inbound-ingest.md)) changes the goal: an SA's external agent (Claude in their IDE, a research bot) should be able to **pull** an account's accumulated knowledge as a single brief, and **push** new findings back in — TTOBAK as a back-data store for the SA's own tools, not just a source. This requires a write surface, which ADR-003 deliberately did not cover.

## Decision
Add account-scoped MCP tools to `mcp-server`, each a thin wrapper over an authenticated REST endpoint (the MCP server holds the user's token; all member-gating happens server-side):

- **`get_account_brief`** → `GET /api/accounts/{id}/brief` — composes account meetings + insights (period/type filtered) into one digest for the agent.
- **`export_vault`** → `GET /api/vault/export` — returns the caller's meetings as portable markdown ([ADR-017](ADR-017-vault-export-and-inbound-ingest.md)).
- **`put_document`** → `POST /api/accounts/{id}/documents` — writes an external markdown note into the account, subject to the loop-guard.

The write path is intentionally narrow (documents only); it never writes meetings or membership. Tool names, the API client (`mcp-server/src/api.ts`), and the README tool list are kept in lockstep.

## Consequences

### Positive
- TTOBAK becomes bidirectional: SAs can round-trip account knowledge through their own agents/tools.
- Authorization is unchanged and centralized — MCP tools reuse the same member-gated REST endpoints, so no parallel auth path.
- Loop-guard ([ADR-017](ADR-017-vault-export-and-inbound-ingest.md)) protects the one write tool from export↔reimport loops.

### Negative
- A write-capable MCP surface widens the blast radius if the user's MCP token leaks (mitigated: writes limited to account documents, member-gated).
- Three more tool definitions to keep in sync across `api.ts`, `index.ts`, and the README.

### Risks
- Agents may push low-quality or duplicative documents; only the marker loop-guard exists today, not content-quality checks.

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| Thin MCP tools over member-gated REST (chosen) | Reuses existing auth, narrow write surface | Token leak risk; tool-list sync burden |
| Direct DynamoDB access from MCP server | Fewer hops | Duplicates auth/validation, bypasses service invariants |
| Keep MCP read-only (ADR-003 only) | Smallest attack surface | No back-data; SAs can't push findings — defeats the substrate goal |

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — `feat/account-foundation`에서 구현(Plan 4/6). `mcp-server`가 `GET /api/accounts/{id}/brief`, `GET /api/vault/export`, `POST /api/accounts/{id}/documents`를 기반으로 계정 back-data 도구를 노출. [ADR-003](ADR-003-mcp-server-for-external-meeting-access.md)을 대체가 아니라 확장.

## 맥락
[ADR-003](ADR-003-mcp-server-for-external-meeting-access.md)은 외부 에이전트에 미팅 **읽기 전용** MCP 접근을 제공했다. Account 기반([ADR-015](ADR-015-account-first-class-shared-entity.md)–[ADR-017](ADR-017-vault-export-and-inbound-ingest.md))은 목표를 바꾼다: SA의 외부 에이전트(IDE의 Claude, 리서치 봇)가 Account의 적립 지식을 하나의 brief로 **가져오고**, 새 발견을 다시 **밀어넣을** 수 있어야 한다 — TTOBAK이 단순 소스가 아니라 SA 도구의 back-data 저장소. 이를 위해 ADR-003이 의도적으로 다루지 않은 쓰기 표면이 필요하다.

## 결정
`mcp-server`에 계정 범위 MCP 도구 추가. 각 도구는 인증된 REST 엔드포인트의 얇은 래퍼(MCP 서버가 사용자 토큰 보유, 멤버 게이팅은 전부 서버 측):

- **`get_account_brief`** → `GET /api/accounts/{id}/brief` — Account 미팅 + 인사이트(기간/타입 필터)를 한 digest로 구성.
- **`export_vault`** → `GET /api/vault/export` — 호출자 미팅을 휴대 가능한 마크다운으로 반환([ADR-017](ADR-017-vault-export-and-inbound-ingest.md)).
- **`put_document`** → `POST /api/accounts/{id}/documents` — 외부 마크다운 노트를 Account에 기록(루프 가드 적용).

쓰기 경로는 의도적으로 좁다(문서 전용). 미팅·멤버십은 절대 쓰지 않는다. 도구명·API 클라이언트(`mcp-server/src/api.ts`)·README 도구 목록을 일치 유지.

## 결과

### 긍정
- TTOBAK이 양방향이 됨: SA가 자기 에이전트/도구로 Account 지식을 왕복.
- 권한은 변경 없이 중앙화 — MCP 도구가 동일한 멤버 게이트 REST를 재사용해 병렬 인증 경로 없음.
- 루프 가드([ADR-017](ADR-017-vault-export-and-inbound-ingest.md))가 유일한 쓰기 도구를 내보내기↔재임포트 루프에서 보호.

### 부정
- 쓰기 가능한 MCP 표면은 사용자 MCP 토큰 유출 시 영향 범위를 넓힘(완화: 쓰기를 계정 문서로 제한, 멤버 게이트).
- `api.ts`·`index.ts`·README 간 동기화할 도구 정의 3개 추가.

### 위험
- 에이전트가 저품질/중복 문서를 밀어넣을 수 있음; 현재는 마커 루프 가드만 있고 콘텐츠 품질 검사는 없음.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| 멤버 게이트 REST 위 얇은 MCP 도구(채택) | 기존 인증 재사용, 좁은 쓰기 표면 | 토큰 유출 위험, 도구 목록 동기화 부담 |
| MCP 서버에서 DynamoDB 직접 접근 | 홉 감소 | 인증/검증 중복, 서비스 불변식 우회 |
| MCP 읽기 전용 유지(ADR-003만) | 최소 공격 표면 | back-data 불가, SA가 발견을 밀어넣지 못해 기반 목표 무산 |
