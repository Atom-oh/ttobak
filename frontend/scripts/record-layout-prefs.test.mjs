import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(path, dependencies = {}, globals = {}) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports,
    require: (name) => {
      assert.ok(name in dependencies, `Unexpected dependency: ${name}`);
      return dependencies[name];
    },
    ...globals,
  });
  return exports;
}

const prefs = loadModule('lib/recordLayoutPrefs.ts');

function memory(initial = {}) {
  const data = { ...initial };
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = String(v); },
  };
}
const throwing = {
  getItem: () => { throw new Error('blocked'); },
  setItem: () => { throw new Error('blocked'); },
};

test('side panel defaults to open and accepts only known values', () => {
  assert.equal(prefs.readSidePanelPref(memory()), 'open');
  assert.equal(prefs.readSidePanelPref(memory({ 'ttobak-record-side-panel': 'closed' })), 'closed');
  assert.equal(prefs.readSidePanelPref(memory({ 'ttobak-record-side-panel': 'open' })), 'open');
  assert.equal(prefs.readSidePanelPref(memory({ 'ttobak-record-side-panel': 'bogus' })), 'open');
});

test('side panel writes the documented values', () => {
  const store = memory();
  prefs.writeSidePanelPref('closed', store);
  assert.equal(store.data['ttobak-record-side-panel'], 'closed');
  prefs.writeSidePanelPref('open', store);
  assert.equal(store.data['ttobak-record-side-panel'], 'open');
});

test('caption panel preference is 1/0 and null when unknown', () => {
  assert.equal(prefs.readCaptionPanelExpanded(memory()), null);
  assert.equal(prefs.readCaptionPanelExpanded(memory({ 'ttobak-caption-panel-expanded': '1' })), true);
  assert.equal(prefs.readCaptionPanelExpanded(memory({ 'ttobak-caption-panel-expanded': '0' })), false);
  assert.equal(prefs.readCaptionPanelExpanded(memory({ 'ttobak-caption-panel-expanded': 'yes' })), null);
  const store = memory();
  prefs.writeCaptionPanelExpanded(true, store);
  assert.equal(store.data['ttobak-caption-panel-expanded'], '1');
  prefs.writeCaptionPanelExpanded(false, store);
  assert.equal(store.data['ttobak-caption-panel-expanded'], '0');
});

test('throwing or missing storage falls back without throwing', () => {
  assert.equal(prefs.readSidePanelPref(throwing), 'open');
  assert.equal(prefs.readCaptionPanelExpanded(throwing), null);
  assert.doesNotThrow(() => prefs.writeSidePanelPref('closed', throwing));
  assert.doesNotThrow(() => prefs.writeCaptionPanelExpanded(true, throwing));
  assert.equal(prefs.readSidePanelPref(null), 'open');
  assert.doesNotThrow(() => prefs.writeSidePanelPref('closed', null));
});

test('window.localStorage access that throws is tolerated', () => {
  const globals = { window: { get localStorage() { throw new Error('denied'); } } };
  const mod = loadModule('lib/recordLayoutPrefs.ts', {}, globals);
  assert.equal(mod.readSidePanelPref(), 'open');
  assert.doesNotThrow(() => mod.writeSidePanelPref('closed'));
});
