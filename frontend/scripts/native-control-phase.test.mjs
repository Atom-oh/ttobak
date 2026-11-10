import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/nativeControlPhase.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

const idle = { recording: false, step: null, startInFlight: false, importing: false, finalizing: false, flowBusy: false, pendingAudio: false };

test('an audio file import reports a busy uploading phase', () => {
  const { nativeControlPhase } = load();
  assert.equal(nativeControlPhase({ ...idle, importing: true }), 'uploading');
  // The page must feed the import state into the reported phase.
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /nativeControlPhase\(\{[^}]*importing: uploadProgress !== null/);
});

// Every phase below must be in update_policy.rs BUSY_PHASES.
const BUSY = ['starting', 'recording', 'notes', 'creating', 'saving', 'uploading', 'redirecting'];

test('browser finalization before the notes step reports a busy phase', () => {
  const { nativeControlPhase } = load();
  // RecordButton clears isRecording at once; the notes step starts only after the backup await.
  const phase = nativeControlPhase({ ...idle, finalizing: true });
  assert.equal(phase, 'saving');
  assert.ok(BUSY.includes(phase));
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /nativeControlPhase\(\{[^}]*finalizing: browserFinalizing/);
});

test('the page flow reservation (draft creation, leftover upload) reports a busy phase', () => {
  const { nativeControlPhase } = load();
  const phase = nativeControlPhase({ ...idle, flowBusy: true });
  assert.equal(phase, 'creating');
  assert.ok(BUSY.includes(phase));
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /nativeControlPhase\(\{[^}]*flowBusy, pendingAudio: postRecording\.hasPendingAudio \}\)/);
});

test('any busy input yields a busy phase', () => {
  const { nativeControlPhase } = load();
  for (const key of ['recording', 'startInFlight', 'importing', 'finalizing', 'flowBusy', 'pendingAudio']) {
    assert.ok(BUSY.includes(nativeControlPhase({ ...idle, [key]: true })), key);
  }
});

test('recording and post-recording steps take precedence; otherwise starting or idle', () => {
  const { nativeControlPhase } = load();
  assert.equal(nativeControlPhase(idle), 'idle');
  assert.equal(nativeControlPhase({ ...idle, startInFlight: true }), 'starting');
  assert.equal(nativeControlPhase({ ...idle, step: 'notes', importing: true }), 'notes');
  assert.equal(nativeControlPhase({ ...idle, recording: true, step: 'notes', importing: true }), 'recording');
  assert.equal(nativeControlPhase({ ...idle, importing: true, startInFlight: true }), 'uploading');
});

test('unsaved browser audio held for retry keeps the app busy', () => {
  const { nativeControlPhase } = load();
  // usePostRecording keeps the Blob at step error so handleRetry can resume.
  assert.equal(nativeControlPhase({ ...idle, step: 'error', pendingAudio: true }), 'saving');
  assert.equal(nativeControlPhase({ ...idle, step: 'error' }), 'error');
  assert.equal(nativeControlPhase({ ...idle, step: 'notes', pendingAudio: true }), 'notes');
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /nativeControlPhase\(\{[^}]*pendingAudio: postRecording\.hasPendingAudio/);
});
