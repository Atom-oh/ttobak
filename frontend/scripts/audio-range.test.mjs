import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load(name, globals = {}) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Blob, crypto: webcrypto, console, ...globals });
  return exports;
}

test('audio time fields accept hours and reject ambiguous or invalid boundaries', () => {
  const { parseAudioTime, formatAudioTime } = load('audioRange');
  assert.equal(parseAudioTime('01:00:00'), 3600);
  assert.equal(parseAudioTime('05:00:00'), 18000);
  assert.equal(parseAudioTime('59:59'), 3599);
  for (const invalid of ['', '-1', '01:60:00', '24:00:00', '01:00:60', 'Infinity', '1:2', '1:00.5']) {
    assert.equal(parseAudioTime(invalid), null, invalid);
  }
  assert.equal(formatAudioTime(18000), '05:00:00');
  assert.equal(formatAudioTime(Infinity), '00:00:00');
});
