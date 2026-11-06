import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');

function load(globals = {}) {
  const { outputText } = ts.transpileModule(read('../src/lib/audioSeek.ts'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, CustomEvent, ...globals });
  return exports;
}

const seek = load();

test('browser WebM without Duration falls back to the server meeting length', () => {
  assert.equal(seek.effectiveDuration(Infinity, 1689), 1689);
  assert.equal(seek.effectiveDuration(NaN, 1689), 1689);
  assert.equal(seek.effectiveDuration(1700.5, 1689), 1700.5);
  assert.equal(seek.effectiveDuration(Infinity, undefined), 0);
  assert.equal(seek.effectiveDuration(Infinity, 0), 0);
  assert.equal(seek.effectiveDuration(0, -5), 0);
});

test('progress-bar seeks never produce NaN or Infinity targets', () => {
  assert.equal(seek.seekFraction(0.5, 1000), 500);
  assert.equal(seek.seekFraction(1.4, 1000), 1000);
  assert.equal(seek.seekFraction(-0.2, 1000), 0);
  assert.equal(seek.seekFraction(0.5, 0), null);
  assert.equal(seek.seekFraction(NaN, 1000), null);
});

test('skip clamps to the start and to a known end only', () => {
  assert.equal(seek.skipTarget(5, -10, 100), 0);
  assert.equal(seek.skipTarget(95, 10, 100), 100);
  assert.equal(seek.skipTarget(95, 10, 0), 105);
  assert.equal(seek.skipTarget(NaN, 10, 100), 10);
});

test('transcript times apply only inside the playable source', () => {
  assert.equal(seek.canSeekTo(120, 600, true), true);
  assert.equal(seek.canSeekTo(700, 600, true), false);
  // Unknown length: a single source accepts, multi-part (merged timeline) does not.
  assert.equal(seek.canSeekTo(120, 0, true), true);
  assert.equal(seek.canSeekTo(120, 0, false), false);
  assert.equal(seek.canSeekTo(120, 600, false), true);
  assert.equal(seek.canSeekTo(-1, 600, true), false);
  assert.equal(seek.canSeekTo(NaN, 600, true), false);
});

test('start-second attributes parse strictly', () => {
  assert.equal(seek.parseStartSeconds('346.1'), 346.1);
  assert.equal(seek.parseStartSeconds('0'), 0);
  for (const bad of [null, undefined, '', ' ', 'abc', '-3', 'Infinity']) {
    assert.equal(seek.parseStartSeconds(bad), null, String(bad));
  }
});

test('requestAudioSeek dispatches the shared event with the time', () => {
  const target = new EventTarget();
  const seen = [];
  target.addEventListener(seek.AUDIO_SEEK_EVENT, (event) => seen.push(event.detail.seconds));
  assert.equal(seek.requestAudioSeek(42.5, target), true);
  assert.equal(seek.requestAudioSeek(NaN, target), false);
  assert.deepEqual(seen, [42.5]);
});

test('player time labels stay readable for unknown and long audio', () => {
  assert.equal(seek.formatPlayerTime(65), '01:05');
  assert.equal(seek.formatPlayerTime(3725), '1:02:05');
  assert.equal(seek.formatPlayerTime(Infinity), '--:--');
  assert.equal(seek.formatPlayerTime(NaN), '--:--');
});

test('transcript anchors, summary links and the player share the seek contract', () => {
  const transcript = read('../src/components/meeting/TranscriptSection.tsx');
  assert.match(transcript, /data-start-seconds=\{group\.untimed \? undefined : group\.startTime\}/);
  assert.match(transcript, /data-start-seconds=\{isDuplicateOfGroupAnchor \|\| seg\.untimed \? undefined : seg\.startTime\}/);

  const renderer = read('../src/components/markdown/MarkdownRenderer.tsx');
  assert.match(renderer, /parseStartSeconds\(el\.getAttribute\('data-start-seconds'\)\)/);
  assert.match(renderer, /requestAudioSeek\(seconds\)/);

  const player = read('../src/components/AudioPlayer.tsx');
  assert.match(player, /window\.addEventListener\(AUDIO_SEEK_EVENT/);
  assert.match(player, /addEventListener\('durationchange'/);
  assert.match(player, /fallbackDuration=\{singleSource \? durationSeconds : undefined\}/);

  const detail = read('../src/app/meeting/[id]/MeetingDetailClient.tsx');
  assert.match(detail, /durationSeconds=\{\(meeting\.audioKeys\?\.length \?\? 0\) <= 1 && \(meeting\.audioPartCount \?\? 0\) <= 1 \? meeting\.duration : undefined\}/);
});
