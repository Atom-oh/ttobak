import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const projectId = '00000000-0000-4000-8000-000000000001';
const source = readFileSync(new URL('../src/lib/api.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
});

function discoveryFixture({ storageFails = false, projectIds = [projectId], accountIds = [] } = {}) {
  let userId = 'recipient';
  let now = Date.now();
  const storage = new Map();
  const requests = [];
  const token = () => `synthetic.${Buffer.from(JSON.stringify({ sub: userId, exp: now / 1000 + 3600 })).toString('base64url')}.signature`;
  const dependencies = {
    './auth': { getIdToken: token, refreshSession: async () => token() },
    './runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  };
  function load() {
    const exports = {};
    vm.runInNewContext(outputText, {
      exports,
      require: (name) => {
        assert.ok(name in dependencies, `Unexpected dependency: ${name}`);
        return dependencies[name];
      },
      process: { env: {} },
      atob,
      URLSearchParams,
      Date: class extends Date { static now() { return now; } },
      window: {
        sessionStorage: {
          getItem: (key) => {
            if (storageFails) throw new Error('Storage unavailable');
            return storage.get(key) ?? null;
          },
          setItem: (key, value) => {
            if (storageFails) throw new Error('Storage unavailable');
            storage.set(key, value);
          },
        },
      },
      fetch: async (url) => {
        requests.push(url);
        return {
          ok: true, status: 200,
          headers: { get: (key) => key === 'content-type' ? 'application/json' : null },
          json: async () => url === '/api/session/bootstrap'
            ? { emailVerified: true, pendingGrants: 0, joinedProjectIds: projectIds, joinedAccountIds: accountIds }
            : { projects: [] },
        };
      },
    });
    return exports;
  }
  return {
    load,
    advance: (milliseconds) => { now += milliseconds; },
    switchUser: (nextUser) => { userId = nextUser; },
    lastHints: () => new URL(requests.at(-1), 'https://example.invalid').searchParams.get('joinedProjectIds'),
    lastRequest: () => requests.at(-1),
  };
}

test('new project discovery survives the old one-minute hint lifetime', async () => {
  const fixture = discoveryFixture();
  const api = fixture.load();
  await api.sessionApi.bootstrap('recipient');
  fixture.advance(61_000);
  await api.projectApi.list();
  assert.equal(fixture.lastHints(), projectId);
});

test('new project discovery survives a reload of the same browser tab', async () => {
  const fixture = discoveryFixture();
  await fixture.load().sessionApi.bootstrap('recipient');
  fixture.advance(61_000);
  await fixture.load().projectApi.list();
  assert.equal(fixture.lastHints(), projectId);
});

test('project discovery never reuses another signed-in users hints', async () => {
  const fixture = discoveryFixture();
  await fixture.load().sessionApi.bootstrap('recipient');
  fixture.switchUser('other-user');
  await fixture.load().projectApi.list();
  assert.equal(fixture.lastHints(), null);
});

test('unavailable browser storage does not block session initialization', async () => {
  const fixture = discoveryFixture({ storageFails: true });
  const api = fixture.load();
  await api.sessionApi.bootstrap('recipient');
  await api.projectApi.list();
  assert.equal(fixture.lastHints(), projectId);
});

test('persisted discovery hints remain bounded and contain only UUIDs', async () => {
  const projectIds = Array.from({ length: 120 }, (_, index) =>
    `00000000-0000-4000-8000-${index.toString(16).padStart(12, '0')}`);
  const fixture = discoveryFixture({ projectIds: [null, 'invalid-id', ...projectIds] });
  await fixture.load().sessionApi.bootstrap('recipient');
  await fixture.load().projectApi.list();
  const hints = fixture.lastHints().split(',');
  assert.ok(hints.length <= 100);
  assert.ok(hints.every(id => projectIds.includes(id)));
});

test('meeting continuation does not repeat bootstrap hints beyond the URL budget', async () => {
  const accountIds = Array.from({ length: 100 }, (_, index) =>
    `00000000-0000-4000-8000-${index.toString(16).padStart(12, '0')}`);
  const fixture = discoveryFixture({ accountIds });
  const api = fixture.load();
  await api.sessionApi.bootstrap('recipient');
  await api.meetingsApi.list({ cursor: 'x'.repeat(6000) });
  const request = fixture.lastRequest();
  assert.equal(new URL(request, 'https://example.invalid').searchParams.has('joinedAccountIds'), false);
  assert.ok(request.length <= 8192);
});
