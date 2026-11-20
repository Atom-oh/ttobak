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
  const overlay = loadModule('lib/captionOverlay.ts', { './liveTranslation': liveTranslation, './interpretSpeakers': loadModule('lib/interpretSpeakers.ts') });
  const calls = [];
  const pushes = [];
  const errors = [];
  const controller = new overlay.CaptionOverlayController({
    translate: async (text, sourceLang, targetLang, isFinal, context) => {
      calls.push({ text, sourceLang, targetLang, isFinal, context });
      if (translateImpl) return translateImpl(text, sourceLang, targetLang);
      return { translatedText: `[${targetLang}] ${text}`, engine: 'llm' };
    },
    push: async (lang, kind, id, order, text, isFinal, ...rest) => {
      pushes.push({ lang, kind, id, order, text, isFinal, ...(rest.length ? { speaker: rest[0] } : {}) });
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
  const overlay = loadModule('lib/captionOverlay.ts', { './liveTranslation': liveTranslation, './interpretSpeakers': loadModule('lib/interpretSpeakers.ts') }, {
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

test('a final segment carries its speaker to the window; odd labels carry none', async () => {
  const { controller, pushes } = fixture();
  controller.setLanguages(['en']);
  controller.update('s1', '안녕하세요 여러분 반갑습니다', true, 'ko-KR', '1');
  await flush();
  // The only speaker so far is the main speaker: slot 0, the configured colour.
  assert.equal(entryPushes(pushes, 'en').at(-1).speaker, 0);
  for (const [id, label] of [['s2', undefined], ['s3', 'spk_0'], ['s4', '30']]) {
    controller.update(id, '오늘 회의를 시작하겠습니다', true, 'ko-KR', label);
    await flush();
    const last = entryPushes(pushes, 'en').at(-1);
    assert.equal(last.id, id);
    assert.equal('speaker' in last, false, String(label));
  }
});

test('the main speaker is pushed as slot 0 and trades slots with label 0', async () => {
  const { controller, pushes } = fixture();
  controller.setLanguages(['en']);
  const say = async (id, label) => {
    controller.update(id, '오늘 회의를 시작하겠습니다', true, 'ko-KR', label);
    await flush();
    return entryPushes(pushes, 'en').at(-1).speaker;
  };
  // Label 0 speaks first and is main; label 2 is a guest.
  assert.equal(await say('a1', '0'), 0);
  assert.equal(await say('b1', '2'), 2);
  // Label 2 pulls ahead by the margin and becomes main: it takes slot 0 and
  // label 0 takes label 2's slot. Lines already shown are not re-sent.
  for (const id of ['b2', 'b3', 'b4']) await say(id, '2');
  assert.equal(await say('b5', '2'), 0);
  assert.equal(await say('a2', '0'), 2);
  assert.equal(await say('c1', '4'), 4);
  assert.equal(pushes.filter((push) => push.id === 'a1').length, 1, 'no re-send of earlier lines');
});

function correctionFixture(correct, switches = {}) {
  const liveTranslation = loadModule('lib/liveTranslation.ts');
  const overlay = loadModule('lib/captionOverlay.ts', { './liveTranslation': liveTranslation, './interpretSpeakers': loadModule('lib/interpretSpeakers.ts') });
  const pushes = [];
  const errors = [];
  const asked = [];
  const controller = new overlay.CaptionOverlayController({
    translate: async (text, _source, targetLang) => ({ translatedText: `[${targetLang}] ${text}`, engine: 'llm' }),
    push: async (lang, kind, id, order, text, isFinal, ...rest) => {
      pushes.push({ lang, kind, id, order, text, isFinal, ...(rest.length ? { speaker: rest[0] } : {}) });
    },
    correct: async (text, lang, context) => {
      asked.push({ text, lang, context });
      return correct(text, lang, context);
    },
    onError: (code) => errors.push(code),
    ...switches,
  });
  return { controller, pushes, errors, asked };
}

test('a same-language final shows the recognizer text, then the corrected text', async () => {
  const { controller, pushes, asked } = correctionFixture(async () => ({ correctedText: '화면 공유되고 있나요?' }));
  controller.setLanguages(['ko', 'en']);
  controller.update('c1', '가면 공유되고 있나요?', true, 'ko-KR');
  await flush();
  const ko = entryPushes(pushes, 'ko').filter((p) => p.isFinal).map((p) => p.text);
  assert.deepEqual(ko, ['가면 공유되고 있나요?', '화면 공유되고 있나요?']);
  // Only the window in the spoken language asks; the other window translates.
  assert.deepEqual(asked.map((a) => a.lang), ['ko']);
  assert.equal(entryPushes(pushes, 'en').at(-1).text, '[en] 가면 공유되고 있나요?');
  controller.stop();
});

test('drafts and an unchanged correction push nothing extra', async () => {
  const { controller, pushes, asked } = correctionFixture(async (text) => ({ correctedText: text }));
  controller.setLanguages(['ko']);
  controller.update('c1', '회의는 세 시에', false, 'ko-KR');
  await flush();
  assert.equal(asked.length, 0, 'a draft is never corrected');
  controller.update('c1', '회의는 세 시에 시작합니다.', true, 'ko-KR');
  await flush();
  assert.equal(asked.length, 1);
  assert.equal(entryPushes(pushes, 'ko').filter((p) => p.isFinal).length, 1);
  controller.stop();
});

test('a failed correction keeps the recognizer text and reports no error', async () => {
  const { controller, pushes, errors } = correctionFixture(async () => {
    throw Object.assign(new Error('x'), { code: 'timeout' });
  });
  controller.setLanguages(['ko']);
  controller.update('c1', '망블리 환경입니다.', true, 'ko-KR');
  await flush();
  assert.deepEqual(entryPushes(pushes, 'ko').filter((p) => p.isFinal).map((p) => p.text), ['망블리 환경입니다.']);
  assert.deepEqual(errors, []);
  controller.stop();
});

test('a Mac app without the correction command is asked once', async () => {
  const { controller, asked } = correctionFixture(async () => {
    throw 'Command caption_correct not found';
  });
  controller.setLanguages(['ko']);
  controller.update('c1', '첫 번째 문장입니다.', true, 'ko-KR');
  await flush();
  controller.update('c2', '두 번째 문장입니다.', true, 'ko-KR');
  await flush();
  assert.equal(asked.length, 1);
  controller.stop();
});

test('the correction switch is read per caption', async () => {
  let on = false;
  const { controller, asked } = correctionFixture(async (text) => ({ correctedText: text }), { correctionEnabled: () => on });
  controller.setLanguages(['ko']);
  controller.update('c1', '첫 번째 문장입니다.', true, 'ko-KR');
  await flush();
  assert.equal(asked.length, 0);
  on = true;
  controller.update('c2', '두 번째 문장입니다.', true, 'ko-KR');
  await flush();
  assert.equal(asked.length, 1);
  controller.stop();
});

test('with speaker colours off no speaker reaches the window', async () => {
  const { controller, pushes } = correctionFixture(async (text) => ({ correctedText: text }), { speakerColorsEnabled: () => false });
  controller.setLanguages(['en']);
  controller.update('s1', '안녕하세요 여러분 반갑습니다', true, 'ko-KR', '0');
  controller.update('s2', '오늘 회의를 시작하겠습니다', true, 'ko-KR', '1');
  await flush();
  const finals = entryPushes(pushes, 'en').filter((p) => p.isFinal);
  assert.equal(finals.length, 2);
  assert.ok(finals.every((p) => !('speaker' in p)));
  controller.stop();
});

test('the correction wrapper invokes the Mac command', async () => {
  const invoked = [];
  const window = { __TAURI__: { core: { invoke: async (cmd, args) => { invoked.push({ cmd, args }); return { correctedText: 'x' }; } } } };
  const overlay = loadModule('lib/captionOverlay.ts', {
    './liveTranslation': loadModule('lib/liveTranslation.ts'), './interpretSpeakers': loadModule('lib/interpretSpeakers.ts'),
  }, { window });
  await overlay.correctCaption('가면 공유', 'ko', ['앞 문장']);
  assert.deepEqual(JSON.parse(JSON.stringify(invoked)), [{ cmd: 'caption_correct', args: { text: '가면 공유', lang: 'ko', context: ['앞 문장'] } }]);
});

test('slide terms ride along with translation and correction until cleared', async () => {
  const liveTranslation = loadModule('lib/liveTranslation.ts');
  const overlay = loadModule('lib/captionOverlay.ts', { './liveTranslation': liveTranslation, './interpretSpeakers': loadModule('lib/interpretSpeakers.ts') });
  const translated = [];
  const corrected = [];
  const controller = new overlay.CaptionOverlayController({
    translate: async (...args) => { translated.push(args); return { translatedText: 'ok', engine: 'llm' }; },
    correct: async (...args) => { corrected.push(args); return { correctedText: args[0] }; },
    push: async () => {},
    onError: () => {},
  });
  controller.setLanguages(['ko', 'en']);
  controller.update('g1', '엔진엑스를 올립니다.', true, 'ko-KR');
  await flush();
  assert.equal(translated.at(-1).length, 5, 'no glossary argument without terms');
  assert.equal(corrected.at(-1).length, 3);

  controller.setGlossary(['Nginx', 'Java 런타임']);
  controller.update('g2', '자바 런타임을 올립니다.', true, 'ko-KR');
  await flush();
  assert.deepEqual([...translated.at(-1)[5]], ['Nginx', 'Java 런타임']);
  assert.deepEqual([...corrected.at(-1)[3]], ['Nginx', 'Java 런타임']);

  controller.setGlossary([]);
  controller.update('g3', '세 번째 문장입니다.', true, 'ko-KR');
  await flush();
  assert.equal(translated.at(-1).length, 5);
  controller.setGlossary(Array.from({ length: 200 }, (_, n) => `t${n}`));
  controller.update('g4', '네 번째 문장입니다.', true, 'ko-KR');
  await flush();
  assert.equal(translated.at(-1)[5].length, overlay.MAX_GLOSSARY_TERMS);
  controller.stop();
});

test('glossary wrappers add the terms only when there are some', async () => {
  const invoked = [];
  const window = { __TAURI__: { core: { invoke: async (cmd, args) => { invoked.push({ cmd, args }); return {}; } } } };
  const overlay = loadModule('lib/captionOverlay.ts', {
    './liveTranslation': loadModule('lib/liveTranslation.ts'), './interpretSpeakers': loadModule('lib/interpretSpeakers.ts'),
  }, { window });
  await overlay.translateCaption('a', 'ko', 'en', true, []);
  await overlay.translateCaption('a', 'ko', 'en', true, [], ['Nginx']);
  await overlay.correctCaption('a', 'ko', [], []);
  await overlay.extractCaptionGlossary('슬라이드 글');
  const plain = JSON.parse(JSON.stringify(invoked));
  assert.equal('glossary' in plain[0].args, false);
  assert.deepEqual(plain[1].args.glossary, ['Nginx']);
  assert.equal('glossary' in plain[2].args, false);
  assert.deepEqual(plain[3], { cmd: 'caption_glossary', args: { slides: '슬라이드 글' } });
});

test('slow corrections never hold back later captions', async () => {
  const pending = [];
  const { controller, pushes, asked } = correctionFixture(() => new Promise((resolve) => { pending.push(resolve); }));
  controller.setLanguages(['ko']);
  for (let n = 1; n <= 6; n++) controller.update(`q${n}`, `${n}번째 문장입니다.`, true, 'ko-KR');
  await flush();
  // Every recognizer text is in the window although no correction has answered.
  assert.deepEqual(entryPushes(pushes, 'ko').filter((p) => p.isFinal).map((p) => p.id), ['q1', 'q2', 'q3', 'q4', 'q5', 'q6']);
  // At most three corrections run at once; the rest keep the recognizer text.
  assert.equal(asked.length, 3);
  pending[0]({ correctedText: '고친 첫 문장입니다.' });
  await flush();
  assert.equal(entryPushes(pushes, 'ko').at(-1).text, '고친 첫 문장입니다.');
  controller.update('q7', '7번째 문장입니다.', true, 'ko-KR');
  await flush();
  assert.equal(asked.length, 4, 'a finished correction frees its place');
  controller.stop();
});

test('a corrected line keeps the colour slot it was first sent with', async () => {
  const pending = [];
  const { controller, pushes } = correctionFixture(() => new Promise((resolve) => { pending.push(resolve); }));
  controller.setLanguages(['ko']);
  controller.update('k1', '첫 화자의 문장입니다.', true, 'ko-KR', '1');
  await flush();
  assert.equal(entryPushes(pushes, 'ko').at(-1).speaker, 0);
  // Another speaker takes over as the main speaker before the correction answers.
  for (let n = 0; n < 5; n++) controller.update(`o${n}`, `다른 화자의 ${n}번 문장입니다.`, true, 'ko-KR', '0');
  await flush();
  pending[0]({ correctedText: '첫 화자의 고친 문장입니다.' });
  await flush();
  const corrected = entryPushes(pushes, 'ko').at(-1);
  assert.equal(corrected.id, 'k1');
  assert.equal(corrected.speaker, 0, 'unchanged although speaker 1 is no longer the main speaker');
  controller.stop();
});
