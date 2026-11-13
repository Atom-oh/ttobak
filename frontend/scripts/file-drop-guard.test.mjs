import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/fileDropGuard.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
const exports = {};
vm.runInNewContext(outputText, { exports });

function target() {
  const listeners = new Map();
  return {
    listeners,
    addEventListener(type, fn) { listeners.set(type, fn); },
    removeEventListener(type, fn) { if (listeners.get(type) === fn) listeners.delete(type); },
  };
}
const event = (types, defaultPrevented = false) => ({
  defaultPrevented, prevented: false, preventDefault() { this.prevented = true; }, dataTransfer: { types, dropEffect: 'copy' },
});

test('stray file drops are blocked so the Mac WebView never navigates to the file', () => {
  const win = target();
  const cleanup = exports.installStrayFileDropGuard(win);
  const over = event(['Files']);
  win.listeners.get('dragover')(over);
  assert.equal(over.prevented, true);
  assert.equal(over.dataTransfer.dropEffect, 'none');
  const drop = event(['Files']);
  win.listeners.get('drop')(drop);
  assert.equal(drop.prevented, true);
  cleanup();
  assert.equal(win.listeners.size, 0);
});

test('drops a zone already handled, and non-file drags, are left alone', () => {
  const win = target();
  exports.installStrayFileDropGuard(win);
  const handled = event(['Files'], true);
  win.listeners.get('dragover')(handled);
  assert.equal(handled.dataTransfer.dropEffect, 'copy');
  const textDrop = event(['text/plain']);
  win.listeners.get('drop')(textDrop);
  assert.equal(textDrop.prevented, false);
});
