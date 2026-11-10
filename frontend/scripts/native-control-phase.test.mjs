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

const idle = { recording: false, step: null, startInFlight: false, importing: false };

test('an audio file import reports a busy uploading phase', () => {
  const { nativeControlPhase } = load();
  assert.equal(nativeControlPhase({ ...idle, importing: true }), 'uploading');
  // The page must feed the import state into the reported phase.
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /nativeControlPhase\(\{[^}]*importing: uploadProgress !== null/);
});

test('recording and post-recording steps take precedence; otherwise starting or idle', () => {
  const { nativeControlPhase } = load();
  assert.equal(nativeControlPhase(idle), 'idle');
  assert.equal(nativeControlPhase({ ...idle, startInFlight: true }), 'starting');
  assert.equal(nativeControlPhase({ ...idle, step: 'notes', importing: true }), 'notes');
  assert.equal(nativeControlPhase({ ...idle, recording: true, step: 'notes', importing: true }), 'recording');
  assert.equal(nativeControlPhase({ ...idle, importing: true, startInFlight: true }), 'uploading');
});
