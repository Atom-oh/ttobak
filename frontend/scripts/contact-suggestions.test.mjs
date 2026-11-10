import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/contactSuggestions.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

test('suggestions from a previous account, user or membership revision are never displayed', () => {
  const { visibleSuggestions, suggestionScope } = load();
  const scope = suggestionScope('account-a', 'user-a', 1);
  const previous = { scope, members: [{ name: 'Team A' }], contacts: [{ name: 'Customer A' }], nextCursor: '' };
  assert.equal(visibleSuggestions(previous, scope).members[0].name, 'Team A');
  assert.equal(visibleSuggestions(previous, suggestionScope('account-b', 'user-a', 1)).members.length, 0);
  assert.equal(visibleSuggestions(previous, suggestionScope('account-a', 'user-b', 1)).contacts.length, 0);
  assert.equal(visibleSuggestions(previous, suggestionScope('account-a', 'user-a', 2)).contacts.length, 0);
});

test('speaker bindings keep only unedited, latest picks for saved labels', () => {
  const { bindingsToSave } = load();
  const pick = (label, id, name) => ({ label, kind: 'contact', id, name });
  assert.equal(bindingsToSave({ spk_0: 'Kim edited' }, [pick('spk_0', 'c1', 'Kim')]).length, 0);
  const latest = bindingsToSave({ spk_0: 'Lee' }, [pick('spk_0', 'c1', 'Kim'), pick('spk_0', 'c2', 'Lee')]);
  assert.equal(latest.map(p => p.id).join(','), 'c2');
  assert.equal(bindingsToSave({ spk_1: 'Kim' }, [pick('spk_0', 'c1', 'Kim')]).length, 0);
  assert.equal(bindingsToSave({ spk_0: 'Kim' }, [pick('spk_0', 'c2', 'Lee'), pick('spk_0', 'c1', 'Kim')]).length, 1);
});
