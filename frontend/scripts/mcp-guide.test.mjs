import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/mcpGuide.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
});
const origin = 'https://ttobak.example.com';
const spa = 'firstpartyclient123';
const read = 'dedicatedread123';
const write = 'dedicatedwrite123';
const baseScopes = 'openid email profile';
const readScopes = `${baseScopes} ttobak.connector/read`;
const privateMcp = { url: '/api/mcp', privateBridgeEnabled: true, readClientId: read, writeClientId: write };

async function load(mcp) {
  const exports = {};
  const issuer = 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_test';
  const config = { cognito: {
    region: 'ap-northeast-2', userPoolId: 'ap-northeast-2_test',
    userPoolClientId: spa, identityPoolId: 'unused',
  }, mcp };
  vm.runInNewContext(outputText, {
    exports, URL, TextDecoder, Uint8Array, Error,
    require: () => ({ getRuntimeConfig: async () => config }),
    fetch: async () => new Response(JSON.stringify({
      issuer, authorization_endpoint: 'https://login.example.com/oauth2/authorize',
    })),
  });
  return { ...exports, config: await exports.loadMcpGuideConfig(origin, new AbortController().signal) };
}

const codeText = guide => guide.steps.flatMap(step => step.code ?? []).map(code => code.text).join('\n');

test('private HTTP uses the dedicated read profile and resource-bound scopes', async () => {
  const { config, mcpClientGuide } = await load(privateMcp);
  assert.equal(config.httpUrl, `${origin}/api/mcp`);
  assert.equal(config.httpClientId, read);
  assert.equal(config.httpScopes.join(' '), readScopes);
  assert.equal(config.privateBridgeEnabled, true);
  for (const client of ['claude', 'codex', 'kirocrew', 'quick', 'kiro-cli']) {
    const guide = mcpClientGuide(client, 'http', config);
    const text = codeText(guide);
    assert.doesNotMatch(text, new RegExp(`${spa}|${write}|ttobak.connector/write`), client);
    assert.match(JSON.stringify(guide), /ttobak\.connector\/read/, client);
    assert.match(JSON.stringify(guide), new RegExp(read), client);
  }
  const codex = codeText(mcpClientGuide('codex', 'http', config));
  assert.match(codex, /--url 'https:\/\/ttobak\.example\.com\/api\/mcp'/);
  assert.match(codex, /--oauth-client-id 'dedicatedread123'/);
  assert.doesNotMatch(codex, /--oauth-resource|oauth_resource/);
  assert.match(codex, /mcp login ttobak --scopes 'openid,email,profile,ttobak\.connector\/read'/);
  const kiro = JSON.parse(mcpClientGuide('kiro-cli', 'http', config).steps[0].code[0].text);
  assert.equal(kiro.mcpServers.ttobak.oauth.clientId, read);
  assert.equal(kiro.mcpServers.ttobak.oauth.oauthScopes.join(' '), readScopes);
});

test('stdio keeps the first-party client in private and rollback modes', async () => {
  for (const privateBridgeEnabled of [true, false]) {
    const { config, mcpClientGuide } = await load({ ...privateMcp, privateBridgeEnabled });
    for (const client of ['claude', 'codex', 'kirocrew', 'kiro-cli']) {
      const text = codeText(mcpClientGuide(client, 'stdio', config));
      assert.match(text, new RegExp(spa));
      assert.doesNotMatch(text, new RegExp(`${read}|${write}|ttobak.connector`));
    }
  }
});

test('explicit flag-off HTTP restores first-party client and base scopes', async () => {
  const { config, mcpClientGuide } = await load({ url: '/api/mcp', privateBridgeEnabled: false });
  assert.equal(config.httpClientId, spa);
  assert.equal(config.httpScopes.join(' '), baseScopes);
  assert.equal(config.httpUrl, `${origin}/api/mcp`);
  assert.match(codeText(mcpClientGuide('quick', 'http', config)), new RegExp(spa));
  assert.doesNotMatch(codeText(mcpClientGuide('quick', 'http', config)), /ttobak.connector/);
  const codex = codeText(mcpClientGuide('codex', 'http', config));
  assert.match(codex, /--url 'https:\/\/ttobak\.example\.com\/api\/mcp'/);
  assert.match(codex, /--oauth-client-id 'firstpartyclient123'/);
  assert.match(codex, /mcp login ttobak --scopes 'openid,email,profile'/);
  assert.doesNotMatch(codex, /--oauth-resource|oauth_resource|ttobak.connector/);
});

test('missing mode, invalid dedicated binding and foreign URLs disable all HTTP steps', async () => {
  for (const mcp of [
    undefined, { url: '/api/mcp' }, { ...privateMcp, privateBridgeEnabled: 'true' },
    { ...privateMcp, readClientId: undefined }, { ...privateMcp, writeClientId: undefined },
    { ...privateMcp, readClientId: spa }, { ...privateMcp, writeClientId: spa },
    { ...privateMcp, writeClientId: read }, { ...privateMcp, readClientId: 'bad id' },
    { ...privateMcp, url: 'https://foreign.example.com/api/mcp' },
    { ...privateMcp, url: '/api/mcp?token=secret' },
  ]) {
    const { config, mcpClientGuide } = await load(mcp);
    assert.equal(config.httpUrl, null, JSON.stringify(mcp));
    for (const client of ['claude', 'codex', 'kirocrew', 'quick', 'kiro-cli']) {
      assert.equal(mcpClientGuide(client, 'http', config).steps.length, 0, client);
      assert.equal(mcpClientGuide(client, 'http', null).steps.length, 0, client);
    }
    assert.match(codeText(mcpClientGuide('claude', 'stdio', config)), new RegExp(spa));
  }
});
