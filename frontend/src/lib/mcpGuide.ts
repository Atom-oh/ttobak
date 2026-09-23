import { getRuntimeConfig } from './runtimeConfig';

export type McpClient = 'claude' | 'codex' | 'kirocrew' | 'quick' | 'kiro-cli';
export type McpTransport = 'stdio' | 'http';

export const MCP_CLIENTS = [
  { id: 'claude', name: 'Claude Code', local: true, description: 'Claude Code CLI', docs: 'https://code.claude.com/docs/en/mcp' },
  { id: 'codex', name: 'Codex', local: true, description: 'Codex CLI', docs: 'https://learn.chatgpt.com/docs/extend/mcp?surface=cli' },
  { id: 'kirocrew', name: 'Kiro Crew', local: true, description: 'Crew 대시보드', docs: 'https://kiro.dev/docs/crew/capabilities/mcp-tools.md' },
  { id: 'quick', name: 'Amazon Quick', local: false, description: 'Quick 커넥터', docs: 'https://docs.aws.amazon.com/quick/latest/userguide/mcp-integration.html' },
  { id: 'kiro-cli', name: 'Kiro CLI', local: true, description: 'Kiro 터미널', docs: 'https://kiro.dev/docs/mcp/configuration.md' },
] as const;

export interface McpGuideConfig {
  apiUrl: string;
  cognitoDomain: string;
  clientId: string;
  httpUrl: string | null;
}

export interface GuideCode {
  title: string;
  text: string;
  needsConfig?: boolean;
  needsHttp?: boolean;
}

export interface GuideStep {
  title: string;
  description: string;
  code?: GuideCode[];
}

export interface ClientGuide {
  introduction: string;
  steps: GuideStep[];
  note?: string;
}

export function configuredMcpUrl(value: unknown, origin: string): string | null {
  if (typeof value !== 'string' || !value.trim()) return null;
  try {
    const url = new URL(value, origin);
    return url.protocol === 'https:' && url.origin === origin && url.pathname === '/api/mcp' &&
      !url.username && !url.password && !url.search && !url.hash ? url.href : null;
  } catch { return null; }
}

// These are public deployment identifiers. Never read/copy browser auth tokens.
export async function loadMcpGuideConfig(origin: string, signal: AbortSignal): Promise<McpGuideConfig> {
  const site = new URL(origin);
  if (site.protocol !== 'https:') throw new Error('HTTPS로 접속한 TTOBAK 사이트에서 연결 설정을 불러와 주세요.');
  const config = await getRuntimeConfig();
  const { region, userPoolId, userPoolClientId } = config.cognito;
  if (typeof region !== 'string' || typeof userPoolId !== 'string' || typeof userPoolClientId !== 'string' ||
      !/^[a-z]{2}(?:-[a-z]+)+-\d+$/.test(region) ||
      !userPoolId.startsWith(region + '_') || !/^[a-zA-Z0-9_-]{1,128}$/.test(userPoolId) ||
      !/^[a-zA-Z0-9]{1,128}$/.test(userPoolClientId)) {
    throw new Error('사이트의 로그인 설정을 불러오지 못했습니다. 새로고침 후 다시 확인해 주세요.');
  }
  const issuer = `https://cognito-idp.${region}.amazonaws.com/${userPoolId}`;
  const response = await fetch(`${issuer}/.well-known/openid-configuration`, {
    signal, credentials: 'omit', cache: 'force-cache',
  });
  if (!response.ok || !response.body) throw new Error('로그인 주소를 불러오지 못했습니다. 잠시 후 다시 시도해 주세요.');
  const reader = response.body.getReader();
  const parts: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const part = await reader.read();
      if (part.done) break;
      size += part.value.length;
      if (size > 32_768) {
        await reader.cancel();
        throw new Error('로그인 설정 응답이 올바르지 않습니다.');
      }
      parts.push(part.value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) { bytes.set(part, offset); offset += part.length; }
  const metadata = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
  if (!metadata || metadata.issuer !== issuer || typeof metadata.authorization_endpoint !== 'string') {
    throw new Error('사이트의 로그인 주소를 확인할 수 없습니다.');
  }
  const authorization = new URL(metadata.authorization_endpoint);
  if (authorization.protocol !== 'https:' || authorization.username || authorization.password ||
      authorization.pathname !== '/oauth2/authorize' || authorization.search || authorization.hash) {
    throw new Error('사이트의 로그인 주소가 올바르지 않습니다.');
  }
  return {
    apiUrl: site.origin, cognitoDomain: authorization.origin, clientId: userPoolClientId,
    httpUrl: configuredMcpUrl(config.mcp?.url, site.origin),
  };
}

const quote = (value: string) => `'${value.replace(/'/g, "'\\''")}'`;
const LOGIN = 'TTOBAK에 로그인해줘';
const CHECK = 'TTOBAK에서 내 최근 회의 목록을 보여줘';
const absolutePath = 'printf \'%s\\n\' "$HOME/.ttobak/server.mjs"';

export function mcpClientGuide(client: McpClient, transport: McpTransport, config: McpGuideConfig | null): ClientGuide {
  const apiUrl = config?.apiUrl ?? 'YOUR_TTOBAK_SITE_URL';
  const cognitoDomain = config?.cognitoDomain ?? 'YOUR_COGNITO_DOMAIN';
  const clientId = config?.clientId ?? 'YOUR_CLIENT_ID';
  const httpUrl = config?.httpUrl ?? 'YOUR_HTTP_MCP_URL';
  const env = { TTOBAK_COGNITO_DOMAIN: cognitoDomain, TTOBAK_CLIENT_ID: clientId, TTOBAK_API_URL: apiUrl };
  const block = (title: string, text: string, needsHttp = false): GuideCode => ({
    title, text, needsConfig: true, needsHttp,
  });
  const localJson = JSON.stringify({ mcpServers: { ttobak: {
    command: 'node', args: ['ABSOLUTE_PATH_TO_SERVER_MJS'], env,
  } } }, null, 2);
  const remoteJson = JSON.stringify({ mcpServers: { ttobak: {
    url: httpUrl,
    oauth: { clientId, redirectUri: 'http://localhost:9876/callback', oauthScopes: ['openid', 'email', 'profile'] },
  } } }, null, 2);
  const download: GuideStep = {
    title: '서버 파일 다운로드',
    description: 'Node.js 18+가 설치된 컴퓨터의 macOS·Linux 터미널에서 실행하세요. Windows에서는 WSL 터미널을 사용하세요. 별도 npm 설치는 필요하지 않습니다.',
    code: [block('다운로드 명령', `mkdir -p "$HOME/.ttobak" && curl --fail --location --show-error \\\n  --output "$HOME/.ttobak/server.mjs" ${quote(apiUrl + '/mcp/ttobak-mcp.mjs')}`)],
  };
  const login: GuideStep = {
    title: 'TTOBAK 로그인',
    description: '클라이언트를 다시 열고 아래와 같이 요청하세요. 서버가 실행되는 컴퓨터에서 열린 브라우저로 로그인합니다. 만료되거나 로그인 갱신에 실패하면 다시 로그인하세요.',
    code: [{ title: '대화에 입력', text: LOGIN }],
  };
  const check: GuideStep = {
    title: '연결 확인',
    description: '/mcp에서 서버 상태를 확인한 뒤 실제 회의 조회를 요청하세요.',
    code: [{ title: '상태 확인', text: '/mcp' }, { title: '조회 예시', text: CHECK }],
  };

  if (transport === 'stdio') {
    if (client === 'quick') return { introduction: 'Amazon Quick은 원격 HTTP MCP가 필요합니다.', steps: [] };
    if (client === 'claude') return {
      introduction: 'Claude Code가 로컬 MCP 서버를 실행합니다. 기존 등록이 있다면 해당 연결 정보만 수정하세요.',
      steps: [download, {
        title: 'Claude Code에 등록',
        description: '아래 명령은 사용자 범위에 ttobak을 등록합니다. 모델이나 도구 승인 설정은 변경하지 않습니다.',
        code: [block('등록 명령', `claude mcp add ttobak --transport stdio --scope user \\\n${Object.entries(env).map(([key, value]) => `  -e ${quote(`${key}=${value}`)} \\\n`).join('')}  -- node "$HOME/.ttobak/server.mjs"`)],
      }, login, check],
      note: 'Claude Code의 Tool Search는 stdio에서도 동작합니다. HTTP로 바꾸는 것과 도구 지연 로딩은 별개입니다.',
    };
    if (client === 'codex') return {
      introduction: 'Codex CLI에서 로컬 MCP 서버를 등록하는 방법입니다.',
      steps: [download, {
        title: 'Codex에 등록',
        description: '기존 ttobak 등록이 있다면 연결 정보를 확인하고 수정하세요.',
        code: [block('등록 명령', `codex mcp add ttobak \\\n${Object.entries(env).map(([key, value]) => `  --env ${quote(`${key}=${value}`)} \\\n`).join('')}  -- node "$HOME/.ttobak/server.mjs"`)],
      }, login, check],
      note: '도구 검색·지연 로딩은 Codex의 지원 기능과 설정을 따릅니다.',
    };
    if (client === 'kirocrew') return {
      introduction: 'Crew가 실행되는 컴퓨터에 서버 파일이 있어야 합니다. 원격 호스트에서 브라우저 로그인이 어렵다면 HTTP 연결을 사용하세요.',
      steps: [download, {
        title: 'Crew에 로컬 서버 추가',
        description: 'Agent Capabilities → Integrations (MCP)에서 명령 기반 서버를 추가하거나, Crew가 읽는 기존 MCP 설정에 아래 ttobak 항목을 병합하세요. 경로 자리표시는 실제 절대경로로 바꾸세요.',
        code: [{ title: '절대경로 확인', text: absolutePath }, block('MCP 설정 예시', localJson)],
      }, {
        title: '검색·연결 및 로그인 확인',
        description: 'Discover & Sync로 설정을 반영하고 Probe All로 서버 연결을 확인하세요. 이후 Crew 대화에서 TTOBAK 로그인을 요청하고 실제 회의를 조회하세요.',
        code: [{ title: '로그인 요청', text: LOGIN }, { title: '조회 예시', text: CHECK }],
      }],
      note: '개인용 Crew에서 본인 계정으로 연결하고 필요한 에이전트에만 도구를 지정하세요. 공용 Crew는 사용자별 자격증명 분리가 필요합니다. Tool Search 설정에서 도구 지연 로딩을 조절할 수 있습니다.',
    };
    return {
      introduction: 'Kiro CLI가 실행되는 컴퓨터에 로컬 MCP 서버를 등록합니다.',
      steps: [download, {
        title: 'Kiro CLI 설정에 추가',
        description: '~/.kiro/settings/mcp.json의 mcpServers에 ttobak 항목을 병합하세요. 기존 서버와 승인 설정을 유지하고, 경로 자리표시는 실제 절대경로로 바꾸세요.',
        code: [{ title: '절대경로 확인', text: absolutePath }, block('Kiro MCP 설정', localJson)],
      }, login, check],
      note: 'Kiro CLI의 Tool Search는 별도로 켜야 하며, 설정된 도구 크기 기준을 넘으면 동작합니다.',
    };
  }

  if (client === 'claude') return {
    introduction: '등록된 HTTP 주소와 사용자별 OAuth 로그인으로 연결합니다. 로컬 서버 파일은 필요하지 않습니다.',
    steps: [{
      title: 'HTTP 서버 등록',
      description: '공개 OAuth 클라이언트 기준입니다. 로그인 콜백 http://localhost:9876/callback이 등록되어 있어야 합니다.',
      code: [block('등록 명령', `claude mcp add --transport http --scope user \\\n  --client-id ${quote(clientId)} --callback-port 9876 \\\n  ttobak ${quote(httpUrl)}`, true)],
    }, {
      title: 'OAuth 로그인 및 확인',
      description: 'Claude Code의 /mcp에서 ttobak 로그인을 진행한 뒤 회의를 조회하세요. HTTP에서는 ttobak_login 도구 대신 클라이언트가 로그인을 처리합니다.',
      code: [{ title: '연결·로그인', text: '/mcp' }, { title: '조회 예시', text: CHECK }],
    }],
  };
  if (client === 'codex') return {
    introduction: 'Codex CLI의 HTTP MCP 등록과 OAuth 로그인 방법입니다.',
    steps: [{
      title: 'HTTP 서버 등록',
      description: '명령이 표시하는 정확한 콜백 URL을 관리자에게 전달해 등록하세요. 콜백의 경로와 포트는 다른 클라이언트와 다를 수 있습니다.',
      code: [block('등록 명령', `codex mcp add ttobak --url ${quote(httpUrl)} \\\n  --oauth-client-id ${quote(clientId)}`, true)],
    }, {
      title: '로그인',
      description: '콜백 등록 후 아래 명령으로 브라우저 로그인을 완료하세요.',
      code: [{ title: 'OAuth 로그인', text: 'codex mcp login ttobak', needsHttp: true }],
    }, check],
  };
  if (client === 'kirocrew') return {
    introduction: 'Crew 대시보드에서 HTTP 주소와 요청 헤더로 연결합니다. 개인용 연결에서 본인의 자격증명을 사용하세요.',
    steps: [{
      title: '원격 서버 추가',
      description: 'Agent Capabilities → Integrations (MCP)에서 URL 기반 서버를 추가하세요.',
      code: [block('서버 URL', httpUrl, true)],
    }, {
      title: '사용자 인증 헤더 설정',
      description: '관리자가 안내한 사용자별 MCP OAuth 발급 절차를 먼저 완료하세요. 서버를 추가할 때 Authorization 헤더에 이 MCP 주소용 Cognito access token을 넣습니다. 웹 로그인 ID token은 사용할 수 없으며, 이 화면에서 토큰을 자동 발급하지는 않습니다.',
      code: [{ title: '헤더 형식 — 자리표시를 실제 값으로 교체', text: 'Authorization: Bearer YOUR_RESOURCE_BOUND_ACCESS_TOKEN' }],
    }, {
      title: '연결 확인',
      description: 'Probe All로 확인한 뒤 사용할 에이전트에 도구를 연결하세요. 토큰이 만료되면 헤더를 갱신해야 합니다. Crew의 자동 OAuth 로그인·갱신을 가정하지 않습니다.',
      code: [{ title: '조회 예시', text: CHECK }],
    }],
    note: 'MCP 토큰 발급 절차가 준비되지 않았다면 stdio를 사용하세요. 공용 Crew에는 개인 토큰을 공유하지 않고 사용자별 자격증명이 분리된 연결을 사용해야 합니다.',
  };
  if (client === 'quick') return {
    introduction: 'Amazon Quick은 HTTP 전용입니다. Quick Enterprise의 MCP 커넥터에서 사용자별 OAuth로 연결하세요.',
    steps: [{
      title: 'MCP 커넥터 만들기',
      description: 'Connectors → Create for your team → Model Context Protocol (MCP)에서 새 연결을 만들고 서버 URL을 입력하세요.',
      code: [block('MCP 서버 URL', httpUrl, true)],
    }, {
      title: '사용자별 OAuth 설정',
      description: 'User authentication (OAuth)를 선택하세요. 아래 Client ID는 공개 클라이언트이므로 Public OAuth client를 선택하고 비밀키를 입력하지 않습니다. 자동 발견이 실패하면 아래 주소를 직접 입력하세요.',
      code: [
        block('Client ID', clientId),
        block('Authorization URL', cognitoDomain + '/oauth2/authorize'),
        block('Token URL', cognitoDomain + '/oauth2/token'),
        { title: 'Scopes', text: 'openid email profile' },
      ],
    }, {
      title: '콜백 등록 후 로그인',
      description: 'Quick 화면의 Redirect URL을 그대로 관리자에게 전달해 Cognito 허용 콜백에 등록하세요. 등록이 끝나면 본인 계정으로 로그인하고 연결을 완료하세요.',
    }, {
      title: '도구 동기화 및 확인',
      description: '커넥터의 Sync로 도구 목록을 반영하고 실제 회의를 조회하세요. 도구 구성이 바뀔 때 다시 Sync해야 합니다. Quick의 작업 제한 시간은 60초입니다.',
      code: [{ title: '조회 예시', text: CHECK }],
    }],
    note: 'Quick에는 stdio와 사용자 지정 인증 헤더를 사용하지 않습니다. 사용자별 OAuth 인증을 선택하세요.',
  };
  return {
    introduction: 'Kiro CLI의 HTTP MCP 설정입니다. 사용자별 OAuth 로그인과 갱신은 Kiro가 처리합니다.',
    steps: [{
      title: 'HTTP 서버 설정',
      description: '~/.kiro/settings/mcp.json에 ttobak 항목을 병합하세요. http://localhost:9876/callback이 Cognito에 등록되어 있어야 합니다. 기존 승인 설정은 유지하세요.',
      code: [block('Kiro HTTP MCP 설정', remoteJson, true)],
    }, {
      title: '로그인 및 연결 확인',
      description: 'Kiro CLI에서 /mcp를 열어 ttobak 인증을 진행하세요. 재인증이 필요하면 /mcp auth를 사용한 뒤 회의를 조회하세요.',
      code: [{ title: '연결·인증', text: '/mcp' }, { title: '재인증', text: '/mcp auth' }, { title: '조회 예시', text: CHECK }],
    }],
    note: 'Tool Search는 연결 방식과 별개이며 Kiro CLI 설정에서 활성화합니다.',
  };
}
