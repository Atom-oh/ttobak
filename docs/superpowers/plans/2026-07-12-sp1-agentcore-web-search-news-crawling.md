# SP1: AgentCore Web Search 뉴스 크롤링 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `news_crawler.py`의 Google News/사이트 RSS 파싱을 AWS Bedrock AgentCore Gateway의 신규 Web Search 커넥터 호출로 교체하고, `research-agent/tools.py`의 `web_search()`도 같은 Gateway로 옮긴다.

**Architecture:** us-east-1에 신규 CDK 스택(`WebSearchGatewayStack`)으로 AgentCore `Gateway`(L2, IAM/SigV4 인증) + `GatewayTarget`(L1 `CfnGatewayTarget` — Web Search 커넥터는 L2가 아직 지원하지 않음, §참조: 조사 결과)을 배포한다. `news_crawler.py`(ap-northeast-2 Lambda)와 `research-agent/tools.py`(AgentCore Runtime 컨테이너)는 `botocore.auth.SigV4Auth`로 직접 서명한 HTTPS POST를 Gateway의 MCP 엔드포인트(`https://{gatewayId}.gateway.bedrock-agentcore.us-east-1.api.aws/mcp`)에 보내 `tools/call`(`WebSearch`)을 호출한다. 기존 요약/dedup/저장 로직(Bedrock summarize+tag, DynamoDB, S3)은 그대로 재사용하고 입력만 전문(scraped body) → 검색 snippet으로 바뀐다.

**Tech Stack:** Python 3.12 (crawler Lambda + research-agent 컨테이너, `boto3`/`botocore`만 — 신규 의존성 없음), CDK TypeScript (`aws-cdk-lib` `^2.241.0` → 실제 설치본을 `^2.261.0`으로 갱신 필요 — 커넥터 타깃 타입이 2.241에는 없음).

## Global Constraints

- SigV4 서명 서비스명은 `bedrock-agentcore` (조사로 확정 — `boto3.client('bedrock-agentcore').meta.service_model.signing_name` 결과).
- Web Search 커넥터는 **us-east-1 전용**(AWS 고정, 변경 불가).
- CDK `agentcore.Gateway`(L2)는 사용 가능하나 `GatewayTarget`(L2)은 connector 타깃 팩토리를 지원하지 않음 → **`CfnGatewayTarget`(L1)을 직접 사용**.
- `aws-cdk-lib`는 `package.json`에 `^2.241.0`으로 이미 선언되어 있어 `npm install`만 다시 하면 connector 필드가 있는 최신 버전(2.261.0 확인됨)으로 자동 갱신됨 — `package.json` 수정 불필요.
- Python 쪽 SigV4 서명은 `botocore`(`boto3`의 의존성)만 쓴다 — `boto3`는 Lambda 런타임에 이미 포함되어 있고 `requirements.txt`(`crawler/`는 `boto3`만 명시, `research-agent/`는 `boto3>=1.34.0` 명시)에 이미 있으므로 **어느 쪽 `requirements.txt`/`requirements-container.txt`도 수정하지 않는다.**
- `customUrls`(사용자 직접 지정 URL) 처리 경로는 변경하지 않음 — `extract_paragraphs`/`_ParagraphExtractor`/`BLOCKED_URL_PATTERNS`/`MAX_CONTENT_LENGTH`는 그대로 유지.
- RSS로의 폴백 없음 — Gateway 호출 실패 시 해당 쿼리만 스킵, 로그만 남기고 다음 크롤 사이클에서 재시도.
- 저장 마크다운/DynamoDB 아이템에는 항상 `url`(출처)과 `publishedDate` 포함 (Acceptable Use 준수).
- research-agent AgentCore Runtime 컨테이너(`ttobakResearchContainer`)의 실제 실행 역할은 CDK 밖 리소스: `arn:aws:iam::180294183052:role/ttobak-agentcore-research-role` (조사로 확인 — `aws bedrock-agentcore-control get-agent-runtime` 결과의 `roleArn`). `researchWorkerRole`과는 다른 역할이니 혼동 금지.

---

## File Structure

| 파일 | 변경 |
|---|---|
| `backend/python/crawler/news_crawler.py` | RSS 함수/상수 삭제, `_gateway_web_search()` 신규, `handler()`/`_process_article()`/`_write_to_s3()` 수정 |
| `backend/python/crawler/test_crawlers.py` | RSS 전용 테스트(`TestNewsCrawlerFetchRss`) 삭제, `_gateway_web_search` 테스트 신규 |
| `backend/python/research-agent/tools.py` | `web_search()` 내부를 Gateway 호출로 교체, Google News RSS 코드 삭제 |
| `infra/lib/web-search-gateway-stack.ts` | 신규 — `Gateway` L2 + `CfnGatewayTarget` L1(web-search connector) + 서비스 역할 정책 + output |
| `infra/lib/crawler-stack.ts` | `commonEnv`에 `WEB_SEARCH_GATEWAY_URL`/`WEB_SEARCH_GATEWAY_REGION` 추가 (props로 전달) |
| `infra/lib/ai-stack.ts` | `crawlerRole`에 `bedrock-agentcore:InvokeGateway` 정책 추가, research-agent 실행 역할을 `fromRoleArn`으로 import해 동일 정책 부여 |
| `infra/bin/infra.ts` | `WebSearchGatewayStack` 인스턴스화(`usEast1Env`, `crossRegionReferences: true`), `crawlerStack`/`aiStack`에 값 전달 + `crossRegionReferences: true` |
| `CLAUDE.md` | `WEB_SEARCH_GATEWAY_URL`/`_REGION` env var 문서화 (Auto-Sync Rules) |

---

## Task 1: CDK — `WebSearchGatewayStack` 신규 (Gateway + GatewayTarget)

**Files:**
- Create: `infra/lib/web-search-gateway-stack.ts`
- Modify: `infra/bin/infra.ts`

**Interfaces:**
- Produces: `WebSearchGatewayStack.gatewayId: string`, `WebSearchGatewayStack.gatewayUrl: string` (public readonly), `WebSearchGatewayStack.gateway: agentcore.Gateway` (public readonly, for `grantInvoke` in Task 2)

먼저 `aws-cdk-lib`를 connector 필드가 있는 버전으로 갱신한다.

- [ ] **Step 1: aws-cdk-lib 갱신 확인**

```bash
cd infra && npm install
node -e "console.log(require('aws-cdk-lib/package.json').version)"
```
Expected: `2.25x.x` 이상 출력 (2.241.0이 아님)

- [ ] **Step 2: `CfnGatewayTarget`에 `connector` 필드가 있는지 확인**

```bash
grep -n "connector" node_modules/aws-cdk-lib/aws-bedrockagentcore/lib/bedrockagentcore.generated.d.ts | head -5
```
Expected: `readonly connector?: CfnGatewayTarget.ConnectorTargetConfigurationProperty` 관련 라인이 출력됨. 출력이 없으면 Step 1의 npm install이 제대로 갱신되지 않은 것 — `rm -rf node_modules/aws-cdk-lib && npm install`로 재시도.

- [ ] **Step 3: `web-search-gateway-stack.ts` 작성**

```typescript
import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as agentcore from 'aws-cdk-lib/aws-bedrockagentcore';
import { Construct } from 'constructs';

export interface WebSearchGatewayStackProps extends cdk.StackProps {}

export class WebSearchGatewayStack extends cdk.Stack {
  public readonly gateway: agentcore.Gateway;
  public readonly gatewayId: string;
  public readonly gatewayUrl: string;

  constructor(scope: Construct, id: string, props: WebSearchGatewayStackProps) {
    super(scope, id, props);

    // Service role the Gateway assumes to call the Web Search connector.
    // Per AWS's Web Search Tool connector guide, the SERVICE role (not just
    // the caller) needs both InvokeGateway and InvokeWebSearch — unlike most
    // other connector types, where the service role only needs the backend
    // service's own actions (e.g. bedrock:Retrieve for Managed KB).
    const gatewayServiceRole = new iam.Role(this, 'WebSearchGatewayServiceRole', {
      roleName: 'ttobak-web-search-gateway-role',
      assumedBy: new iam.ServicePrincipal('bedrock-agentcore.amazonaws.com', {
        conditions: {
          StringEquals: { 'aws:SourceAccount': cdk.Aws.ACCOUNT_ID },
          ArnLike: { 'aws:SourceArn': `arn:aws:bedrock-agentcore:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:gateway/*` },
        },
      }),
      description: 'Service role assumed by the TTOBAK Web Search AgentCore Gateway',
    });
    gatewayServiceRole.addToPolicy(new iam.PolicyStatement({
      sid: 'InvokeGateway',
      effect: iam.Effect.ALLOW,
      actions: ['bedrock-agentcore:InvokeGateway'],
      resources: [`arn:aws:bedrock-agentcore:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:gateway/*`],
    }));
    gatewayServiceRole.addToPolicy(new iam.PolicyStatement({
      sid: 'InvokeWebSearch',
      effect: iam.Effect.ALLOW,
      actions: ['bedrock-agentcore:InvokeWebSearch'],
      resources: [`arn:aws:bedrock-agentcore:${cdk.Aws.REGION}:aws:tool/web-search.v1`],
    }));

    this.gateway = new agentcore.Gateway(this, 'WebSearchGateway', {
      gatewayName: 'ttobak-web-search-gateway',
      description: 'TTOBAK news crawler + research-agent web search (AWS Web Search connector)',
      authorizerConfiguration: agentcore.GatewayAuthorizer.usingAwsIam(),
      role: gatewayServiceRole,
    });

    // L2 GatewayTarget doesn't support connector targets yet — use the L1
    // CfnGatewayTarget directly, matching AWS's documented CLI/boto3 payload
    // shape for the Web Search Tool connector.
    new agentcore.CfnGatewayTarget(this, 'WebSearchTarget', {
      gatewayIdentifier: this.gateway.gatewayId,
      name: 'web-search-tool',
      targetConfiguration: {
        mcp: {
          connector: {
            source: { connectorId: 'web-search' },
            configurations: [
              { name: 'WebSearch', parameterValues: {} },
            ],
          },
        },
      },
      credentialProviderConfigurations: [
        { credentialProviderType: 'GATEWAY_IAM_ROLE' },
      ],
    });

    this.gatewayId = this.gateway.gatewayId;
    this.gatewayUrl = this.gateway.gatewayUrl!;

    new cdk.CfnOutput(this, 'GatewayId', { value: this.gatewayId, exportName: 'TtobakWebSearchGatewayId' });
    new cdk.CfnOutput(this, 'GatewayUrl', { value: this.gatewayUrl, exportName: 'TtobakWebSearchGatewayUrl' });
  }
}
```

- [ ] **Step 4: `infra/bin/infra.ts`에 스택 추가**

`infra/bin/infra.ts`의 import 목록에 추가:

```typescript
import { WebSearchGatewayStack } from '../lib/web-search-gateway-stack';
```

`const usEast1Env = {...}` 선언 바로 다음(EdgeAuthStack 앞)에 추가:

```typescript
// Stack: Web Search Gateway (us-east-1 only — AWS Web Search connector constraint)
const webSearchGatewayStack = new WebSearchGatewayStack(app, 'TtobakWebSearchGatewayStack', {
  env: usEast1Env,
  crossRegionReferences: true,
  description: 'TTOBAK AI Meeting Assistant - Web Search Gateway (AgentCore, us-east-1)',
});
```

- [ ] **Step 5: synth으로 문법/타입 검증**

```bash
cd infra && npx cdk synth TtobakWebSearchGatewayStack 2>&1 | tail -40
```
Expected: CloudFormation 템플릿이 출력됨 (AWS 자격증명 없어도 synth는 로컬 검증만 하므로 성공해야 함). 타입 에러나 미해결 import가 있으면 실패.

- [ ] **Step 6: Commit**

```bash
git add infra/lib/web-search-gateway-stack.ts infra/bin/infra.ts infra/package-lock.json
git commit -m "feat(infra): add WebSearchGatewayStack (AgentCore Gateway + Web Search connector)"
```

---

## Task 2: CDK — crawler/ai-stack에 Gateway URL/IAM 배선

**Files:**
- Modify: `infra/lib/crawler-stack.ts`
- Modify: `infra/lib/ai-stack.ts`
- Modify: `infra/bin/infra.ts`

**Interfaces:**
- Consumes: `WebSearchGatewayStack.gateway`(Task 1의 `agentcore.Gateway` 인스턴스, `.grantInvoke(role)` 호출용), `WebSearchGatewayStack.gatewayUrl`
- Produces: `commonEnv.WEB_SEARCH_GATEWAY_URL`/`WEB_SEARCH_GATEWAY_REGION` (Task 3의 Python 코드가 `os.environ`으로 읽음)

- [ ] **Step 1: `crawler-stack.ts`의 props에 Gateway URL 추가**

`CrawlerStackProps` 인터페이스에 추가:

```typescript
export interface CrawlerStackProps extends cdk.StackProps {
  crawlerRole: iam.IRole;
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  knowledgeBaseId?: string;
  dataSourceId?: string;
  webSearchGatewayUrl: string;
}
```

`commonEnv`에 추가:

```typescript
const commonEnv = {
  TABLE_NAME: props.table.tableName,
  KB_BUCKET_NAME: props.kbBucket.bucketName,
  KB_ID: props.knowledgeBaseId || '',
  DATA_SOURCE_ID: props.dataSourceId || '',
  SUMMARIZE_MODEL_ID: 'global.anthropic.claude-sonnet-5',
  WEB_SEARCH_GATEWAY_URL: props.webSearchGatewayUrl,
  WEB_SEARCH_GATEWAY_REGION: 'us-east-1',
};
```

(`orchestrator`/`ingestTrigger` Lambda는 이 env var가 필요 없으므로 `newsCrawler`/`techCrawler`에만 이미 쓰이는 `environment: commonEnv` 그대로 — 별도 수정 불필요)

- [ ] **Step 2: `ai-stack.ts`의 `crawlerRole`에 `InvokeGateway` 정책 추가**

`AiStackProps`에 추가:

```typescript
export interface AiStackProps extends cdk.StackProps {
  bucket: s3.IBucket;
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  agentCoreRuntimeArn?: string;
  userPoolArn: string;
  webSearchGatewayArn: string;
  researchAgentExecutionRoleArn: string;
}
```

`this.crawlerRole.addToPolicy(...)` 블록 뒤(431-436행 부근)에 추가:

```typescript
this.crawlerRole.addToPolicy(new iam.PolicyStatement({
  sid: 'InvokeWebSearchGateway',
  effect: iam.Effect.ALLOW,
  actions: ['bedrock-agentcore:InvokeGateway'],
  resources: [props.webSearchGatewayArn],
}));

// research-agent's actual caller is the AgentCore Runtime container
// (ttobakResearchContainer), not researchWorkerRole (that role only invokes
// the container itself via InvokeAgentRuntime) — import the container's
// real execution role and grant it the same Gateway invoke permission.
const researchAgentExecutionRole = iam.Role.fromRoleArn(
  this, 'ResearchAgentExecutionRole', props.researchAgentExecutionRoleArn
);
researchAgentExecutionRole.attachInlinePolicy(new iam.Policy(this, 'ResearchAgentGatewayInvoke', {
  statements: [new iam.PolicyStatement({
    sid: 'InvokeWebSearchGateway',
    effect: iam.Effect.ALLOW,
    actions: ['bedrock-agentcore:InvokeGateway'],
    resources: [props.webSearchGatewayArn],
  })],
}));
```

- [ ] **Step 3: `infra/bin/infra.ts`에서 값 전달 + 의존성 배선**

`aiStack` 생성 호출에 두 필드 추가(`agentCoreRuntimeArn` 다음 줄):

```typescript
webSearchGatewayArn: webSearchGatewayStack.gateway.gatewayArn,
researchAgentExecutionRoleArn: 'arn:aws:iam::180294183052:role/ttobak-agentcore-research-role',
```

`aiStack.addDependency(...)` 블록에 추가:

```typescript
aiStack.addDependency(webSearchGatewayStack);
```

(`aiStack`은 `env`(ap-northeast-2)이고 `webSearchGatewayStack`은 `usEast1Env`이므로 cross-region reference — `aiStack`을 생성하는 `new AiStack(...)` 호출에 `crossRegionReferences: true`를 추가해야 한다. `AiStackProps`가 `cdk.StackProps`를 extends하므로 프로퍼티 자체는 이미 지원됨.)

`aiStack` 생성 블록을:
```typescript
const aiStack = new AiStack(app, 'TtobakAiStack', {
  env,
  crossRegionReferences: true,
  description: 'TTOBAK AI Meeting Assistant - AI Services (IAM roles)',
  ...
```
로 수정.

`crawlerStack` 생성 호출에 추가:

```typescript
webSearchGatewayUrl: webSearchGatewayStack.gatewayUrl,
```

`crawlerStack` 생성 블록도 `crossRegionReferences: true` 추가하고 `crawlerStack.addDependency(webSearchGatewayStack);` 추가.

- [ ] **Step 4: synth으로 cross-region 배선 검증**

```bash
cd infra && npx cdk synth TtobakAiStack TtobakCrawlerStack TtobakWebSearchGatewayStack 2>&1 | tail -60
```
Expected: 세 스택 모두 에러 없이 템플릿 출력. cross-region reference 관련 에러(`crossRegionReferences`가 빠지면 명확한 에러 메시지가 뜸)가 없어야 함.

- [ ] **Step 5: Commit**

```bash
git add infra/lib/crawler-stack.ts infra/lib/ai-stack.ts infra/bin/infra.ts
git commit -m "feat(infra): wire crawlerRole and research-agent execution role to InvokeGateway"
```

---

## Task 3: `news_crawler.py` — `_gateway_web_search()` + RSS 제거

**Files:**
- Modify: `backend/python/crawler/news_crawler.py`
- Modify: `backend/python/crawler/test_crawlers.py`

**Interfaces:**
- Produces: `_gateway_web_search(query: str, max_results: int = 10) -> list[dict]` — 각 dict는 `{"text": str, "url": str, "title": str, "publishedDate": str}`. 빈 리스트는 검색 결과 없음 또는 에러를 의미.

먼저 실패하는 테스트를 작성한다(기존 RSS 테스트는 이번 태스크에서 함께 제거).

- [ ] **Step 1: 기존 RSS 전용 테스트 제거**

`test_crawlers.py`에서 `class TestNewsCrawlerFetchRss` 블록(라인 264-303 부근, `_parse_rss` 호출 테스트 3건)을 삭제한다. `TestNewsCrawlerDedupSkip`/`TestNewsCrawlerNewArticle`/`TestNewsCrawlerHTMLExtraction`은 `_process_article`/`extract_paragraphs`를 테스트하므로 그대로 유지.

- [ ] **Step 2: `_gateway_web_search`의 실패하는 테스트 작성**

`test_crawlers.py`의 `TestNewsCrawlerDedupSkip` 클래스 앞에 추가:

```python
class TestGatewayWebSearch(unittest.TestCase):
    """Test news_crawler._gateway_web_search parses the AgentCore Gateway MCP response."""

    @mock.patch('news_crawler._sigv4_post')
    def test_parses_successful_response(self, mock_post):
        mock_post.return_value = json.dumps({
            'content': [{
                'type': 'text',
                'text': json.dumps({
                    'id': 'abc123',
                    'results': [
                        {'text': 'AI 클라우드 투자 확대', 'url': 'https://example.com/1',
                         'title': 'Example Article', 'publishedDate': '2026-07-01'},
                    ],
                }),
            }],
            'isError': False,
        })

        results = news_crawler._gateway_web_search('우리은행 AI')

        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]['url'], 'https://example.com/1')
        self.assertEqual(results[0]['text'], 'AI 클라우드 투자 확대')
        self.assertEqual(results[0]['publishedDate'], '2026-07-01')

    @mock.patch('news_crawler._sigv4_post')
    def test_empty_results_on_no_matches(self, mock_post):
        mock_post.return_value = json.dumps({
            'content': [{'type': 'text', 'text': json.dumps({'id': 'x', 'results': []})}],
            'isError': False,
        })

        results = news_crawler._gateway_web_search('존재하지않는검색어유니크12345')
        self.assertEqual(results, [])

    @mock.patch('news_crawler._sigv4_post')
    def test_empty_results_on_gateway_error(self, mock_post):
        mock_post.return_value = json.dumps({
            'content': [{'type': 'text', 'text': 'internal error'}],
            'isError': True,
        })

        results = news_crawler._gateway_web_search('query')
        self.assertEqual(results, [])

    @mock.patch('news_crawler._sigv4_post')
    def test_empty_results_on_transport_exception(self, mock_post):
        mock_post.side_effect = Exception('connection timeout')

        results = news_crawler._gateway_web_search('query')
        self.assertEqual(results, [])

    @mock.patch('news_crawler._sigv4_post')
    def test_query_truncated_to_200_chars_and_max_results_passed(self, mock_post):
        mock_post.return_value = json.dumps({
            'content': [{'type': 'text', 'text': json.dumps({'id': 'x', 'results': []})}],
            'isError': False,
        })

        long_query = 'a' * 300
        news_crawler._gateway_web_search(long_query, max_results=3)

        sent_body = json.loads(mock_post.call_args[0][0])
        self.assertEqual(len(sent_body['params']['arguments']['query']), 200)
        self.assertEqual(sent_body['params']['arguments']['maxResults'], 3)
        self.assertEqual(sent_body['params']['name'], 'WebSearch')
        self.assertEqual(sent_body['method'], 'tools/call')
```

- [ ] **Step 3: 테스트 실행해서 실패 확인**

```bash
cd backend/python/crawler && python3 -m unittest test_crawlers.TestGatewayWebSearch -v
```
Expected: `AttributeError: module 'news_crawler' has no attribute '_gateway_web_search'` (또는 `_sigv4_post`) — FAIL

- [ ] **Step 4: `news_crawler.py`에서 RSS 관련 코드 삭제**

삭제할 것 (파일 내 정확한 위치, 위에서 읽은 라인 번호 기준):
- `import xml.etree.ElementTree as ET` (라인 17)
- `from urllib.parse import quote_plus, urlencode` → `quote_plus`는 더 이상 안 쓰므로 이 import 라인 전체 삭제 (라인 20)
- `GOOGLE_NEWS_RSS` (라인 38)
- `SITE_RSS_FEEDS` (라인 41-49)
- `MAX_ARTICLES_PER_QUERY`, `MAX_ARTICLES_PER_FEED` (라인 51-52)
- `MAX_RSS_SIZE`, `_parse_rss()` 함수 전체 (라인 140-188)
- `_search_google_news()` 함수 전체 (라인 191-199)
- `_is_google_news_redirect()` 함수 전체 (라인 202-203) — RSS 리다이렉트 스킵용, Gateway 결과엔 불필요
- `_fetch_site_rss()` 함수 전체 (라인 206-218)

유지: `extract_paragraphs`/`_ParagraphExtractor`, `_fetch_url`, `_strip_html_tags`, `BLOCKED_URL_PATTERNS`, `MAX_CONTENT_LENGTH`, `MIN_BODY_LENGTH`, `_generate_search_queries`, `KNOWN_OUTLET_NAMES` (모두 `customUrls` 경로 또는 검색어 생성에 계속 필요).

- [ ] **Step 5: `_gateway_web_search` + `_sigv4_post` 신규 함수 추가**

`_generate_search_queries` 함수 뒤(원래 `_search_google_news`가 있던 자리)에 추가:

```python
import botocore.session
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from urllib.request import Request as _HttpRequest, urlopen as _http_urlopen

WEB_SEARCH_GATEWAY_URL = os.environ.get('WEB_SEARCH_GATEWAY_URL', '')
WEB_SEARCH_GATEWAY_REGION = os.environ.get('WEB_SEARCH_GATEWAY_REGION', 'us-east-1')


def _sigv4_post(body_json: str) -> str:
    """POST body_json to the Gateway MCP endpoint, SigV4-signed. Returns the
    raw response body text. Raises on transport/HTTP failure."""
    session = botocore.session.get_session()
    credentials = session.get_credentials()
    request = AWSRequest(
        method='POST',
        url=WEB_SEARCH_GATEWAY_URL,
        data=body_json,
        headers={'Content-Type': 'application/json'},
    )
    SigV4Auth(credentials, 'bedrock-agentcore', WEB_SEARCH_GATEWAY_REGION).add_auth(request)
    prepared = request.prepare()
    req = _HttpRequest(prepared.url, data=prepared.body.encode('utf-8') if isinstance(prepared.body, str) else prepared.body,
                        headers=dict(prepared.headers), method='POST')
    with _http_urlopen(req, timeout=FETCH_TIMEOUT_SECONDS) as resp:
        return resp.read().decode('utf-8')


def _gateway_web_search(query: str, max_results: int = 10) -> list:
    """Search via the AgentCore Gateway Web Search connector. Returns
    [{"text", "url", "title", "publishedDate"}, ...], or [] on any failure
    (transport error, gateway isError, empty results) — callers must not
    fall back to RSS."""
    body = json.dumps({
        'jsonrpc': '2.0',
        'id': 1,
        'method': 'tools/call',
        'params': {
            'name': 'WebSearch',
            'arguments': {'query': query[:200], 'maxResults': max_results},
        },
    })
    try:
        raw_response = _sigv4_post(body)
        parsed = json.loads(raw_response)
        if parsed.get('isError'):
            logger.warning(f'Web search gateway returned isError for "{query}"')
            return []
        content = parsed.get('content', [])
        if not content:
            return []
        inner = json.loads(content[0]['text'])
        return inner.get('results', [])
    except Exception as e:
        logger.warning(f'Web search gateway call failed for "{query}": {e}')
        return []
```

- [ ] **Step 6: 테스트 재실행해서 통과 확인**

```bash
python3 -m unittest test_crawlers.TestGatewayWebSearch -v
```
Expected: 5 tests, OK

- [ ] **Step 7: Commit**

```bash
git add backend/python/crawler/news_crawler.py backend/python/crawler/test_crawlers.py
git commit -m "feat(crawler): add _gateway_web_search, remove RSS fetch/parse code"
```

---

## Task 4: `news_crawler.py` — `handler()`/`_process_article()`/`_write_to_s3()`를 Gateway 결과 기준으로 수정

**Files:**
- Modify: `backend/python/crawler/news_crawler.py`
- Modify: `backend/python/crawler/test_crawlers.py`

**Interfaces:**
- Consumes: `_gateway_web_search(query, max_results)` (Task 3에서 정의)
- Produces: `handler(event, context) -> {"docsAdded": int, "docsUpdated": int, "errors": [str]}` (시그니처 변경 없음, 내부 검색 소스만 변경)

- [ ] **Step 1: `_process_article`이 snippet을 받도록 수정하는 실패 테스트**

`test_crawlers.py`의 `TestNewsCrawlerNewArticle.test_new_article_writes_s3_and_dynamo`를 다음으로 교체(기존엔 `_fetch_url`을 모킹해 HTML을 줬지만, 이제 `_process_article`이 이미 뽑아진 snippet 텍스트를 직접 받도록 시그니처가 바뀜):

```python
    @mock.patch.object(news_crawler, '_write_metadata')
    @mock.patch.object(news_crawler, '_write_to_s3')
    @mock.patch.object(news_crawler, '_summarize_and_tag', return_value=('Article summary', ['AI']))
    @mock.patch.object(news_crawler, '_doc_exists', return_value=False)
    def test_new_article_writes_s3_and_dynamo(self, mock_exists, mock_summarize, mock_s3, mock_meta):
        """Verify S3 + DynamoDB writes for a new search-result snippet."""
        result = news_crawler._process_article(
            'tech-news', 'New AWS Article', 'https://example.com/new-article',
            'Mon, 14 Apr 2026 10:00:00 GMT', 'This is the search result snippet text.'
        )

        self.assertTrue(result)
        mock_summarize.assert_called_once()
        mock_s3.assert_called_once()
        mock_meta.assert_called_once()

        s3_call_args = mock_s3.call_args
        self.assertEqual(s3_call_args[0][0], 'tech-news')
        self.assertEqual(s3_call_args[0][2], 'New AWS Article')
```

그리고 `test_low_content_article_skipped`(이제 무의미 — snippet은 항상 유효하므로 길이 체크가 없음)를 삭제한다.

`TestNewsCrawlerDedupSkip.test_dedup_skip`도 새 시그니처에 맞게 수정(5번째 인자 `description` 추가):

```python
    @mock.patch.object(news_crawler, '_write_metadata')
    @mock.patch.object(news_crawler, '_write_to_s3')
    @mock.patch.object(news_crawler, '_doc_exists', return_value=True)
    def test_dedup_skip(self, mock_exists, mock_s3, mock_meta):
        """Mock existing doc, verify skip."""
        result = news_crawler._process_article(
            'tech-news', 'Old Article', 'https://example.com/old', '2026-04-14', 'snippet text'
        )

        self.assertFalse(result)
        mock_exists.assert_called_once()
        mock_s3.assert_not_called()
        mock_meta.assert_not_called()
```

- [ ] **Step 2: 테스트 실행해서 실패 확인**

```bash
python3 -m unittest test_crawlers.TestNewsCrawlerDedupSkip test_crawlers.TestNewsCrawlerNewArticle -v
```
Expected: FAIL — `_process_article() takes from 4 to 6 positional arguments but 5 were given` 또는 유사 시그니처 불일치

- [ ] **Step 3: `_process_article`/`_write_to_s3`/`handler` 수정**

`_process_article`을 다음으로 교체(기존 `_fetch_url`/`extract_paragraphs`/`MIN_BODY_LENGTH` 체크 제거, snippet을 파라미터로 직접 받음):

```python
def _process_article(source_id: str, title: str, url: str,
                     pub_date: str, snippet: str = '',
                     crawler_source_name: str = '') -> bool:
    if _is_blocked_url(url):
        logger.info(f'Skipping paywalled/premium URL: {url}')
        return False

    doc_hash = _make_hash(url)

    if _doc_exists(source_id, doc_hash):
        logger.debug(f'Skipping duplicate: {url}')
        return False

    summary, tags = _summarize_and_tag(title, snippet, crawler_source_name)
    source_name = _extract_source_name(title)
    _write_to_s3(source_id, doc_hash, title, url, snippet, summary, pub_date, tags)
    _write_metadata(source_id, doc_hash, title, url, pub_date, summary, source_name, tags)
    return True
```

`_write_to_s3`를 다음으로 교체 ("원문 발췌" 30,000자 블록 제거, snippet은 짧으므로 길이 제한 없이 그대로 포함, 라벨을 "검색 스니펫"으로 변경):

```python
def _write_to_s3(source_id: str, doc_hash: str, title: str, url: str,
                 snippet: str, summary: str, pub_date: str, tags: list) -> None:
    tag_line = f'**Tags:** {", ".join(tags)}\n' if tags else ''
    md = (
        f'# {title}\n\n'
        f'**Published:** {pub_date}\n'
        f'**Source:** {url}\n'
        f'{tag_line}\n'
        f'---\n\n'
        f'{summary}\n'
    )
    if snippet:
        md += f'\n---\n\n## 검색 스니펫\n\n{snippet}\n'
    key = f'shared/news/{source_id}/{doc_hash}.md'
    s3.put_object(
        Bucket=KB_BUCKET_NAME,
        Key=key,
        Body=md.encode('utf-8'),
        ContentType='text/markdown; charset=utf-8',
    )
    logger.info(f'Wrote s3://{KB_BUCKET_NAME}/{key}')
```

`handler()`를 다음으로 교체 (site RSS + Google News 루프를 `_gateway_web_search` 루프 하나로 통합, `customUrls` 처리는 그대로 유지):

```python
def handler(event, context):
    """Process news articles via AgentCore Gateway Web Search + custom URLs.

    Expected event:
      {
        "sourceId": "wooribank",
        "sourceName": "우리은행",
        "newsQueries": ["AI", "클라우드", "디지털전환"],
        "customUrls": [{"url": "https://...", "title": "..."}]
      }
    """
    source_id = event.get('sourceId', 'unknown')
    source_name = event.get('sourceName', '')
    keywords = event.get('newsQueries') or []
    custom_urls = event.get('customUrls') or []

    all_queries = _generate_search_queries(source_name, keywords)
    logger.info(f'News crawler: sourceId={source_id}, sourceName={source_name}, '
                f'keywords={keywords}, queries={all_queries}, customUrls={len(custom_urls)}')

    docs_added = 0
    docs_updated = 0
    errors = []
    seen_urls = set()

    def _try_process(title, url, pub_date='', snippet=''):
        nonlocal docs_added
        if url in seen_urls:
            return
        seen_urls.add(url)
        try:
            if _process_article(source_id, title, url, pub_date, snippet, source_name):
                docs_added += 1
        except Exception as e:
            error_msg = f'{url}: {e}'
            logger.error(f'Article error: {error_msg}', exc_info=True)
            errors.append(error_msg)

    # 1. AgentCore Gateway Web Search — one search per generated query
    for query in all_queries:
        results = _gateway_web_search(query)
        logger.info(f'Web search "{query}": {len(results)} result(s)')
        for r in results:
            _try_process(r.get('title', ''), r.get('url', ''),
                         r.get('publishedDate', ''), r.get('text', ''))

    # 2. Custom URLs (direct fetch — unchanged)
    for entry in custom_urls:
        url = entry.get('url', '')
        title = entry.get('title', url)
        if url:
            try:
                html = _fetch_url(url)
                text = extract_paragraphs(html)
            except Exception as e:
                logger.info(f'Could not fetch custom URL body: {e}')
                text = ''
            if not text or len(text) < MIN_BODY_LENGTH:
                logger.info(f'Skipping custom URL with insufficient body: {url}')
                continue
            _try_process(title, url, '', text)

    result = {
        'docsAdded': docs_added,
        'docsUpdated': docs_updated,
        'errors': errors,
    }
    logger.info(f'News crawler complete: {json.dumps(result)}')
    return result
```

- [ ] **Step 4: 테스트 재실행해서 통과 확인**

```bash
python3 -m unittest test_crawlers.TestNewsCrawlerDedupSkip test_crawlers.TestNewsCrawlerNewArticle test_crawlers.TestGatewayWebSearch test_crawlers.TestNewsCrawlerHTMLExtraction -v
```
Expected: OK (기존 `TestNewsCrawlerHTMLExtraction`은 `extract_paragraphs` 테스트라 영향 없음, 통과 유지되어야 함)

- [ ] **Step 5: Commit**

```bash
git add backend/python/crawler/news_crawler.py backend/python/crawler/test_crawlers.py
git commit -m "feat(crawler): route newsQueries through AgentCore Web Search instead of RSS"
```

---

## Task 5: `research-agent/tools.py` — `web_search()`를 같은 Gateway로 교체

**Files:**
- Modify: `backend/python/research-agent/tools.py`
- Create: `backend/python/research-agent/test_tools.py`

**Interfaces:**
- Consumes: 없음 (Task 3의 `_sigv4_post`/`_gateway_web_search` 로직을 이 파일에 독립 구현 — 별도 배포 아티팩트이므로 공유 모듈화하지 않음, 스펙 §5.2 확정 사항)
- Produces: `web_search(query: str, max_results: int = 10) -> str` (변경 없음 — `@tool` 데코레이터 시그니처 유지, 내부 구현만 교체)

- [ ] **Step 1: 실패하는 테스트 작성**

```python
"""Unit tests for research-agent tools.py — web_search only (fetch_page/save_report unchanged)."""

import json
import os
import unittest
from unittest import mock

os.environ['TABLE_NAME'] = 'test-table'
os.environ['KB_BUCKET_NAME'] = 'test-bucket'
os.environ['WEB_SEARCH_GATEWAY_URL'] = 'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp'
os.environ['WEB_SEARCH_GATEWAY_REGION'] = 'us-east-1'

import tools


class TestWebSearch(unittest.TestCase):
    @mock.patch('tools._sigv4_post')
    def test_returns_results_from_gateway(self, mock_post):
        mock_post.return_value = json.dumps({
            'content': [{'type': 'text', 'text': json.dumps({
                'id': 'x',
                'results': [{'text': 'snippet', 'url': 'https://example.com', 'title': 'T', 'publishedDate': '2026-07-01'}],
            })}],
            'isError': False,
        })

        raw = tools.web_search('AWS Bedrock')
        parsed = json.loads(raw)

        self.assertEqual(len(parsed['results']), 1)
        self.assertEqual(parsed['results'][0]['url'], 'https://example.com')

    @mock.patch('tools._sigv4_post')
    def test_returns_empty_results_on_error(self, mock_post):
        mock_post.side_effect = Exception('timeout')

        raw = tools.web_search('query')
        parsed = json.loads(raw)

        self.assertEqual(parsed['results'], [])
        self.assertIn('error', parsed)


if __name__ == '__main__':
    unittest.main()
```

- [ ] **Step 2: 테스트 실행해서 실패 확인**

```bash
cd backend/python/research-agent && python3 -m unittest test_tools -v
```
Expected: `AttributeError: module 'tools' has no attribute '_sigv4_post'` — FAIL

- [ ] **Step 3: `tools.py`의 `web_search` 교체**

`tools.py` 상단 import 블록을:
```python
import json
import os
import logging
import hashlib
from datetime import datetime
from html.parser import HTMLParser
from urllib.request import Request, urlopen
from urllib.error import URLError

import botocore.session
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest

from strands.tools import tool
```
로 교체 (`xml.etree.ElementTree`와 `urllib.parse.quote_plus`는 더 이상 필요 없으므로 제거).

`TABLE_NAME`/`KB_BUCKET` 선언 다음에 추가:
```python
WEB_SEARCH_GATEWAY_URL = os.environ.get("WEB_SEARCH_GATEWAY_URL", "")
WEB_SEARCH_GATEWAY_REGION = os.environ.get("WEB_SEARCH_GATEWAY_REGION", "us-east-1")
FETCH_TIMEOUT_SECONDS = 10
```

기존 `web_search()` 함수(Google News RSS 버전)를 다음으로 전체 교체:

```python
def _sigv4_post(body_json: str) -> str:
    """POST body_json to the Gateway MCP endpoint, SigV4-signed."""
    session = botocore.session.get_session()
    credentials = session.get_credentials()
    request = AWSRequest(
        method="POST",
        url=WEB_SEARCH_GATEWAY_URL,
        data=body_json,
        headers={"Content-Type": "application/json"},
    )
    SigV4Auth(credentials, "bedrock-agentcore", WEB_SEARCH_GATEWAY_REGION).add_auth(request)
    prepared = request.prepare()
    req = Request(
        prepared.url,
        data=prepared.body.encode("utf-8") if isinstance(prepared.body, str) else prepared.body,
        headers=dict(prepared.headers),
        method="POST",
    )
    with urlopen(req, timeout=FETCH_TIMEOUT_SECONDS) as resp:
        return resp.read().decode("utf-8")


@tool
def web_search(query: str, max_results: int = 10) -> str:
    """Search the web via the AgentCore Web Search connector. Returns article
    snippets, titles, and URLs.

    Args:
        query: Search query (Korean or English)
        max_results: Maximum number of results to return (default 10)
    """
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "WebSearch",
            "arguments": {"query": query[:200], "maxResults": max_results},
        },
    })
    try:
        raw_response = _sigv4_post(body)
        parsed = json.loads(raw_response)
        if parsed.get("isError"):
            return json.dumps({"results": [], "message": "Search returned an error"})
        content = parsed.get("content", [])
        if not content:
            return json.dumps({"results": [], "message": "No results found"})
        inner = json.loads(content[0]["text"])
        return json.dumps({"results": inner.get("results", [])}, ensure_ascii=False)
    except Exception as e:
        logger.warning(f"Web search failed for '{query}': {e}")
        return json.dumps({"results": [], "error": str(e)})
```

- [ ] **Step 4: 테스트 재실행해서 통과 확인**

```bash
python3 -m unittest test_tools -v
```
Expected: OK, 2 tests

- [ ] **Step 5: Commit**

```bash
git add backend/python/research-agent/tools.py backend/python/research-agent/test_tools.py
git commit -m "feat(research-agent): route web_search through AgentCore Web Search connector"
```

---

## Task 6: 문서/env — CLAUDE.md 동기화 + 배포 검증

**Files:**
- Modify: `CLAUDE.md`

**Interfaces:** 없음 (문서만)

- [ ] **Step 1: `CLAUDE.md`의 "Lambda Environment Variables" 절에 추가**

기존 라인:
```
CDK injects env vars per Lambda — see CDK stacks for full list. Common: `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID`. The `api` Lambda also gets `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID`, `KB_BUCKET_NAME`, `KMS_KEY_ID`, `FRONTEND_BASE_URL` (...).
```
다음 줄로 추가:
```
The `transcribe`/news crawler Lambda (`ttobak-crawler-news`) also gets `WEB_SEARCH_GATEWAY_URL`/`WEB_SEARCH_GATEWAY_REGION` (AgentCore Gateway Web Search connector endpoint, us-east-1 — see ADR for cross-region Gateway invoke pattern). The research-agent AgentCore Runtime container (deployed outside CDK) needs the same two env vars injected via its own deploy pipeline, not `infra/lib/crawler-stack.ts`'s `commonEnv`.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: document WEB_SEARCH_GATEWAY_URL/_REGION env vars"
```

---

## Task 7: 배포 및 실사용 검증 (수동, 코드 변경 없음)

**Files:** 없음 — 배포/검증 절차만

- [ ] **Step 1: 전체 synth**

```bash
cd infra && npx cdk synth --all 2>&1 | tail -20
```
Expected: 모든 스택 정상 synth

- [ ] **Step 2: 배포 (사용자 승인 필요 — 실제 AWS 리소스 생성)**

```bash
npx cdk deploy TtobakWebSearchGatewayStack TtobakAiStack TtobakCrawlerStack --require-approval never
```

- [ ] **Step 3: `news_crawler.py`/`tools.py` Lambda/컨테이너 재배포**

기존 배포 절차(CLAUDE.md Build Commands 참조)대로 크롤러 Lambda 코드를 갱신. research-agent 컨테이너는 CI/CD(README에 명시된 self-hosted runner 빌드) 경로로 재배포.

- [ ] **Step 4: research-agent 실행 역할에 Gateway invoke 권한이 실제 반영됐는지 확인**

```bash
aws iam get-role-policy --role-name ttobak-agentcore-research-role --policy-name ResearchAgentGatewayInvoke 2>&1
```
Expected: `bedrock-agentcore:InvokeGateway` 액션이 포함된 정책 문서 출력 (Task 2에서 CDK가 attach한 인라인 정책)

- [ ] **Step 5: 크롤러 1개 소스 수동 트리거**

```bash
aws stepfunctions start-execution \
  --state-machine-arn arn:aws:states:ap-northeast-2:180294183052:stateMachine:ttobak-crawler-workflow \
  --input '{}'
```
Step Functions 콘솔에서 실행 상태 확인.

- [ ] **Step 6: 결과 검증**

```bash
aws s3 ls s3://ttobak-kb-180294183052/shared/news/ --recursive | tail -10
```
Expected: 최근 타임스탬프의 신규 `.md` 파일 존재. 파일 내용에 "검색 스니펫" 섹션과 `**Source:**` URL이 있는지 확인.
