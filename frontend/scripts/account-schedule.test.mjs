import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as jsxRuntime from 'react/jsx-runtime';
import { accountComponentHarness } from './account-component-harness.mjs';

const settle = () => new Promise(resolve => setImmediate(resolve));
const windowQuery = { from: '2026-11-10T00:00:00.000Z', to: '2026-11-17T00:00:00.000Z' };
const row = key => ({ meetingKey: key, title: `Shared ${key}`, startsAt: '2026-11-10T01:00:00Z', endsAt: '2026-11-10T02:00:00Z',
  timeZone: 'Asia/Seoul', isAllDay: false, sharedBy: ['a@example.invalid', 'b@example.invalid'], sharedByMe: false });

function setup(context, accountCalendar) {
  const harness = accountComponentHarness();
  const copy = {};
  const transpile = path => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
  }).outputText;
  vm.runInNewContext(transpile('../src/lib/calendarSharing.ts'), { exports: copy });
  const deps = {
    react: harness.react, 'react/jsx-runtime': jsxRuntime,
    '@/components/auth/AuthProvider': { useAuth: () => ({ user: { userId: 'me' } }) },
    '@/hooks/useCalendarEvents': { useCalendarWindow: () => ({ now: new Date(), window: windowQuery }) },
    '@/lib/api': { calendarApi: { accountCalendar } },
    '@/lib/calendarSharing': copy,
  };
  const exports = {};
  vm.runInNewContext(transpile('../src/components/account/AccountSchedule.tsx'), {
    exports, Error, Number, Map, require: name => deps[name],
  });
  context.after(() => harness.unmount());
  return { copy, render: () => harness.render(exports.AccountSchedule, new Set(), { accountId: 'acc 1' }) };
}

const texts = (node, out = []) => {
  if (typeof node === 'string') out.push(node);
  else if (Array.isArray(node)) node.forEach(child => texts(child, out));
  else if (node && typeof node === 'object') texts(node.props?.children, out);
  return out;
};
const buttons = (node, out = []) => {
  if (Array.isArray(node)) node.forEach(child => buttons(child, out));
  else if (node && typeof node === 'object') {
    if (node.type === 'button') out.push(node);
    buttons(node.props?.children, out);
  }
  return out;
};

test('load more requests the returned cursor and stops when it is null', async context => {
  const calls = [];
  const pages = [{ events: [row('m1')], nextCursor: 'c2' }, { events: [row('m2')], nextCursor: null }];
  const { copy, render } = setup(context, async (accountId, query, options) => { calls.push({ accountId, query, options }); return pages[calls.length - 1]; });
  render(); await settle();
  let tree = render();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].accountId, 'acc 1');
  assert.equal(calls[0].query.cursor, undefined);
  const more = buttons(tree).find(button => texts(button).join('') === copy.SHARING_COPY.loadMore);
  assert.ok(more, 'load more is offered while a cursor exists');
  more.props.onClick(); await settle();
  tree = render();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].query.cursor, 'c2');
  assert.ok(texts(tree).includes('Shared m1') && texts(tree).includes('Shared m2'));
  assert.equal(buttons(tree).some(button => texts(button).join('') === copy.SHARING_COPY.loadMore), false);
});

test('a 403 renders nothing and offers no retry', async context => {
  const { render } = setup(context, async () => { throw Object.assign(new Error('forbidden'), { status: 403 }); });
  render(); await settle();
  assert.equal(render(), null);
});

test('an empty schedule shows the empty copy', async context => {
  const { copy, render } = setup(context, async () => ({ events: [], nextCursor: null }));
  render(); await settle();
  assert.ok(texts(render()).includes(copy.SHARING_COPY.empty));
});

test('AccountDetailClient mounts the schedule only for direct members', () => {
  const source = readFileSync(new URL('../src/components/AccountDetailClient.tsx', import.meta.url), 'utf8');
  assert.match(source, /eventsVisible && user\?\.userId && <div className="mb-8"><AccountSchedule /);
});
