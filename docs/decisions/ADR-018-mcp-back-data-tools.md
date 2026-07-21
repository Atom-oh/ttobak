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

> **Superseded in part** — see [Post-Implementation Updates](#post-implementation-updates) below: account/membership writes were added, so the membership half of "it never writes meetings or membership" no longer holds. Meetings are still never written.

## Post-Implementation Updates

1. **Write surface widened beyond documents-only**: three more write-capable tool groups were added, each still a thin wrapper over an existing member/owner-gated REST endpoint (no new auth path):
   - **KB tools** (`ttobak_kb_upload`, `ttobak_kb_sync`, `ttobak_kb_list_files`, `ttobak_kb_delete_file`) → `POST /api/kb/upload`, `POST /api/kb/sync`, `GET /api/kb/files`, `DELETE /api/kb/files/{fileId}`. Upload is a local-file-to-presigned-URL flow (`uploadToKB` in `mcp-server/src/api.ts`), separate from sync so multiple files can be uploaded before one ingestion run.
   - **`ttobak_upload_document`** → `POST /api/upload/presigned` (category `doc`) + `POST /api/accounts/{id}/documents` or `POST /api/documents`. Extends the original `put_document` (markdown text only) to binary files (pdf/pptx/ppt) — same loop-guard-free path as the REST API itself already allowed, just now reachable from MCP.
   - **`ttobak_create_account`** / **`ttobak_add_account_member`** → `POST /api/accounts`, `POST /api/accounts/{id}/members`. This is the one genuine expansion beyond "documents only": account and membership writes are now MCP-reachable, where the original decision explicitly said "it never writes meetings or membership." Membership adds still require the caller to already be the account owner (server-enforced), and role is restricted to `AM`/`TAM`/`SSA` (never `owner`) — same restriction the REST endpoint itself enforces.
2. **KB uploads are user-scoped, not account-scoped**: unlike documents, KB files are not tied to an account — there's no account-membership gate on `/api/kb/*`, only authentication. But the shared Bedrock Knowledge Base is shared *infrastructure*, not shared *visibility*: uploads land under the caller's own `kb/{userID}/` prefix (`backend/internal/service/kb.go`), and the QA Lambda's retrieval filter (`backend/python/qa/handler.py`, `x-amz-bedrock-kb-source-uri` filter) restricts `ttobak_ask` to the caller's own `kb/{userID}/` uploads, their own/shared meetings, and the crawler's `shared/` docs — another user's queries cannot retrieve your KB uploads. `ttobak_kb_delete_file` is likewise scoped server-side to the caller's own files. Deletion only removes the S3 object — the Bedrock index still returns it (to the uploader only) until the next ingestion sync.
3. **`ttobak_kb_sync` wiring**: `kb.go`'s `SyncKB` returns `{status: "skipped"}` unless the API Lambda has both the `KB_ID`/`KB_DATASOURCE_ID` env vars and `bedrock:StartIngestionJob`. This PR adds both: `infra/lib/gateway-stack.ts`'s `ApiFunction` now injects `KB_ID`/`KB_DATASOURCE_ID` (note the name landmine — `cmd/api/main.go` reads `KB_DATASOURCE_ID`, *not* the `DATA_SOURCE_ID` name `SummarizeFunction` uses; injecting the wrong name would silently leave sync skipped), and `apiRole` in `infra/lib/ai-stack.ts` gets `bedrock:StartIngestionJob` (matching the `summarizeRole`/`kbRole`/`crawlerRole` grant). These take effect on the next `TtobakGatewayStack`/`TtobakAiStack` deploy; until then (or on any environment deployed without them) `SyncKB` still returns `"skipped"`, and uploads are indexed by other pipelines' ingestion runs on the same data source anyway — `cmd/summarize/main.go` calls `TriggerIngestion` after every completed meeting summary, and the daily crawler triggers one on runs where it added/updated documents (`backend/python/crawler/ingest_trigger.py` skips zero-change runs, and MCP uploads don't count toward its counters). So even in the skipped state an uploaded file becomes searchable at the next meeting-summary completion or document-bearing crawler run. Tool messages/README disclose the skipped-fallback behavior.

## Consequences

### Positive
- TTOBAK becomes bidirectional: SAs can round-trip account knowledge through their own agents/tools.
- Authorization is unchanged and centralized — MCP tools reuse the same REST endpoints and their server-side gates (member-gated for account documents, owner-gated for membership, auth-only for the shared KB), so no parallel auth path.
- Loop-guard ([ADR-017](ADR-017-vault-export-and-inbound-ingest.md)) protects `put_document` from export↔reimport loops. (The post-implementation write tools have no loop-guard — they don't round-trip exported content.)

### Negative
- A write-capable MCP surface widens the blast radius if the user's MCP token leaks. The original mitigation ("writes limited to account documents, member-gated") no longer covers the widened surface: KB writes are auth-only (though retrieval stays scoped to the uploader — see Update 2), and account/membership writes are owner-gated rather than member-gated. `guardUploadPath` in `api.ts` narrows the local-file-read side (credential dotdirs under `$HOME`, system paths like `/etc`/`/proc`/`/var/run/secrets`, secret-shaped filenames like `.env*`/`*credentials*`/`*.pem`/`id_*`, symlinks resolved, size caps) but is a speed bump, not a sandbox — the MCP host's tool-call approval remains the real gate.
- Ten tool definitions defined by ADR-018 overall (three in the original decision + seven added by this PR; the server's full tool list is larger still) to keep in sync across `api.ts`, `index.ts`, and the README's English/Korean tables.

### Risks
- Agents may push low-quality or duplicative documents; only the marker loop-guard exists today, not content-quality checks.
- KB uploads and deletions take effect in search only at the next ingestion run (per Post-Implementation Update 3: `ttobak_kb_sync` once the KB env/IAM this PR adds is deployed, otherwise a meeting-summary or crawler-triggered run) — a deleted file stays retrievable by its uploader until then.

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

> **일부 결정이 이후 대체됨** — 아래 [구현 후 업데이트](#구현-후-업데이트) 참조: 계정/멤버십 쓰기가 추가되어 "미팅·멤버십은 절대 쓰지 않는다" 중 **멤버십** 부분은 더 이상 성립하지 않는다. 미팅은 여전히 쓰지 않는다.

## 구현 후 업데이트

1. **문서 전용을 넘어선 쓰기 표면 확장**: 세 가지 쓰기 가능 도구 그룹이 추가됐다. 각각 여전히 기존의 멤버/소유자 게이트 REST 엔드포인트의 얇은 래퍼(신규 인증 경로 없음):
   - **KB 도구** (`ttobak_kb_upload`, `ttobak_kb_sync`, `ttobak_kb_list_files`, `ttobak_kb_delete_file`) → `POST /api/kb/upload`, `POST /api/kb/sync`, `GET /api/kb/files`, `DELETE /api/kb/files/{fileId}`. 업로드는 로컬 파일 → presigned URL 흐름(`mcp-server/src/api.ts`의 `uploadToKB`)이며, 여러 파일을 먼저 업로드하고 한 번에 인제스천할 수 있도록 sync와 분리돼 있다.
   - **`ttobak_upload_document`** → `POST /api/upload/presigned`(category `doc`) + `POST /api/accounts/{id}/documents` 또는 `POST /api/documents`. 기존 `put_document`(마크다운 텍스트만)를 바이너리 파일(pdf/pptx/ppt)로 확장 — REST API 자체가 이미 허용하던 경로를 MCP에서도 도달 가능하게 만든 것뿐이다.
   - **`ttobak_create_account`** / **`ttobak_add_account_member`** → `POST /api/accounts`, `POST /api/accounts/{id}/members`. "문서 전용"을 실질적으로 넘어서는 유일한 확장 — 원래 결정이 명시적으로 "미팅·멤버십은 절대 쓰지 않는다"고 했던 부분이 이제 MCP에서 계정·멤버십 쓰기로 확장됐다. 멤버 추가는 여전히 호출자가 이미 계정 소유자여야 하고(서버 강제), role은 `AM`/`TAM`/`SSA`로만 제한(`owner`는 불가) — REST 엔드포인트 자체가 강제하는 것과 동일한 제약이다.
2. **KB 업로드는 사용자 범위이며, Account 범위가 아님**: 문서와 달리 KB 파일은 Account에 묶이지 않는다 — `/api/kb/*`에는 계정 멤버십 게이트가 없고 인증만 필요하다. 다만 공유 Bedrock Knowledge Base는 *인프라*가 공유일 뿐 *가시성*이 공유는 아니다: 업로드는 호출자 본인의 `kb/{userID}/` prefix에 저장되고(`backend/internal/service/kb.go`), QA Lambda의 retrieval filter(`backend/python/qa/handler.py`의 `x-amz-bedrock-kb-source-uri` 필터)가 `ttobak_ask`를 호출자 본인의 `kb/{userID}/` 업로드 + 본인/공유 미팅 + 크롤러의 `shared/` 문서로 제한하므로, 다른 사용자의 질의는 내 KB 업로드를 검색할 수 없다. `ttobak_kb_delete_file`도 마찬가지로 서버 측에서 본인 파일로만 제한된다. 또한 삭제는 S3 객체만 제거하므로 다음 인제스천 sync 전까지는 (업로더 본인에게만) Bedrock 인덱스에 여전히 검색된다.
3. **`ttobak_kb_sync` 배선**: `kb.go`의 `SyncKB`는 API Lambda에 `KB_ID`/`KB_DATASOURCE_ID` env와 `bedrock:StartIngestionJob`이 모두 있어야 동작하고, 없으면 `{status: "skipped"}`를 반환한다. 이 PR이 둘 다 추가한다: `infra/lib/gateway-stack.ts`의 `ApiFunction`에 `KB_ID`/`KB_DATASOURCE_ID`를 주입하고(이름 함정 주의 — `cmd/api/main.go`는 `KB_DATASOURCE_ID`를 읽으며, `SummarizeFunction`이 쓰는 `DATA_SOURCE_ID`가 *아니다*; 잘못된 이름을 주입하면 sync가 조용히 skipped로 남는다), `infra/lib/ai-stack.ts`의 `apiRole`에 `bedrock:StartIngestionJob`을 부여한다(`summarizeRole`/`kbRole`/`crawlerRole`과 동일 패턴). 이는 다음 `TtobakGatewayStack`/`TtobakAiStack` 배포 시점에 반영되며, 그 전까지(또는 이들이 없는 환경에서는) `SyncKB`가 여전히 `"skipped"`를 반환한다 — 그래도 업로드는 동일 데이터소스에 대한 다른 파이프라인의 인제스천으로 인덱싱된다: `cmd/summarize/main.go`가 미팅 요약 완료 시마다 `TriggerIngestion`을 호출하고, 일일 크롤러도 문서를 추가/갱신한 실행에서 트리거한다(`backend/python/crawler/ingest_trigger.py`는 0건 변경 실행을 skip, MCP 업로드는 그 카운터에 미포함). 즉 skipped 상태에서도 업로드 파일은 다음 미팅 요약 완료 또는 문서를 얻은 크롤러 실행 시점에 검색 가능해진다. skipped-fallback 동작은 도구 메시지/README에 고지한다.

## 결과

### 긍정
- TTOBAK이 양방향이 됨: SA가 자기 에이전트/도구로 Account 지식을 왕복.
- 권한은 변경 없이 중앙화 — MCP 도구가 동일한 REST 엔드포인트와 그 서버 측 게이트(계정 문서는 멤버 게이트, 멤버십은 소유자 게이트, 공유 KB는 인증만)를 재사용해 병렬 인증 경로 없음.
- 루프 가드([ADR-017](ADR-017-vault-export-and-inbound-ingest.md))가 `put_document`를 내보내기↔재임포트 루프에서 보호. (구현 후 추가된 쓰기 도구들에는 루프 가드가 없음 — 내보낸 콘텐츠를 왕복시키는 경로가 아니기 때문.)

### 부정
- 쓰기 가능한 MCP 표면은 사용자 MCP 토큰 유출 시 영향 범위를 넓힘. 원래의 완화("쓰기를 계정 문서로 제한, 멤버 게이트")는 확장된 표면을 더 이상 커버하지 못함: KB 쓰기는 인증만 요구하고(다만 검색은 업로더 본인으로 스코프됨 — 업데이트 2 참조), 계정/멤버십 쓰기는 멤버가 아닌 소유자 게이트다. `api.ts`의 `guardUploadPath`가 로컬 파일 읽기 측면($HOME 아래 자격증명 dotdir, `/etc`·`/proc`·`/var/run/secrets` 등 시스템 경로, `.env*`·`*credentials*`·`*.pem`·`id_*` 등 시크릿형 파일명, symlink 해석, 크기 상한)을 좁히지만 이는 sandbox가 아니라 속도 방지턱 — 실질 게이트는 MCP 호스트의 도구 호출 승인이다.
- ADR-018 전체가 정의하는 도구가 총 10개(원 결정 3개 + 이 PR 추가분 7개; 서버 전체 도구 수는 그보다 많음) — `api.ts`·`index.ts`·README 영/한 표 간 동기화 대상.

### 위험
- 에이전트가 저품질/중복 문서를 밀어넣을 수 있음; 현재는 마커 루프 가드만 있고 콘텐츠 품질 검사는 없음.
- KB 업로드/삭제는 다음 인제스천 실행 시점에야 검색에 반영됨(구현 후 업데이트 3 참조 — 이 PR이 추가한 KB env/IAM이 배포되면 `ttobak_kb_sync`, 아니면 미팅 요약/크롤러 트리거) — 삭제한 파일은 그때까지 업로더 본인에게 계속 검색됨.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| 멤버 게이트 REST 위 얇은 MCP 도구(채택) | 기존 인증 재사용, 좁은 쓰기 표면 | 토큰 유출 위험, 도구 목록 동기화 부담 |
| MCP 서버에서 DynamoDB 직접 접근 | 홉 감소 | 인증/검증 중복, 서비스 불변식 우회 |
| MCP 읽기 전용 유지(ADR-003만) | 최소 공격 표면 | back-data 불가, SA가 발견을 밀어넣지 못해 기반 목표 무산 |
