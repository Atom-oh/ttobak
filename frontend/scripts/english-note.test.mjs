import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/englishNote.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}
const view = (note, canEdit) => ({ ...load().englishNoteView(note, canEdit) });

test('no English note offers generation to editors only', () => {
  assert.deepEqual(view(null, true), { showToggle: false, action: 'generate', badge: null });
  assert.deepEqual(view(null, false), { showToggle: false, action: null, badge: null });
  assert.deepEqual(view({ status: 'unknown', stale: false }, true), { showToggle: false, action: 'generate', badge: null });
});

test('a running or queued run shows the generating badge and no action', () => {
  for (const status of ['queued', 'running']) {
    assert.deepEqual(view({ status, stale: false }, true), { showToggle: false, action: null, badge: 'generating' });
  }
  assert.deepEqual(view({ status: 'running', content: 'x', stale: false }, true), { showToggle: true, action: null, badge: 'generating' });
});

test('a failed run offers retry to editors', () => {
  assert.deepEqual(view({ status: 'failed', errorCode: 'TIMEOUT', stale: false }, true), { showToggle: false, action: 'retry', badge: 'failed' });
  assert.deepEqual(view({ status: 'failed', stale: false }, false), { showToggle: false, action: null, badge: 'failed' });
  assert.equal(view({ status: 'failed', content: 'x', stale: false }, true).showToggle, true);
});

test('a stored note shows the toggle; stale notes are flagged', () => {
  assert.deepEqual(view({ status: 'succeeded', content: 'x', stale: true }, true), { showToggle: true, action: 'regenerate', badge: 'stale' });
  assert.deepEqual(view({ status: 'succeeded', content: 'x', stale: true }, false), { showToggle: true, action: null, badge: 'stale' });
  assert.deepEqual(view({ status: 'succeeded', content: 'x', stale: false }, true), { showToggle: true, action: 'regenerate', badge: null });
});

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');

test('summary controls drop the sharing-safe label and the note-language select', () => {
  const controls = read('../src/components/meeting/ResummaryControls.tsx');
  assert.ok(!controls.includes('공유용'));
  assert.ok(!controls.includes('noteLanguages'));
  assert.ok(!controls.includes('이 언어로 다시 생성'));
  assert.ok(!read('../src/app/meeting/[id]/MeetingDetailClient.tsx').includes('onNoteLanguageChange'));
});

test('the summary card offers English generation, badges and a Korean/English toggle', () => {
  const card = read('../src/components/meeting/AISummaryCard.tsx');
  for (const label of ['영문 회의록 생성', '영문 회의록 다시 생성', '영문 회의록 생성 중', '영문 회의록 생성 실패', '원본이 바뀌었어요', '한국어', 'English']) {
    assert.ok(card.includes(label), label);
  }
});

test('linked meetings are navigation only', () => {
  assert.ok(!read('../src/components/meeting/LinkMeetingsModal.tsx').includes('컨텍스트'));
});

test('English note status loads on every authenticated visit, not only when a note exists', () => {
  const { shouldLoadEnglishNote } = load();
  assert.equal(shouldLoadEnglishNote(true), true);
  assert.equal(shouldLoadEnglishNote(false), false);
  const hook = read('../src/hooks/useEnglishNote.ts');
  assert.ok(hook.includes('shouldLoadEnglishNote(enabled)'));
  assert.ok(!hook.includes('hasNote'));
});

test('English request errors and run failures have Korean messages', () => {
  const { englishRequestErrors, englishFailures } = load();
  for (const code of ['MEETING_BUSY', 'SOURCE_NOT_READY', 'NO_SUMMARY_SOURCE', 'SHARING_REGEN_REQUIRED', 'SUMMARY_TOO_LARGE', 'SUMMARY_UNAVAILABLE', 'CONFLICT', 'FORBIDDEN']) {
    assert.match(englishRequestErrors[code] ?? '', /[가-힣]/, code);
  }
  for (const code of ['INVALID_OUTPUT', 'PROVIDER_FAILED', 'PERSISTENCE_FAILED', 'OUTPUT_TOO_LARGE', 'SOURCE_CHANGED', 'INTERRUPTED', 'PUBLISH_FAILED', 'TIMEOUT', 'ACCESS_REVOKED', 'SOURCE_MISSING', 'SOURCE_TOO_LARGE', 'SOURCE_UNAVAILABLE', 'SOURCE_NOT_READY', 'NO_SUMMARY_SOURCE']) {
    assert.match(englishFailures[code] ?? '', /[가-힣]/, code);
  }
});

test('switching meetings clears an aborted request flag and error', () => {
  const hook = read('../src/hooks/useEnglishNote.ts');
  const effect = hook.slice(hook.indexOf('alive.current = true;'), hook.indexOf('}, [meetingId]);'));
  assert.ok(effect.includes('setRequesting(false)'), 'meetingId effect must reset requesting');
  assert.ok(effect.includes('setError(null)'), 'meetingId effect must reset error');
});
