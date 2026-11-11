import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { posix } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';

// Renders the shipped /meetings workspace with inert list/calendar stand-ins to
// pin the page layout: meetings first, schedule as an xl sidebar that narrower
// screens show collapsed by default.
function render(search = '') {
  const h = React.createElement;
  const dependencies = {
    react: React,
    'react/jsx-runtime': jsxRuntime,
    'next/link': { default: ({ href, children, ...props }) => h('a', { href, ...props }, children) },
    'next/navigation': { useSearchParams: () => new URLSearchParams(search) },
    '@/components/auth/AuthProvider': { useAuth: () => ({ user: { userId: 'owner' }, isAuthenticated: true, isLoading: false, membershipRevision: 0 }) },
    '@/components/layout/AppLayout': { AppLayout: ({ children }) => h('main', null, children) },
    '@/components/MeetingList': { MeetingList: () => h('section', { 'data-part': 'meeting-list' }) },
    '@/components/ui/Skeleton': { SkeletonCard: () => null },
    '@/components/workspace/UpcomingMeetings': {
      PrivateUpcomingMeetings: ({ highlight }) => h('div', { 'data-part': 'calendar', 'data-highlight': highlight ?? '' }),
    },
    '@/lib/api': { accountApi: { list: async () => ({ accounts: [] }) }, meetingsApi: { list: async () => ({ meetings: [], nextCursor: null }) } },
    '@/lib/auth': { getIdToken: () => null },
    '@/lib/postUploadRefresh': { useProcessingRefresh() {}, useUploadSeed: () => ({ current: null }), withUploadSeed: rows => rows },
    '@/types/specreqTask': { parseTaskHighlight: value => value || undefined },
  };
  const cache = new Map();
  const load = path => {
    if (path in dependencies) return dependencies[path];
    if (cache.has(path)) return cache.get(path);
    const sourceText = readFileSync(new URL(`../src/${path.replace(/^@\//, '')}.tsx`, import.meta.url), 'utf8');
    const exports = {};
    cache.set(path, exports);
    vm.runInNewContext(ts.transpileModule(sourceText, {
      fileName: 'component.tsx',
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
    }).outputText, {
      exports, console, AbortController, URLSearchParams,
      require: name => load(name.startsWith('.') ? posix.join(posix.dirname(path), name) : name),
    });
    return exports;
  };
  const { MeetingsWorkspace } = load('@/components/workspace/MeetingsWorkspace');
  return renderToStaticMarkup(h(MeetingsWorkspace));
}

test('meetings render before the schedule, which is an xl sidebar', () => {
  const html = render();
  const list = html.indexOf('data-part="meeting-list"');
  const calendar = html.indexOf('data-part="calendar"');
  assert.ok(list >= 0 && calendar >= 0);
  assert.ok(list < calendar, 'the meeting list leads the page on every screen size');
  assert.match(html, /xl:grid-cols-\[minmax\(0,1fr\)_20rem\]/);
  assert.match(html, /<aside aria-label="내 일정" class="[^"]*xl:sticky/);
});

test('narrow screens collapse the schedule by default but keep it mounted', () => {
  const html = render();
  assert.match(html, /<button type="button" aria-expanded="false" aria-controls="([^"]+)"[^>]*xl:hidden/);
  const panelId = html.match(/aria-controls="([^"]+)"/)[1];
  assert.match(html, new RegExp(`<div id="${panelId}" class="hidden xl:block"><div data-part="calendar"`));
});

test('a task highlight link opens the schedule panel', () => {
  const html = render('?task=evt-1');
  assert.match(html, /aria-expanded="true"/);
  assert.match(html, /class="mt-3 xl:mt-0"><div data-part="calendar" data-highlight="evt-1"/);
});
