import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import * as jsx from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';
import NextLink from 'next/link.js';
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize from 'rehype-sanitize';
import * as marked from 'marked';

function load(path, dependencies = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
      jsx: ts.JsxEmit.ReactJSX,
    },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports, URL, require(name) {
      assert.ok(name in dependencies, `Unexpected dependency ${name}`);
      return dependencies[name];
    },
  });
  return exports;
}

const urls = load('../src/lib/markdownUrl.ts');
const { restoreMarkdownIPv6Authority: restore } = urls;
const html = load('../src/lib/markdownHtml.ts', { marked, './markdownUrl': urls });
const transform = load('../src/lib/markdownUrlTransform.ts', {
  'react-markdown': { defaultUrlTransform }, './markdownUrl': urls,
});

test('only a valid encoded IPv6 authority is restored; suffix bytes stay exact', () => {
  const suffix = '/%2f/a?b=2&a=1&a=3&copy;#part%2f';
  assert.equal(restore(`https://%5B2606:4700:4700::1111%5D:443${suffix}`),
    `https://[2606:4700:4700::1111]:443${suffix}`);
  for (const value of [
    'https://example.com/%5B::1%5D', 'https://%5Bexample.com%5D/a',
    'https://%5B2606::1%5D@evil.example/a', 'https://%5B2606::1%5Devil.example/a',
    'https://%5B2606::1%5D:65536/a', 'https://%5B2606::1::2%5D/a',
    'https://%5Bfe80::1%25eth0%5D/a', 'javascript://%5B::1%5D/a',
    '//%5B::1%5D/a', '/relative/%5B::1%5D', 'transcript://segment-1',
  ]) assert.equal(restore(value), value);
});

test('actual React Markdown and marked preserve IPv6 citations and query entities', () => {
  const content = '[원문](<https://[2606:4700:4700::1111]/%2f/a?b=2&amp;copy;&amp;a=1#part>)';
  const expected = 'href="https://[2606:4700:4700::1111]/%2f/a?b=2&amp;copy;&amp;a=1#part"';
  const react = renderToStaticMarkup(React.createElement(ReactMarkdown, {
    remarkPlugins: [remarkGfm], rehypePlugins: [rehypeRaw, rehypeSanitize],
    urlTransform: transform.safeMarkdownUrl,
  }, content));
  for (const output of [react, html.markdownToHtml(content)]) {
    assert.ok(output.includes(expected), output);
    assert.equal((output.match(/<a /g) ?? []).length, 1);
    assert.ok(!output.includes('%5B'));
  }
});

test('React Markdown still strips unsafe destinations and injected HTML', () => {
  const content = '[bad](javascript:alert%281%29)\n\n<script>alert(1)</script>';
  const output = renderToStaticMarkup(React.createElement(ReactMarkdown, {
    rehypePlugins: [rehypeRaw, rehypeSanitize],
    urlTransform: transform.safeMarkdownUrl,
  }, content));
  assert.ok(!output.includes('javascript:'));
  assert.ok(!output.includes('<script'));
});

test('marked retains attribute escaping and existing checkbox overrides', () => {
  const parser = new marked.Marked({ renderer: html.markdownLinkRenderer });
  parser.use({ renderer: { checkbox({ checked }) { return checked ? '[x] ' : '[ ] '; } } });
  const output = parser.parse('- [x] done\n\n[link](<https://[2606::1]/a?x=%22onmouseover%3Dfoo> "quoted \\"title\\"")');
  assert.ok(output.includes('[x] done'));
  assert.ok(output.includes('href="https://[2606::1]/a?x=%22onmouseover%3Dfoo"'));
  assert.ok(!output.includes(' onmouseover='));
  assert.ok(output.includes('&quot;title&quot;'));
});

test('news provenance displays a distinct label without exposing internal storage keys', () => {
  const { qaSources } = load('../src/lib/qaSources.ts');
  const [source] = qaSources([], [{
    resourceKind: 'publishedNews', uri: 's3://private/news/v1/source/hash/token.md',
    partial: true, provenanceScope: 'validated_history',
  }]);
  assert.equal(source.label, '뉴스 근거');
  assert.equal(source.href, undefined);
  assert.equal(source.external, false);
  assert.deepEqual(Array.from(source.caveats), ['이전 대화의 근거', '부분 근거']);
});

test('the actual QA answer preserves source links and displays bounded news provenance', () => {
  const { QAChatMessage } = load('../src/components/qa/QAChatMessage.tsx', {
    'react/jsx-runtime': jsx, 'react-markdown': { default: ReactMarkdown },
    'remark-gfm': { default: remarkGfm }, 'next/link': { default: NextLink },
    '@/lib/qaSources': load('../src/lib/qaSources.ts'),
    '@/lib/markdownUrlTransform': transform,
  });
  const output = renderToStaticMarkup(React.createElement(QAChatMessage, {
    question: '공개 뉴스 테스트',
    answer: '[원문](<https://[2606:4700:4700::1111]/article?a=1&amp;b=2>)',
    sourceDetails: [{
      resourceKind: 'publishedNews', uri: 's3://synthetic/news/v1/source/hash/token.md',
      partial: true, provenanceScope: 'validated_history',
    }],
  }));
  assert.ok(output.includes('href="https://[2606:4700:4700::1111]/article?a=1&amp;b=2"'));
  assert.equal((output.match(/뉴스 근거/g) ?? []).length, 1);
  assert.ok(output.includes('이전 대화의 근거'));
  assert.ok(output.includes('부분 근거'));
  assert.ok(!output.includes('s3://'));
});
