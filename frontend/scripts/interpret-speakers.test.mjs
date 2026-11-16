import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(read('../src/lib/interpretSpeakers.ts'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
});
const loaded = { exports: {} };
vm.runInNewContext(outputText, { module: loaded, exports: loaded.exports });
const { dominantSpeaker, speakerNumber, speakerColorIndex, rememberSpeaker, SPEAKER_COLOR_COUNT } = loaded.exports;

test('the speaker with most words names the result', () => {
  assert.equal(dominantSpeaker([{ Speaker: '1' }, {}, { Speaker: '0' }, { Speaker: '1' }]), '1');
  assert.equal(dominantSpeaker([{ Speaker: '0' }, { Speaker: '1' }]), '0', 'a tie keeps the first voice');
});

test('a result with no labelled word has no speaker', () => {
  assert.equal(dominantSpeaker([{}, { Speaker: '' }]), undefined);
  assert.equal(dominantSpeaker([]), undefined);
  assert.equal(dominantSpeaker(undefined), undefined);
});

test('only a numeric label is a speaker', () => {
  assert.equal(speakerNumber('0'), 0);
  assert.equal(speakerNumber('12'), 12);
  for (const label of [undefined, '', 'spk_0', '-1', '1.5', '1234']) {
    assert.equal(speakerNumber(label), null);
    assert.equal(speakerColorIndex(label), null);
  }
});

test('colours repeat after the palette', () => {
  assert.equal(speakerColorIndex('1'), 1);
  assert.equal(speakerColorIndex(String(SPEAKER_COLOR_COUNT + 2)), 2);
});

test('remembering a speaker keeps identity when nothing changes', () => {
  const start = { a: '0' };
  assert.equal(rememberSpeaker(start, 'a', '0'), start);
  assert.equal(rememberSpeaker(start, 'b', undefined), start);
  assert.deepEqual({ ...rememberSpeaker(start, 'b', '1') }, { a: '0', b: '1' });
  assert.deepEqual({ ...rememberSpeaker(start, 'a', '1') }, { a: '1' });
});

test('the oldest speakers are dropped past the cap', () => {
  let speakers = {};
  for (const id of ['a', 'b', 'c']) speakers = rememberSpeaker(speakers, id, '0', 2);
  assert.deepEqual(Object.keys(speakers), ['b', 'c']);
});

test('the interpret session asks for speaker labels and the page has one colour per slot', () => {
  assert.match(read('../src/hooks/useInterpretSession.ts'), /speakerLabels: true/);
  assert.match(read('../src/lib/transcribeStreamingClient.ts'), /if \(this\.config\.showSpeakerLabel\) commandInput\.ShowSpeakerLabel = true;/);
  const palette = read('../src/app/interpret/page.tsx').match(/const SPEAKER_TEXT = \[([\s\S]*?)\];/)[1];
  assert.equal(palette.split('\n').filter((row) => row.includes("'text-")).length, SPEAKER_COLOR_COUNT);
});

const { mainSpeaker, speakerColorSlot, MAIN_SPEAKER_LEAD } = loaded.exports;
const lines = (...labels) => Object.fromEntries(labels.map((label, at) => [`s${at}`, label]));

test('the speaker with the most lines is the main speaker, whatever its label', () => {
  assert.equal(mainSpeaker(lines('1', '0', '1', '1')), '1');
  assert.equal(mainSpeaker(lines('0', '1')), '0', 'a tie goes to the lower label');
  assert.equal(mainSpeaker({}), undefined);
  assert.equal(mainSpeaker(lines('x', 'y')), undefined, 'labels that are not speaker numbers do not count');
});

test('the main speaker keeps the role until another leads by a clear margin', () => {
  // "0" is main; "1" overtakes by one or two lines without taking over.
  assert.equal(mainSpeaker(lines('0', '0', '1', '1', '1'), '0'), '0');
  assert.equal(mainSpeaker(lines('0', '0', '1', '1', '1', '1'), '0'), '0');
  const ahead = lines('0', '0', ...Array(2 + MAIN_SPEAKER_LEAD).fill('1'));
  assert.equal(mainSpeaker(ahead, '0'), '1');
  // A main speaker whose lines all scrolled out of memory loses the role at once.
  assert.equal(mainSpeaker(lines('1'), '0'), '1');
});

test('the main speaker and unlabelled lines use the default colour; others get a slot', () => {
  assert.equal(speakerColorSlot('1', '1'), null);
  assert.equal(speakerColorSlot(undefined, '1'), null);
  assert.equal(speakerColorSlot('0', '1'), 0);
  assert.equal(speakerColorSlot('7', '1'), 7 % SPEAKER_COLOR_COUNT);
  assert.equal(speakerColorSlot('0', undefined), 0);
});

test('window slots: the main speaker takes slot 0 and label 0 takes the main speaker\'s number', () => {
  const { windowSpeakerSlot } = loaded.exports;
  assert.equal(windowSpeakerSlot(3, 3), 0);
  assert.equal(windowSpeakerSlot(0, 3), 3);
  assert.equal(windowSpeakerSlot(1, 3), 1);
  assert.equal(windowSpeakerSlot(0, 0), 0);
  assert.equal(windowSpeakerSlot(2, undefined), 2);
});
