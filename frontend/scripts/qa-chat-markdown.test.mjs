import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

// Chat answers style each Markdown element themselves: without the typography
// plugin, `prose` classes leave lists, headings, tables and code unstyled.
function loadComponents() {
  const source = readFileSync(new URL('../src/components/qa/chatMarkdown.tsx', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
  });
  const exports = {};
  const modules = { react: React, 'react/jsx-runtime': jsxRuntime };
  vm.runInNewContext(outputText, { exports, require: (name) => modules[name] });
  return exports.chatMarkdownComponents;
}

const components = loadComponents();
const render = (md) =>
  renderToStaticMarkup(React.createElement(ReactMarkdown, { remarkPlugins: [remarkGfm], components }, md));

test('lists keep their markers and indentation', () => {
  const html = render('- 하나\n- 둘\n\n1. 첫째\n2. 둘째');
  assert.match(html, /<ul class="[^"]*list-disc[^"]*pl-5/);
  assert.match(html, /<ol class="[^"]*list-decimal[^"]*pl-5/);
  assert.equal((html.match(/<li /g) || []).length, 4);
});

test('headings stay headings, sized for the bubble', () => {
  const html = render('## 비용 비교\n\n본문');
  assert.match(html, /<h2 class="[^"]*text-\[15px\][^"]*font-semibold/);
  assert.match(html, /<p class="[^"]*leading-relaxed[^"]*">본문<\/p>/);
});

test('GFM tables get borders and cell padding inside a scroll wrapper', () => {
  const html = render('| 항목 | 비용 |\n|---|---:|\n| EKS | $73 |');
  assert.match(html, /<div class="[^"]*overflow-x-auto[^"]*border[^"]*"><table/);
  assert.match(html, /<th class="[^"]*px-2\.5[^"]*border-b[^"]*">항목<\/th>/);
  // a column alignment from the delimiter row (---:) is kept on both cells
  assert.match(html, /<th style="text-align:right" class="[^"]*px-2\.5/);
  assert.match(html, /<td style="text-align:right" class="[^"]*px-2\.5/);
});

test('fenced code is a plain block; inline code is a chip', () => {
  const html = render('`kubectl get pods`\n\n```bash\nkubectl apply -f a.yaml\n```');
  assert.match(html, /<pre class="[^"]*overflow-x-auto[^"]*"><code class="language-bash /);
  assert.match(html, /<code class="rounded[^"]*font-mono[^"]*">kubectl get pods<\/code>/);
});

test('external links open safely; the timeout blockquote is styled', () => {
  const html = render('[문서](https://docs.aws.amazon.com/) \n\n> ⚠️ 응답 시간 초과');
  assert.match(html, /<a href="https:\/\/docs\.aws\.amazon\.com\/" class="[^"]*" target="_blank" rel="noopener noreferrer">문서<\/a>/);
  assert.match(html, /<blockquote class="[^"]*border-l-2/);
});

test('raw HTML in an answer is not rendered', () => {
  const html = render('<img src=x onerror=alert(1)> 텍스트');
  assert.doesNotMatch(html, /<img/);
});

test('QAChatMessage renders answers with these components, not prose classes', () => {
  const src = readFileSync(new URL('../src/components/qa/QAChatMessage.tsx', import.meta.url), 'utf8');
  assert.match(src, /<ReactMarkdown[^>]*components=\{chatMarkdownComponents\}/);
  assert.doesNotMatch(src, /\bprose\b/);
});
