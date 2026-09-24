import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Recorder, parseAudioDevices, parseMaxVolume, validateDevice, validateMaxMinutes } from '../dist/recorder.js';
import { recorderFixture } from './fixtures/fake-ffmpeg.mjs';

// Recorder unit tests. The MCP tools that drive it are tested separately.

test('device list, device validation and volume parsing', () => {
  const stderr = 'x AVFoundation video devices:\nx [0] Cam\nx AVFoundation audio devices:\nx [0] Mic\nx [1] USB\n';
  assert.deepEqual(parseAudioDevices(stderr), [{ index: 0, name: 'Mic' }, { index: 1, name: 'USB' }]);
  assert.equal(parseMaxVolume('max_volume: -91.0 dB'), -91);
  assert.equal(parseMaxVolume('no volume'), null);
  assert.equal(validateDevice(undefined), 'default');
  assert.equal(validateDevice(2), '2');
  assert.equal(validateMaxMinutes(undefined), 240);
  assert.equal(validateMaxMinutes(30), 30);
  for (const bad of [0, 241, 1.5, '30']) assert.throws(() => validateMaxMinutes(bad), /maxMinutes/);
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
  const dead = 2 ** 22 + 12345;
  const liveFfmpeg = '44444444-4444-4444-8444-444444444444';
  const noFfmpegPid = '55555555-5555-4555-8555-555555555555';
  for (const [id, ownerPid, ffmpegPid] of [[live, process.ppid, process.ppid], [orphan, dead, dead],
    [liveFfmpeg, dead, process.ppid], [noFfmpegPid, dead, undefined]]) {
    writeFileSync(join(dir, `${id}.wav`), Buffer.alloc(1024));
    writeFileSync(join(dir, `${id}.json`), JSON.stringify({ id, title: id, startedAt: new Date().toISOString(), state: 'recording', ownerPid, ffmpegPid }));
  }
  assert.deepEqual(recorder.listSaved().map((r) => r.id), [orphan],
    'only a capture whose owner and ffmpeg both exited is recoverable; a live or unknown ffmpeg hides it');
  assert.throws(() => recorder.getSaved(live), /No saved recording/);

  const release = recorder.acquire(orphan);
  assert.throws(() => recorder.acquire(orphan), /being uploaded by another request/);
  assert.equal(recorder.listSaved()[0].uploading, true);
  release();
  assert.ok(!existsSync(join(dir, `${orphan}.lock`)), 'release removes the lock file');
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

test('stale lock takeover is exclusive and unreadable locks count as held', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  const other = new Recorder({ dir, platform: 'darwin' });
  mkdirSync(dir, { recursive: true });
  const id = '33333333-3333-4333-8333-333333333333';
  writeFileSync(join(dir, `${id}.wav`), Buffer.alloc(1024));
  writeFileSync(join(dir, `${id}.json`), JSON.stringify({ id, title: id, startedAt: new Date().toISOString(), state: 'kept' }));
  const lock = join(dir, `${id}.lock`);

  writeFileSync(lock, '');
  assert.throws(() => recorder.acquire(id), /being uploaded/, 'an empty lock may be mid-creation and is never removed');
  assert.equal(recorder.listSaved()[0].uploading, true);

  writeFileSync(lock, `${2 ** 22 + 12345}:dead-holder`);
  const release = recorder.acquire(id);
  assert.throws(() => other.acquire(id), /being uploaded/, 'a second contender cannot take over the fresh lock');
  assert.deepEqual(readdirSync(dir).filter((name) => name.includes('.lock.') || name.endsWith('.tmp')), []);
  release();

  writeFileSync(lock, `${2 ** 22 + 12345}:dead-holder`);
  writeFileSync(`${lock}.takeover`, `${2 ** 22 + 12345}:crashed`);
  assert.throws(() => other.acquire(id), /lock is being recovered; if this persists, delete/);
});

test('silenceCheck only analyzes recordings in the recordings directory', async (t) => {
  const { root, recorder } = recorderFixture(t);
  for (const file of [join(root, 'ffmpeg'), '/etc/passwd', join(root, 'recordings', '..', 'x.wav')]) {
    await assert.rejects(recorder.silenceCheck(file), /only a recording/);
  }
  assert.throws(() => recorder.saveProgress('../x', {}), /Invalid recording id/);
});

test('shutdown during an in-flight stop leaves that caller holding the lock', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  const { id } = await recorder.start({ title: 'in flight' });
  const stopping = recorder.stop();
  await recorder.shutdown();
  const stopped = await stopping;
  assert.throws(() => recorder.acquire(id), /being uploaded/, 'shutdown must not release a lock it does not own');
  stopped.release();
  assert.ok(!existsSync(join(dir, `${id}.lock`)));
});

test('the ffmpeg PID is persisted before the startup grace period and silence is checked', async (t) => {
  const { dir, recorder } = recorderFixture(t);
  const starting = recorder.start({ title: 'grace' });
  await new Promise((resolve) => setTimeout(resolve, 100));
  const [sidecar] = readdirSync(dir).filter((name) => name.endsWith('.json'));
  const early = JSON.parse(readFileSync(join(dir, sidecar), 'utf8'));
  assert.equal(early.state, 'recording');
  assert.equal(typeof early.ffmpegPid, 'number', 'written right after spawn, not after the grace wait');
  await starting;
  const stopped = await recorder.stop();
  t.after(() => stopped.release());
  assert.deepEqual(await recorder.silenceCheck(stopped.file), { maxVolumeDb: -12.5, silent: false });
  process.env.FAKE_MAX_VOLUME = '-91.0';
  t.after(() => { delete process.env.FAKE_MAX_VOLUME; });
  assert.equal((await recorder.silenceCheck(stopped.file)).silent, true);
});

test('a relative or trailing-slash recordings directory is normalized', async (t) => {
  const { root, dir, recorder: base } = recorderFixture(t);
  const ffmpegPath = join(root, 'ffmpeg');
  const recorder = new Recorder({ ffmpegPath, dir: `${dir}/`, platform: 'darwin' });
  await assert.rejects(recorder.silenceCheck(join(dir, 'x.wav')), /only a recording/);
  const file = join(dir, '66666666-6666-4666-8666-666666666666.wav');
  assert.equal((await recorder.silenceCheck(file)).silent, false, 'a trailing slash does not reject real recordings');
  assert.equal((await base.silenceCheck(file)).silent, false);
});
