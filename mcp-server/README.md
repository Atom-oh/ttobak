# TTOBAK MCP Server — Claude Code Integration Guide

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

## English

### What This Does

A local MCP (Model Context Protocol) server that gives Claude Code direct access to your TTOBAK meeting data. Once connected, Claude can list meetings, read summaries/transcripts, and answer questions about your meetings using natural language.

```
Claude Code  ──stdio──>  TTOBAK MCP Server  ──HTTPS──>  CloudFront  ──>  API Gateway  ──>  Lambda
                              │
                         ~/.ttobak/tokens.json
                         (Cognito OAuth PKCE)
```

### Bundle Provenance

`frontend/public/mcp/ttobak-mcp.mjs` (served to the public web app so users can add the MCP server without installing from source) is a build artifact, not hand-edited. `mcp-server/src/*.ts` is the sole source of truth. After changing `src/`, always regenerate and copy the bundle -- never edit the `.mjs` directly:

```bash
cd mcp-server && npm run build && npm run bundle
cp dist/ttobak-mcp.mjs ../frontend/public/mcp/ttobak-mcp.mjs
```

To verify the committed bundle is a clean rebuild (no drift, no hand-edits) rather than trust a diff-size comparison against a possibly-stale base commit's bundle:

```bash
cd mcp-server && npm run build && npm run bundle
diff dist/ttobak-mcp.mjs ../frontend/public/mcp/ttobak-mcp.mjs   # must be empty
```

`npm test` also builds the bundle twice, checks byte-for-byte reproducibility,
and runs the bounded-API adapter protocol regressions against both the compiled
modules and the standalone bundle. Fixtures supply fixed server pages and exercise
real HTTP/auth code, including oversized-response aborts; backend tests own
Unicode pagination, source verification, and cursor validity.
Run `npm run test:bundle` for the bundle checks alone.
This does not publish or copy the frontend artifact; the release owner must still
perform the copy/comparison above. CI's published-artifact comparison is mandatory.

### Prerequisites

- Node.js 18+
- AWS credentials (for initial setup script only)
- A TTOBAK account (Cognito user)

---

### Setup

#### Method 1: CLI Registration (Recommended)

```bash
# 1. Install and build
cd mcp-server && npm install && npm run build

# 2. Register with Claude Code
claude mcp add ttobak --transport stdio --scope project \
  --env TTOBAK_COGNITO_DOMAIN="https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com" \
  --env TTOBAK_CLIENT_ID="33rh85mv6l9n7tn3s5h16prfdr" \
  --env TTOBAK_API_URL="https://ttobak.atomai.click" \
  --env TTOBAK_REGION="ap-northeast-2" \
  -- node mcp-server/dist/index.js
```

#### Method 2: Auto Setup Script

```bash
# Discovers config from CloudFormation and writes .mcp.json
./mcp-server/scripts/setup.sh
```

#### Method 3: Manual `.mcp.json` (Already in Repo)

The repo includes a pre-configured `.mcp.json` at the project root:

```json
{
  "mcpServers": {
    "ttobak": {
      "command": "node",
      "args": ["mcp-server/dist/index.js"],
      "env": {
        "TTOBAK_COGNITO_DOMAIN": "https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com",
        "TTOBAK_CLIENT_ID": "33rh85mv6l9n7tn3s5h16prfdr",
        "TTOBAK_API_URL": "https://ttobak.atomai.click",
        "TTOBAK_REGION": "ap-northeast-2"
      }
    }
  }
}
```

Just install and build, then restart Claude Code:

```bash
cd mcp-server && npm install && npm run build
```

---

### Verify Connection

After restarting Claude Code, check the MCP server status:

```
/mcp
```

You should see:

```
ttobak
  Status: connected
  Tools:  ttobak_login, ttobak_status, ttobak_list_meetings,
          ttobak_get_meeting, ttobak_read_transcript, ttobak_list_accounts, ttobak_get_account,
          ttobak_get_account_meetings, ttobak_get_account_insights,
          ttobak_get_account_brief, ttobak_export_vault,
          ttobak_put_document, ttobak_list_documents, ttobak_get_document, ttobak_update_document,
          ttobak_ask, ttobak_kb_upload, ttobak_kb_sync,
          ttobak_kb_list_files, ttobak_kb_delete_file,
          ttobak_upload_document, ttobak_create_account,
          ttobak_add_account_member, ttobak_create_project,
          ttobak_list_projects, ttobak_get_project,
          ttobak_get_project_brief, ttobak_get_project_insights,
          ttobak_update_project, ttobak_link_project_account,
          ttobak_unlink_project_account, ttobak_logout
```

If the status shows "failed", check:
- `mcp-server/dist/index.js` exists (run `npm run build` if missing)
- Environment variables are set in `.mcp.json`

You can also verify from the command line:

```bash
claude mcp list          # List all registered servers
claude mcp get ttobak    # Check ttobak server details
```

---

### Authentication

The first time you use any TTOBAK tool, the MCP server will open your browser for Cognito login.

```
1. Claude Code calls ttobak_login (or any data tool)
2. Browser opens → Cognito Hosted UI login page
3. You enter email + password
4. Browser redirects to localhost:9876/callback
5. Page shows "TTOBAK MCP Authenticated"
6. Close browser tab → back to Claude Code
7. Token saved to ~/.ttobak/tokens.json (30-day refresh)
```

After the initial login, tokens auto-refresh. You won't need to log in again for ~30 days.

To explicitly trigger login:

```
Use the ttobak_login tool to authenticate.
```

---

### Available Tools

For document tools, omit `accountId` for your personal Document Hub; supplying it
explicitly selects an account's shared space. Directly-shared personal documents
can be read, but only their owner can revise them. `put_document` always creates a
new ID; use `update_document` with the existing ID and title for revisions.
Document Hub notes are not automatically indexed by `ttobak_ask`: use the document
list/read tools for their current contents.

Account filters are explicit: obtain `parentAccountId` relationships from
`ttobak_list_accounts`, then include the group and its accessible descendant IDs
in `accountIds`. Keep that same filter while following pagination cursors.

#### Reading notes, summaries, and long transcripts

Start with `ttobak_get_meeting` and `{"meetingId":"meeting-id"}`. It returns the
current saved **notes** first. Read the generated summary separately with
`{"meetingId":"meeting-id","section":"summary"}`; it is not a replacement for
user-authored corrections. The requested field (`notes` or `content`) preserves
its exact characters. Small identifying metadata, bounded participants/tags,
`availableCodePoints`, `revision`, and `page` accompany it.

Both reading tools call **only**
`GET /api/meetings/{meetingId}/reading`, using `kind=meeting|transcript`,
`pageSize`, optional `cursor`, and the applicable section/source/time-range
options. The API must be deployed before releasing this adapter. A missing route
or API failure is an explicit tool error; there is no fallback to the full
meeting endpoint.

`actionItems` remains available as a bounded preview alongside
`actionItemsAnalysis` (`unknown`, `queued`, `running`, `succeeded`, or `failed`,
plus optional `errorCode`, `runId`, and `leaseUntil`). A legacy server without
analysis status is reported as **unknown**, never inferred successful from `[]`.
`actionItemsPreview.available` distinguishes an absent collection from an empty
one; its `complete`, `totalItems`, and `metadataTruncated` describe the preview.
User-set completion flags are preserved.

To read all items and every field when the preview is shortened, call
`ttobak_get_meeting` with `section:"actionItems"` and follow its cursor. Join
`actionItemsJson` across pages, then parse that complete JSON array. Individual
JSON pages may end inside a string and are not standalone JSON documents.

Read transcripts with `ttobak_read_transcript`:

```json
{"meetingId":"meeting-id","source":"selected","pageSize":4000}
```

- Source `selected` follows current A/B selection and availability. Explicit `A`
  or `B` reads that source only. An unselected source never borrows the selected
  source's speaker labels or timestamps.
- Join `chunks[].text` in response order, then follow `page.nextCursor` with the
  **same meeting, section/source, and time range**. `page.complete=true` and
  `nextCursor=null` mean no continuation remains for that requested content;
  do not claim completeness until all preceding pages have been read.
- Offsets are zero-based **Unicode code points**, with exclusive end offsets,
  not JavaScript UTF-16 indices or UTF-8 byte offsets. Korean, emoji, punctuation,
  newlines, and speaker headers survive page reconstruction unchanged.
- `pageSize` accepts integers 1–8000 (default 4000). The backend may return less
  text to fit its **14,000-byte API JSON limit** (including the trailing newline)
  and **50-chunk limit**. It owns segment splitting and metadata shortening.
  The adapter counts HTTP bytes before buffering/JSON parsing and aborts responses
  over **32,000 bytes**, then separately enforces a **32,000-byte serialized MCP
  tool-result limit** after wrapping. It never reslices or silently truncates a
  server page.
- Treat server-issued `revision` and `page.nextCursor` as opaque. Forward the
  cursor unchanged; the client does not decode, hash, or mint continuations.
  The backend rejects a changed source, selection, timing, or provider with
  `STALE_CURSOR`; restart without the cursor. Malformed/out-of-range cursors are
  server errors. Every page rechecks access through the authenticated reading API.

For a time range, pass **both** `startTime` and `endTime` in seconds, for example:

```json
{"meetingId":"meeting-id","source":"selected","startTime":60,"endTime":120}
```

This selects whole segments overlapping `[60,120)`, only when every supplied
segment is verified against the current selected text and has valid times.
`chunk.segment` reports original utterance times (`timingScope=whole_segment`);
`partial=true` means only part of that segment's text fits this page. Those times
are not word-level boundaries for the excerpt. Offsets expose any gaps between
matching segments; `matchingCodePoints` counts the selected raw spans and
`totalCodePoints` counts the whole source. Completeness then applies only to the
requested range (`completenessScope=requested_time_range`).

Without verified segments, ordinary reads use exact raw-text pages (`mode=text`)
with no timing/speaker claims. Time-range requests return `TIME_RANGE_UNAVAILABLE`
instead of fabricated timestamps. `sttProvider` is explicitly meeting-level
metadata, not proof of which engine produced an individual variant.

**Compatibility:** the existing tool name and `meetingId` input remain valid,
but `ttobak_get_meeting` now projects a bounded notes/summary view. It no longer
returns `transcriptA`, `transcriptB`, `transcription`, `speakerMap`, attachments,
or shares. Action items use the bounded preview/explicit JSON pages above.
Migrate transcript consumers to
`ttobak_read_transcript` and summary consumers to `section=summary`. The required
reading API bounds the response before Lambda serialization, including notes-only
requests on meetings with large S3-backed transcripts. The adapter receives only
that server page. Source selection, Unicode/time windows, and completeness remain
backend responsibilities.

| Tool | Description | Example Prompt |
|------|-------------|----------------|
| `ttobak_login` | Authenticate via browser | "Log in to TTOBAK" |
| `ttobak_status` | Check auth status and config | "Check TTOBAK connection status" |
| `ttobak_list_meetings` | List meetings with explicit `accountIds` filters and pagination | "Show meetings for Toss and its subsidiaries" |
| `ttobak_get_meeting` | Bounded saved notes first; `section=summary` reads the summary | "Read the saved corrections for meeting X" |
| `ttobak_read_transcript` | Source-bound transcript pages, verified segment/time ranges, continuation | "Read meeting X's transcript from 60 to 120 seconds" |
| `ttobak_list_accounts` | List accounts you belong to | "Show my accounts" |
| `ttobak_get_account` | Account detail and members | "Show the Hana Bank account info" |
| `ttobak_get_account_meetings` | Meetings shared into an account | "List meetings shared into Hana Bank" |
| `ttobak_get_account_insights` | Typed insights by period/type | "Hana Bank's May risk and opportunity insights" |
| `ttobak_get_account_brief` | Bundled account raw material | "Give me Hana Bank's quarterly brief in one shot" |
| `ttobak_export_vault` | Export meetings as Obsidian markdown files | "Export my meetings to my vault" |
| `ttobak_put_document` | Create a personal note, or explicitly share to an account | "Save this prep note privately" |
| `ttobak_list_documents` | List personal/directly-shared or account documents | "Find my prep notes" |
| `ttobak_get_document` | Read current document content | "Show the Hana Bank prep doc" |
| `ttobak_update_document` | Revise the same document; omitted body is preserved | "Correct that note without making a copy" |
| `ttobak_ask` | RAG Q&A across meetings and the Knowledge Base | "What decisions were made about the API redesign?" |
| `ttobak_kb_upload` | Upload a local file (pdf/md/pptx/docx) into your Knowledge Base space (retrieval is scoped to your own uploads) | "Upload this whitepaper to the KB" |
| `ttobak_kb_sync` | Trigger a full-data-source Knowledge Base ingestion job (returns "skipped" on a deployment without the KB env vars configured — see tool description) | "Sync the KB now" |
| `ttobak_kb_list_files` | List your uploaded KB files | "What have I uploaded to the KB?" |
| `ttobak_kb_delete_file` | Delete a KB file by ID (stays in the search index until the next ingestion run) | "Delete that old KB file" |
| `ttobak_upload_document` | Upload a local file (pdf/pptx/ppt) as a document, personal or account-shared | "Upload this deck to Hana Bank" |
| `ttobak_create_account` | Create an account, optionally under `parentAccountId` | "Create Hana Bank under Hana Financial Group" |
| `ttobak_add_account_member` | Any existing member can add a teammate as AM/TAM/SSA/SA/SA Manager/AM Manager | "Add jane@x.com to Hana Bank as TAM" |
| `ttobak_create_project` | Create a Project (SFDC Opportunity) | "Create a project for the Hana Bank renewal" |
| `ttobak_list_projects` | List projects you own, are directly invited to, or reach via a linked Account's membership | "Show my projects" |
| `ttobak_get_project` | Project detail: members, linked accounts | "Show the Hana renewal project" |
| `ttobak_get_project_brief` | Bundled project raw material (meetings, research, insights) | "Give me the Hana renewal project brief" |
| `ttobak_get_project_insights` | Typed insights aggregated from the project's linked meetings | "What risks came up in the Hana renewal project?" |
| `ttobak_update_project` | Update a project's metadata (name, description, SFDC fields, stage); omitted fields keep their current value, links/members unaffected -- `name` must still be resent | "Rename that project to Hana Renewal 2.0" |
| `ttobak_link_project_account` | Link an Account to a project (project owner + member of that Account) | "Link Hana Bank to the renewal project" |
| `ttobak_unlink_project_account` | Unlink an Account from a project (project owner) | "Unlink Hana Bank from that project" |
| `ttobak_logout` | Clear stored tokens | "Log out of TTOBAK" |

### Usage Examples

**Daily briefing:**
```
List my TTOBAK meetings from this week and summarize the key decisions.
```

**Deep dive into a specific meeting:**
```
Get meeting abc123 from TTOBAK and list all action items with owners.
```

**Cross-meeting analysis:**
```
Ask TTOBAK: "What topics came up in multiple meetings this month?"
```

**Pre-meeting prep:**
```
Get the last 3 meetings with the design team from TTOBAK
and brief me on open issues.
```

---

### Troubleshooting

| Issue | Solution |
|-------|----------|
| Server shows "failed" in `/mcp` | Run `cd mcp-server && npm run build` and restart Claude Code |
| "Missing required env vars" | Check `.mcp.json` has all 4 env vars set |
| Browser doesn't open on login | Copy the URL from Claude Code stderr and open manually |
| "invalid_grant" on token exchange | Tokens expired. Run `ttobak_logout` then `ttobak_login` |
| Login works but API calls fail | Verify `TTOBAK_API_URL` points to the correct CloudFront domain |
| Port 9876 in use | Stop the process using port 9876: `lsof -ti:9876 \| xargs kill` |

### Uninstall

```bash
# Remove MCP server registration
claude mcp remove ttobak

# Remove stored tokens
rm -rf ~/.ttobak

# Remove server code (optional)
rm -rf mcp-server/
```

---

<a id="korean"></a>

## 한국어

### 개요

Claude Code에서 TTOBAK 미팅 데이터에 직접 접근할 수 있는 로컬 MCP (Model Context Protocol) 서버입니다. 연결하면 Claude가 미팅 목록 조회, 요약/트랜스크립트 읽기, 자연어로 미팅에 대한 질문을 할 수 있습니다.

```
Claude Code  ──stdio──>  TTOBAK MCP Server  ──HTTPS──>  CloudFront  ──>  API Gateway  ──>  Lambda
                              │
                         ~/.ttobak/tokens.json
                         (Cognito OAuth PKCE)
```

### 사전 요구사항

- Node.js 18+
- AWS 자격 증명 (초기 설정 스크립트에만 필요)
- TTOBAK 계정 (Cognito 사용자)

---

### 설정

#### 방법 1: CLI 등록 (권장)

```bash
# 1. 설치 및 빌드
cd mcp-server && npm install && npm run build

# 2. Claude Code에 등록
claude mcp add ttobak --transport stdio --scope project \
  --env TTOBAK_COGNITO_DOMAIN="https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com" \
  --env TTOBAK_CLIENT_ID="33rh85mv6l9n7tn3s5h16prfdr" \
  --env TTOBAK_API_URL="https://ttobak.atomai.click" \
  --env TTOBAK_REGION="ap-northeast-2" \
  -- node mcp-server/dist/index.js
```

#### 방법 2: 자동 설정 스크립트

```bash
# CloudFormation에서 설정값을 자동 탐색하여 .mcp.json에 기록
./mcp-server/scripts/setup.sh
```

#### 방법 3: `.mcp.json` 수동 설정 (레포에 이미 포함)

프로젝트 루트에 사전 설정된 `.mcp.json`이 포함되어 있습니다:

```json
{
  "mcpServers": {
    "ttobak": {
      "command": "node",
      "args": ["mcp-server/dist/index.js"],
      "env": {
        "TTOBAK_COGNITO_DOMAIN": "https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com",
        "TTOBAK_CLIENT_ID": "33rh85mv6l9n7tn3s5h16prfdr",
        "TTOBAK_API_URL": "https://ttobak.atomai.click",
        "TTOBAK_REGION": "ap-northeast-2"
      }
    }
  }
}
```

설치 및 빌드 후 Claude Code를 재시작하면 됩니다:

```bash
cd mcp-server && npm install && npm run build
```

---

### 연결 확인

Claude Code를 재시작한 후 MCP 서버 상태를 확인합니다:

```
/mcp
```

다음과 같이 표시되어야 합니다:

```
ttobak
  Status: connected
  Tools:  ttobak_login, ttobak_status, ttobak_list_meetings,
          ttobak_get_meeting, ttobak_read_transcript, ttobak_list_accounts, ttobak_get_account,
          ttobak_get_account_meetings, ttobak_get_account_insights,
          ttobak_get_account_brief, ttobak_export_vault,
          ttobak_put_document, ttobak_list_documents, ttobak_get_document, ttobak_update_document,
          ttobak_ask, ttobak_kb_upload, ttobak_kb_sync,
          ttobak_kb_list_files, ttobak_kb_delete_file,
          ttobak_upload_document, ttobak_create_account,
          ttobak_add_account_member, ttobak_create_project,
          ttobak_list_projects, ttobak_get_project,
          ttobak_get_project_brief, ttobak_get_project_insights,
          ttobak_update_project, ttobak_link_project_account,
          ttobak_unlink_project_account, ttobak_logout
```

"failed" 상태가 표시되면:
- `mcp-server/dist/index.js`가 존재하는지 확인 (없으면 `npm run build` 실행)
- `.mcp.json`에 4개 환경변수가 모두 설정되어 있는지 확인

커맨드 라인에서도 확인할 수 있습니다:

```bash
claude mcp list          # 등록된 모든 서버 목록
claude mcp get ttobak    # ttobak 서버 상세 정보
```

---

### 인증

TTOBAK 도구를 처음 사용할 때 MCP 서버가 브라우저를 열어 Cognito 로그인을 진행합니다.

```
1. Claude Code가 ttobak_login 호출 (또는 데이터 도구 호출 시 자동)
2. 브라우저 열림 → Cognito Hosted UI 로그인 페이지
3. 이메일 + 비밀번호 입력
4. 브라우저가 localhost:9876/callback으로 리다이렉트
5. "TTOBAK MCP Authenticated" 페이지 표시
6. 브라우저 탭 닫기 → Claude Code로 복귀
7. 토큰이 ~/.ttobak/tokens.json에 저장 (30일 리프레시)
```

초기 로그인 후에는 토큰이 자동 갱신됩니다. 약 30일간 재로그인 불필요합니다.

명시적으로 로그인을 트리거하려면:

```
TTOBAK에 로그인해줘
```

---

### 사용 가능한 도구

문서 도구에서 `accountId`를 생략하면 개인 문서함을 사용하고, 지정하면 해당
Account 공유 공간을 사용합니다. 직접 공유받은 개인 문서는 읽기 전용입니다.
`put_document`는 항상 새 ID를 만들므로 수정할 때는 기존 ID와 제목을
`update_document`에 전달하세요. 개인 문서함의 노트는 `ttobak_ask`에 자동
색인되지 않으며, 문서 목록·읽기 도구로 현재 내용을 조회할 수 있습니다.

그룹 필터는 `ttobak_list_accounts`의 `parentAccountId` 관계를 따라 그룹과
접근 가능한 하위 계열사 ID를 모두 `accountIds`에 넣습니다.
다음 페이지를 조회할 때도 동일한 필터를 유지해야 합니다.

#### 메모부터 읽고 긴 녹취록 이어 읽기

`ttobak_get_meeting({"meetingId":"meeting-id"})`는 현재 저장된 사용자 메모를
먼저 반환합니다. 생성된 요약은 `section:"summary"`로 별도로 읽으세요.
메모의 정정 내용을 요약으로 대체하지 마세요. 응답의 `notes` 또는 `content`와
`page.nextCursor`를 따라 같은 section으로 이어 읽습니다.

두 읽기 도구는 인증된 `GET /api/meetings/{meetingId}/reading`만 호출합니다.
`kind`, `pageSize`, `cursor`와 해당 section/source/시간 범위를 전달합니다.
MCP 배포 전에 이 API를 먼저 배포해야 합니다. 라우트가 없거나 API가 실패하면
명시적 도구 오류이며, 전체 미팅 조회로 되돌아가지 않습니다.

`actionItems`의 제한된 미리보기와 `actionItemsAnalysis` 상태
(`unknown/queued/running/succeeded/failed`, 선택적 errorCode/runId/leaseUntil)를
함께 제공합니다. 구버전 서버에 상태가 없으면 `unknown`이며, `[]`만 보고
추출 성공으로 판단하지 않습니다. `actionItemsPreview.available`은 원본 필드의
존재 여부, `complete/totalItems/metadataTruncated`는 미리보기 범위를 나타냅니다.
사용자가 체크한 완료 상태는 보존합니다. 전체 항목은 `section:"actionItems"`로
`actionItemsJson` 페이지를 끝까지 합친 다음 JSON 배열로 해석하세요.
개별 페이지는 문자열 중간에서 끝날 수 있어 독립된 JSON이 아닙니다.

녹취록은 `ttobak_read_transcript`에 `meetingId`, `source:"selected"`를
전달해 읽습니다. 서버가 현재 선택/가용성에 따라 A/B를 결정하며, `source:"A"` 또는
`"B"`로 특정 원문을 지정할 수도 있습니다. 선택되지 않은 원문에는 다른 원문의
화자·시간 정보를 붙이지 않습니다.

- `chunks[].text`를 응답 순서대로 이어 붙입니다. 같은 미팅·source·시간 범위를
  유지한 채 `page.nextCursor`를 다음 호출의 `cursor`에 전달하세요.
  이전 페이지를 모두 읽고 `page.complete=true`, `nextCursor=null`일 때만
  요청 범위를 끝까지 읽은 것입니다.
- `startOffset`/`endOffset`은 0부터 시작하는 유니코드 코드 포인트 위치이며,
  끝 위치는 제외합니다. UTF-16 인덱스나 바이트 위치가 아닙니다.
  한글·이모지·공백·줄바꿈·화자 헤더를 빠뜨리거나 겹치지 않게 보존합니다.
- `pageSize`는 1–8000 정수, 기본 4000입니다. 서버는 끝 줄바꿈을 포함한 API
  JSON 14,000바이트와 청크 50개 제한에 맞춰 페이지를 만듭니다. 어댑터는 HTTP
  본문을 버퍼에 추가하거나 JSON으로 해석하기 전에 바이트 수를 세어 32,000바이트
  초과 응답을 중단하고, MCP로 감싼 최종 결과도 32,000바이트 이하인지 확인합니다.
  클라이언트에서 원문을 다시 나누거나 조용히 자르지 않습니다.
- 서버가 발급한 `revision`과 `page.nextCursor`는 내부 형식을 해석하지 않고
  그대로 사용합니다. 원문·선택·시간·출처가 바뀌면 서버가 `STALE_CURSOR`를
  반환하므로 cursor 없이 다시 읽으세요. 매 페이지에서 인증된 읽기 API로 접근을
  재확인하며 접근 해제/삭제/401 오류를 캐시나 전체 미팅 조회로 우회하지 않습니다.
- 시간 범위는 초 단위 `startTime`, `endTime`을 함께 전달하며
  `0 <= startTime < endTime`이어야 합니다. 현재 선택 원문 전체와 일치하고
  시간이 유효한 세그먼트가 있을 때만 `[startTime,endTime)`과 겹치는 발화를
  반환합니다. 발화 일부만 담긴 청크의 `partial=true`여도 시간은 원래 발화 전체의
  시간이며 단어 단위 구간을 추정하지 않습니다.
- 검증된 세그먼트가 없으면 `mode=text`로 원문 위치만 제공합니다.
  이 경우 시간 범위 요청은 `TIME_RANGE_UNAVAILABLE` 오류입니다.
  시간 범위 읽기의 완료는 해당 구간에 한정되며, source offset으로 제외된 구간의
  간격을 확인할 수 있습니다. `sttProvider`는 미팅 수준 정보입니다.

기존 `ttobak_get_meeting` 이름/meetingId 입력은 유지하지만 전체 응답 전달은
의도적으로 변경됐습니다. 녹취록 A/B·전체 화자 매핑·첨부·공유는 기본 응답에서
제외합니다. 액션 항목은 제한된 미리보기와 명시적인 JSON 페이지로 제공합니다.
요약은 `section=summary`, 녹취록은 새 도구로
전환하세요. 서버 읽기 API가 Lambda 응답을 만들기 전에 크기를 제한하고,
MCP는 그 페이지를 그대로 전달합니다. 원문 선택·검증·페이지 나누기는 서버 책임입니다.
`npm test`는 일반 모듈과 번들 양쪽의 프로토콜 테스트 및 번들 재현성을 검증합니다.
프런트엔드 공개 번들 복사/CI 비교는 배포 담당자가 별도로 수행해야 합니다.

| 도구 | 설명 | 예시 프롬프트 |
|------|------|---------------|
| `ttobak_login` | 브라우저를 통한 인증 | "TTOBAK에 로그인해줘" |
| `ttobak_status` | 인증 상태 및 설정 확인 | "TTOBAK 연결 상태 확인해줘" |
| `ttobak_list_meetings` | `accountIds` 필터와 페이지네이션으로 미팅 조회 | "토스와 계열사 미팅 보여줘" |
| `ttobak_get_meeting` | 사용자 메모 우선 페이지 읽기, `section=summary`로 요약 읽기 | "미팅 X의 정정 메모부터 읽어줘" |
| `ttobak_read_transcript` | 원문 A/B·검증된 시간 범위·이어 읽기 | "미팅 X의 60–120초 녹취록을 읽어줘" |
| `ttobak_list_accounts` | 내 Account 목록 | "내 어카운트 목록 보여줘" |
| `ttobak_get_account` | Account 상세/멤버 | "하나은행 어카운트 정보" |
| `ttobak_get_account_meetings` | 공유 미팅 목록 | "하나은행 공유 미팅 목록" |
| `ttobak_get_account_insights` | 기간·유형별 인사이트 | "하나은행 5월 리스크/기회 인사이트" |
| `ttobak_get_account_brief` | 묶음 원재료 | "하나은행 분기 브리프 한 번에" |
| `ttobak_export_vault` | 미팅을 Obsidian 마크다운 파일로 내보내기 | "내 미팅을 vault로 내보내줘" |
| `ttobak_put_document` | 개인 노트 생성 또는 지정 Account에 공유 | "이 회의 준비 노트를 개인 문서로 저장해줘" |
| `ttobak_list_documents` | 개인·직접 공유받은 문서 또는 Account 문서 목록 | "내가 적은 준비 노트 찾아줘" |
| `ttobak_get_document` | 문서의 현재 내용 조회 | "하나은행 prep 문서 보여줘" |
| `ttobak_update_document` | 동일 문서 수정, 생략한 본문 보존 | "복사본 만들지 말고 이 노트의 납기를 수정해줘" |
| `ttobak_ask` | 미팅 + Knowledge Base 기반 RAG Q&A | "API 재설계에 대해 어떤 결정이 있었어?" |
| `ttobak_kb_upload` | 로컬 파일(pdf/md/pptx/docx)을 내 Knowledge Base 공간에 업로드 (검색은 본인 업로드로 스코프됨) | "이 백서를 KB에 올려줘" |
| `ttobak_kb_sync` | 전체 데이터소스 대상 Knowledge Base 인제스천 실행 (KB env 미설정 배포에서는 "skipped" 반환 — 도구 설명 참조) | "지금 KB 동기화해줘" |
| `ttobak_kb_list_files` | 내가 업로드한 KB 파일 목록 | "내가 KB에 뭐 올렸었지?" |
| `ttobak_kb_delete_file` | KB 파일 ID로 삭제 (다음 인제스천까지는 검색 인덱스에 잔존) | "그 오래된 KB 파일 삭제해줘" |
| `ttobak_upload_document` | 로컬 파일(pdf/pptx/ppt)을 문서로 업로드(개인 또는 Account 공유) | "이 덱을 하나은행에 업로드해줘" |
| `ttobak_create_account` | `parentAccountId`로 상위 그룹을 지정해 Account 생성 | "하나금융그룹 아래 하나은행을 만들어줘" |
| `ttobak_add_account_member` | 기존 멤버 누구나 팀원 추가(AM/TAM/SSA/SA/SA Manager/AM Manager) | "jane@x.com을 하나은행에 TAM으로 추가해줘" |
| `ttobak_create_project` | Project(SFDC Opportunity) 생성 | "하나은행 갱신 프로젝트 만들어줘" |
| `ttobak_list_projects` | 내가 소유하거나 직접 초대되었거나 연결된 Account 멤버십으로 접근 가능한 프로젝트 목록 | "내 프로젝트 목록 보여줘" |
| `ttobak_get_project` | 프로젝트 상세: 멤버, 연결 Account | "하나은행 갱신 프로젝트 보여줘" |
| `ttobak_get_project_brief` | 프로젝트 묶음 원재료(미팅, 리서치, 인사이트) | "하나은행 갱신 프로젝트 브리프 한 번에" |
| `ttobak_get_project_insights` | 프로젝트에 연결된 미팅에서 집계한 유형별 인사이트 | "하나은행 갱신 프로젝트에서 어떤 리스크가 있었어?" |
| `ttobak_update_project` | 프로젝트 메타데이터 수정(name/description/SFDC 필드/stage) — 생략한 필드는 현재 값 유지, 링크·멤버는 영향 없음. 백엔드 요구사항상 name은 항상 재전송 필요 | "그 프로젝트 이름을 하나은행 갱신 2.0으로 바꿔줘" |
| `ttobak_link_project_account` | 프로젝트에 Account 연결(프로젝트 소유자 + 해당 Account 멤버여야 함) | "하나은행을 갱신 프로젝트에 연결해줘" |
| `ttobak_unlink_project_account` | 프로젝트에서 Account 연결 해제(프로젝트 소유자) | "하나은행을 그 프로젝트에서 연결 해제해줘" |
| `ttobak_logout` | 저장된 토큰 삭제 | "TTOBAK에서 로그아웃해줘" |

### 사용 예시

**일일 브리핑:**
```
이번 주 TTOBAK 미팅을 조회하고 주요 결정 사항을 요약해줘.
```

**특정 미팅 심층 분석:**
```
TTOBAK에서 미팅 abc123을 가져와서 담당자별 액션 아이템을 정리해줘.
```

**교차 미팅 분석:**
```
TTOBAK에 물어봐: "이번 달 여러 미팅에서 반복적으로 나온 주제가 뭐야?"
```

**미팅 사전 준비:**
```
TTOBAK에서 디자인 팀과의 최근 3개 미팅을 가져와서
미해결 이슈를 브리핑해줘.
```

---

### 문제 해결

| 문제 | 해결 방법 |
|------|-----------|
| `/mcp`에서 서버가 "failed" 표시 | `cd mcp-server && npm run build` 실행 후 Claude Code 재시작 |
| "Missing required env vars" 오류 | `.mcp.json`에 4개 환경변수가 모두 설정되어 있는지 확인 |
| 로그인 시 브라우저가 열리지 않음 | Claude Code stderr에 표시된 URL을 복사하여 수동으로 열기 |
| "invalid_grant" 토큰 교환 오류 | 토큰 만료됨. `ttobak_logout` 후 `ttobak_login` 실행 |
| 로그인은 되지만 API 호출 실패 | `TTOBAK_API_URL`이 올바른 CloudFront 도메인을 가리키는지 확인 |
| 포트 9876이 사용 중 | 해당 포트 사용 프로세스 종료: `lsof -ti:9876 \| xargs kill` |

### 제거

```bash
# MCP 서버 등록 해제
claude mcp remove ttobak

# 저장된 토큰 제거
rm -rf ~/.ttobak

# 서버 코드 삭제 (선택사항)
rm -rf mcp-server/
```
