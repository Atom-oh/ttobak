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
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
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
const { RecordLayoutMenu } = loadModule('components/record/RecordLayoutMenu.tsx', {
  react: React,
  'react/jsx-runtime': jsxRuntime,
  '@/lib/recordLayoutPrefs': prefs,
});

const ALL_ON = { translation: true, summary: true, notes: true, transcript: true };
const render = (props) => renderToStaticMarkup(React.createElement(RecordLayoutMenu, {
  cards: ALL_ON, sidePanelOpen: true, translationEnabled: true, onToggle: () => {}, ...props,
}));
const checkboxes = (html) => [...html.matchAll(/<input[^>]*type="checkbox"[^>]*>/g)].map((m) => m[0]);

test('collapsed menu renders only the button', () => {
  const html = render();
  assert.match(html, /화면 구성/);
  assert.match(html, /aria-haspopup="true"/);
  assert.match(html, /aria-expanded="false"/);
  assert.equal(checkboxes(html).length, 0);
});

test('open menu lists one labelled checkbox per card', () => {
  const html = render({ initialOpen: true });
  assert.match(html, /aria-expanded="true"/);
  assert.equal(checkboxes(html).length, 5);
  for (const label of ['통역', '실시간 요약', '메모', '실시간 자막', 'Q&amp;A·자료']) assert.match(html, new RegExp(`<label[^>]*>.*?${label}`));
});

test('translation is absent when translation is off', () => {
  const html = render({ initialOpen: true, translationEnabled: false });
  assert.equal(checkboxes(html).length, 4);
  assert.doesNotMatch(html, />통역</);
});

test('the last visible main card is locked', () => {
  const html = render({ initialOpen: true, translationEnabled: false, cards: { ...ALL_ON, summary: false, notes: false } });
  const locked = checkboxes(html).filter((c) => c.includes('aria-disabled="true"'));
  assert.equal(locked.length, 1);
  assert.match(locked[0], /checked=""/);
  assert.match(html, /최소 하나/);
});
