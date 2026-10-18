import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(name, deps = {}) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports, require: (id) => { assert.ok(id in deps, id); return deps[id]; },
    console: { warn() {}, error() {} }, AbortController,
  });
  return exports;
}

const live = loadModule('liveVocabulary');
const plain = (v) => JSON.parse(JSON.stringify(v));

test('well-formed vocabularies are sent one per stream language in option order; none sends nothing', () => {
  const both = { 'en-US': 'v-en', 'ko-KR': 'v-ko' };
  for (const [opts, want] of [
    [{ vocabularies: both, multiLanguage: true, languageOptions: 'ko-KR,en-US' }, { VocabularyNames: 'v-ko,v-en' }],
    [{ vocabularies: both, multiLanguage: true, languageOptions: 'en-US, ko-KR,ko-KR' }, { VocabularyNames: 'v-en,v-ko' }],
    [{ vocabularies: { 'ko-KR': 'v-ko' }, multiLanguage: true, languageOptions: 'ko-KR,en-US' }, { VocabularyNames: 'v-ko' }],
    [{ vocabularies: { 'en-US': 'v-en' }, multiLanguage: true, languageOptions: 'ko-KR,ja-JP' }, {}],
    [{ vocabularies: both, multiLanguage: false, languageCode: 'en-US' }, { VocabularyName: 'v-en' }],
    [{ vocabularies: { 'ko-KR': 'bad,name' }, multiLanguage: true, languageOptions: 'ko-KR,en-US' }, {}],
    [{ vocabularies: undefined, multiLanguage: true, languageOptions: 'ko-KR,en-US' }, {}],
  ]) assert.deepEqual(plain(live.vocabularyRequestFields(opts)), want, JSON.stringify(opts));
});

test('the retry is limited to one vocabulary BadRequest before any stream event', () => {
  const base = { error: { name: 'BadRequestException', message: 'The vocabulary was not found.' }, usedVocabulary: true, receivedEvent: false, alreadyRetried: false };
  assert.equal(live.shouldRetryWithoutVocabulary(base), true);
  assert.equal(live.shouldRetryWithoutVocabulary({ ...base, error: { $metadata: { httpStatusCode: 400 }, message: 'Vocabulary not READY' } }), true);
  for (const change of [{ error: { name: 'BadRequestException', message: 'Request timed out.' } },
    { error: { name: 'LimitExceededException', message: 'vocabulary' } }, { error: null },
    { usedVocabulary: false }, { receivedEvent: true }, { alreadyRetried: true }]) {
    assert.equal(live.shouldRetryWithoutVocabulary({ ...base, ...change }), false, JSON.stringify(change));
  }
});

function session(sends, vocabularies = { 'ko-KR': 'v-ko', 'en-US': 'v-en' }) {
  const f = { inputs: [], errors: [], texts: [], fallbacks: 0 };
  const { TranscribeStreamingSession } = loadModule('transcribeStreamingClient', {
    '@/lib/auth': { getIdToken: () => 'token', refreshSession: async () => 'token' },
    '@/lib/liveVocabulary': live,
    '@aws-sdk/client-transcribe-streaming': {
      StartStreamTranscriptionCommand: class { constructor(input) { this.input = input; } },
      TranscribeStreamingClient: class { async send(c) { f.inputs.push(c.input); return sends[f.inputs.length - 1](); } },
    },
    '@aws-sdk/credential-providers': { fromCognitoIdentityPool: () => ({}) },
  });
  f.s = new TranscribeStreamingSession({
    region: 'r', identityPoolId: 'i', userPoolId: 'u', multiLanguage: true, languageOptions: 'ko-KR,en-US', vocabularies,
    onTranscript: (t) => f.texts.push(t), onError: (e) => f.errors.push(e), onVocabularyFallback: () => { f.fallbacks++; },
  });
  return f;
}
const rejected = () => { throw Object.assign(new Error('The requested vocabulary could not be found.'), { name: 'BadRequestException' }); };
const stream = (body) => () => ({ TranscriptResultStream: body() });
const speech = stream(async function* () {
  yield { TranscriptEvent: { Transcript: { Results: [{ IsPartial: false, Alternatives: [{ Transcript: 'synthetic speech' }] }] } } };
});

test('a vocabulary BadRequest at stream start retries once without vocabulary', async () => {
  const f = session([rejected, speech]);
  await f.s.startNative();
  assert.deepEqual(f.inputs.map((i) => [i.VocabularyNames, i.IdentifyMultipleLanguages]), [['v-ko,v-en', true], [undefined, true]]);
  assert.equal(f.fallbacks, 1);
  assert.deepEqual(f.texts, ['synthetic speech']);
  assert.deepEqual(f.errors, ['transcribe-stream-error'], 'the later stream end stays the recoverable error');
  f.s.stop();
});

test('no inline retry without vocabulary, after an event, or for unrelated errors', async () => {
  const cases = [
    [session([rejected], {}), 0],
    [session([stream(async function* () { yield { TranscriptEvent: {} }; rejected(); })]), 1],
    [session([() => { throw Object.assign(new Error('Request timed out.'), { name: 'BadRequestException' }); }]), 0],
  ];
  for (const [f, fallbacks] of cases) {
    await f.s.startNative();
    assert.equal(f.inputs.length, 1);
    assert.equal(f.fallbacks, fallbacks, 'a mid-stream rejection still tells the owner to reconnect without it');
    assert.deepEqual(f.errors, ['transcribe-stream-error']);
    f.s.stop();
  }
});
