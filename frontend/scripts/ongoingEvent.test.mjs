import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(path) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, require: (id) => assert.fail(id) });
  return exports;
}

const ongoing = loadModule('lib/ongoingEvent.ts');
const now = new Date('2026-11-11T05:00:00Z');
const at = minutes => new Date(now.getTime() + minutes * 60_000).toISOString();
const event = (eventId, start, end, extra = {}) => ({
  eventId, sourceId: 's', provider: 'outlook', title: eventId, startsAt: at(start), endsAt: at(end),
  timeZone: 'Asia/Seoul', isAllDay: false, ...extra,
});
const found = (events, when = now) => ongoing.ongoingEventFor(events, when)?.eventId ?? null;

test('event_in_window_is_found', () => {
  assert.equal(found([event('a', -30, 30, { accountId: 'acc' })]), 'a');
  assert.equal(found([event('later', 20, 60)]), null);
  assert.equal(found([]), null);
});

test('ten_minutes_early_counts', () => {
  assert.equal(found([event('a', 10, 60)]), 'a');
  assert.equal(found([event('a', 11, 60)]), null);
});

test('ended_event_ignored', () => {
  assert.equal(found([event('a', -60, 0)]), null);
  assert.equal(found([event('a', -60, -1)]), null);
  assert.equal(found([event('bad', -60, 60, { startsAt: 'not a date' })]), null);
});

test('all_day_ignored', () => {
  assert.equal(found([event('a', -60, 60, { isAllDay: true, accountId: 'acc' })]), null);
});

test('account_event_preferred', () => {
  assert.equal(found([event('internal', -1, 30), event('customer', -40, 30, { accountId: 'acc' })]), 'customer');
  assert.equal(found([event('far', -50, 30, { accountId: 'a1' }), event('near', 5, 30, { accountId: 'a2' })]), 'near');
  assert.equal(found([event('internal', -50, 30), event('internal-near', -5, 30)]), 'internal-near');
});

test('tie_between_accounts_returns_null', () => {
  assert.equal(found([event('a', -10, 30, { accountId: 'a1' }), event('b', -10, 50, { accountId: 'a2' })]), null);
  assert.equal(found([event('a', -10, 30, { accountId: 'a1' }), event('b', 10, 50, { accountId: 'a2' })]), null);
  assert.equal(found([event('b', -10, 30, { accountId: 'a1' }), event('a', -10, 50, { accountId: 'a1' })]), 'a');
});

test('manual_or_url_account_is_never_overwritten', () => {
  assert.equal(ongoing.shouldAutoLink({ accountId: '', source: 'none' }), true);
  for (const source of ['url', 'mcp', 'user', 'auto']) {
    assert.equal(ongoing.shouldAutoLink({ accountId: '', source }), false, source);
  }
  assert.equal(ongoing.shouldAutoLink({ accountId: 'acc', source: 'none' }), false);
  assert.equal(ongoing.shouldAutoLink({ accountId: '', recordingAccountId: 'acc', source: 'none' }), false);
});

test('lookup window and chip copy', () => {
  const window = ongoing.ongoingEventWindow(now);
  assert.equal(window.from, '2026-11-11T04:00:00.000Z');
  assert.equal(window.to, '2026-11-11T06:00:00.000Z');
  assert.equal(ongoing.autoLinkedLabel('Synthetic Corp'), '고객사: Synthetic Corp (일정에서 자동 연결)');
});

test('record page marks URL, MCP and user Account sources before auto-link', () => {
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /initialSource: searchParams\.get\('accountId'\) \? 'url' : 'none'/);
  assert.match(page, /markAccountSource\('mcp'\);\s*setReferenceAccountId\(request\.params\.accountId\)/);
  assert.doesNotMatch(page, /onAccountChange=\{setReferenceAccountId\}/, 'every picker marks a user choice');
  assert.match(page, /markAccountSource\('user'\); void postRecording\.changeRecordingAccount/);
  assert.match(page, /enabled: !isUploadMode/, 'upload mode never auto-links');
  assert.match(page, /accountId === postRecording\.recordingLinks\.boundAccountId \? undefined : calendarLink\.dismiss/,
    'a bound draft hides the change button');
  const chip = readFileSync(new URL('../src/components/record/AutoLinkChip.tsx', import.meta.url), 'utf8');
  assert.match(chip, /\{onChange && <button/);
  const hook = readFileSync(new URL('../src/hooks/useCalendarAutoLink.ts', import.meta.url), 'utf8');
  assert.match(hook, /\.catch\(\(\) => \{/, 'calendar failures stay silent');
});

// Behavioral tests of the hook with a stub React and stubbed APIs.
import { posix } from 'node:path';
import { accountComponentHarness } from './account-component-harness.mjs';

function hookLoader(dependencies) {
  const cache = new Map();
  const load = path => {
    if (path in dependencies) return dependencies[path];
    if (cache.has(path)) return cache.get(path);
    const source = readFileSync(new URL(`../src/${path.replace(/^@\//, '')}.ts`, import.meta.url), 'utf8');
    const exports = {};
    cache.set(path, exports);
    vm.runInNewContext(ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    }).outputText, {
      exports, Date, Promise,
      require: name => load(name.startsWith('.') ? posix.join(posix.dirname(path), name) : name),
    });
    return exports;
  };
  return load;
}

const settle = () => new Promise(resolve => setImmediate(resolve));

function autoLinkFixture(context, { initialTitle = '', initialSource = 'none', enabled = true } = {}) {
  const harness = accountComponentHarness();
  context.after(() => harness.unmount());
  const pending = [];
  const calls = [];
  const live = new Date();
  const liveEvent = {
    eventId: 'e', sourceId: 's', provider: 'outlook', title: 'Synthetic customer call', timeZone: 'UTC', isAllDay: false,
    startsAt: new Date(live.getTime() - 5 * 60_000).toISOString(), endsAt: new Date(live.getTime() + 30 * 60_000).toISOString(),
    accountId: 'acc-1',
  };
  const api = {
    calendarApi: { events: (query, options) => { calls.push(['events', query, options]); return new Promise(resolve => pending.push(() => resolve({ events: [liveEvent], nextCursor: null }))); } },
    accountApi: { list: async () => ({ accounts: [{ accountId: 'acc-1', name: 'Synthetic Corp', role: 'SA' }] }) },
  };
  // The harness compares dependency arrays; an effect without one runs every render.
  const react = { ...harness.react, useEffect: (effect, deps) => harness.react.useEffect(effect, deps ?? [Symbol('every render')]) };
  const { useCalendarAutoLink: runAutoLink } = hookLoader({ react, '@/lib/api': api })('@/hooks/useCalendarAutoLink');
  const recording = [];
  const props = { enabled };
  const seen = {};
  // A plain test probe rendered by the stub harness, not a React component.
  function probe() {
    const [accountId, setAccountId] = harness.react.useState('');
    const [title, setTitle] = harness.react.useState(initialTitle);
    seen.result = runAutoLink({
      userId: 'owner', accountId, setAccountId, setTitle, recordingAccountId: undefined,
      isRecordingActive: () => false, applyToRecording: id => recording.push(id), initialSource, enabled: props.enabled,
    });
    seen.page = { accountId, setAccountId, title, setTitle };
    return null;
  }
  const render = () => harness.render(probe);
  render();
  const resolveCalendar = async () => { pending.splice(0).forEach(resolve => resolve()); await settle(); await settle(); render(); };
  return { render, resolveCalendar, calls, recording, props, result: () => seen.result, page: () => seen.page };
}

test('auto-link fills an empty Account and title from the ongoing event', async context => {
  const f = autoLinkFixture(context);
  assert.equal(f.calls.length, 1);
  assert.equal(f.calls[0][2].expectedUserId, 'owner');
  await f.resolveCalendar();
  assert.equal(f.page().accountId, 'acc-1');
  assert.equal(f.page().title, 'Synthetic customer call');
  assert.deepEqual({ ...f.result().autoLink }, { accountId: 'acc-1', name: 'Synthetic Corp' });
});

test('a user choice made while the calendar read is pending is never overwritten', async context => {
  const f = autoLinkFixture(context);
  f.result().markSource('user');
  f.page().setAccountId('acc-user');
  f.render();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, 'acc-user');
  assert.equal(f.result().autoLink, null);
});

test('a user who cleared the Account while the read was pending is not re-linked', async context => {
  const f = autoLinkFixture(context);
  f.result().markSource('user');
  f.page().setAccountId('');
  f.render();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, '');
});

test('a title set while the read was pending is kept', async context => {
  const f = autoLinkFixture(context);
  f.page().setTitle('MCP title');
  f.render();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, 'acc-1');
  assert.equal(f.page().title, 'MCP title');
});

test('URL and MCP sources and upload mode never read the calendar', async context => {
  assert.equal(autoLinkFixture(context, { initialSource: 'url' }).calls.length, 0);
  const upload = autoLinkFixture(context, { enabled: false });
  assert.equal(upload.calls.length, 0);
  upload.result().check();
  assert.equal(upload.calls.length, 0, 'a recording start in upload mode does not read it either');
});

test('switching to upload mode while the read is pending links nothing; an applied link is dropped', async context => {
  const pendingSwitch = autoLinkFixture(context);
  pendingSwitch.props.enabled = false;
  pendingSwitch.render();
  await pendingSwitch.resolveCalendar();
  assert.equal(pendingSwitch.page().accountId, '');
  const applied = autoLinkFixture(context);
  await applied.resolveCalendar();
  assert.equal(applied.page().accountId, 'acc-1');
  applied.props.enabled = false;
  applied.render();
  assert.equal(applied.page().accountId, '');
  assert.equal(applied.result().autoLink, null);
});

test('the change button clears the auto link for the rest of the session', async context => {
  const f = autoLinkFixture(context);
  await f.resolveCalendar();
  f.result().dismiss();
  f.render();
  assert.equal(f.page().accountId, '');
  assert.equal(f.result().autoLink, null);
  f.result().check();
  assert.equal(f.calls.length, 1, 'not re-applied');
});
