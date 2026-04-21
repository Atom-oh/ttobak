# ADR-003: MCP Server for External Meeting Data Access

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context

Ttobak stores meeting notes (recordings, transcripts, summaries, action items) behind a Cognito-authenticated API served through CloudFront. There is a need to access this data programmatically from a local Claude Code instance for pre-meeting briefings and cross-meeting analysis.

Requirements:
- Authenticated access to the existing REST API
- All traffic must flow through CloudFront (security policy)
- No new public-facing infrastructure
- Seamless integration with Claude Code's tool ecosystem
- Long-lived sessions without repeated manual login

### Authentication Chain

Three layers validate JWT tokens in sequence:

1. **Lambda@Edge** (us-east-1): RSA-SHA256 signature verification, checks `aud` matches the SPA client ID for ID tokens
2. **API Gateway HTTP JWT Authorizer**: validates `aud` against the SPA client ID
3. **Go backend middleware**: JWKS-based verification of issuer and expiration (does not check `aud`)

Any new authentication mechanism must satisfy all three layers.

## Options Considered

### Option 1: Local MCP Server with OAuth PKCE (Chosen)

Build a local stdio MCP server that authenticates via OAuth 2.0 Authorization Code + PKCE against the existing Cognito User Pool. The server runs as a child process of Claude Code, opens a browser for login, receives the callback on `localhost:9876`, and stores tokens locally.

- **Pros**: Native Claude Code integration via MCP protocol; uses existing Cognito auth with no new infrastructure; refresh tokens provide 30-day sessions; browser-based login is familiar UX (same as Notion/Slack MCP); no secrets stored in config files
- **Cons**: Requires OAuth callback URL registered in Cognito; first-time login opens browser; MCP server code to maintain

### Option 2: API Key Authentication

Issue API keys stored in DynamoDB, validated by a new middleware layer in the Go backend. The MCP server or any HTTP client sends the key via a header.

- **Pros**: Simple stateless auth; no browser interaction; works in headless environments
- **Cons**: Requires new API key management (generation, rotation, revocation UI); key stored on disk in plaintext; must update Lambda@Edge and API Gateway authorizer to accept API keys alongside JWTs; new attack surface (key leakage)

### Option 3: Dedicated REST Briefing Endpoint (No MCP)

Add a `/api/briefing` endpoint that returns pre-formatted meeting summaries. Access via `curl` or a simple script.

- **Pros**: No MCP SDK dependency; works with any HTTP client
- **Cons**: Poor Claude Code integration (no structured tool interface); must build formatting logic server-side; no interactive Q&A capability; still needs an auth mechanism (circles back to Option 1 or 2)

### Option 4: New Cognito App Client for MCP

Create a separate `ttobak-mcp-client` in Cognito for OAuth PKCE, keeping it isolated from the SPA client.

- **Pros**: Clean separation of concerns; independent OAuth configuration
- **Cons**: The new client ID produces tokens with a different `aud` claim, requiring updates to Lambda@Edge (embedded client ID), API Gateway JWT authorizer (audience array), and potentially the Go backend; Lambda@Edge changes require us-east-1 deployment and CloudFront association update; significantly more deployment risk for marginal isolation benefit

## Decision

Use Option 1: a local MCP server with OAuth PKCE, reusing the existing SPA client.

Adding OAuth configuration (`authorization_code` grant, `localhost:9876/callback`) to the existing SPA client (`generateSecret: false`) enables PKCE without modifying any auth layer. The ID token's `aud` claim matches the SPA client ID that Lambda@Edge, API Gateway, and the Go backend already accept.

The MCP server exposes five tools: `ttobak_list_meetings`, `ttobak_get_meeting`, `ttobak_ask` (RAG Q&A), `ttobak_login`, and `ttobak_logout`. Tokens are stored in `~/.ttobak/tokens.json` with `chmod 600`.

Option 4 was rejected because it requires coordinated changes across three auth layers (including Lambda@Edge in us-east-1) for isolation that provides no practical security benefit in a single-user project.

## Consequences

### Positive
- Zero new AWS infrastructure; only a CDK config change to the existing SPA client
- Claude Code can natively call `ttobak_list_meetings`, `ttobak_get_meeting`, and `ttobak_ask` as MCP tools
- Refresh tokens provide ~30-day sessions before re-login is needed
- The same pattern can be extended to expose additional tools (export, translation, KB search)

### Negative
- Adding OAuth config to the SPA client couples browser auth and MCP auth on the same client; a future multi-tenant deployment may want to separate them (can be revisited via Option 4 at that point)
- The MCP server requires `node` and `npm install` on the developer's local machine
- Browser-based login does not work in headless/CI environments (acceptable for the briefing use case)

## References
- `mcp-server/` — MCP server implementation
- `infra/lib/auth-stack.ts` — Cognito SPA client OAuth configuration
- `infra/lib/edge-auth-stack.ts` — Lambda@Edge JWT validation (audience check at line 143)
- `infra/lib/gateway-stack.ts` — API Gateway JWT authorizer (audience at line 173)
- [Cognito OAuth 2.0 PKCE](https://docs.aws.amazon.com/cognito/latest/developerguide/authorization-endpoint.html)
- [MCP Protocol Specification](https://modelcontextprotocol.io)

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경

Ttobak은 미팅 노트(녹음, 트랜스크립트, 요약, 액션 아이템)를 Cognito 인증 기반 API 뒤에 저장하며, CloudFront를 통해 서비스합니다. 로컬 Claude Code 인스턴스에서 이 데이터에 프로그래밍 방식으로 접근하여 미팅 전 브리핑 및 교차 미팅 분석을 수행할 필요가 있습니다.

요구사항:
- 기존 REST API에 대한 인증된 접근
- 모든 트래픽은 CloudFront를 통해야 함 (보안 정책)
- 새로운 퍼블릭 인프라 없음
- Claude Code 도구 생태계와의 원활한 통합
- 반복적인 수동 로그인 없는 장기 세션 유지

### 인증 체인

세 가지 레이어가 순서대로 JWT 토큰을 검증합니다:

1. **Lambda@Edge** (us-east-1): RSA-SHA256 서명 검증, ID 토큰의 `aud`가 SPA 클라이언트 ID와 일치하는지 확인
2. **API Gateway HTTP JWT Authorizer**: SPA 클라이언트 ID에 대해 `aud` 검증
3. **Go 백엔드 미들웨어**: JWKS 기반 issuer 및 만료 검증 (`aud` 미확인)

새로운 인증 메커니즘은 세 레이어 모두를 통과해야 합니다.

## 검토한 옵션

### 옵션 1: OAuth PKCE 기반 로컬 MCP 서버 (선택됨)

기존 Cognito User Pool에 OAuth 2.0 Authorization Code + PKCE로 인증하는 로컬 stdio MCP 서버를 구축합니다. Claude Code의 자식 프로세스로 실행되며, 브라우저를 열어 로그인하고 `localhost:9876`에서 콜백을 받아 토큰을 로컬에 저장합니다.

- **장점**: MCP 프로토콜을 통한 네이티브 Claude Code 통합; 새 인프라 없이 기존 Cognito 인증 사용; 리프레시 토큰으로 30일 세션 유지; Notion/Slack MCP와 동일한 브라우저 기반 로그인 UX; 설정 파일에 시크릿 저장 불필요
- **단점**: Cognito에 OAuth 콜백 URL 등록 필요; 첫 로그인 시 브라우저 열림; MCP 서버 코드 유지보수 필요

### 옵션 2: API 키 인증

DynamoDB에 API 키를 저장하고 Go 백엔드의 새 미들웨어 레이어에서 검증합니다. MCP 서버 또는 HTTP 클라이언트가 헤더를 통해 키를 전송합니다.

- **장점**: 단순한 무상태 인증; 브라우저 상호작용 불필요; 헤드리스 환경에서 작동
- **단점**: 새로운 API 키 관리 필요 (생성, 교체, 폐기 UI); 디스크에 키가 평문으로 저장됨; JWT와 함께 API 키를 수용하도록 Lambda@Edge 및 API Gateway 수정 필요; 키 유출에 대한 새로운 공격 표면

### 옵션 3: 전용 REST 브리핑 엔드포인트 (MCP 없음)

사전 포맷된 미팅 요약을 반환하는 `/api/briefing` 엔드포인트를 추가합니다. `curl` 또는 간단한 스크립트로 접근합니다.

- **장점**: MCP SDK 의존성 없음; 모든 HTTP 클라이언트에서 작동
- **단점**: Claude Code 통합 불량 (구조화된 도구 인터페이스 없음); 서버 측 포맷팅 로직 구축 필요; 대화형 Q&A 기능 없음; 여전히 인증 메커니즘 필요 (옵션 1 또는 2로 회귀)

### 옵션 4: MCP 전용 새 Cognito 앱 클라이언트

OAuth PKCE용으로 별도의 `ttobak-mcp-client`를 Cognito에 생성하여 SPA 클라이언트와 분리합니다.

- **장점**: 관심사의 깔끔한 분리; 독립적인 OAuth 설정
- **단점**: 새 클라이언트 ID는 다른 `aud` 클레임을 가진 토큰을 생성하여 Lambda@Edge (임베디드 클라이언트 ID), API Gateway JWT authorizer (audience 배열), Go 백엔드 수정이 필요합니다. Lambda@Edge 변경은 us-east-1 배포 및 CloudFront 연결 업데이트가 필요합니다. 미미한 분리 이점에 비해 배포 위험이 크게 증가합니다.

## 결정

옵션 1을 선택합니다: 기존 SPA 클라이언트를 재사용하는 OAuth PKCE 기반 로컬 MCP 서버.

기존 SPA 클라이언트(`generateSecret: false`)에 OAuth 설정(`authorization_code` 그랜트, `localhost:9876/callback`)을 추가하면 인증 레이어를 수정하지 않고 PKCE가 가능합니다. ID 토큰의 `aud` 클레임은 Lambda@Edge, API Gateway, Go 백엔드가 이미 수용하는 SPA 클라이언트 ID와 일치합니다.

MCP 서버는 다섯 가지 도구를 노출합니다: `ttobak_list_meetings`, `ttobak_get_meeting`, `ttobak_ask` (RAG Q&A), `ttobak_login`, `ttobak_logout`. 토큰은 `~/.ttobak/tokens.json`에 `chmod 600`으로 저장됩니다.

옵션 4는 단일 사용자 프로젝트에서 실질적인 보안 이점이 없는 분리를 위해 세 가지 인증 레이어(us-east-1의 Lambda@Edge 포함)에 대한 조율된 변경이 필요하여 기각되었습니다.

## 영향

### 긍정적
- 새로운 AWS 인프라 불필요; 기존 SPA 클라이언트에 대한 CDK 설정 변경만 필요
- Claude Code가 `ttobak_list_meetings`, `ttobak_get_meeting`, `ttobak_ask`를 MCP 도구로 네이티브 호출 가능
- 리프레시 토큰으로 재로그인 전 약 30일 세션 유지
- 동일한 패턴을 확장하여 추가 도구 노출 가능 (내보내기, 번역, KB 검색)

### 부정적
- SPA 클라이언트에 OAuth 설정을 추가하면 브라우저 인증과 MCP 인증이 동일 클라이언트에 결합됨; 향후 멀티테넌트 배포 시 분리가 필요할 수 있음 (그 시점에 옵션 4로 재검토 가능)
- MCP 서버는 개발자 로컬 머신에 `node`와 `npm install`이 필요
- 브라우저 기반 로그인은 헤드리스/CI 환경에서 작동하지 않음 (브리핑 용도로는 허용 가능)

## 참고 자료
- `mcp-server/` — MCP 서버 구현
- `infra/lib/auth-stack.ts` — Cognito SPA 클라이언트 OAuth 설정
- `infra/lib/edge-auth-stack.ts` — Lambda@Edge JWT 검증 (143행 audience 확인)
- `infra/lib/gateway-stack.ts` — API Gateway JWT authorizer (173행 audience)
- [Cognito OAuth 2.0 PKCE](https://docs.aws.amazon.com/cognito/latest/developerguide/authorization-endpoint.html)
- [MCP Protocol Specification](https://modelcontextprotocol.io)
