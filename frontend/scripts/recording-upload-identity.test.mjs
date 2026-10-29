import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { posix } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const transpile = text => ts.transpileModule(text, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;
const page = ts.createSourceFile('page.tsx', readFileSync(
  new URL('../src/app/record/page.tsx', import.meta.url), 'utf8'),
ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
let callback;
function find(node) {
  if (ts.isVariableDeclaration(node) && node.name.getText(page) === 'handleAudioUpload') {
    callback = node.initializer.getText(page);
  }
  ts.forEachChild(node, find);
}
find(page);
assert.ok(callback);

function fixture({ pauseAt, switchAfterPut = false, initiatingUser = 'creator' } = {}) {
  let currentUser = 'creator';
  let finish;
  let paused = false;
  const requests = [], puts = [], errors = [], navigations = [];
  const token = () => `synthetic.${Buffer.from(JSON.stringify({
    sub: currentUser, exp: Date.now() / 1000 + 3600,
  })).toString('base64url')}.signature`;
  const payload = (url, method) => {
    if (url === '/api/accounts/customer') return {
      accountId: 'customer', ownerUserId: 'creator',
      members: [{ userId: 'creator', role: 'owner' }], accessPermission: 'edit',
    };
    if (url === '/api/upload/presigned') return { uploadUrl: 'https://example.invalid/audio', key: 'audio/creator/draft/file' };
    if (url === '/api/meetings' && method === 'POST') return { meetingId: 'draft', preparationApplied: false };
    return { meetingId: 'draft', notes: '', notesRevision: 'r1', supportsNotesComparison: true, supportsPrivateAccountLink: true };
  };
  class XHR {
    status = 200;
    listeners = {};
    upload = { addEventListener() {} };
    addEventListener(name, listener) { this.listeners[name] = listener; }
    open(method, url) { this.url = url; }
    setRequestHeader(name) { assert.equal(name, 'Content-Type', 'S3 must never receive a bearer token'); }
    send() {
      puts.push(this.url);
      if (switchAfterPut) currentUser = 'another';
      this.listeners.load();
    }
  }
  const dependencies = {
    './auth': { getIdToken: token, refreshSession: async () => token() },
    './runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  };
  const cache = new Map();
  function load(path) {
    if (path in dependencies) return dependencies[path];
    if (cache.has(path)) return cache.get(path);
    const exports = {};
    cache.set(path, exports);
    vm.runInNewContext(transpile(readFileSync(new URL(`../src/${path}.ts`, import.meta.url), 'utf8')), {
      exports, atob, URLSearchParams, process: { env: {} }, XMLHttpRequest: XHR,
      require: name => load(name.startsWith('.') && !(name in dependencies)
        ? posix.normalize(posix.join(posix.dirname(path), name)) : name),
      fetch: async (url, options) => {
        const method = options.method || 'GET';
        const userId = JSON.parse(Buffer.from(options.headers.Authorization.split('.')[1], 'base64url').toString()).sub;
        requests.push({ url, method, userId });
        const response = status => ({
          status, ok: status === 200,
          headers: { get: key => key === 'content-type' ? 'application/json' : null },
          json: async () => payload(url, method),
        });
        if (!paused && pauseAt === `${method} ${url}`) {
          paused = true;
          return new Promise(resolve => { finish = status => resolve(response(status)); });
        }
        return response(200);
      },
    });
    return exports;
  }
  const api = load('lib/api');
  const exports = {};
  vm.runInNewContext(transpile(`exports.upload = ${callback};`), {
    exports, console: { error() {} }, isMeetingFlowBusy: () => false,
    meetingTitle: 'Prepared audio', referenceAccountId: 'customer', contextText: 'Private notes',
    user: initiatingUser ? { userId: initiatingUser } : null,
    meetingsApi: api.meetingsApi, meetingAccountApi: api.meetingAccountApi,
    resolveRecordingAccountId: load('lib/recordingAccount').resolveRecordingAccountId,
    preparationNotes: context => context,
    withTimeout: promise => promise, ...load('lib/upload'),
    setUploadProgress() {}, session: { setSpeechError: error => errors.push(error) },
    router: { push: value => navigations.push(value) },
  });
  return {
    requests, puts, errors, navigations,
    upload: () => exports.upload([
      { name: 'one.webm', type: 'audio/webm', size: 5 },
      { name: 'two.webm', type: 'audio/webm', size: 5 },
    ]),
    switchUser: () => { currentUser = 'another'; },
    respond: status => { assert.ok(finish, 'requested pause boundary was reached'); finish(status); },
  };
}
const tick = () => new Promise(resolve => setImmediate(resolve));
const boundaries = [
  'GET /api/accounts/customer', 'POST /api/meetings', 'GET /api/meetings/draft',
  'PUT /api/meetings/draft', 'POST /api/meetings/draft/account',
  'POST /api/upload/presigned', 'POST /api/upload/complete',
];
for (const boundary of boundaries) {
  for (const status of [200, 401]) {
    test(`upload stops after a session switch at ${boundary} (${status})`, async () => {
      const f = fixture({ pauseAt: boundary });
      const result = f.upload();
      await tick();
      f.switchUser();
      f.respond(status);
      await result;
      assert.ok(f.errors.length > 0, 'the switched session must abort the upload flow');
      assert.ok(f.requests.every(request => request.userId === 'creator'), 'no request or retry may use another user');
      assert.deepEqual(f.navigations, []);
    });
  }
}
test('a session switch after S3 PUT prevents completion and the next audio part', async () => {
  const f = fixture({ switchAfterPut: true });
  await f.upload();
  assert.equal(f.puts.length, 1);
  assert.equal(f.requests.filter(request => request.url === '/api/upload/complete').length, 0);
  assert.ok(f.errors.length > 0);
  assert.deepEqual(f.navigations, []);
});
test('an upload without an initiating user makes no requests', async () => {
  const f = fixture({ initiatingUser: null });
  await f.upload();
  assert.deepEqual(f.requests, []);
  assert.ok(f.errors.length > 0);
});
test('the initiating user can refresh and finish both audio parts', async () => {
  const f = fixture({ pauseAt: 'POST /api/meetings' });
  const result = f.upload();
  await tick();
  f.respond(401);
  await result;
  assert.deepEqual(f.errors, []);
  assert.equal(f.puts.length, 2);
  assert.equal(f.requests.filter(request => request.url === '/api/upload/complete').length, 2);
  assert.ok(f.requests.every(request => request.userId === 'creator'));
  assert.deepEqual(f.navigations, ['/meeting/draft']);
});
