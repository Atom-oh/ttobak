import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:net';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer, settleRecordingWork } from '../dist/index.js';
import { sendAppRequest, MAX_APP_RESPONSE_BYTES } from '../dist/app-control.js';

// A stand-in for the Mac app's control socket (mac-app control.rs): records
// each request line and answers with `respond(request)`.
async function fakeApp(t, respond) {
  const dir = mkdtempSync(join(tmpdir(), 'ttobak-app-'));
  const socketPath = join(dir, 'control.sock');
  const requests = [];
  const server = createServer((socket) => {
    let buffered = '';
    socket.on('data', (chunk) => {
      buffered += chunk;
      if (!buffered.includes('\n')) return;
      const request = JSON.parse(buffered.split('\n')[0]);
      requests.push(request);
      const reply = respond(request);
      if (reply === undefined) return; // never answer (timeout case)
      socket.end(typeof reply === 'string' ? reply : JSON.stringify(reply) + '\n');
    });
  });
  await new Promise((resolve) => server.listen(socketPath, resolve));
  t.after(() => { server.close(); rmSync(dir, { recursive: true, force: true }); });
  return { socketPath, requests };
}

async function connect(t, appControl, mode = 'stdio') {
  const server = createMcpServer({
    mode, appControl,
    api: {}, auth: { getIdToken: async () => 'x', isAuthenticated: () => true },
    apiUrl: 'https://ttobak.example.com', cognitoDomain: 'https://auth.invalid', clientId: 'fixture',
  });
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  const client = new Client({ name: 'app-control-test', version: '1.0.0' });
  await client.connect(clientTransport);
  t.after(() => client.close());
  const call = async (name, args = {}) => {
    const result = await client.callTool({ name, arguments: args });
    const body = result.content[0].text;
    return { isError: !!result.isError, body, json: result.isError ? null : JSON.parse(body) };
  };
  return { client, call };
}

const ok = (request, data) => ({ v: 1, id: request.id, ok: true, data });

test('start, status and stop forward the protocol and return app data', async (t) => {
  const app = await fakeApp(t, (request) => ok(request, { action: request.action, meetingId: 'm-1' }));
  const { call } = await connect(t, { socketPath: app.socketPath });

  const started = await call('ttobak_app_start_recording', { title: ' 고객 미팅 ', accountId: 'acc-1' });
  assert.equal(started.isError, false, started.body);
  assert.equal(started.json.meetingId, 'm-1');
  assert.equal((await call('ttobak_app_status')).json.action, 'status');
  await call('ttobak_app_stop_recording', { upload: false });
  await call('ttobak_app_stop_recording');

  const [start, status, stop, stopDefault] = app.requests;
  assert.deepEqual({ ...start, id: undefined }, { v: 1, id: undefined, action: 'start', title: '고객 미팅', accountId: 'acc-1' });
  assert.match(start.id, /^[0-9a-f-]{36}$/);
  assert.equal(status.action, 'status');
  assert.equal(stop.upload, false);
  assert.equal('upload' in stopDefault, false, 'the app applies its own upload default');
});

test('app errors and input validation surface as tool errors', async (t) => {
  const app = await fakeApp(t, (request) => ({ v: 1, id: request.id, ok: false, error: { code: 'busy', message: 'already recording' } }));
  const { call } = await connect(t, { socketPath: app.socketPath });
  assert.match((await call('ttobak_app_start_recording', { title: 'x' })).body, /busy: already recording/);
  assert.match((await call('ttobak_app_start_recording', { title: '   ' })).body, /title must be/);
  assert.match((await call('ttobak_app_start_recording', { title: 'x', accountId: 5 })).body, /accountId/);
  assert.match((await call('ttobak_app_stop_recording', { upload: 'yes' })).body, /upload must be a boolean/);
  assert.equal(app.requests.length, 1, 'invalid input never reaches the app');
});

test('missing app, bad responses and silence are reported, never guessed', async (t) => {
  const missing = await connect(t, { socketPath: join(tmpdir(), 'ttobak-no-such-app.sock') });
  assert.match((await missing.call('ttobak_app_status')).body, /app_not_running/);

  const garbled = await fakeApp(t, () => 'not json\n');
  assert.match((await sendAppRequest({ action: 'status' }, { socketPath: garbled.socketPath }).catch((e) => e)).code, /bad_response/);

  const wrongId = await fakeApp(t, () => ({ v: 1, id: 'someone-else', ok: true, data: {} }));
  assert.equal((await sendAppRequest({ action: 'status' }, { socketPath: wrongId.socketPath }).catch((e) => e)).code, 'bad_response');

  const huge = await fakeApp(t, () => 'x'.repeat(MAX_APP_RESPONSE_BYTES + 10));
  assert.equal((await sendAppRequest({ action: 'status' }, { socketPath: huge.socketPath }).catch((e) => e)).code, 'bad_response');

  const silent = await fakeApp(t, () => undefined);
  const { call } = await connect(t, { socketPath: silent.socketPath, timeoutMs: 200 });
  const timedOut = await call('ttobak_app_stop_recording');
  assert.match(timedOut.body, /timeout: .*state is unknown/);
});

test('app control tools are stdio-only', async (t) => {
  const app = await fakeApp(t, (request) => ok(request, {}));
  const stdio = await connect(t, { socketPath: app.socketPath });
  const stdioNames = (await stdio.client.listTools()).tools.map((tool) => tool.name);
  for (const name of ['ttobak_app_status', 'ttobak_app_start_recording', 'ttobak_app_stop_recording']) {
    assert.ok(stdioNames.includes(name));
  }
  const http = await connect(t, { socketPath: app.socketPath }, 'http');
  const httpNames = (await http.client.listTools()).tools.map((tool) => tool.name);
  for (const name of ['ttobak_app_status', 'ttobak_app_start_recording', 'ttobak_app_stop_recording']) {
    assert.ok(!httpNames.includes(name), `${name} must not be exposed over HTTP`);
    assert.equal((await http.call(name, { title: 'x' })).isError, true);
  }
  assert.equal(app.requests.length, 0);
});

test('titles are bounded by code points and app calls hold stdio shutdown', async (t) => {
  const app = await fakeApp(t, (request) => (request.action === 'stop' ? undefined : ok(request, {})));
  const { call } = await connect(t, { socketPath: app.socketPath, timeoutMs: 300 });
  const emoji = '🎙'.repeat(200); // 200 code points, 400 UTF-16 units
  assert.equal((await call('ttobak_app_start_recording', { title: emoji })).isError, false);
  assert.match((await call('ttobak_app_start_recording', { title: emoji + '🎙' })).body, /title must be/);

  const stopping = call('ttobak_app_stop_recording');
  while (!app.requests.some((r) => r.action === 'stop')) await new Promise((r) => setTimeout(r, 10));
  let settled = false;
  const settling = settleRecordingWork().then(() => { settled = true; });
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(settled, false, 'shutdown waits for the in-flight app stop');
  assert.match((await stopping).body, /timeout/, 'the fake app never answers stop');
  await settling;
  assert.equal(settled, true);
});
