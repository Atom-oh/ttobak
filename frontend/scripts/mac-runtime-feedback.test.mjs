import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import * as jsxRuntime from 'react/jsx-runtime';
import ts from 'typescript';

const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
const syntax = ts.createSourceFile('page.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);

function findNode(predicate) {
  let found;
  function visit(node) {
    if (predicate(node)) {
      found = node;
      return;
    }
    if (!found) ts.forEachChild(node, visit);
  }
  visit(syntax);
  assert.ok(found, 'runtime node missing');
  return found;
}

function load(source, context = {}, dependencies = {}) {
  const result = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
      jsx: ts.JsxEmit.ReactJSX,
    },
  });
  const exports = {};
  vm.runInNewContext(result.outputText, {
    exports,
    require: (name) => {
      if (name === 'react/jsx-runtime') return jsxRuntime;
      assert.ok(name in dependencies, `unexpected import: ${name}`);
      return dependencies[name];
    },
    ...context,
  });
  return exports;
}

function switchHandler(context) {
  const declaration = findNode((node) =>
    ts.isVariableDeclaration(node) && node.name.getText(syntax) === 'switchNativeMic');
  return load(`export const switchNativeMic = ${declaration.initializer.getText(syntax)};`, context).switchNativeMic;
}

function renderSection(classFragment, context) {
  const element = findNode((node) =>
    ts.isJsxElement(node) && node.openingElement.tagName.getText(syntax) === 'div'
    && node.openingElement.attributes.getText(syntax).includes(classFragment));
  const { render } = load(`export const render = () => (${element.getText(syntax)});`, context);
  return renderToStaticMarkup(render());
}

const { nativeMicSwitchError } = load(
  readFileSync(new URL('../src/lib/nativeMicSwitch.ts', import.meta.url), 'utf8'),
  { Error },
);

for (const [rejection, detail] of [
  ['The selected device has no input.', /\(The selected device has no input\.\)/],
  [new Error('Device is unavailable.'), /\(Device is unavailable\.\)/],
]) {
  test(`Korean recovery shows the app's reason: ${String(rejection)}`, async () => {
    const states = [];
    const change = switchHandler({
      setMicSwitch: (value) => states.push(value),
      switchNativeRecordingInput: () => Promise.reject(rejection),
      getNativeCaptureLost: async () => false,
      nativeMicSwitchError,
      setNativeInputUid: () => assert.fail('failed selection'),
      setNativeInput: () => assert.fail('failed input'),
      Error,
    });
    change('replacement');
    assert.equal(states[0].busy, true);
    await new Promise(setImmediate);
    await new Promise(setImmediate);
    assert.equal(states.at(-1).busy, false);
    assert.match(states.at(-1).error, /마이크를 바꾸지 못했습니다/);
    assert.match(states.at(-1).error, /연결/);
    assert.match(states.at(-1).error, /녹음을 종료해 저장/);
    assert.match(states.at(-1).error, detail);
  });
}

test('a switch that stopped capture says the recording stopped', async () => {
  const states = [];
  const change = switchHandler({
    setMicSwitch: (value) => states.push(value),
    switchNativeRecordingInput: () => Promise.reject('capture stopped'),
    getNativeCaptureLost: async () => true,
    nativeMicSwitchError,
    setNativeInputUid: () => assert.fail('failed selection'),
    setNativeInput: () => assert.fail('failed input'),
    Error,
  });
  change('replacement');
  await new Promise(setImmediate);
  await new Promise(setImmediate);
  assert.match(states.at(-1).error, /녹음이 중단됐습니다/);
  assert.match(states.at(-1).error, /지금 녹음을 종료해 저장/);
});

test('the switch error bounds the detail and omits an empty one', () => {
  assert.doesNotMatch(nativeMicSwitchError(undefined, false), /\(/);
  assert.doesNotMatch(nativeMicSwitchError('   ', false), /\(/);
  const long = nativeMicSwitchError('x'.repeat(1000), false);
  assert.ok(long.length < 400);
});

function renderStatus(nativeMicMuted) {
  return renderSection('flex items-center gap-2 px-4 py-2 bg-purple-50', {
    nativeInput: { name: 'USB microphone', reason: 'requested' },
    nativeMicMuted,
  });
}

test('mute status names the app and is announced', () => {
  const markup = renderStatus({ muted: true, app: 'Zoom', reason: 'muted' });
  assert.match(markup, /Zoom에서 음소거 중이라 내 목소리는 녹음하지 않습니다/);
  assert.match(markup, /role="status"/);
  assert.match(markup, /aria-live="polite"/);
  assert.match(markup, /aria-atomic="true"/);
  assert.match(markup, /text-xl/);
});

function renderSwitch(micSwitch) {
  return renderSection('mb-4 flex flex-col gap-1', {
    nativeInputUid: 'existing',
    switchNativeMic: () => {},
    micSwitch,
    NativeMicSelector: ({ errorId, disabled }) => React.createElement('select', {
      'aria-describedby': errorId,
      disabled,
    }),
  });
}

test('errors are announced, associated and readable', () => {
  const markup = renderSwitch({ busy: false, error: '마이크를 바꾸지 못했습니다.' });
  assert.match(markup, /role="alert"/);
  assert.match(markup, /id="native-mic-switch-error"/);
  assert.match(markup, /aria-describedby="native-mic-switch-error"/);
  assert.match(markup, /text-red-700/);
  assert.match(markup, /text-xl/);
});

test('pending status removes obsolete error association', () => {
  const markup = renderSwitch({ busy: true, error: null });
  assert.match(markup, /disabled=""/);
  assert.match(markup, /role="status"/);
  assert.match(markup, /마이크를 바꾸는 중입니다/);
  assert.doesNotMatch(markup, /aria-describedby/);
});

test('selector exposes only a present error', () => {
  const source = readFileSync(new URL('../src/components/record/NativeMicSelector.tsx', import.meta.url), 'utf8');
  const { NativeMicSelector } = load(source, {}, {
    react: {
      useState: () => [[{ uid: 'input', name: 'Microphone', is_default: true, used_by: [] }], () => {}],
      useEffect: () => {},
    },
    '@/lib/tauri': { listNativeInputDevices: async () => [] },
  });
  const failure = renderToStaticMarkup(React.createElement(NativeMicSelector, {
    value: 'input', onChange: () => {}, errorId: 'native-mic-switch-error',
  }));
  assert.match(failure, /aria-describedby="native-mic-switch-error"/);
  assert.match(failure, /aria-invalid="true"/);
  const ordinary = renderToStaticMarkup(React.createElement(NativeMicSelector, {
    value: 'input', onChange: () => {},
  }));
  assert.doesNotMatch(ordinary, /aria-describedby|aria-invalid/);
});

test('the live microphone picker requires an app build that can switch', () => {
  assert.match(page, /isNativeRecording && nativeCanSwitch && \(/);
  const button = readFileSync(new URL('../src/components/RecordButton.tsx', import.meta.url), 'utf8');
  // Forwarded even for a mic-less start (no `input`).
  assert.match(button, /onNativeInput\?\.\(resp\.input \?\? null, resp\.can_switch_input === true\)/);
  assert.doesNotMatch(button, /if \(resp\.input\) onNativeInput/);
});

test('the wake retry timer is cleared with the page and rechecks the failed upload', () => {
  const effect = page.slice(page.indexOf('onNativeSystemWake(() =>'), page.indexOf('// --- Mac app local control'));
  assert.match(effect, /return \(\) => \{\s*off\(\);\s*if \(timer\) clearTimeout\(timer\);/);
  // The state is read when the timer fires, not when the wake arrives.
  assert.ok(effect.indexOf('setTimeout(') < effect.indexOf('wakeRetryRef.current'));
});
