import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/macRelease.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
});
const exports = {};
vm.runInNewContext(outputText, { exports });
const { MAC_INSTALL_COMMAND, publishedVersion } = exports;

test('install command is exact', () => {
  assert.equal(MAC_INSTALL_COMMAND, 'curl -fsSL https://ttobak.atomai.click/mac/install.sh | sh');
});

test('publishedVersion accepts a semver string', () => {
  assert.equal(publishedVersion({ version: '0.2.0' }), '0.2.0');
});

test('publishedVersion rejects malformed manifests', () => {
  for (const value of [{}, { version: 1 }, '<html>', { version: 'x' }, null, undefined, []]) {
    assert.equal(publishedVersion(value), null);
  }
});
