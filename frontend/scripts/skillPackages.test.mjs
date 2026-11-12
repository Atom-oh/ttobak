import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(path) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, require: (id) => assert.fail(id) });
  return exports;
}

const pkg = loadModule('lib/skillPackageCopy.ts');
const RULES = ['not-zip', 'zip-too-large', 'too-many-entries', 'uncompressed-too-large', 'unsafe-path', 'not-regular-file',
  'top-level', 'no-skills', 'skill-missing-md', 'bad-skill-name', 'bad-server-name', 'bad-registry', 'bad-http-url',
  'report-skill-layout', 'bad-manifest', 'bad-server-list'];

test('every validation rule has Korean copy', () => {
  assert.equal(pkg.packageErrorText('INVALID_PACKAGE', 'unsafe-path: ../x'), 'zip 안에 허용되지 않는 경로가 있어요: ../x');
  for (const rule of RULES) {
    const text = pkg.packageErrorText('INVALID_PACKAGE', `${rule}: detail`);
    assert.ok(/[가-힣]/.test(text) && !text.startsWith(rule), `${rule}: ${text}`);
  }
  // A rule without a detail has no trailing colon.
  assert.equal(pkg.packageErrorText('INVALID_PACKAGE', 'no-skills'), pkg.packageErrorText('INVALID_PACKAGE', 'no-skills: '));
  assert.ok(!pkg.packageErrorText('INVALID_PACKAGE', 'no-skills').endsWith(':'));
});

test('other errors fall back to fixed copy, never raw server text', () => {
  assert.equal(pkg.packageErrorText('CONFLICT', 'x'), '다른 곳에서 패키지가 바뀌었어요. 새로고침한 뒤 다시 시도하세요.');
  assert.equal(pkg.packageErrorText('FORBIDDEN', 'x'), '조직 패키지는 관리자만 올릴 수 있어요.');
  assert.equal(pkg.packageErrorText('INTERNAL_ERROR', '<b>x</b>'), '패키지를 처리하지 못했어요. 잠시 후 다시 시도하세요.');
});

test('a file over 20 MiB is refused before any request', () => {
  // pkgTooLargeClient in docs/superpowers/plans/2026-11-12-one-click-intake-setup-ui-copy.json
  assert.equal(pkg.localFileError({ size: 20 * 1024 * 1024 + 1, name: 'a.zip' }), 'zip은 20MB 이하만 올릴 수 있어요.');
  assert.equal(pkg.localFileError({ size: 10, name: 'a.txt' }), 'zip 파일만 올릴 수 있어요.');
  assert.equal(pkg.localFileError({ size: 0, name: 'a.zip' }), 'zip 파일이 비어 있어요.');
  assert.equal(pkg.localFileError({ size: 10, name: 'A.ZIP' }), null);
});
