# MCP 서버 확장: 뉴스/KB 검색 도구 + 다중 AI 클라이언트 지원 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** TTOBAK MCP 서버(`mcp-server/`)에 뉴스/인사이트 검색 도구 2개 + KB 검색 도구 1개를 추가하고, 다른 AI CLI(Codex/Amazon Q/Kiro)에서도 같은 stdio 서버를 연결하는 방법을 문서화한다.

**Architecture:** `POST /api/qa/search-kb`를 Python QA Lambda에 신규 라우트로 추가해 기존 `retrieve_from_kb()`(LLM 생성 없는 단일 Bedrock `retrieve()` 호출)를 그대로 노출한다. MCP 서버는 기존 `TtobakApi`(`api.ts`)에 3개 메서드를 추가하고, `index.ts`의 정적 도구 배열 + switch 디스패치에 3개 케이스를 더한다 — 신규 인증/에러 처리 없이 기존 패턴 그대로 재사용.

**Tech Stack:** TypeScript(`mcp-server/`, 기존 `@modelcontextprotocol/sdk`만), Python 3.12(`backend/python/qa/handler.py`, 신규 의존성 없음). CDK/인프라 변경 없음(기존 QA Lambda 라우팅에 조건 분기만 추가).

## Global Constraints

- `search-kb`는 `ttobak_ask`(최대 3라운드 에이전틱 루프)와 별개의 저비용 경로 — LLM 답변 생성 없이 검색 결과만 반환.
- 원격 HTTP MCP 서버는 만들지 않음 — stdio + 각 기기 로컬 빌드 방식 유지.
- 여러 기기 간 토큰 공유 없음 — 각 기기가 독립적으로 `~/.ttobak/tokens.json`을 갖고 최초 1회 로그인.
- SP1(뉴스 크롤링 엔진 교체)과 무관 — 이 작업은 크롤러가 이미 모아둔 데이터(`GET /api/insights`)를 노출할 뿐 크롤링 방식 자체는 건드리지 않음.

---

## File Structure

| 파일 | 변경 |
|---|---|
| `backend/python/qa/handler.py` | `lambda_handler`의 라우팅에 `/api/qa/search-kb` 케이스 추가, `handle_search_kb()` 신규 |
| `mcp-server/src/api.ts` | `searchNews`, `getNewsDetail`, `searchKb` 3개 메서드 추가 |
| `mcp-server/src/index.ts` | 도구 스키마 3개 추가, switch case 3개 추가 |
| `mcp-server/README.md` | 도구 테이블에 3행 추가 + "다른 AI 툴에서 연결하기" 섹션(영/한) 추가 |

---

## Task 1: 백엔드 — `POST /api/qa/search-kb`

**Files:**
- Modify: `backend/python/qa/handler.py`

**Interfaces:**
- Produces: `POST /api/qa/search-kb` — 요청 `{"question": string, "numberOfResults"?: number}`, 응답 `{"results": [{"text": string, "score": number, "uri": string}]}`

이 Lambda 모듈에는 기존 테스트 파일이 없다(`find backend/python/qa -iname "*test*"` 결과 없음). 이번 태스크에서 처음으로 `test_handler.py`를 만든다.

- [ ] **Step 1: 실패하는 테스트 작성**

`backend/python/qa/test_handler.py` 신규 생성:

```python
"""Unit tests for the QA Lambda's /api/qa/search-kb route."""

import base64
import json
import os
import unittest
from unittest import mock

os.environ['TABLE_NAME'] = 'test-table'
os.environ['KB_ID'] = 'test-kb'

_mock_dynamodb_resource = mock.MagicMock()
_mock_bedrock_agent_runtime = mock.MagicMock()

_boto3_resource_patcher = mock.patch('boto3.resource', return_value=_mock_dynamodb_resource)
_boto3_client_patcher = mock.patch('boto3.client', side_effect=lambda svc, **kw: {
    'bedrock-agent-runtime': _mock_bedrock_agent_runtime,
}.get(svc, mock.MagicMock()))

_boto3_resource_patcher.start()
_boto3_client_patcher.start()

import handler


def _make_event(path, body_dict, user_sub='user-123'):
    payload = base64.urlsafe_b64encode(json.dumps({'sub': user_sub}).encode()).decode().rstrip('=')
    fake_jwt = f'header.{payload}.sig'
    return {
        'requestContext': {'http': {'method': 'POST'}},
        'rawPath': path,
        'headers': {'authorization': f'Bearer {fake_jwt}'},
        'body': json.dumps(body_dict),
        'isBase64Encoded': False,
    }


class TestSearchKbRoute(unittest.TestCase):
    def setUp(self):
        handler.table = mock.MagicMock()
        handler.table.get_item.return_value = {}  # no cache hit

    @mock.patch.object(handler, 'retrieve_from_kb')
    def test_returns_results_from_retrieve_from_kb(self, mock_retrieve):
        mock_retrieve.return_value = [
            {'text': 'relevant snippet', 'uri': 's3://bucket/key.md', 'score': 0.82},
        ]

        event = _make_event('/api/qa/search-kb', {'question': 'What is the KB pricing?'})
        result = handler.lambda_handler(event, None)

        self.assertEqual(result['statusCode'], 200)
        body = json.loads(result['body'])
        self.assertEqual(body['results'], mock_retrieve.return_value)
        mock_retrieve.assert_called_once_with('What is the KB pricing?', 5, 'user-123')

    @mock.patch.object(handler, 'retrieve_from_kb')
    def test_passes_custom_number_of_results(self, mock_retrieve):
        mock_retrieve.return_value = []

        event = _make_event('/api/qa/search-kb', {'question': 'q', 'numberOfResults': 3})
        handler.lambda_handler(event, None)

        mock_retrieve.assert_called_once_with('q', 3, 'user-123')

    def test_missing_question_returns_400(self):
        event = _make_event('/api/qa/search-kb', {})
        result = handler.lambda_handler(event, None)

        self.assertEqual(result['statusCode'], 400)
        body = json.loads(result['body'])
        self.assertEqual(body['error']['code'], 'BAD_REQUEST')


if __name__ == '__main__':
    unittest.main()
```

- [ ] **Step 2: 테스트 실행해서 실패 확인**

```bash
cd backend/python/qa && python3 -m unittest test_handler -v
```
Expected: `AssertionError: 404 != 200` (라우트 없음 — 현재 `lambda_handler`가 `/api/qa/search-kb`를 모르므로 `NOT_FOUND` 반환)

- [ ] **Step 3: `handle_search_kb()` 추가 + 라우팅 연결**

`handler.py`의 `retrieve_from_kb` 함수 바로 뒤(422행, 다음 섹션 구분선 앞)에 추가:

```python
def handle_search_kb(question, number_of_results, user_id):
    """POST /api/qa/search-kb — cheap KB-only search, no LLM generation.

    Unlike handle_ask (up to 3 agentic tool-use rounds against Bedrock
    converse()), this is a single retrieve_from_kb() call — for callers
    (e.g. the MCP server's ttobak_search_kb tool) that just need matching
    snippets, not a synthesized answer.
    """
    results = retrieve_from_kb(question, number_of_results, user_id)
    return response(200, {'results': results})
```

`lambda_handler`의 라우팅 블록(190-207행)을:

```python
    # Route handling
    if path == '/api/qa/ask':
        question = body.get('question', '').strip()
        if not question:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'question is required'}})
        return handle_ask(question, body.get('context'), body.get('meetingId'), body.get('sessionId'), user_id)
    elif path == '/api/qa/search-kb':
        question = body.get('question', '').strip()
        if not question:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'question is required'}})
        number_of_results = body.get('numberOfResults', 5)
        return handle_search_kb(question, number_of_results, user_id)
    elif path == '/api/qa/detect-questions':
        return handle_detect_questions(body)
    elif path.startswith('/api/qa/meeting/'):
        question = body.get('question', '').strip()
        if not question:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'question is required'}})
        meeting_id = path.split('/api/qa/meeting/')[1].split('/')[0]
        if not meeting_id:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'meetingId is required'}})
        return handle_meeting_ask(question, meeting_id, user_id, body.get('sessionId'))
    else:
        return response(404, {'error': {'code': 'NOT_FOUND', 'message': 'Route not found'}})
```
로 교체 (새 `elif path == '/api/qa/search-kb':` 블록만 추가, 나머지는 그대로).

- [ ] **Step 4: 테스트 재실행해서 통과 확인**

```bash
python3 -m unittest test_handler -v
```
Expected: OK, 3 tests

- [ ] **Step 5: Commit**

```bash
git add backend/python/qa/handler.py backend/python/qa/test_handler.py
git commit -m "feat(qa): add POST /api/qa/search-kb — cheap KB-only search route"
```

---

## Task 2: MCP — `api.ts`에 3개 메서드 추가

**Files:**
- Modify: `mcp-server/src/api.ts`

**Interfaces:**
- Consumes: `GET /api/insights`, `GET /api/insights/{sourceId}/{docHash}` (기존, 변경 없음), `POST /api/qa/search-kb` (Task 1에서 신설)
- Produces: `TtobakApi.searchNews(params?)`, `TtobakApi.getNewsDetail(sourceId, docHash)`, `TtobakApi.searchKb(question, numberOfResults?)`

TypeScript 프로젝트라 유닛 테스트 프레임워크가 없다(기존 `mcp-server/`에 테스트 파일 없음, `package.json`에 test 스크립트 없음) — 검증은 `tsc --noEmit` + Task 4의 수동 도구 호출로 한다.

- [ ] **Step 1: `getDocument` 메서드 뒤(90행)에 3개 메서드 추가**

```typescript
  async searchNews(params?: {
    type?: string;
    source?: string;
    tags?: string[];
    sort?: string;
    page?: number;
    limit?: number;
  }) {
    const q = new URLSearchParams();
    if (params?.type) q.set('type', params.type);
    if (params?.source) q.set('source', params.source);
    if (params?.tags && params.tags.length) q.set('tags', params.tags.join(','));
    if (params?.sort) q.set('sort', params.sort);
    if (params?.page) q.set('page', String(params.page));
    if (params?.limit) q.set('limit', String(params.limit));
    const qs = q.toString();
    return this.get(`/api/insights${qs ? '?' + qs : ''}`);
  }

  async getNewsDetail(sourceId: string, docHash: string) {
    return this.get(`/api/insights/${sourceId}/${docHash}`);
  }

  async searchKb(question: string, numberOfResults?: number) {
    const body: Record<string, unknown> = { question };
    if (numberOfResults) body.numberOfResults = numberOfResults;
    return this.post('/api/qa/search-kb', body);
  }
```

- [ ] **Step 2: TypeScript 컴파일 확인**

```bash
cd mcp-server && npx tsc --noEmit
```
Expected: 에러 없음

- [ ] **Step 3: Commit**

```bash
git add mcp-server/src/api.ts
git commit -m "feat(mcp): add searchNews/getNewsDetail/searchKb API client methods"
```

---

## Task 3: MCP — `index.ts`에 도구 3개 등록

**Files:**
- Modify: `mcp-server/src/index.ts`

**Interfaces:**
- Consumes: `TtobakApi.searchNews`, `TtobakApi.getNewsDetail`, `TtobakApi.searchKb` (Task 2)
- Produces: MCP 도구 `ttobak_search_news`, `ttobak_get_news_detail`, `ttobak_search_kb` (`tools/list`에 노출)

- [ ] **Step 1: `ListToolsRequestSchema`의 배열에 3개 스키마 추가**

`ttobak_ask` 항목(165-178행) 바로 앞에 삽입:

```typescript
    {
      name: 'ttobak_search_news',
      description:
        'Search crawled news/tech insights (company news, AWS doc updates). Filter by type, source, tags, sort, and paginate.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          type: { type: 'string', description: 'Optional docType filter (e.g. "news")' },
          source: { type: 'string', description: 'Optional crawler sourceId filter' },
          tags: { type: 'array', items: { type: 'string' }, description: 'Optional tags (AND match)' },
          sort: { type: 'string', enum: ['newest', 'oldest', 'title'], description: 'Sort order (default newest)' },
          page: { type: 'number', description: 'Page number (default 1)' },
          limit: { type: 'number', description: 'Results per page (default 20, max 50)' },
        },
      },
    },
    {
      name: 'ttobak_get_news_detail',
      description: 'Get the full content of a crawled news/insight document.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          sourceId: { type: 'string', description: 'Crawler source ID' },
          docHash: { type: 'string', description: 'Document hash' },
        },
        required: ['sourceId', 'docHash'],
      },
    },
    {
      name: 'ttobak_search_kb',
      description:
        'Search the knowledge base for matching snippets (no LLM synthesis — cheaper/faster than ttobak_ask). Use for quick fact lookups.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          question: { type: 'string', description: 'Search query' },
          numberOfResults: { type: 'number', description: 'Max results (default 5, max 10)' },
        },
        required: ['question'],
      },
    },
```

- [ ] **Step 2: `CallToolRequestSchema`의 switch에 3개 case 추가**

`case 'ttobak_ask': {` 블록(293-302행) 바로 앞에 삽입:

```typescript
      case 'ttobak_search_news': {
        const result = await api.searchNews(args as Record<string, unknown>);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_news_detail': {
        const { sourceId, docHash } = args as { sourceId: string; docHash: string };
        if (!sourceId) return error('sourceId is required');
        if (!docHash) return error('docHash is required');
        const result = await api.getNewsDetail(sourceId, docHash);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_search_kb': {
        const { question, numberOfResults } = args as { question: string; numberOfResults?: number };
        if (!question) return error('question is required');
        const result = await api.searchKb(question, numberOfResults);
        return text(JSON.stringify(result, null, 2));
      }

```

- [ ] **Step 3: TypeScript 컴파일 + 빌드 확인**

```bash
cd mcp-server && npm run build
```
Expected: 에러 없이 `dist/index.js` 생성

- [ ] **Step 4: Commit**

```bash
git add mcp-server/src/index.ts
git commit -m "feat(mcp): register ttobak_search_news/get_news_detail/search_kb tools"
```

---

## Task 4: 수동 검증 — 실제 MCP 도구 호출

**Files:** 없음 — 검증만 (코드 변경 없음)

- [ ] **Step 1: Claude Code 재시작 후 `/mcp`로 15개 도구 확인**

```
/mcp
```
Expected: `ttobak` 서버의 Tools 목록에 `ttobak_search_news`, `ttobak_get_news_detail`, `ttobak_search_kb`가 기존 12개와 함께 총 15개로 표시됨.

- [ ] **Step 2: `ttobak_search_news` 호출**

Claude Code에서: "TTOBAK에서 최근 뉴스 인사이트 5개만 검색해줘" 같은 프롬프트로 `ttobak_search_news` 도구 호출 → 응답에 `documents` 배열과 `totalCount`가 있는지 확인.

- [ ] **Step 3: `ttobak_get_news_detail` 호출**

Step 2 응답에서 얻은 `sourceId`/`docHash`로: "그 문서 3번째 것 상세 내용 보여줘" → 본문(`content` 필드) 포함 응답 확인.

- [ ] **Step 4: `ttobak_search_kb` 호출**

"TTOBAK KB에서 '클라우드 마이그레이션' 관련 내용 검색해줘" → `results` 배열에 `text`/`score`/`uri`가 있는지, 응답 속도가 `ttobak_ask`보다 빠른지(에이전틱 루프 없음) 확인.

---

## Task 5: README — 다중 AI 클라이언트 등록 가이드 + 도구 테이블 갱신

**Files:**
- Modify: `mcp-server/README.md`

**Interfaces:** 없음 (문서만)

- [ ] **Step 1: 영문 도구 테이블에 3행 추가**

"### Available Tools" 아래 테이블(영문 섹션, `ttobak_ask` 행 바로 앞)에 추가:

```markdown
| `ttobak_search_news` | Search crawled news/insights | "Search TTOBAK news for AI investment trends" |
| `ttobak_get_news_detail` | Get full content of a news document | "Show me the full article for that doc" |
| `ttobak_search_kb` | Quick KB snippet search (no LLM synthesis) | "Search TTOBAK's KB for cloud migration docs" |
```

같은 3행을 한국어 섹션의 대응 테이블에도 번역해 추가:

```markdown
| `ttobak_search_news` | 크롤링된 뉴스/인사이트 검색 | "TTOBAK에서 AI 투자 트렌드 뉴스 검색해줘" |
| `ttobak_get_news_detail` | 뉴스 문서 전체 내용 조회 | "그 문서 전체 내용 보여줘" |
| `ttobak_search_kb` | 빠른 KB 스니펫 검색 (LLM 생성 없음) | "TTOBAK KB에서 클라우드 마이그레이션 문서 검색해줘" |
```

- [ ] **Step 2: 영문 섹션에 "Connecting from Other AI Tools" 추가**

"### Uninstall" 섹션(영문, 198행 부근) 바로 앞에 추가:

```markdown
### Connecting from Other AI Tools

The MCP server itself doesn't change per client — it's the same stdio process with the same Cognito PKCE auth. Each machine/CLI needs its own local clone + build, and its own first-time login (tokens are not shared across devices).

**Prerequisite (once per machine):**
```bash
git clone <this-repo> && cd ttobak/mcp-server
npm install && npm run build
```

**Codex CLI** — add to `~/.codex/config.toml`:
```toml
[mcp_servers.ttobak]
command = "node"
args = ["/path/to/ttobak/mcp-server/dist/index.js"]
env = { TTOBAK_COGNITO_DOMAIN = "https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com", TTOBAK_CLIENT_ID = "33rh85mv6l9n7tn3s5h16prfdr", TTOBAK_API_URL = "https://ttobak.atomai.click", TTOBAK_REGION = "ap-northeast-2" }
```

**Amazon Q Developer CLI** — add to `~/.aws/amazonq/mcp.json`:
```json
{
  "mcpServers": {
    "ttobak": {
      "command": "node",
      "args": ["/path/to/ttobak/mcp-server/dist/index.js"],
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

**Kiro** — add the same `command`/`args`/`env` block to Kiro's MCP settings (workspace or global — check Kiro's docs for the exact config file path on your install).

First tool call on each new machine opens a browser for Cognito login, same as the Claude Code setup above — tokens land in that machine's own `~/.ttobak/tokens.json` and don't sync anywhere.
```

- [ ] **Step 3: 한국어 섹션에 동일 내용 번역 추가**

"### 제거" 섹션(한국어, 403행 부근) 바로 앞에 추가:

```markdown
### 다른 AI 툴에서 연결하기

MCP 서버 자체는 클라이언트가 바뀌어도 동일합니다 — 같은 stdio 프로세스, 같은 Cognito PKCE 인증입니다. 각 기기/CLI는 자체적으로 로컬 clone + 빌드가 필요하고, 최초 로그인도 각자 따로 해야 합니다(토큰은 기기 간 공유되지 않습니다).

**사전 준비 (기기별 1회):**
```bash
git clone <this-repo> && cd ttobak/mcp-server
npm install && npm run build
```

**Codex CLI** — `~/.codex/config.toml`에 추가:
```toml
[mcp_servers.ttobak]
command = "node"
args = ["/path/to/ttobak/mcp-server/dist/index.js"]
env = { TTOBAK_COGNITO_DOMAIN = "https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com", TTOBAK_CLIENT_ID = "33rh85mv6l9n7tn3s5h16prfdr", TTOBAK_API_URL = "https://ttobak.atomai.click", TTOBAK_REGION = "ap-northeast-2" }
```

**Amazon Q Developer CLI** — `~/.aws/amazonq/mcp.json`에 추가:
```json
{
  "mcpServers": {
    "ttobak": {
      "command": "node",
      "args": ["/path/to/ttobak/mcp-server/dist/index.js"],
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

**Kiro** — 동일한 `command`/`args`/`env`를 Kiro의 MCP 설정(워크스페이스 또는 전역)에 추가하세요. 정확한 설정 파일 경로는 설치한 Kiro 버전의 문서를 확인하세요.

새 기기에서 첫 도구 호출 시 위 Claude Code 설정과 동일하게 브라우저 로그인이 열리고, 토큰은 그 기기의 `~/.ttobak/tokens.json`에만 저장되며 다른 곳과 동기화되지 않습니다.
```

- [ ] **Step 4: Commit**

```bash
git add mcp-server/README.md
git commit -m "docs(mcp): document news/kb search tools and multi-client setup"
```
