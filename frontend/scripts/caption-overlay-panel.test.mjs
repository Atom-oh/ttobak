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
    setTimeout, clearTimeout, AbortController, TextEncoder, Date, Promise,
    ...globals,
  });
  return exports;
}

const liveTranslation = loadModule('lib/liveTranslation.ts');
const overlay = loadModule('lib/captionOverlay.ts', { './liveTranslation': liveTranslation, './interpretSpeakers': loadModule('lib/interpretSpeakers.ts') });
const layoutPrefs = loadModule('lib/recordLayoutPrefs.ts');
const { CaptionOverlayPanel } = loadModule('components/record/CaptionOverlayPanel.tsx', {
  'react': React,
  'react/jsx-runtime': jsxRuntime,
  '@/lib/captionOverlay': overlay,
  '@/lib/recordLayoutPrefs': layoutPrefs,
});

const style = {
  fontFamily: 'Apple SD Gothic Neo', fontSize: 48, fontColor: '#FFFFFF',
  outlineColor: '#000000', outlineWidth: 3, backgroundOpacity: 0.4,
};

function props({ profile = 'dev', region = 'us-east-1', extraLanguage = null, windows = {}, ...overrides } = {}) {
  const noop = () => {};
  return {
    supported: true,
    view: {
      settings: { profile, region, extraLanguage, windows },
      profiles: ['dev', 'prod'],
      fonts: ['Apple SD Gothic Neo', 'Helvetica Neue'],
    },
    error: null,
    openLangs: [],
    extraLanguage: undefined,
    onSegment: noop,
    setProfile: noop,
    setRegion: noop,
    setExtraLanguage: noop,
    toggleWindow: noop,
    updateStyle: noop,
    retry: noop,
    correction: true,
    setCorrection: noop,
    speakerColors: true,
    setSpeakerColors: noop,
    glossary: [],
    glossaryBusy: false,
    glossaryNote: null,
    loadGlossary: noop,
    clearGlossary: noop,
    isRecording: false,
    ...overrides,
  };
}

// Details are collapsed by default; tests that inspect them expand explicitly.
const render = (p) => renderToStaticMarkup(React.createElement(CaptionOverlayPanel, { defaultExpanded: true, ...p }));
const renderCollapsed = (p) => renderToStaticMarkup(React.createElement(CaptionOverlayPanel, p));
const buttonTag = (html, id) => html.match(new RegExp(`<button[^>]*id="${id}"[^>]*>`))?.[0];

test('renders nothing outside the Mac app', () => {
  assert.equal(render(props({ supported: false, view: null })), '');
});

test('collapsed panel is one compact row with toggles and a settings button', () => {
  const html = renderCollapsed(props());
  assert.ok(html.includes('자막 창'));
  assert.ok(buttonTag(html, 'caption-ko'));
  assert.ok(buttonTag(html, 'caption-en'));
  const toggle = html.match(/<button[^>]*aria-controls="[^"]*"[^>]*>/)?.[0];
  assert.ok(toggle, 'disclosure button');
  assert.match(toggle, /type="button"/);
  assert.match(toggle, /aria-expanded="false"/);
  assert.ok(html.includes('설정'));
  assert.ok(!html.includes('id="caption-profile"'));
  assert.ok(!html.includes('id="caption-region"'));
  assert.ok(!html.includes('id="caption-extra-language"'));
  assert.ok(!html.includes('Zoom에서는'));
  assert.ok(!html.includes('인식 언어는 녹음을 시작하기 전에만'));
});

test('expanded panel reveals the details and links the disclosure to them', () => {
  const html = render(props());
  const toggle = html.match(/<button[^>]*aria-controls="[^"]*"[^>]*>/)?.[0];
  assert.match(toggle, /aria-expanded="true"/);
  const controls = toggle.match(/aria-controls="([^"]*)"/)[1];
  assert.ok(html.includes(`id="${controls}"`), 'details container id');
  assert.ok(html.includes('id="caption-profile"'));
  assert.ok(html.includes('Zoom에서는 창이 아니라 전체 화면을 공유해야 자막이 함께 보여요.'));
  assert.ok(buttonTag(html, 'caption-ko'));
});

test('collapsed panel keeps the error line and the profile-missing note', () => {
  const html = renderCollapsed(props({ profile: null, error: 'credentials-expired' }));
  assert.ok(html.includes('AWS 프로필을 먼저 선택해 주세요.'));
  assert.ok(html.includes('자격 증명이 만료됐어요. 터미널에서 mwinit을 실행한 뒤 다시 시도해 주세요.'));
  assert.ok(html.includes('다시 시도'));
  assert.ok(!html.includes('id="caption-profile"'));
});

test('disables window toggles without a profile', () => {
  const html = render(props({ profile: null }));
  for (const lang of ['ko', 'en']) {
    const tag = buttonTag(html, `caption-${lang}`);
    assert.ok(tag, `toggle for ${lang}`);
    assert.match(tag, /disabled=""/);
  }
  assert.ok(html.includes('AWS 프로필을 먼저 선택해 주세요.'));

  const ready = render(props({ profile: 'dev' }));
  assert.doesNotMatch(buttonTag(ready, 'caption-ko'), /disabled=""/);
  assert.ok(!ready.includes('AWS 프로필을 먼저 선택해 주세요.'));
});

test('offers only one third language', () => {
  const zh = render(props({ extraLanguage: 'zh' }));
  assert.ok(buttonTag(zh, 'caption-zh'));
  assert.ok(!zh.includes('caption-ja'));
  for (const label of ['없음', '日本語', '中文']) assert.ok(zh.includes(label), label);

  const none = render(props());
  assert.ok(!none.includes('caption-ja'));
  assert.ok(!none.includes('caption-zh'));
});

test('locks the recognition language while recording', () => {
  const html = render(props({ isRecording: true }));
  const select = html.match(/<select[^>]*id="caption-extra-language"[^>]*>/)?.[0];
  assert.ok(select);
  assert.match(select, /disabled=""/);
  assert.ok(html.includes('인식 언어는 녹음을 시작하기 전에만 바꿀 수 있어요.'));
});

test('shows a style editor for each open window', () => {
  const html = render(props({
    openLangs: ['ko'],
    windows: { ko: { open: true, style, x: null, y: null, width: 900, height: 180 } },
  }));
  assert.ok(html.includes('id="caption-ko-font"'));
  assert.ok(html.includes('Helvetica Neue'));
  assert.match(html, /id="caption-ko-size"[^>]*min="16"[^>]*max="160"|min="16"[^>]*max="160"[^>]*id="caption-ko-size"/);
  assert.ok(html.includes('id="caption-ko-font-color"'));
  assert.ok(html.includes('id="caption-ko-outline-color"'));
  assert.ok(html.includes('id="caption-ko-outline-width"'));
  assert.ok(html.includes('id="caption-ko-background"'));
  assert.ok(html.includes('40%'));
  assert.ok(!html.includes('id="caption-en-font"'));
});

test('shows the Zoom notice', () => {
  assert.ok(render(props()).includes('Zoom에서는 창이 아니라 전체 화면을 공유해야 자막이 함께 보여요.'));
});

test('shows the mwinit message', () => {
  const html = render(props({ error: 'credentials-expired' }));
  assert.ok(html.includes('자격 증명이 만료됐어요. 터미널에서 mwinit을 실행한 뒤 다시 시도해 주세요.'));
  assert.ok(html.includes('다시 시도'));

  const unloaded = render(props({ view: null, error: 'window-error' }));
  assert.ok(unloaded.includes('자막 번역에 실패했어요. 잠시 후 다시 시도해 주세요.'));
  assert.ok(unloaded.includes('다시 시도'));
});

test('reopens only allowed saved-open windows', () => {
  const layout = (open) => ({ open, style, x: null, y: null, width: 900, height: 180 });
  const settings = (extraLanguage, windows) => ({ profile: 'dev', region: 'us-east-1', extraLanguage, windows });
  const reopen = (s) => [...overlay.savedOpenWindows(s)];

  assert.deepEqual(reopen(settings(null, { ko: layout(true), en: layout(true) })), ['ko', 'en']);
  assert.deepEqual(reopen(settings('ja', { en: layout(false), ja: layout(true) })), ['ja']);
  assert.deepEqual(reopen(settings('zh', { ko: layout(true), ja: layout(true) })), ['ko']);
  assert.deepEqual(reopen(settings(null, { ja: layout(true), zh: layout(true) })), []);
  assert.deepEqual(reopen(settings('ja', { ko: layout(false) })), []);
  assert.deepEqual(reopen(settings('ja', {})), []);
  assert.deepEqual(reopen(null), []);
  assert.deepEqual(reopen(undefined), []);
});

test('region is a select with the saved region selected', () => {
  const html = render(props({ region: 'ap-northeast-2' }));
  const select = html.match(/<select[^>]*id="caption-region"[^>]*>(.*?)<\/select>/)?.[1];
  assert.ok(select, 'region select');
  assert.ok(!/<input[^>]*id="caption-region"/.test(html));
  assert.match(select, /<option value="ap-northeast-2" selected="">ap-northeast-2<\/option>/);
  assert.equal(select.match(/<option /g).length, overlay.CAPTION_REGIONS.length);
});

test('an unlisted saved region stays available as an option', () => {
  const html = render(props({ region: 'ca-central-1' }));
  const select = html.match(/<select[^>]*id="caption-region"[^>]*>(.*?)<\/select>/)?.[1];
  assert.match(select, /<option value="ca-central-1" selected="">ca-central-1<\/option>/);
  assert.equal(select.match(/<option /g).length, overlay.CAPTION_REGIONS.length + 1);
  assert.deepEqual([...overlay.CAPTION_REGIONS], ['ap-northeast-2', 'us-east-1']);
  assert.deepEqual([...overlay.captionRegionOptions('us-east-1')], ['ap-northeast-2', 'us-east-1']);
  assert.deepEqual([...overlay.captionRegionOptions('us-west-2')], ['ap-northeast-2', 'us-east-1', 'us-west-2']);
});

test('a declined profile change has its own message and re-reads settings', () => {
  assert.equal(overlay.captionErrorMessage('confirmation-declined'), 'AWS 프로필 변경을 취소했어요.');
  assert.equal(overlay.captionErrorMessage('confirmation-cooldown'), '잠시 후 다시 시도해 주세요.');
  const hook = readFileSync(new URL('../src/hooks/useCaptionOverlay.ts', import.meta.url), 'utf8');
  const handler = hook.slice(hook.indexOf('const saveFailed'), hook.indexOf('const setProfile'));
  assert.match(handler, /'confirmation-declined'/);
  assert.match(handler, /getCaptionSettings\(\)/);
  assert.match(hook, /save\(profile, settings\.region, settings\.extraLanguage\)\.catch\(saveFailed\)/);
  assert.match(hook, /save\(settings\.profile, trimmed, settings\.extraLanguage\)\.catch\(saveFailed\)/);
});

test('settings offer the correction and speaker colour switches with their state', () => {
  const inputTag = (html, id) => html.match(new RegExp(`<input[^>]*id="${id}"[^>]*>`))?.[0];
  const on = render(props());
  assert.match(inputTag(on, 'caption-correction'), /checked=""/);
  assert.match(inputTag(on, 'caption-speaker-colors'), /checked=""/);
  assert.ok(on.includes('오인식 교정'));
  assert.ok(on.includes('화자별 색'));
  assert.ok(on.includes('선택한 AWS 계정으로 모델을 한 번 더 불러요'), 'the cost is stated');
  const off = render(props({ correction: false, speakerColors: false }));
  assert.doesNotMatch(inputTag(off, 'caption-correction'), /checked/);
  assert.doesNotMatch(inputTag(off, 'caption-speaker-colors'), /checked/);
  assert.equal(renderCollapsed(props()).includes('caption-correction'), false);
});

test('caption switches are on unless turned off', () => {
  const store = new Map();
  const storage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
  assert.equal(layoutPrefs.readCaptionSwitch(layoutPrefs.CAPTION_CORRECTION_KEY, storage), true);
  layoutPrefs.writeCaptionSwitch(layoutPrefs.CAPTION_CORRECTION_KEY, false, storage);
  assert.equal(layoutPrefs.readCaptionSwitch(layoutPrefs.CAPTION_CORRECTION_KEY, storage), false);
  assert.equal(layoutPrefs.readCaptionSwitch(layoutPrefs.CAPTION_SPEAKER_COLORS_KEY, storage), true);
});

test('slide terms: upload control, applied count with a preview, busy and note states', () => {
  const inputTag = (html) => html.match(/<input[^>]*id="caption-slides"[^>]*>/)?.[0];
  const idle = render(props());
  assert.match(inputTag(idle), /type="file"/);
  assert.match(inputTag(idle), /accept="\.pptx,\.txt,\.md"/);
  assert.doesNotMatch(inputTag(idle), / disabled=""/);
  assert.ok(idle.includes('용어는 저장하지 않고 이 화면을 떠나면 사라져요'));
  assert.equal(idle.includes('적용 중'), false);

  const terms = Array.from({ length: 15 }, (_, n) => `Term${n}`);
  const applied = render(props({ glossary: terms }));
  assert.ok(applied.includes('용어 15개 적용 중'));
  assert.ok(applied.includes('Term0, Term1'));
  assert.ok(applied.includes('외 3개'));
  assert.equal(applied.includes('Term12'), false);
  assert.ok(applied.includes('지우기'));

  assert.match(inputTag(render(props({ glossaryBusy: true }))), / disabled=""/);
  assert.ok(render(props({ glossaryBusy: true })).includes('용어를 뽑는 중'));
  assert.match(inputTag(render(props({ profile: null }))), / disabled=""/, 'needs an AWS profile');
  assert.ok(render(props({ glossaryNote: '파일에서 글자를 찾지 못했어요.' })).includes('파일에서 글자를 찾지 못했어요.'));
});
