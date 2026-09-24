import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Recorder, parseAudioDevices, parseMaxVolume, validateDevice } from '../dist/recorder.js';
import { recorderFixture } from './fixtures/fake-ffmpeg.mjs';

// Recorder unit tests. The MCP tools that drive it are tested separately.

test('device list, device validation and volume parsing', () => {
  const stderr = 'x AVFoundation video devices:\nx [0] Cam\nx AVFoundation audio devices:\nx [0] Mic\nx [1] USB\n';
  assert.deepEqual(parseAudioDevices(stderr), [{ index: 0, name: 'Mic' }, { index: 1, name: 'USB' }]);
  assert.equal(parseMaxVolume('max_volume: -91.0 dB'), -91);
  assert.equal(parseMaxVolume('no volume'), null);
  assert.equal(validateDevice(undefined), 'default');
  assert.equal(validateDevice(2), '2');
  for (const bad of ['0:1', 'a\nb', -1, 1.5, {}]) assert.throws(() => validateDevice(bad), /device/);
});

test('recording refuses non-macOS hosts', async () => {
  const recorder = new Recorder({ platform: 'linux', dir: tmpdir() });
  await assert.rejects(recorder.start({ title: 't' }), /only on macOS/);
});

test('startup failures are reported and leave nothing behind', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  process.env.FAKE_FAIL = '1';
  t.after(() => { delete process.env.FAKE_FAIL; });
  await assert.rejects(recorder.start({ title: 'x' }), /Input\/output error/);
  assert.deepEqual(readdirSync(dir), []);
  assert.equal(recorder.status(), null);
});

test('overlapping starts spawn one ffmpeg and a stopping recording stays reserved', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  const results = await Promise.allSettled([recorder.start({ title: 'a' }), recorder.start({ title: 'b' })]);
  assert.deepEqual(results.map((r) => r.status).sort(), ['fulfilled', 'rejected']);
  assert.match(results.find((r) => r.status === 'rejected').reason.message, /already starting/);
  assert.equal(readdirSync(dir).filter((name) => name.endsWith('.wav')).length, 1);

  const { id } = results.find((r) => r.status === 'fulfilled').value;
  const stopping = recorder.stop();
  assert.deepEqual(recorder.listSaved(), [], 'an unfinalized recording is never offered for upload');
  assert.throws(() => recorder.getSaved(id), /No saved recording/);
  await assert.rejects(recorder.start({ title: 'c' }), /already running/);
  await assert.rejects(recorder.stop(), /already stopping/);
  const stopped = await stopping;
  assert.equal(stopped.id, id);
  assert.throws(() => recorder.acquire(id), /being uploaded/, 'stop returns holding the upload lock');
  stopped.release();
  assert.equal(recorder.getSaved(id).id, id);
});

test('a capture owned by another live process or held by an upload is not offered', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  mkdirSync(dir, { recursive: true });
  const live = '11111111-1111-4111-8111-111111111111';
  const orphan = '22222222-2222-4222-8222-222222222222';
  for (const [id, pid] of [[live, process.ppid], [orphan, 2 ** 22 + 12345]]) {
    writeFileSync(join(dir, `${id}.wav`), Buffer.alloc(1024));
    writeFileSync(join(dir, `${id}.json`), JSON.stringify({ id, title: id, startedAt: new Date().toISOString(), state: 'recording', ownerPid: pid, ffmpegPid: pid }));
  }
  assert.deepEqual(recorder.listSaved().map((r) => r.id), [orphan], 'only a capture whose owner and ffmpeg exited is recoverable');
  assert.throws(() => recorder.getSaved(live), /No saved recording/);

  const release = recorder.acquire(orphan);
  assert.throws(() => recorder.acquire(orphan), /being uploaded by another request/);
  assert.equal(recorder.listSaved()[0].uploading, true);
  release();
  writeFileSync(join(dir, `${orphan}.lock`), `${2 ** 22 + 12345}:stale`);
  recorder.acquire(orphan)();
});

test('upload progress is validated, persisted atomically and removed with the recording', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  await recorder.start({ title: 'progress' });
  const stopped = await recorder.stop();
  const { id } = stopped;
  assert.throws(() => recorder.saveProgress(id, { uploadKey: 'audio/u/m/../../x.wav' }), /Invalid upload key/);
  assert.throws(() => recorder.saveProgress(id, { meetingId: 'a/b' }), /Invalid meeting id/);
  const uploadKey = 'audio/0a1b-user/meeting-1/1700000000000_mcp_upload_1700000000000.wav';
  recorder.saveProgress(id, { meetingId: 'meeting-1', uploadKey, uploadPut: false });
  assert.deepEqual(readdirSync(dir).filter((name) => name.endsWith('.tmp')), [], 'sidecar writes rename into place');
  assert.deepEqual(
    (({ meetingId, uploadKey: key, uploadPut }) => ({ meetingId, uploadKey: key, uploadPut }))(recorder.getSaved(id)),
    { meetingId: 'meeting-1', uploadKey, uploadPut: false });
  recorder.saveProgress(id, { uploadKey: undefined, uploadPut: undefined });
  assert.equal(recorder.getSaved(id).uploadKey, undefined);
  recorder.discard(id);
  stopped.release();
  assert.deepEqual(readdirSync(dir), []);
});
