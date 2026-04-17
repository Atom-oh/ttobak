# Ttobak MCP Server — Claude Code Integration Guide

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

## English

### What This Does

A local MCP (Model Context Protocol) server that gives Claude Code direct access to your Ttobak meeting data. Once connected, Claude can list meetings, read summaries/transcripts, and answer questions about your meetings using natural language.

```
Claude Code  ──stdio──>  Ttobak MCP Server  ──HTTPS──>  CloudFront  ──>  API Gateway  ──>  Lambda
                              │
                         ~/.ttobak/tokens.json
                         (Cognito OAuth PKCE)
```

### Prerequisites

- Node.js 18+
- AWS credentials (for initial setup script only)
- A Ttobak account (Cognito user)

---

### Setup

#### Method 1: CLI Registration (Recommended)

```bash
# 1. Install and build
cd mcp-server && npm install && npm run build

# 2. Register with Claude Code
claude mcp add --transport stdio --scope project \
  --env TTOBAK_COGNITO_DOMAIN="https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com" \
  --env TTOBAK_CLIENT_ID="33rh85mv6l9n7tn3s5h16prfdr" \
  --env TTOBAK_API_URL="https://ttobak.atomai.click" \
  --env TTOBAK_REGION="ap-northeast-2" \
  ttobak -- node mcp-server/dist/index.js
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
          ttobak_get_meeting, ttobak_ask, ttobak_logout
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

The first time you use any Ttobak tool, the MCP server will open your browser for Cognito login.

```
1. Claude Code calls ttobak_login (or any data tool)
2. Browser opens → Cognito Hosted UI login page
3. You enter email + password
4. Browser redirects to localhost:9876/callback
5. Page shows "Ttobak MCP Authenticated"
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

| Tool | Description | Example Prompt |
|------|-------------|----------------|
| `ttobak_login` | Authenticate via browser | "Log in to Ttobak" |
| `ttobak_status` | Check auth status and config | "Check Ttobak connection status" |
| `ttobak_list_meetings` | List meetings (paginated) | "Show my recent meetings" |
| `ttobak_get_meeting` | Full meeting detail | "Get the details of meeting X" |
| `ttobak_ask` | RAG Q&A across meetings | "What decisions were made about the API redesign?" |
| `ttobak_logout` | Clear stored tokens | "Log out of Ttobak" |

### Usage Examples

**Daily briefing:**
```
List my Ttobak meetings from this week and summarize the key decisions.
```

**Deep dive into a specific meeting:**
```
Get meeting abc123 from Ttobak and list all action items with owners.
```

**Cross-meeting analysis:**
```
Ask Ttobak: "What topics came up in multiple meetings this month?"
```

**Pre-meeting prep:**
```
Get the last 3 meetings with the design team from Ttobak
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

Claude Code에서 Ttobak 미팅 데이터에 직접 접근할 수 있는 로컬 MCP (Model Context Protocol) 서버입니다. 연결하면 Claude가 미팅 목록 조회, 요약/트랜스크립트 읽기, 자연어로 미팅에 대한 질문을 할 수 있습니다.

```
Claude Code  ──stdio──>  Ttobak MCP Server  ──HTTPS──>  CloudFront  ──>  API Gateway  ──>  Lambda
                              │
                         ~/.ttobak/tokens.json
                         (Cognito OAuth PKCE)
```

### 사전 요구사항

- Node.js 18+
- AWS 자격 증명 (초기 설정 스크립트에만 필요)
- Ttobak 계정 (Cognito 사용자)

---

### 설정

#### 방법 1: CLI 등록 (권장)

```bash
# 1. 설치 및 빌드
cd mcp-server && npm install && npm run build

# 2. Claude Code에 등록
claude mcp add --transport stdio --scope project \
  --env TTOBAK_COGNITO_DOMAIN="https://ttobak-auth-180294183052.auth.ap-northeast-2.amazoncognito.com" \
  --env TTOBAK_CLIENT_ID="33rh85mv6l9n7tn3s5h16prfdr" \
  --env TTOBAK_API_URL="https://ttobak.atomai.click" \
  --env TTOBAK_REGION="ap-northeast-2" \
  ttobak -- node mcp-server/dist/index.js
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
          ttobak_get_meeting, ttobak_ask, ttobak_logout
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

Ttobak 도구를 처음 사용할 때 MCP 서버가 브라우저를 열어 Cognito 로그인을 진행합니다.

```
1. Claude Code가 ttobak_login 호출 (또는 데이터 도구 호출 시 자동)
2. 브라우저 열림 → Cognito Hosted UI 로그인 페이지
3. 이메일 + 비밀번호 입력
4. 브라우저가 localhost:9876/callback으로 리다이렉트
5. "Ttobak MCP Authenticated" 페이지 표시
6. 브라우저 탭 닫기 → Claude Code로 복귀
7. 토큰이 ~/.ttobak/tokens.json에 저장 (30일 리프레시)
```

초기 로그인 후에는 토큰이 자동 갱신됩니다. 약 30일간 재로그인 불필요합니다.

명시적으로 로그인을 트리거하려면:

```
Ttobak에 로그인해줘
```

---

### 사용 가능한 도구

| 도구 | 설명 | 예시 프롬프트 |
|------|------|---------------|
| `ttobak_login` | 브라우저를 통한 인증 | "Ttobak에 로그인해줘" |
| `ttobak_status` | 인증 상태 및 설정 확인 | "Ttobak 연결 상태 확인해줘" |
| `ttobak_list_meetings` | 미팅 목록 조회 (페이지네이션) | "최근 미팅 목록 보여줘" |
| `ttobak_get_meeting` | 미팅 상세 정보 | "미팅 X의 상세 내용을 가져와줘" |
| `ttobak_ask` | 미팅 기반 RAG Q&A | "API 재설계에 대해 어떤 결정이 있었어?" |
| `ttobak_logout` | 저장된 토큰 삭제 | "Ttobak에서 로그아웃해줘" |

### 사용 예시

**일일 브리핑:**
```
이번 주 Ttobak 미팅을 조회하고 주요 결정 사항을 요약해줘.
```

**특정 미팅 심층 분석:**
```
Ttobak에서 미팅 abc123을 가져와서 담당자별 액션 아이템을 정리해줘.
```

**교차 미팅 분석:**
```
Ttobak에 물어봐: "이번 달 여러 미팅에서 반복적으로 나온 주제가 뭐야?"
```

**미팅 사전 준비:**
```
Ttobak에서 디자인 팀과의 최근 3개 미팅을 가져와서
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
