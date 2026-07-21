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

> **Superseded in part** — see [Post-Implementation Updates](#post-implementation-updates) below: account/membership writes were added, so "it never writes meetings or membership" is no longer accurate as of that update.

## Post-Implementation Updates

1. **Write surface widened beyond documents-only**: three more write-capable tool groups were added, each still a thin wrapper over an existing member/owner-gated REST endpoint (no new auth path):
   - **KB tools** (`ttobak_kb_upload`, `ttobak_kb_sync`, `ttobak_kb_list_files`, `ttobak_kb_delete_file`) → `POST /api/kb/upload`, `POST /api/kb/sync`, `GET /api/kb/files`, `DELETE /api/kb/files/{fileId}`. Upload is a local-file-to-presigned-URL flow (`uploadToKB` in `mcp-server/src/api.ts`), separate from sync so multiple files can be uploaded before one ingestion run.
   - **`ttobak_upload_document`** → `POST /api/upload/presigned` (category `doc`) + `POST /api/accounts/{id}/documents` or `POST /api/documents`. Extends the original `put_document` (markdown text only) to binary files (pdf/pptx/ppt) — same loop-guard-free path as the REST API itself already allowed, just now reachable from MCP.
   - **`ttobak_create_account`** / **`ttobak_add_account_member`** → `POST /api/accounts`, `POST /api/accounts/{id}/members`. This is the one genuine expansion beyond "documents only": account and membership writes are now MCP-reachable, where the original decision explicitly said "it never writes meetings or membership." Membership adds still require the caller to already be the account owner (server-enforced), and role is restricted to `AM`/`TAM`/`SSA` (never `owner`) — same restriction the REST endpoint itself enforces.
2. **KB uploads are not account-scoped**: unlike documents, KB files ingest into one shared Bedrock Knowledge Base regardless of which account they're "about" — there's no account-membership gate on `/api/kb/*`, only authentication. Any authenticated user can add to the shared KB; `ttobak_kb_delete_file` is scoped server-side to the caller's own uploaded files (`kb/{userID}/` prefix in `backend/internal/service/kb.go`), not any file in the shared KB. Deletion also only removes the S3 object — the Bedrock index still returns it until the next ingestion sync.
3. **`ttobak_kb_sync` is currently a no-op on the deployed API Lambda**: `infra/lib/gateway-stack.ts`'s `ApiFunction` gets no `KB_ID`/`KB_DATASOURCE_ID` env vars (only `SummarizeFunction`/`QAFunction` do), and `apiRole` has no `bedrock:StartIngestionJob` IAM permission (only `summarizeRole`/`kbRole`/`crawlerRole` do, in `infra/lib/ai-stack.ts`) — so `kb.go`'s `SyncKB` always returns `{status: "skipped"}`. Files uploaded via `ttobak_kb_upload` aren't permanently unindexed, though: the news/tech crawler already triggers periodic ingestion on the same Bedrock data source, so an uploaded file becomes searchable at the next crawler-driven sync rather than never. Follow-up (tracked, not yet scheduled): a small infra PR injecting the two env vars + the IAM action into `ApiFunction`/`apiRole`, scoped to the KB's ARN — note the name landmine: `cmd/api/main.go` reads `KB_DATASOURCE_ID`, but CDK's existing pattern for `SummarizeFunction`/`QAFunction` injects a differently-named `DATA_SOURCE_ID`, so copying that pattern verbatim would still leave `api` reading an unset variable.

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

> **일부 결정이 이후 대체됨** — 아래 [구현 후 업데이트](#구현-후-업데이트) 참조: 계정/멤버십 쓰기가 추가되어 "미팅·멤버십은 절대 쓰지 않는다"는 더 이상 사실이 아니다(해당 업데이트 이후).

## 구현 후 업데이트

1. **문서 전용을 넘어선 쓰기 표면 확장**: 세 가지 쓰기 가능 도구 그룹이 추가됐다. 각각 여전히 기존의 멤버/소유자 게이트 REST 엔드포인트의 얇은 래퍼(신규 인증 경로 없음):
   - **KB 도구** (`ttobak_kb_upload`, `ttobak_kb_sync`, `ttobak_kb_list_files`, `ttobak_kb_delete_file`) → `POST /api/kb/upload`, `POST /api/kb/sync`, `GET /api/kb/files`, `DELETE /api/kb/files/{fileId}`. 업로드는 로컬 파일 → presigned URL 흐름(`mcp-server/src/api.ts`의 `uploadToKB`)이며, 여러 파일을 먼저 업로드하고 한 번에 인제스천할 수 있도록 sync와 분리돼 있다.
   - **`ttobak_upload_document`** → `POST /api/upload/presigned`(category `doc`) + `POST /api/accounts/{id}/documents` 또는 `POST /api/documents`. 기존 `put_document`(마크다운 텍스트만)를 바이너리 파일(pdf/pptx/ppt)로 확장 — REST API 자체가 이미 허용하던 경로를 MCP에서도 도달 가능하게 만든 것뿐이다.
   - **`ttobak_create_account`** / **`ttobak_add_account_member`** → `POST /api/accounts`, `POST /api/accounts/{id}/members`. "문서 전용"을 실질적으로 넘어서는 유일한 확장 — 원래 결정이 명시적으로 "미팅·멤버십은 절대 쓰지 않는다"고 했던 부분이 이제 MCP에서 계정·멤버십 쓰기로 확장됐다. 멤버 추가는 여전히 호출자가 이미 계정 소유자여야 하고(서버 강제), role은 `AM`/`TAM`/`SSA`로만 제한(`owner`는 불가) — REST 엔드포인트 자체가 강제하는 것과 동일한 제약이다.
2. **KB 업로드는 Account 범위가 아님**: 문서와 달리 KB 파일은 "어느 Account에 대한 것인지"와 무관하게 하나의 공유 Bedrock Knowledge Base로 인제스트된다 — `/api/kb/*`에는 계정 멤버십 게이트가 없고 인증만 필요하다. 인증된 사용자라면 누구나 공유 KB에 추가할 수 있으나, `ttobak_kb_delete_file`은 서버 측에서 호출자 본인이 업로드한 파일(`backend/internal/service/kb.go`의 `kb/{userID}/` prefix)로만 제한되며 공유 KB의 임의 파일을 삭제할 수 없다. 또한 삭제는 S3 객체만 제거하므로 다음 인제스천 sync 전까지는 Bedrock 인덱스에 여전히 검색된다.
3. **`ttobak_kb_sync`는 현재 배포된 API Lambda에서 no-op**: `infra/lib/gateway-stack.ts`의 `ApiFunction`에는 `KB_ID`/`KB_DATASOURCE_ID` env가 주입되지 않고(`SummarizeFunction`/`QAFunction`에만 주입됨), `apiRole`에는 `bedrock:StartIngestionJob` IAM 권한이 없다(`summarizeRole`/`kbRole`/`crawlerRole`에만 있음, `infra/lib/ai-stack.ts`) — 그래서 `kb.go`의 `SyncKB`는 항상 `{status: "skipped"}`를 반환한다. 다만 `ttobak_kb_upload`로 올린 파일이 영원히 미인덱싱되는 것은 아니다 — 뉴스/기술 크롤러가 이미 동일 Bedrock 데이터소스에 주기적 인제스천을 트리거하므로, 다음 크롤러 주도 sync 시점에는 검색 가능해진다. 후속 작업(추적 중, 아직 일정 미정): `ApiFunction`/`apiRole`에 두 env + IAM 액션을 KB ARN으로 스코프하여 주입하는 소규모 infra PR — 이름 함정 주의: `cmd/api/main.go`는 `KB_DATASOURCE_ID`를 읽지만, CDK가 `SummarizeFunction`/`QAFunction`에 쓰는 기존 패턴은 이름이 다른 `DATA_SOURCE_ID`를 주입하므로 그 패턴을 그대로 복사하면 api는 여전히 미설정 변수를 읽게 된다.

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
