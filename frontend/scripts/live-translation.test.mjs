import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import * as jsxRuntime from 'react/jsx-runtime';

function loadModule(path, dependencies = {}, globals = {}) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
      jsx: ts.JsxEmit.ReactJSX,
    },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports,
    require: (name) => {
      assert.ok(name in dependencies, `Unexpected dependency: ${name}`);
      return dependencies[name];
    },
    setTimeout, clearTimeout, AbortController, TextEncoder, Date,
    ...globals,
  });
  return exports;
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

// Most cases below specify draft (interim) behavior, so they opt in; the
// product default is covered by 'draft translation is off unless opted in'.
function fixture({ targetLang = 'ko-en', enabled = true, honorAbort = true, interim = true } = {}) {
  let now = 0;
  let nextTimerId = 0;
  const timers = new Map();
  const entries = new Map();
  const updates = [];
  const requests = [];
  const translationModule = loadModule('lib/liveTranslation.ts', {}, {
    setTimeout(callback, delay) {
      const timerId = ++nextTimerId;
      timers.set(timerId, { callback, at: now + delay });
      return timerId;
    },
    clearTimeout: (timerId) => timers.delete(timerId),
  });
  const translator = new translationModule.LiveTranslation({
    enabled, targetLang, ...(interim === null ? {} : { interim }),
    onUpdate(entry) {
      entries.set(entry.id, entry);
      updates.push(entry);
    },
    onReset: () => entries.clear(),
    translate(text, sourceLang, destinationLang, signal) {
      return new Promise((resolve, reject) => {
        requests.push({ text, sourceLang, targetLang: destinationLang, signal, resolve, reject });
        if (honorAbort) signal.addEventListener('abort', () => reject(new Error('Aborted')), { once: true });
      });
    },
  });
  async function advance(milliseconds) {
    const until = now + milliseconds;
    while (true) {
      const due = [...timers].filter(([, timer]) => timer.at <= until)
        .sort((left, right) => left[1].at - right[1].at)[0];
      if (!due) break;
      now = due[1].at;
      timers.delete(due[0]);
      due[1].callback();
      await tick();
    }
    now = until;
    await tick();
  }
  return { translator, requests, entries, updates, advance, translationModule };
}

test('Korean and English finals translate independently, even when responses arrive in reverse order', async () => {
  const state = fixture();
  state.translator.update('first', '다음 주에 배포합니다.', true, 'ko-KR');
  state.translator.update('second', 'Sounds good.', true, 'en-US');
  assert.deepEqual(state.requests.map(({ sourceLang, targetLang }) => [sourceLang, targetLang]), [
    ['ko', 'en'], ['en', 'ko'],
  ]);
  state.requests[1].resolve({ translatedText: '좋습니다.' });
  await tick();
  state.requests[0].resolve({ translatedText: 'We deploy next week.' });
  await tick();
  const entries = [...state.entries.values()].sort((left, right) => left.order - right.order);
  assert.deepEqual(entries.map((entry) => entry.id), ['first', 'second']);
  assert.equal(entries[0].translated, 'We deploy next week.');
  assert.equal(entries[1].translated, '좋습니다.');
  assert.ok(entries.every((entry) => entry.status === 'complete'));
  state.translator.stop();
});

test('continuous partial updates do not postpone translation until the speaker stops', async () => {
  const state = fixture();
  state.translator.update('speech', '오늘', false, 'ko-KR');
  await state.advance(200);
  state.translator.update('speech', '오늘 배포', false, 'ko-KR');
  await state.advance(200);
  state.translator.update('speech', '오늘 배포합니다', false, 'ko-KR');
  await state.advance(100);
  assert.equal(state.requests.length, 1);
  assert.equal(state.requests[0].text, '오늘 배포합니다');
  state.translator.update('speech', '오늘 배포합니다 그리고', false, 'ko-KR');
  state.requests[0].resolve({ translatedText: 'We deploy today' });
  await tick();
  assert.equal(state.entries.get('speech').translated, 'We deploy today');
  assert.equal(state.entries.get('speech').isFinal, false);
  await state.advance(500);
  assert.equal(state.requests[1].text, '오늘 배포합니다 그리고');
  state.translator.stop();
});

test('a final replaces its partial in place and rejects a late provisional response', async () => {
  const state = fixture({ honorAbort: false });
  state.translator.update('speech', 'We ship', false, 'en-US');
  await state.advance(500);
  state.translator.update('speech', 'We ship tomorrow.', true, 'en-US');
  assert.equal(state.requests.length, 2);
  assert.equal(state.requests[0].signal.aborted, true);
  state.requests[1].resolve({ translatedText: '내일 출시합니다.' });
  await tick();
  state.requests[0].resolve({ translatedText: '출시합니다' });
  await tick();
  assert.equal(state.entries.size, 1);
  assert.equal(state.entries.get('speech').translated, '내일 출시합니다.');
  assert.equal(state.entries.get('speech').status, 'complete');
  state.translator.stop();
});

test('language changes within a partial do not publish a translation in the old direction', async () => {
  const state = fixture();
  state.translator.update('speech', 'Hello', false, 'en-US');
  await state.advance(500);
  state.translator.update('speech', '안녕하세요', false, 'ko-KR');
  state.requests[0].resolve({ translatedText: '안녕하세요' });
  await tick();
  assert.equal(state.entries.get('speech').translated, '');
  await state.advance(500);
  assert.equal(state.requests[1].sourceLang, 'ko');
  assert.equal(state.requests[1].targetLang, 'en');
  state.translator.stop();
});

test('revised partial text rejects translations of superseded words', async () => {
  const state = fixture();
  state.translator.update('speech', 'The law', false, 'en-US');
  await state.advance(500);
  state.translator.update('speech', 'The largest', false, 'en-US');
  state.requests[0].resolve({ translatedText: '법' });
  await tick();
  assert.equal(state.entries.get('speech').translated, '');
  await state.advance(500);
  assert.equal(state.requests[1].text, 'The largest');
  state.translator.stop();
});

test('disabling interpretation cancels requests and rejects old results after re-enabling', async () => {
  const state = fixture({ honorAbort: false });
  state.translator.update('old', 'Hello.', true, 'en-US');
  state.translator.updateEnabled(false);
  assert.equal(state.requests[0].signal.aborted, true);
  assert.equal(state.entries.size, 0);
  state.translator.updateEnabled(true);
  state.translator.update('new', '안녕.', true, 'ko-KR');
  state.requests[0].resolve({ translatedText: '이전 결과' });
  state.requests[1].resolve({ translatedText: 'Hello.' });
  await tick();
  assert.equal(state.entries.has('old'), false);
  assert.equal(state.entries.get('new').translated, 'Hello.');
  state.translator.stop();
});

test('changing target clears the old view and cannot relabel in-flight translations', async () => {
  const state = fixture({ honorAbort: false });
  state.translator.update('old', '안녕.', true, 'ko-KR');
  state.translator.updateTargetLang('ja');
  state.requests[0].resolve({ translatedText: 'Hello.' });
  await tick();
  assert.equal(state.entries.size, 0);
  state.translator.update('new', '안녕.', true, 'ko-KR');
  assert.equal(state.requests[1].targetLang, 'ja');
  state.requests[1].resolve({ translatedText: 'こんにちは。' });
  await tick();
  assert.equal(state.entries.get('new').targetLang, 'ja');
  state.translator.stop();
});

test('final failures retain originals, surface failure, and release the queue', async () => {
  const state = fixture();
  state.translator.update('first', '첫 문장', true, 'ko-KR');
  state.translator.update('second', 'Second sentence', true, 'en-US');
  state.translator.update('third', '셋째 문장', true, 'ko-KR');
  assert.equal(state.requests.length, 2);
  state.requests[0].reject(new Error('Unavailable'));
  await tick();
  assert.equal(state.entries.get('first').original, '첫 문장');
  assert.equal(state.entries.get('first').status, 'error');
  assert.equal(state.requests.length, 3);
  state.translator.stop();
});

test('request timeouts release stalled translations without stopping the next utterance', async () => {
  const state = fixture();
  state.translator.update('first', '첫 문장', true, 'ko-KR');
  state.translator.update('second', '둘째 문장', true, 'ko-KR');
  state.translator.update('third', '셋째 문장', true, 'ko-KR');
  await state.advance(8000);
  assert.equal(state.requests[0].signal.aborted, true);
  assert.equal(state.entries.get('first').status, 'error');
  assert.equal(state.requests[2].text, '셋째 문장');
  state.translator.stop();
});

test('UTF-8 input and result bounds fail visibly instead of submitting oversized text', async () => {
  const state = fixture();
  state.translator.update('large', '한'.repeat(3001), true, 'ko-KR');
  assert.equal(state.requests.length, 0);
  assert.equal(state.entries.get('large').status, 'error');
  state.translator.update('normal', 'Hello.', true, 'en-US');
  state.requests[0].resolve({ translatedText: '한'.repeat(10001) });
  await tick();
  assert.equal(state.entries.get('normal').status, 'error');
  assert.equal(state.entries.get('normal').translated, '');
  state.translator.stop();
});

test('a stalled API has bounded concurrency and overflow is visible', () => {
  const state = fixture();
  for (let index = 0; index < 45; index++) {
    state.translator.update(`segment-${index}`, 'Synthetic sentence', true, 'en-US');
  }
  assert.equal(state.requests.length, 2);
  assert.equal(state.entries.get('segment-44').status, 'error');
  state.translator.stop();
});

test('disabled translation does no work and same-language single output needs no API request', async () => {
  const state = fixture({ enabled: false, targetLang: 'en' });
  state.translator.update('disabled', '안녕', true, 'ko-KR');
  assert.equal(state.entries.size, 0);
  state.translator.updateEnabled(true);
  state.translator.update('english', 'Hello.', true, 'en-US');
  await tick();
  assert.equal(state.requests.length, 0);
  assert.equal(state.entries.get('english').translated, 'Hello.');
  state.translator.stop();
});

test('missing language metadata is resolved from each utterance, not the previous one', () => {
  const state = fixture();
  state.translator.update('korean', '이번 release는 Friday까지', true);
  state.translator.update('english', 'Sounds good.', true);
  assert.equal(state.requests[0].sourceLang, 'ko');
  assert.equal(state.requests[1].sourceLang, 'en');
  state.translator.stop();
});

test('stop discards queued work and in-flight callbacks without publishing new captions', async () => {
  const state = fixture({ honorAbort: false });
  state.translator.update('first', 'Hello.', true, 'en-US');
  const count = state.updates.length;
  state.translator.stop();
  state.requests[0].resolve({ translatedText: '안녕.' });
  state.translator.update('second', '안녕.', true, 'ko-KR');
  await tick();
  assert.equal(state.updates.length, count);
});

test('bilingual rendering keeps Korean on the left, English on the right, including the first partial', async () => {
  const state = fixture();
  state.translator.update('english', 'Hello.', false, 'en-US');
  const { TranslationView } = loadModule('components/TranslationView.tsx', {
    react: React,
    'react/jsx-runtime': jsxRuntime,
    '@/lib/liveTranslation': state.translationModule,
  });
  const render = () => renderToStaticMarkup(React.createElement(TranslationView, {
    translations: [...state.entries.values()], targetLang: 'ko-en', onTargetLangChange() {}, isActive: false,
  }));
  const partial = render();
  assert.match(partial, /Hello\./);
  assert.match(partial, /통역 중/);
  assert.ok(partial.indexOf('lang="ko"') < partial.indexOf('lang="en"'));
  state.translator.update('english', 'Hello.', true, 'en-US');
  state.requests[0].resolve({ translatedText: '안녕하세요.' });
  await tick();
  const final = render();
  assert.ok(final.indexOf('안녕하세요.') < final.indexOf('Hello.'));
  state.translator.stop();
});

test('caption rendering treats recognized and translated markup as text', async () => {
  const state = fixture();
  state.translator.update('markup', '<SCRIPT data-example=true>example()</SCRIPT>', true, 'en-US');
  state.requests[0].resolve({ translatedText: '<IMG src=x onerror=example()>' });
  await tick();
  const { TranslationView } = loadModule('components/TranslationView.tsx', {
    react: React,
    'react/jsx-runtime': jsxRuntime,
    '@/lib/liveTranslation': state.translationModule,
  });
  const html = renderToStaticMarkup(React.createElement(TranslationView, {
    translations: [...state.entries.values()], targetLang: 'ko-en', onTargetLangChange() {}, isActive: false,
  }));
  assert.doesNotMatch(html, /<(?:script|img)\b/i);
  assert.match(html, /&lt;script\b/i);
  state.translator.stop();
});

test('a new partial segment is scheduled after an older partial response finishes', async () => {
  const state = fixture();
  state.translator.update('older', '첫 구절', false, 'ko-KR');
  await state.advance(500);
  state.translator.update('newer', 'Next phrase', false, 'en-US');
  state.requests[0].resolve({ translatedText: 'First phrase' });
  await tick();
  await state.advance(500);
  assert.equal(state.requests.length, 2);
  assert.equal(state.requests[1].text, 'Next phrase');
  state.translator.stop();
});

test('oversized source rendering is bounded and explicitly identifies abbreviation', () => {
  const state = fixture();
  state.translator.update('huge', '한'.repeat(100000), true, 'ko-KR');
  const entry = state.entries.get('huge');
  assert.equal(entry.original.length, 9000);
  assert.equal(entry.originalTruncated, true);
  assert.equal(entry.status, 'error');
  assert.equal(state.requests.length, 0);
  state.translator.stop();
});

test('streaming forwards segment language and ID and uses plural vocabularies with multilingual recognition', async () => {
  let commandInput;
  const transcripts = [];
  const { TranscribeStreamingSession } = loadModule('lib/transcribeStreamingClient.ts', {
    '@/lib/auth': { getIdToken: () => 'synthetic-token', refreshSession: async () => null },
    '@aws-sdk/client-transcribe-streaming': {
      StartStreamTranscriptionCommand: class { constructor(input) { this.input = input; } },
      TranscribeStreamingClient: class {
        async send(command) {
          commandInput = command.input;
          return { TranscriptResultStream: (async function* () {
            yield { TranscriptEvent: { Transcript: { Results: [{
              ResultId: 'utterance', IsPartial: false, LanguageCode: 'en-US',
              Alternatives: [{ Transcript: 'Hello.' }],
            }] } } };
          })() };
        }
      },
    },
    '@aws-sdk/credential-providers': { fromCognitoIdentityPool: () => ({}) },
  });
  const session = new TranscribeStreamingSession({
    region: 'synthetic-region', identityPoolId: 'synthetic-identity', userPoolId: 'synthetic-user',
    multiLanguage: true, vocabularyName: 'synthetic-vocabulary',
    onTranscript: (...args) => transcripts.push(args), onError() {},
  });
  await session.startNative();
  assert.equal(commandInput.IdentifyMultipleLanguages, true);
  assert.equal(commandInput.VocabularyNames, 'synthetic-vocabulary');
  assert.equal(commandInput.VocabularyName, undefined);
  assert.deepEqual(transcripts, [['Hello.', true, 'en-US', 'utterance']]);
  session.stop();
});

test('failed authoritative translation never retains a provisional translation for changed speech', async () => {
  const state = fixture();
  state.translator.update('speech', 'We can deploy.', false, 'en-US');
  await state.advance(500);
  state.requests[0].resolve({ translatedText: '배포할 수 있습니다.' });
  await tick();
  assert.equal(state.entries.get('speech').translated, '배포할 수 있습니다.');
  state.translator.update('speech', 'We cannot deploy.', true, 'en-US');
  assert.equal(state.entries.get('speech').translated, '');
  state.requests[1].reject(new Error('Unavailable'));
  await tick();
  assert.equal(state.entries.get('speech').original, 'We cannot deploy.');
  assert.equal(state.entries.get('speech').translated, '');
  assert.equal(state.entries.get('speech').status, 'error');
  state.translator.stop();
});

test('caption placeholders use Korean and only completed translations enter the live region', async () => {
  const state = fixture();
  state.translator.update('complete', 'Hello.', true, 'en-US');
  state.requests[0].resolve({ translatedText: '안녕하세요.' });
  await tick();
  state.translator.update('pending', '지금 말하는 중', false, 'ko-KR');
  const { TranslationView } = loadModule('components/TranslationView.tsx', {
    react: React,
    'react/jsx-runtime': jsxRuntime,
    '@/lib/liveTranslation': state.translationModule,
  });
  const html = renderToStaticMarkup(React.createElement(TranslationView, {
    translations: [...state.entries.values()], targetLang: 'ko-en', onTargetLangChange() {}, isActive: false,
  }));
  assert.match(html, /lang="ko"[^>]*>통역 중/);
  const announcements = html.slice(html.indexOf('role="log"'));
  assert.match(announcements, /aria-live="polite"/);
  assert.match(announcements, /안녕하세요/);
  assert.doesNotMatch(announcements, /지금 말하는 중|통역 중/);
  state.translator.stop();
});

test('single-target speech already in the target language displays one original column', async () => {
  const state = fixture({ targetLang: 'ko' });
  state.translator.update('korean', '한국어 원문입니다.', true, 'ko-KR');
  await tick();
  const { TranslationView } = loadModule('components/TranslationView.tsx', {
    react: React,
    'react/jsx-runtime': jsxRuntime,
    '@/lib/liveTranslation': state.translationModule,
  });
  const html = renderToStaticMarkup(React.createElement(TranslationView, {
    translations: [...state.entries.values()], targetLang: 'ko', onTargetLangChange() {}, isActive: false,
  }));
  assert.match(html, /grid grid-cols-1/);
  assert.doesNotMatch(html, /<span>번역<\/span>/);
  assert.equal(state.requests.length, 0);
  state.translator.stop();
});

test('caption updates preserve a reader scroll position and resume following near the bottom', () => {
  const state = fixture();
  const container = { scrollHeight: 1000, scrollTop: 0, clientHeight: 300 };
  const references = [{ current: container }, { current: true }];
  let referenceIndex = 0;
  let effect;
  const { TranslationView } = loadModule('components/TranslationView.tsx', {
    react: {
      useRef: () => references[referenceIndex++],
      useEffect: (callback) => { effect = callback; },
    },
    'react/jsx-runtime': jsxRuntime,
    '@/lib/liveTranslation': state.translationModule,
  });
  function scrollElement(element) {
    if (typeof element?.props?.onScroll === 'function') return element;
    for (const child of React.Children.toArray(element?.props?.children)) {
      const found = scrollElement(child);
      if (found) return found;
    }
  }
  const render = () => {
    referenceIndex = 0;
    const view = TranslationView({
      translations: [{
        id: 'speech', order: 0, original: 'Hello', translated: '', sourceLang: 'en',
        targetLang: 'ko', timestamp: '2026-10-11T09:00:00Z', isFinal: false, status: 'pending',
      }],
      targetLang: 'ko-en', onTargetLangChange() {}, isActive: true,
    });
    effect();
    return scrollElement(view);
  };
  const scroll = render();
  container.scrollTop = 100;
  scroll.props.onScroll({ currentTarget: container });
  container.scrollHeight = 1500;
  render();
  assert.equal(container.scrollTop, 100);
  container.scrollTop = 1200;
  scroll.props.onScroll({ currentTarget: container });
  container.scrollHeight = 1700;
  render();
  assert.equal(container.scrollTop, 1700);
  state.translator.stop();
});

test('the translation deadline settles even while authentication ignores cancellation', async () => {
  const state = fixture({ honorAbort: false });
  state.translator.update('first', '첫 문장', true, 'ko-KR');
  state.translator.update('second', 'Second sentence', true, 'en-US');
  state.translator.update('third', '셋째 문장', true, 'ko-KR');
  await state.advance(8000);
  assert.equal(state.requests.length, 3);
  assert.equal(state.entries.get('first').status, 'error');
  state.requests[0].resolve({ translatedText: 'Late result' });
  await tick();
  assert.equal(state.entries.get('first').translated, '');
  assert.equal(state.entries.get('first').status, 'error');
  state.translator.stop();
});

test('draft translation is off unless opted in; finals still translate', async () => {
  const state = fixture({ interim: null });
  state.translator.update('speech', '오늘 배포', false, 'ko-KR');
  await state.advance(2000);
  assert.equal(state.requests.length, 0);
  assert.equal(state.entries.get('speech').original, '오늘 배포');
  assert.equal(state.entries.get('speech').translated, '');
  state.translator.update('speech', '오늘 배포합니다.', true, 'ko-KR');
  assert.equal(state.requests.length, 1);
  assert.equal(state.requests[0].text, '오늘 배포합니다.');
  state.requests[0].resolve({ translatedText: 'We deploy today.' });
  await tick();
  assert.equal(state.entries.get('speech').status, 'complete');
  state.translator.stop();
});

test('turning drafts off cancels the pending draft without marking it failed', async () => {
  const state = fixture({ honorAbort: false });
  state.translator.update('speech', 'We ship', false, 'en-US');
  await state.advance(500);
  assert.equal(state.requests.length, 1);
  state.translator.updateInterim(false);
  assert.equal(state.requests[0].signal.aborted, true);
  state.requests[0].resolve({ translatedText: '출시합니다' });
  await tick();
  assert.equal(state.entries.get('speech').status, 'pending');
  assert.equal(state.entries.get('speech').translated, '');
  state.translator.update('speech', 'We ship tomorrow', false, 'en-US');
  await state.advance(2000);
  assert.equal(state.requests.length, 1);
  state.translator.updateInterim(true);
  state.translator.update('speech', 'We ship tomorrow morning', false, 'en-US');
  await state.advance(500);
  assert.equal(state.requests.length, 2);
  state.translator.stop();
});

test('the draft toggle is labelled with its cost and only shown with interpretation on', () => {
  const { RecordingConfig } = loadModule('components/record/RecordingConfig.tsx', {
    react: React,
    'react/jsx-runtime': jsxRuntime,
    '@/lib/device': { hasMobileMicConflictRisk: () => false },
    '@/lib/liveTranslation': loadModule('lib/liveTranslation.ts'),
  });
  const render = (translationEnabled, interimTranslation) => renderToStaticMarkup(React.createElement(RecordingConfig, {
    summaryInterval: 100, onSummaryIntervalChange() {}, translationEnabled, onTranslationToggle() {},
    targetLang: 'ko-en', onTargetLangChange() {}, interimTranslation, onInterimTranslationToggle() {},
  }));
  assert.doesNotMatch(render(false, false), /초안 번역/);
  const draftInput = (html) => {
    const label = html.indexOf('초안 번역 (비용↑)');
    assert.ok(label > 0, 'draft toggle is missing');
    return html.slice(html.lastIndexOf('<input', label), label);
  };
  assert.doesNotMatch(draftInput(render(true, false)), /checked/);
  assert.match(draftInput(render(true, true)), /checked/);
});
