import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { TtobakApi, UploadRejectedError } from '../dist/api.js';
import { recorderFixture } from './fixtures/fake-ffmpeg.mjs';
import { createMcpServer, settleRecordingWork } from '../dist/index.js';

function fakeApi({ failComplete = false } = {}) {
  const calls = [], puts = [];
  // put: 'ok' | 'reject' (S3 failure status) | 'network' (outcome unknown)
  const control = { failComplete, put: 'ok' };
  const api = new TtobakApi({ getIdToken: async () => 'synthetic-token' }, 'https://ttobak.example.com');
  api.request = async (method, path, body) => {
    calls.push({ method, path, body });
    if (path === '/api/meetings') return { meetingId: 'meeting-1', status: 'recording' };
    if (path === '/api/upload/presigned') {
      return { uploadUrl: 'https://bucket.s3.amazonaws.com/x', key: `audio/user-1/${body.meetingId}/${1700000000000 + calls.length}_${body.fileName}` };
    }
    if (path === '/api/upload/complete') {
      if (control.failComplete) throw new Error('HTTP 500');
      return { status: 'ok' };
    }
    throw new Error(`unexpected ${method} ${path}`);
  };
  api.putFileStream = async (url, file, size, type) => {
    puts.push({ url, file, size, type });
    if (control.put === 'reject') throw new UploadRejectedError('Audio upload to S3 failed: HTTP 403');
    if (control.put === 'network') throw new Error('socket hang up');
    if (control.gate) await control.gate;
  };
  return { api, calls, puts, control };
}

async function connect(t, { api, recorder, mode = 'stdio' }) {
  const server = createMcpServer({
    mode, api, recorder, auth: { getIdToken: async () => 'synthetic-token', isAuthenticated: () => true },
    apiUrl: 'https://ttobak.example.com', cognitoDomain: 'https://auth.invalid', clientId: 'fixture-client',
  });
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  const client = new Client({ name: 'recording-test', version: '1.0.0' });
  await client.connect(clientTransport);
  t.after(async () => { await recorder?.shutdown(); await client.close(); });
  const call = async (name, args = {}) => {
    const result = await client.callTool({ name, arguments: args });
    const body = result.content[0].text;
    return { isError: !!result.isError, body, json: result.isError ? null : JSON.parse(body) };
  };
  return { client, call };
}

test('start, status and stop upload a new meeting and delete the local file afterwards', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  const { api, calls, puts } = fakeApi();
  const { call } = await connect(t, { api, recorder });

  const devices = await call('ttobak_list_audio_devices');
  assert.equal(devices.json.devices[1].name, 'External USB Mic');
  const started = await call('ttobak_start_recording', { title: '고객 미팅', device: 1 });
  assert.equal(started.isError, false, started.body);
  assert.equal(calls.length, 0, 'starting a recording must not contact TTOBAK');
  const again = await call('ttobak_start_recording', { title: 'second' });
  assert.match(again.body, /already running/);
  const status = await call('ttobak_recording_status');
  assert.equal(status.json.active.running, true);

  const stopped = await call('ttobak_stop_recording', { participants: ['Kim'], accountId: 'acc-1' });
  assert.equal(stopped.isError, false, stopped.body);
  assert.deepEqual(calls.map((c) => c.path), ['/api/meetings', '/api/upload/presigned', '/api/upload/complete']);
  assert.equal(calls[0].body.title, '고객 미팅');
  assert.deepEqual(calls[0].body.participants, ['Kim']);
  assert.equal(calls[0].body.accountId, 'acc-1');
  assert.equal(calls[1].body.category, 'audio');
  assert.equal(calls[1].body.meetingId, 'meeting-1');
  assert.match(calls[1].body.fileName, /^mcp_upload_\d+\.wav$/);
  assert.match(calls[2].body.key, new RegExp(`^audio/user-1/meeting-1/\\d+_${calls[1].body.fileName}$`));
  assert.equal(puts[0].type, 'audio/wav');
  assert.equal(stopped.json.url, 'https://ttobak.example.com/meeting/meeting-1');
  assert.deepEqual(readdirSync(dir), [], 'uploaded recording and sidecar are removed');
});

test('silent recordings and failed completions are kept locally for retry', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  const failing = fakeApi({ failComplete: true });
  const { call } = await connect(t, { api: failing.api, recorder });

  process.env.FAKE_MAX_VOLUME = '-91.0';
  t.after(() => { delete process.env.FAKE_MAX_VOLUME; });
  await call('ttobak_start_recording', { title: 'muted' });
  const silent = await call('ttobak_stop_recording');
  assert.equal(silent.json.uploaded, false);
  assert.match(silent.json.warnings.join(' '), /microphone access/);
  assert.equal(failing.calls.length, 0, 'a silent recording is not uploaded by default');
  const { recordingId } = silent.json;

  const failed = await call('ttobak_upload_audio', { recordingId });
  assert.equal(failed.isError, true);
  assert.match(failed.body, /audio is stored for meeting meeting-1/);
  assert.match(failed.body, /only completes, without uploading again/);
  assert.ok(existsSync(join(dir, `${recordingId}.wav`)), 'failed completion keeps the file');

  const status = await call('ttobak_recording_status');
  assert.equal(status.json.saved[0].id, recordingId);
  assert.equal(status.json.saved[0].meetingId, 'meeting-1');

  assert.equal(status.json.saved[0].uploadPut, true);

  failing.control.failComplete = false;
  const retried = await call('ttobak_upload_audio', { recordingId });
  assert.equal(retried.isError, false, retried.body);
  assert.equal(retried.json.created, false);
  assert.equal(failing.calls.filter((c) => c.path === '/api/meetings').length, 1, 'retry reuses the meeting it created');
  assert.equal(failing.calls.filter((c) => c.path === '/api/upload/presigned').length, 1, 'a stored object is never uploaded twice');
  assert.equal(failing.puts.length, 1);
  assert.equal(failing.calls.at(-1).body.key, status.json.saved[0].uploadKey);
  assert.deepEqual(readdirSync(dir), []);
});

test('rejected and unconfirmed PUTs are reported by phase and resumed safely', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  const fake = fakeApi();
  const { call } = await connect(t, { api: fake.api, recorder });
  await call('ttobak_start_recording', { title: 'phases' });
  const kept = await call('ttobak_stop_recording', { upload: false });
  const { recordingId } = kept.json;

  fake.control.put = 'reject';
  const rejected = await call('ttobak_upload_audio', { recordingId });
  assert.match(rejected.body, /No audio was uploaded.*reuses meeting meeting-1/);
  fake.control.put = 'network';
  const unknown = await call('ttobak_upload_audio', { recordingId });
  assert.match(unknown.body, /did not confirm, so it may be stored and transcribing/);
  const undecided = await call('ttobak_upload_audio', { recordingId });
  assert.match(undecided.body, /previousUpload "complete"/, 'an unknown PUT is never silently repeated');
  assert.equal(fake.puts.length, 2);

  fake.control.put = 'ok';
  const completed = await call('ttobak_upload_audio', { recordingId, previousUpload: 'complete' });
  assert.equal(completed.isError, false, completed.body);
  assert.equal(fake.puts.length, 2, 'previousUpload "complete" only completes');
  assert.equal(fake.calls.filter((c) => c.path === '/api/meetings').length, 1);
  assert.deepEqual(readdirSync(dir), []);
});

test('upload_audio validates input before creating a meeting', async (t) => {
  const { root, recorder } = recorderFixture(t);
  const { api, calls } = fakeApi();
  const { call } = await connect(t, { api, recorder });
  const notes = join(root, 'notes.txt');
  writeFileSync(notes, 'text');
  const audio = join(root, 'realtime_part_001_call.m4a');
  writeFileSync(audio, Buffer.alloc(1024));

  assert.match((await call('ttobak_upload_audio', {})).body, /exactly one/);
  assert.match((await call('ttobak_upload_audio', { filePath: audio, recordingId: 'x' })).body, /exactly one/);
  assert.match((await call('ttobak_upload_audio', { filePath: 'relative.m4a' })).body, /absolute/);
  assert.match((await call('ttobak_upload_audio', { filePath: notes })).body, /Unsupported audio format/);
  const caf = join(root, 'memo.caf');
  writeFileSync(caf, Buffer.alloc(1024));
  assert.match((await call('ttobak_upload_audio', { filePath: caf })).body, /Unsupported audio format/, 'the Transcribe fallback cannot read CAF');
  assert.match((await call('ttobak_upload_audio', { recordingId: '../../etc/passwd' })).body, /No saved recording/);
  assert.match((await call('ttobak_upload_audio', { filePath: audio, meetingId: 'someone-else' })).body, /meetingId is not accepted/);
  assert.equal(calls.length, 0, 'invalid uploads must not create meetings');

  const uploaded = await call('ttobak_upload_audio', { filePath: audio });
  assert.equal(uploaded.isError, false, uploaded.body);
  assert.equal(calls[0].body.title, 'realtime_part_001_call');
  assert.match(calls[1].body.fileName, /^mcp_upload_\d+\.m4a$/, 'transcribe skip markers never reach the object key');
  assert.ok(existsSync(audio), 'a caller-owned file is never deleted');
});

test('HTTP transport never exposes local recording or audio file tools', async (t) => {
  const { recorder } = recorderFixture(t);
  const { api } = fakeApi();
  const { client, call } = await connect(t, { api, recorder, mode: 'http' });
  const names = (await client.listTools()).tools.map((tool) => tool.name);
  for (const name of ['ttobak_list_audio_devices', 'ttobak_start_recording', 'ttobak_recording_status', 'ttobak_stop_recording', 'ttobak_upload_audio']) {
    assert.ok(!names.includes(name), `${name} must be stdio-only`);
    assert.equal((await call(name, {})).isError, true);
  }
});

test('stdio shutdown waits for an in-flight recording upload', async (t) => {
  const { recorder } = recorderFixture(t);
  const fake = fakeApi();
  const { call } = await connect(t, { api: fake.api, recorder });
  let open;
  fake.control.gate = new Promise((resolve) => { open = resolve; });
  await call('ttobak_start_recording', { title: 'shutdown' });
  const stopping = call('ttobak_stop_recording');
  while (!fake.puts.length) await new Promise((resolve) => setTimeout(resolve, 10));
  let settled = false;
  const settling = settleRecordingWork().then(() => { settled = true; });
  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.equal(settled, false, 'shutdown must not proceed while the PUT is in flight');
  open();
  await settling;
  assert.equal((await stopping).isError, false);
  assert.deepEqual(fake.calls.map((c) => c.path), ['/api/meetings', '/api/upload/presigned', '/api/upload/complete']);
});
