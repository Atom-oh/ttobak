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
    setTimeout, clearTimeout, AbortController, TextEncoder, Date, Promise,
    ...globals,
  });
  return exports;
}

const tick = () => new Promise((resolve) => setImmediate(resolve));
const flush = async () => { for (let i = 0; i < 5; i++) await tick(); };

function fixture(translateImpl) {
  const liveTranslation = loadModule('lib/liveTranslation.ts');
  const overlay = loadModule('lib/captionOverlay.ts', { './liveTranslation': liveTranslation });
  const calls = [];
  const pushes = [];
  const errors = [];
  const controller = new overlay.CaptionOverlayController({
    translate: async (text, sourceLang, targetLang, isFinal, context) => {
      calls.push({ text, sourceLang, targetLang, isFinal, context });
      if (translateImpl) return translateImpl(text, sourceLang, targetLang);
      return { translatedText: `[${targetLang}] ${text}`, engine: 'llm' };
    },
    push: async (lang, kind, id, order, text, isFinal) => {
      pushes.push({ lang, kind, id, order, text, isFinal });
    },
    onError: (code) => errors.push(code),
  });
  return { overlay, controller, calls, pushes, errors };
}

const entryPushes = (pushes, lang) => pushes.filter((p) => p.lang === lang && p.kind === 'entry');

test('fans out to every open language', async () => {
  const { controller, pushes, calls } = fixture();
  controller.setLanguages(['ko', 'en', 'zh']);
  controller.update('u1', '안녕하세요 여러분', true, 'ko-KR');
  await flush();
  const ko = entryPushes(pushes, 'ko').at(-1);
  assert.equal(ko.text, '안녕하세요 여러분');
  assert.equal(ko.isFinal, true);
  assert.equal(entryPushes(pushes, 'en').at(-1).text, '[en] 안녕하세요 여러분');
  assert.equal(entryPushes(pushes, 'zh').at(-1).text, '[zh] 안녕하세요 여러분');
  assert.equal(calls.length, 2);
  assert.ok(calls.every((c) => c.isFinal === true && Array.isArray(c.context)));
  controller.stop();
});

test('pending entries without a translation push nothing', async () => {
  const { controller, pushes } = fixture(() => new Promise(() => {}));
  controller.setLanguages(['en']);
  controller.update('u1', '안녕하세요', true, 'ko-KR');
  await flush();
  assert.equal(pushes.length, 0);
  controller.stop();
});

test('late interim does not replace a final', async () => {
  const { controller, pushes } = fixture();
  controller.setLanguages(['en']);
  controller.update('u1', '안녕하세요 여러분', true, 'ko-KR');
  await flush();
  const before = pushes.length;
  controller.update('u1', '안녕', false, 'ko-KR');
  await flush();
  assert.equal(pushes.length, before);
  assert.equal(entryPushes(pushes, 'en').at(-1).isFinal, true);
  controller.stop();
});

test('unselected source language still translates into open windows', async () => {
  const { controller, calls } = fixture();
  controller.setLanguages(['ko', 'en']);
  controller.update('u1', '你好世界', true);
  await flush();
  assert.equal(calls.length, 2);
  assert.ok(calls.every((c) => c.sourceLang === 'zh'));
  assert.deepEqual(calls.map((c) => c.targetLang).sort(), ['en', 'ko']);
  controller.stop();
});

test('expired credentials surface once and recover', async () => {
  let failing = true;
  const { controller, pushes, errors } = fixture(() => {
    if (failing) return Promise.reject({ code: 'credentials-expired', message: 'x' });
    return Promise.resolve({ translatedText: 'hello', engine: 'llm' });
  });
  controller.setLanguages(['en']);
  controller.update('u1', '안녕하세요', true, 'ko-KR');
  controller.update('u2', '반갑습니다', true, 'ko-KR');
  await flush();
  assert.deepEqual(errors, ['credentials-expired']);
  // Failed entries only clear their lines.
  assert.ok(pushes.length > 0 && pushes.every((p) => p.kind === 'entry' && p.text === ''));
  failing = false;
  controller.resetErrors();
  controller.update('u3', '다시 시작', true, 'ko-KR');
  await flush();
  assert.equal(entryPushes(pushes, 'en').at(-1).text, 'hello');
  assert.deepEqual(errors, ['credentials-expired']);
  controller.stop();
});

test('a failed final clears the line that showed its draft', async () => {
  let fail = false;
  const { controller, pushes } = fixture((text, sourceLang, targetLang) => {
    if (fail) return Promise.reject({ code: 'credentials-expired', message: 'x' });
    return Promise.resolve({ translatedText: `[${targetLang}] ${text}`, engine: 'translate' });
  });
  controller.setLanguages(['en']);
  controller.update('u1', '안녕하세요 여러분', false, 'ko-KR');
  await new Promise((resolve) => setTimeout(resolve, 600));
  await flush();
  assert.equal(entryPushes(pushes, 'en').at(-1).text, '[en] 안녕하세요 여러분');
  fail = true;
  controller.update('u1', '안녕하세요 여러분 반갑습니다', true, 'ko-KR');
  await flush();
  const last = entryPushes(pushes, 'en').at(-1);
  assert.equal(last.id, 'u1');
  assert.equal(last.text, '');
  controller.stop();
});

test('skipped fillers push nothing', async () => {
  const { controller, pushes } = fixture();
  controller.setLanguages(['en']);
  controller.update('u1', '음', true, 'ko-KR');
  await flush();
  assert.equal(entryPushes(pushes, 'en').length, 0);
  controller.stop();
});

test('a final longer than the push cap keeps its tail', async () => {
  const long = `${'가'.repeat(2000)}끝`;
  const { overlay, controller, pushes } = fixture(() => Promise.resolve({ translatedText: long, engine: 'translate' }));
  controller.setLanguages(['en']);
  controller.update('u1', 'hello there', true, 'ko-KR');
  await flush();
  const text = entryPushes(pushes, 'en').at(-1).text;
  const bytes = new TextEncoder().encode(text).length;
  assert.ok(bytes <= overlay.MAX_PUSH_TEXT_BYTES, `${bytes}`);
  assert.ok(bytes > overlay.MAX_PUSH_TEXT_BYTES - 3);
  assert.ok(text.endsWith('가끝'));
  assert.ok(long.endsWith(text));
  controller.stop();
});

test('tail cut keeps whole characters', () => {
  const { overlay } = fixture();
  assert.equal(overlay.MAX_PUSH_TEXT_BYTES, 4000);
  assert.equal(overlay.utf8Tail('abc', 10), 'abc');
  assert.equal(overlay.utf8Tail('a가b', 4), '가b');
  assert.equal(overlay.utf8Tail('a가b', 3), 'b');
  assert.equal(overlay.utf8Tail('x😀', 4), '😀');
  assert.equal(overlay.utf8Tail('x😀', 3), '');
});

test('closing a language stops its requests', async () => {
  const { controller, pushes } = fixture();
  controller.setLanguages(['ko', 'en']);
  controller.setLanguages(['ko']);
  controller.update('u1', '안녕하세요 여러분', true, 'ko-KR');
  await flush();
  assert.equal(entryPushes(pushes, 'en').length, 0);
  assert.equal(entryPushes(pushes, 'ko').length > 0, true);
  controller.stop();
});

test('clear and preview reach every language', async () => {
  const { controller, pushes } = fixture();
  controller.setLanguages(['ko', 'en']);
  controller.clear();
  controller.preview();
  await flush();
  assert.deepEqual(pushes.filter((p) => p.kind === 'clear').map((p) => p.lang).sort(), ['en', 'ko']);
  assert.deepEqual(pushes.filter((p) => p.kind === 'preview').map((p) => p.lang).sort(), ['en', 'ko']);
  controller.stop();
});

test('error messages', () => {
  const { overlay } = fixture();
  assert.equal(
    overlay.captionErrorMessage('credentials-expired'),
    '자격 증명이 만료됐어요. 터미널에서 mwinit을 실행한 뒤 다시 시도해 주세요.',
  );
  assert.equal(overlay.captionErrorMessage('profile-missing'), 'AWS 프로필을 먼저 선택해 주세요.');
  assert.equal(overlay.captionErrorMessage('rate-limited'), '번역 요청이 너무 많아 잠시 멈췄어요.');
  assert.match(overlay.captionErrorMessage('model-access'), /GPT-6 Luna/);
  assert.equal(overlay.captionErrorMessage('other'), '자막 번역에 실패했어요. 잠시 후 다시 시도해 주세요.');
});

test('wrappers invoke the Mac commands with camelCase arguments', async () => {
  const liveTranslation = loadModule('lib/liveTranslation.ts');
  const invoked = [];
  const overlay = loadModule('lib/captionOverlay.ts', { './liveTranslation': liveTranslation }, {
    window: { __TAURI__: { core: { invoke: async (cmd, args) => { invoked.push([cmd, args]); return {}; } } } },
  });
  await overlay.openCaptionWindow('ko');
  await overlay.pushCaption('en', 'entry', 'a', 3, 'hi', true);
  await overlay.translateCaption('hi', 'en', 'ko', false, ['x']);
  await overlay.setCaptionSettings('p', 'us-east-1', null);
  assert.deepEqual(invoked.map((i) => i[0]), [
    'caption_overlay_open', 'caption_overlay_push', 'caption_translate', 'caption_settings_set',
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(invoked[1][1])), { lang: 'en', kind: 'entry', id: 'a', order: 3, text: 'hi', isFinal: true });
  assert.deepEqual(JSON.parse(JSON.stringify(invoked[2][1])), { text: 'hi', sourceLang: 'en', targetLang: 'ko', isFinal: false, context: ['x'] });
});

test('preview decision: automatic reopen at mount stays hidden unless arranging', () => {
  const { captionPreviewAction: act } = fixture().overlay;
  assert.equal(act('mount-reopen', false, false), 'clear');
  assert.equal(act('mount-reopen', false, true), 'preview');
  for (const expanded of [false, true]) {
    assert.equal(act('mount-reopen', true, expanded), 'none');
  }
});

test('preview decision: explicit open previews while arranging, else briefly', () => {
  const { captionPreviewAction: act } = fixture().overlay;
  assert.equal(act('toggle-open', false, true), 'preview');
  assert.equal(act('toggle-open', false, false), 'timed-preview');
  assert.equal(act('toggle-open', true, true), 'none');
  assert.equal(act('toggle-open', true, false), 'none');
});

test('preview decision: expanding previews and collapsing clears, never while recording', () => {
  const { captionPreviewAction: act } = fixture().overlay;
  assert.equal(act('settings-expand', false, true), 'preview');
  assert.equal(act('settings-collapse', false, false), 'clear');
  assert.equal(act('settings-expand', true, true), 'none');
  assert.equal(act('settings-collapse', true, false), 'none');
});

test('preview decision: recording start and stop clear the windows', () => {
  const { captionPreviewAction: act } = fixture().overlay;
  for (const expanded of [false, true]) {
    assert.equal(act('recording-start', true, expanded), 'clear');
    assert.equal(act('recording-stop', false, expanded), 'clear');
  }
});

test('timed preview lasts about four seconds', () => {
  assert.equal(fixture().overlay.TIMED_PREVIEW_MS, 4000);
});
