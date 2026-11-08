import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';

const source = readFileSync(new URL('../src/lib/mcpGuide.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
});
const origin = 'https://ttobak.example.com';
const spa = 'firstpartyclient123';
const read = 'dedicatedread123';
const write = 'dedicatedwrite123';
const baseScopes = 'openid email profile';
const readScopes = `${baseScopes} ${origin}/api/mcp/read`;
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
    assert.doesNotMatch(text, new RegExp(`${spa}|${write}|ttobak.connector`), client);
    assert.ok(!text.includes(`${origin}/api/mcp/write`), client);
    assert.ok(JSON.stringify(guide).includes(`${origin}/api/mcp/read`), client);
    assert.match(JSON.stringify(guide), new RegExp(read), client);
  }
  const codex = codeText(mcpClientGuide('codex', 'http', config));
  assert.match(codex, /--url 'https:\/\/ttobak\.example\.com\/api\/mcp'/);
  assert.match(codex, /--oauth-client-id 'dedicatedread123'/);
  assert.doesNotMatch(codex, /--oauth-resource|oauth_resource/);
  assert.ok(codex.includes(`mcp login ttobak --scopes 'openid,email,profile,${origin}/api/mcp/read'`));
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
      assert.doesNotMatch(text, new RegExp(`${read}|${write}|ttobak.connector|/api/mcp/(read|write)`));
    }
  }
});

test('explicit flag-off HTTP restores first-party client and base scopes', async () => {
  const { config, mcpClientGuide } = await load({ url: '/api/mcp', privateBridgeEnabled: false });
  assert.equal(config.httpClientId, spa);
  assert.equal(config.httpScopes.join(' '), baseScopes);
  assert.ok(!JSON.stringify(config.httpScopes).includes('/api/mcp/'));
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

test('Outlook guide requires a user-configured local reader and host scheduler', async () => {
  const library = await load(privateMcp);
  assert.equal(typeof library.outlookSyncGuide, 'function');
  const guide = library.outlookSyncGuide();
  const text = JSON.stringify(guide);
  for (const name of ['ttobak_sync_calendar_events', 'ttobak_list_calendar_events', 'ttobak_calendar_sync_status']) {
    assert.ok(text.includes(name), name);
  }
  for (const requirement of [/stdio/, /HTTP.*지원하지/, /로컬.*호스트/, /10분/, /중복 실행/,
    /읽기 전용/, /7일/, /30일/, /dry run/, /체크포인트/, /lastCompletedRevision/,
    /409/, /private/, /반복 일정/, /절전/, /인증/, /중지/]) {
    assert.match(text, requirement);
  }
  assert.match(text, /MCP.*스케줄러가 아닙니다/);
  assert.match(text, /설정.*완료.*뜻이 아닙니다/);
  assert.match(text, /비밀번호.*브라우저 토큰.*복사하지/);
  assert.doesNotMatch(codeText(guide), /outlook-mcp|--password|--access-token|Calendar[s]?\.ReadWrite/);
  assert.ok(guide.steps.flatMap(step => step.code ?? []).every(code => !code.needsConfig && !code.needsHttp));
  for (const client of ['claude', 'codex', 'kirocrew', 'quick', 'kiro-cli']) {
    assert.doesNotMatch(codeText(library.mcpClientGuide(client, 'http', library.config)), /ttobak_sync_calendar_events/);
  }
});

test('Outlook synthetic batch uses the closed local MCP input contract', async () => {
  const library = await load(privateMcp);
  assert.equal(typeof library.outlookSyncGuide, 'function');
  const code = library.outlookSyncGuide().steps.flatMap(step => step.code ?? [])
    .find(code => code.title.includes('ttobak_sync_calendar_events'));
  assert.ok(code);
  const batch = JSON.parse(code.text);
  assert.deepEqual(Object.keys(batch).sort(),
    ['batchId', 'complete', 'expectedRevision', 'mutations', 'sourceId', 'windowEnd', 'windowStart']);
  assert.equal(batch.expectedRevision, 0);
  assert.equal(batch.complete, false);
  assert.deepEqual(Object.keys(batch.mutations[0].event).sort(), ['endsAt', 'externalId', 'startsAt', 'timeZone', 'title']);
  assert.equal(batch.mutations[0].op, 'upsert');
  assert.equal(batch.mutations[0].event.externalId, 'SyntheticOccurrence/AbC');
});

const outlookRunbook = readFileSync(new URL('../../docs/runbooks/local-outlook-calendar-sync.md', import.meta.url), 'utf8');
const runbookSection = title => outlookRunbook.split(`## ${title}\n`)[1]?.split('\n## ')[0].replace(/\s+/g, ' ') ?? '';

test('Outlook runbook requires both local exclusions and tracked removals before reader acceptance', () => {
  const contract = runbookSection('Local reader contract');
  const acceptance = runbookSection('Dry run and manual acceptance');
  for (const sensitivity of ['private', 'confidential']) {
    assert.match(contract, new RegExp(`Exclude[^.]*\`${sensitivity}\`[^.]*by default`));
    assert.match(contract, new RegExp(`newly[^.]*\`${sensitivity}\`[^.]*explicit removal[^.]*tracked occurrence ID`));
    assert.match(acceptance, new RegExp(`Exclude[^.]*\`${sensitivity}\`[^.]*by default`));
    assert.match(acceptance, new RegExp(`${sensitivity} reclassification`));
  }
  assert.match(contract, /Do not forward sensitivity flags or[^.]*replacement titles/);
  assert.match(acceptance, /explicit removals[^.]*tracked occurrence ID/);
  assert.match(outlookRunbook, /guide and protocol checks do not verify[^.]*reader[^.]*filtering/);
});

test('Outlook Korean guide requires both local exclusions and tracked removals before reader acceptance', async () => {
  const { outlookSyncGuide } = await load(privateMcp);
  const guide = outlookSyncGuide();
  const dryRun = guide.steps.find(step => step.title.includes('dry run')).description;
  const manual = guide.steps.find(step => step.title.includes('수동 실행')).description;
  const removal = guide.steps.find(step => step.title.includes('삭제 규칙')).description;
  for (const sensitivity of ['private', 'confidential']) {
    assert.match(dryRun, new RegExp(`${sensitivity}[^.]*기본적으로 로컬에서 제외`));
    assert.match(manual, new RegExp(`${sensitivity} 재분류`));
    assert.match(removal, new RegExp(`${sensitivity}[^.]*이전에 가져온 회차 externalId[^.]*명시적[^.]*remove`));
  }
  assert.match(removal, /민감도 필드[^.]*대체 제목[^.]*보내지/);
  assert.match(removal, /조회 실패로 삭제를 추론하지/);
  assert.match(guide.note, /가이드·프로토콜 검증[^.]*리더[^.]*필터링[^.]*증명하지/);
});

test('Outlook English copyable prompt independently requires both default exclusions and tracked removals', () => {
  const prompt = outlookRunbook.match(/## Copyable host instruction\n\n```text\n([\s\S]*?)\n```/)?.[1].replace(/\s+/g, ' ');
  assert.ok(prompt);
  for (const sensitivity of ['private', 'confidential']) {
    assert.match(prompt, new RegExp(`Exclude[^.]*${sensitivity}[^.]*by default`));
    assert.match(prompt, new RegExp(`explicitly remove[^.]*tracked occurrence externalId[^.]*${sensitivity} reclassification`));
  }
  assert.match(prompt, /Do not upload sensitivity fields or[^.]*replacement titles/);
  assert.match(prompt, /Missing or failed pages are not deletions/);
  assert.match(prompt, /guide or protocol checks[^.]*reader filtering/);
});

test('Outlook Korean copyable prompt independently requires both default exclusions and tracked removals', async () => {
  const { outlookSyncGuide } = await load(privateMcp);
  const prompt = outlookSyncGuide().steps.flatMap(step => step.code ?? [])
    .find(code => code.title.includes('로컬 호스트에 요청')).text;
  for (const sensitivity of ['private', 'confidential']) {
    assert.match(prompt, new RegExp(`${sensitivity}[^.]*기본적으로 로컬에서 제외`));
    assert.match(prompt, new RegExp(`${sensitivity}[^.]*이전에 가져온 회차 externalId[^.]*명시적 remove`));
  }
  assert.match(prompt, /민감도 필드[^.]*대체 제목[^.]*보내지/);
  assert.match(prompt, /실패한 페이지는 완료나 누락 삭제로 처리하지/);
  assert.match(prompt, /가이드·프로토콜 검증[^.]*리더[^.]*필터링[^.]*증명하지/);
});

test('settings Outlook anchor and copyable prerequisites survive collapsed, HTTP and config error states', async () => {
  const library = await load(privateMcp);
  const component = ts.transpileModule(readFileSync(new URL('../src/components/McpGuide.tsx', import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
  }).outputText;
  for (const expanded of [false, true]) {
    for (const client of ['claude', 'codex', 'kirocrew', 'quick', 'kiro-cli']) {
      for (const transport of ['stdio', 'http']) {
        for (const state of [{ status: 'loading' }, { status: 'error', message: '설정 실패' }, { status: 'ready', config: library.config }]) {
          let slot = 0;
          const initial = [expanded, client, transport, state, 0];
          const exports = {};
          vm.runInNewContext(component, { exports, require(name) {
            if (name === 'react') return { ...React, useState(value) {
              const current = slot++;
              return [current < initial.length ? initial[current] : value, () => {}];
            } };
            if (name === 'react/jsx-runtime') return jsxRuntime;
            if (name === '@/lib/mcpGuide') return library;
            throw new Error(`Unexpected import: ${name}`);
          } });
          const html = renderToStaticMarkup(React.createElement(exports.McpGuide));
          const section = html.match(/<section\b[^>]*id="outlook-sync"[\s\S]*?<\/section>/)?.[0];
          assert.ok(section, `${expanded}/${client}/${transport}/${state.status}`);
          assert.match(section, /로컬.*stdio/);
          assert.match(section, /HTTP.*지원하지/);
          assert.match(section, /호스트.*요청/);
          assert.doesNotMatch(section, /disabled=""|hidden=""|display:none/);
        }
      }
    }
  }
});
