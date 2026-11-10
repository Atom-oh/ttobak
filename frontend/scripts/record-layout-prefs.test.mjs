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

const ALL_ON = { translation: true, summary: true, notes: true, transcript: true };

test('card prefs default to every card shown and survive corrupt values', () => {
  assert.deepEqual({ ...prefs.readRecordCardPrefs(memory()) }, ALL_ON);
  assert.deepEqual({ ...prefs.readRecordCardPrefs(memory({ 'ttobak-record-cards': '{not json' })) }, ALL_ON);
  assert.deepEqual({ ...prefs.readRecordCardPrefs(memory({ 'ttobak-record-cards': '[1,2]' })) }, ALL_ON);
  assert.deepEqual({ ...prefs.readRecordCardPrefs(memory({ 'ttobak-record-cards': 'null' })) }, ALL_ON);
  assert.deepEqual(
    { ...prefs.readRecordCardPrefs(memory({ 'ttobak-record-cards': '{"summary":false,"notes":"no","bogus":false}' })) },
    { ...ALL_ON, summary: false },
  );
  assert.deepEqual({ ...prefs.readRecordCardPrefs(throwing) }, ALL_ON);
  // A stored state with no main card is not honoured.
  assert.deepEqual(
    { ...prefs.readRecordCardPrefs(memory({ 'ttobak-record-cards': '{"translation":false,"summary":false,"notes":false,"transcript":false}' })) },
    ALL_ON,
  );
});

test('card prefs write one JSON value', () => {
  const store = memory();
  prefs.writeRecordCardPrefs({ ...ALL_ON, notes: false }, store);
  assert.deepEqual(JSON.parse(store.data['ttobak-record-cards']), { ...ALL_ON, notes: false });
  assert.doesNotThrow(() => prefs.writeRecordCardPrefs(ALL_ON, throwing));
});

test('effective prefs ignore translation when it is off and never leave zero main cards', () => {
  const onlyTranslation = { translation: true, summary: false, notes: false, transcript: false };
  assert.deepEqual({ ...prefs.effectiveCardPrefs(onlyTranslation, true) }, onlyTranslation);
  const fallback = prefs.effectiveCardPrefs(onlyTranslation, false);
  assert.equal(fallback.summary && fallback.notes, true);
  assert.equal(prefs.visibleMainCount(onlyTranslation, false), 0);
  assert.equal(prefs.visibleMainCount(ALL_ON, false), 3);
  assert.equal(prefs.visibleMainCount(ALL_ON, true), 4);
});

test('toggling the last visible main card is ignored', () => {
  const one = { translation: false, summary: true, notes: false, transcript: false };
  assert.equal(prefs.toggleCard(one, 'summary', false, true), one);
  assert.deepEqual({ ...prefs.toggleCard(one, 'notes', true, true) }, { ...one, notes: true });
  // Translation off: an enabled translation pref does not count as visible.
  const hiddenTranslation = { ...one, translation: true };
  assert.equal(prefs.toggleCard(hiddenTranslation, 'summary', false, false), hiddenTranslation);
  assert.deepEqual({ ...prefs.toggleCard(hiddenTranslation, 'summary', false, true) }, { ...hiddenTranslation, summary: false });
});

test('menu items list translation only when enabled and lock the last card', () => {
  const keys = (items) => [...items].map((i) => i.key);
  assert.deepEqual(keys(prefs.layoutMenuItems(ALL_ON, true, false)), ['summary', 'notes', 'transcript', 'sidePanel']);
  assert.deepEqual(keys(prefs.layoutMenuItems(ALL_ON, true, true)), ['translation', 'summary', 'notes', 'transcript', 'sidePanel']);
  assert.deepEqual([...prefs.layoutMenuItems(ALL_ON, false, true)].map((i) => i.label), ['통역', '실시간 요약', '메모', '실시간 자막', 'Q&A·자료']);
  assert.equal(prefs.layoutMenuItems(ALL_ON, false, true).at(-1).checked, false);
  assert.ok(prefs.layoutMenuItems(ALL_ON, true, true).every((i) => !i.locked));
  const items = prefs.layoutMenuItems({ translation: true, summary: false, notes: false, transcript: true }, true, false);
  assert.equal(items.find((i) => i.key === 'transcript').locked, true);
  assert.equal(items.find((i) => i.key === 'sidePanel').locked, false);
});

test('layout shares space between visible rows', () => {
  const full = prefs.recordLayout(ALL_ON, true, false);
  assert.equal(full.translationRow, 'flex-1 min-h-0');
  assert.match(full.pairRow, /flex-1 min-h-0 grid grid-rows-1 grid-cols-2/);
  assert.equal(full.summaryCard, 'min-h-0');
  assert.equal(full.transcriptRow, 'shrink-0');
  assert.equal(full.transcriptBodyShown, false);
  assert.equal(full.transcriptCollapsible, true);

  const noTranslation = prefs.recordLayout(ALL_ON, false, true);
  assert.equal(noTranslation.translationRow, 'hidden');
  assert.equal(noTranslation.transcriptRow, 'flex-1 min-h-0');
  assert.equal(noTranslation.transcriptBodyShown, true);

  const notesOnly = prefs.recordLayout({ ...ALL_ON, summary: false }, false, false);
  assert.match(notesOnly.pairRow, /grid-cols-1/);
  assert.equal(notesOnly.summaryCard, 'hidden');
  assert.equal(notesOnly.notesCard, 'min-h-0');

  // The transcript alone is always expanded and cannot be collapsed to a strip.
  const transcriptOnly = prefs.recordLayout({ translation: false, summary: false, notes: false, transcript: true }, true, false);
  assert.equal(transcriptOnly.pairRow, 'hidden');
  assert.equal(transcriptOnly.transcriptRow, 'flex-1 min-h-0');
  assert.equal(transcriptOnly.transcriptBodyShown, true);
  assert.equal(transcriptOnly.transcriptCollapsible, false);

  const hiddenTranscript = prefs.recordLayout({ ...ALL_ON, transcript: false }, false, true);
  assert.equal(hiddenTranscript.transcriptRow, 'hidden');
  assert.equal(hiddenTranscript.transcriptBodyShown, false);
});
