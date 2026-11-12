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

test('a stale lookup generation never auto-links', () => {
  assert.equal(ongoing.shouldAutoLink({ accountId: '', source: 'none', generation: 3, currentGeneration: 3 }), true);
  assert.equal(ongoing.shouldAutoLink({ accountId: '', source: 'none', generation: 3, currentGeneration: 4 }), false);
});

test('calendar pages are followed within a bound; a remaining cursor abstains', async () => {
  const pages = (count, last = null) => {
    const seen = [];
    const fetchPage = async cursor => {
      seen.push(cursor);
      const index = seen.length;
      return { events: [event(`e${index}`, -5, 30)], nextCursor: index < count ? `c${index}` : last };
    };
    return { seen, fetchPage };
  };
  const three = pages(3);
  assert.deepEqual([...(await ongoing.readOngoingEvents(three.fetchPage))].map(item => item.eventId), ['e1', 'e2', 'e3']);
  assert.deepEqual(three.seen, [undefined, 'c1', 'c2']);
  const bounded = pages(5);
  assert.equal((await ongoing.readOngoingEvents(bounded.fetchPage)).length, 5);
  const endless = pages(Infinity);
  assert.equal(await ongoing.readOngoingEvents(endless.fetchPage), null, 'a partial set is never used');
  assert.equal(endless.seen.length, ongoing.ONGOING_EVENT_MAX_PAGES);
  let current = true;
  const stale = pages(3);
  const read = ongoing.readOngoingEvents(async cursor => { current = false; return stale.fetchPage(cursor); }, () => current);
  assert.equal(await read, null, 'a stale lookup stops paging');
  assert.equal(stale.seen.length, 1);
});

test('a long calendar subject is trimmed before it fills the title', () => {
  assert.equal(ongoing.autoLinkTitle('  Weekly sync  '), 'Weekly sync');
  const long = ongoing.autoLinkTitle('가'.repeat(250));
  assert.equal(Array.from(long).length, ongoing.AUTO_TITLE_MAX);
  assert.equal(Array.from(ongoing.autoLinkTitle('😀'.repeat(201))).length, 200, 'code points, never a split pair');
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
  assert.match(page, /dropAutoLink\(\);\s*if \(request\.params\.accountId\) markAccountSource\('mcp'\);\s*setMeetingTitle\(title\);/,
    'an MCP start releases auto values before writing its own');
  assert.doesNotMatch(page, /onAccountChange=\{setReferenceAccountId\}/, 'every picker marks a user choice');
  assert.match(page, /markAccountSource\('user'\); void postRecording\.changeRecordingAccount/);
  assert.match(page, /enabled: !isUploadMode/, 'upload mode never auto-links');
  assert.match(page, /accountId === postRecording\.recordingLinks\.boundAccountId\s*\|\| \(recordingAccountLocked && !recordingAccountPending\) \? undefined : calendarLink\.dismiss/,
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

function autoLinkFixture(context, { initialTitle = '', initialSource = 'none', enabled = true, active = false, canDismiss = true, cursors = 0 } = {}) {
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
    calendarApi: { events: (query, options) => {
      calls.push(['events', query, options]);
      const index = calls.filter(call => call[0] === 'events').length;
      return new Promise(resolve => pending.push(() => resolve({ events: [liveEvent], nextCursor: index <= cursors ? `c${index}` : null })));
    } },
    accountApi: { list: async () => ({ accounts: [{ accountId: 'acc-1', name: 'Synthetic Corp', role: 'SA' }] }) },
  };
  // The harness compares dependency arrays; an effect without one runs every render.
  const react = { ...harness.react, useEffect: (effect, deps) => harness.react.useEffect(effect, deps ?? [Symbol('every render')]) };
  const { useCalendarAutoLink: runAutoLink } = hookLoader({ react, '@/lib/api': api })('@/hooks/useCalendarAutoLink');
  const recording = [];
  const props = { enabled, canDismiss };
  const seen = {};
  // A plain test probe rendered by the stub harness, not a React component.
  function probe() {
    const [accountId, setAccountId] = harness.react.useState('');
    const [title, setTitle] = harness.react.useState(initialTitle);
    seen.result = runAutoLink({
      userId: 'owner', accountId, setAccountId, title, setTitle, recordingAccountId: undefined,
      isRecordingActive: () => active, applyToRecording: id => recording.push(id), initialSource, enabled: props.enabled,
      canDismiss: props.canDismiss,
    });
    seen.page = { accountId, setAccountId, title, setTitle };
    return null;
  }
  const render = () => harness.render(probe);
  render();
  // Recording start: the only place the auto link is applied.
  const start = () => { seen.result.check(); render(); };
  const resolveCalendar = async () => {
    for (let round = 0; round < 10 && pending.length; round++) {
      pending.splice(0).forEach(resolve => resolve()); await settle(); await settle();
    }
    render();
  };
  return { render, start, resolveCalendar, calls, recording, props, result: () => seen.result, page: () => seen.page };
}

test('mount alone reads and links nothing', async context => {
  const f = autoLinkFixture(context);
  f.render();
  await f.resolveCalendar();
  assert.equal(f.calls.length, 0);
  assert.equal(f.page().accountId, '');
  assert.equal(f.page().title, '');
  assert.equal(f.result().autoLink, null);
});

test('auto-link fills an empty Account and title from the ongoing event at recording start', async context => {
  const f = autoLinkFixture(context);
  f.start();
  assert.equal(f.calls.length, 1);
  assert.equal(f.calls[0][2].expectedUserId, 'owner');
  await f.resolveCalendar();
  assert.equal(f.page().accountId, 'acc-1');
  assert.equal(f.page().title, 'Synthetic customer call');
  assert.deepEqual({ ...f.result().autoLink }, { accountId: 'acc-1', name: 'Synthetic Corp' });
});

test('a user choice made while the calendar read is pending is never overwritten', async context => {
  const f = autoLinkFixture(context);
  f.start();
  f.result().markSource('user');
  f.page().setAccountId('acc-user');
  f.render();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, 'acc-user');
  assert.equal(f.result().autoLink, null);
});

test('a user who cleared the Account while the read was pending is not re-linked', async context => {
  const f = autoLinkFixture(context);
  f.start();
  f.result().markSource('user');
  f.page().setAccountId('');
  f.render();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, '');
});

test('a title set while the read was pending is kept', async context => {
  const f = autoLinkFixture(context);
  f.start();
  f.page().setTitle('MCP title');
  f.render();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, 'acc-1');
  assert.equal(f.page().title, 'MCP title');
});

test('URL and MCP sources and upload mode never read the calendar', async context => {
  const url = autoLinkFixture(context, { initialSource: 'url' });
  url.start();
  assert.equal(url.calls.length, 0);
  const upload = autoLinkFixture(context, { enabled: false });
  assert.equal(upload.calls.length, 0);
  upload.result().check();
  assert.equal(upload.calls.length, 0, 'a recording start in upload mode does not read it either');
});

test('switching to upload mode while the read is pending links nothing; an applied link is dropped', async context => {
  const pendingSwitch = autoLinkFixture(context);
  pendingSwitch.start();
  pendingSwitch.props.enabled = false;
  pendingSwitch.render();
  await pendingSwitch.resolveCalendar();
  assert.equal(pendingSwitch.page().accountId, '');
  const applied = autoLinkFixture(context);
  applied.start();
  await applied.resolveCalendar();
  assert.equal(applied.page().accountId, 'acc-1');
  applied.props.enabled = false;
  applied.render();
  applied.render();
  assert.equal(applied.page().accountId, '');
  assert.equal(applied.page().title, '', 'the auto-filled title is reverted');
  assert.equal(applied.result().autoLink, null);
});

test('the change button clears the auto link for the rest of the session', async context => {
  const f = autoLinkFixture(context);
  f.start();
  await f.resolveCalendar();
  f.result().dismiss();
  f.render();
  assert.equal(f.page().accountId, '');
  assert.equal(f.page().title, '', 'the auto-filled title is reverted');
  assert.equal(f.result().autoLink, null);
  f.result().check();
  assert.equal(f.calls.length, 1, 'not re-applied');
});

test('an edited title survives removing the auto link', async context => {
  const f = autoLinkFixture(context);
  f.start();
  await f.resolveCalendar();
  f.page().setTitle('Edited title');
  f.render();
  f.result().dismiss();
  f.render();
  assert.equal(f.page().accountId, '');
  assert.equal(f.page().title, 'Edited title');
});

test('a leftover upload never carries an auto link', async context => {
  const none = autoLinkFixture(context);
  assert.equal(none.result().dropAutoLink(), undefined, 'nothing auto-linked: no overrides');
  const f = autoLinkFixture(context);
  f.start();
  await f.resolveCalendar();
  assert.equal(f.result().source(), 'auto');
  assert.deepEqual({ ...f.result().dropAutoLink() }, { accountId: '', title: '' });
  f.render();
  assert.equal(f.page().accountId, '');
  assert.equal(f.page().title, '');
  assert.equal(f.result().autoLink, null);
  assert.equal(f.result().dropAutoLink(), undefined, 'already dropped');
  const edited = autoLinkFixture(context);
  edited.start();
  await edited.resolveCalendar();
  edited.page().setTitle('My leftover');
  edited.render();
  assert.deepEqual({ ...edited.result().dropAutoLink() }, { accountId: '' }, 'an edited title is kept');
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /const dropped = calendarLink\.dropAutoLink\(\);\s*resetSessionStateForNewMeeting\(\);\s*await postRecording\.createDraftMeeting\(dropped\);/,
    'handleLeftoverUpload drops the auto link before creating its draft');
  const hook = readFileSync(new URL('../src/hooks/useCalendarAutoLink.ts', import.meta.url), 'utf8');
  assert.doesNotMatch(hook, /checkedUserRef/, 'no mount-time application');
});

test('a calendar result arriving after stop or failure does nothing', async context => {
  const f = autoLinkFixture(context, { active: true });
  f.start();
  f.result().invalidate();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, '');
  assert.equal(f.page().title, '', 'no title fill');
  assert.deepEqual(f.recording, [], 'nothing applied to the recording');
  assert.equal(f.result().autoLink, null);
  const live = autoLinkFixture(context, { active: true });
  live.start();
  await live.resolveCalendar();
  assert.deepEqual(live.recording, ['acc-1'], 'a current lookup still applies');
});

test('a leftover upload during a pending lookup is never linked by its late result', async context => {
  const f = autoLinkFixture(context, { active: true });
  f.start();
  assert.equal(f.result().dropAutoLink(), undefined);
  await f.resolveCalendar();
  assert.equal(f.page().accountId, '');
  assert.equal(f.page().title, '');
  assert.deepEqual(f.recording, []);
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /onRecordingStop=\{\(\) => \{ endAutoLinkRecording\(\);/, 'stop ends the lookup');
  assert.match(page, /onError=\{\(error, opts\) => \{\s*endAutoLinkRecording\(\);/, 'a failure ends the lookup');
  assert.match(page, /wasRecordingLiveRef\.current && !recordingLive\) endAutoLinkRecording\(\)/, 'returning to idle ends the lookup');
});

test('the hook follows calendar pages and abstains past the bound', async context => {
  const paged = autoLinkFixture(context, { cursors: 2 });
  paged.start();
  await paged.resolveCalendar();
  assert.deepEqual(paged.calls.map(call => call[1].cursor), [undefined, 'c1', 'c2']);
  assert.equal(paged.page().accountId, 'acc-1');
  const endless = autoLinkFixture(context, { cursors: Infinity });
  endless.start();
  await endless.resolveCalendar();
  assert.equal(endless.calls.length, 5);
  assert.equal(endless.page().accountId, '', 'a partial set links nothing');
});

test('the change button waits while the draft link is pending', async context => {
  const f = autoLinkFixture(context, { active: true, canDismiss: false });
  f.start();
  await f.resolveCalendar();
  f.result().dismiss();
  f.render();
  assert.equal(f.page().accountId, 'acc-1', 'nothing cleared while clearing would be refused');
  assert.deepEqual(f.recording, ['acc-1']);
  f.props.canDismiss = true;
  f.render();
  f.result().dismiss();
  f.render();
  assert.equal(f.page().accountId, '');
  assert.deepEqual(f.recording, ['acc-1', '']);
  const chip = readFileSync(new URL('../src/components/record/AutoLinkChip.tsx', import.meta.url), 'utf8');
  assert.match(chip, /disabled=\{pending\}/);
});

test('a user change releases calendar values, so a leftover upload inherits none', async context => {
  const controls = autoLinkFixture(context, { active: true });
  controls.start();
  await controls.resolveCalendar();
  controls.result().markSource('user'); // RecordingLinkControls picks another customer
  controls.render();
  assert.equal(controls.page().accountId, '', 'the auto Account no longer sits on the page');
  assert.equal(controls.page().title, '', 'nor its unedited title');
  assert.equal(controls.result().dropAutoLink(), undefined, 'the leftover path sees no calendar values');
  const picker = autoLinkFixture(context);
  picker.start();
  await picker.resolveCalendar();
  picker.result().markSource('user');
  picker.page().setAccountId('acc-user');
  picker.render();
  assert.equal(picker.page().accountId, 'acc-user', "the user's choice is kept");
  assert.equal(picker.page().title, '');
  const edited = autoLinkFixture(context);
  edited.start();
  await edited.resolveCalendar();
  edited.page().setTitle('My title');
  edited.render();
  edited.result().markSource('user');
  edited.render();
  assert.equal(edited.page().title, 'My title', 'an edited title is the user\'s');
});

test('a recording that failed to idle does not carry its auto link into the next start', async context => {
  const f = autoLinkFixture(context);
  f.start();
  await f.resolveCalendar();
  assert.equal(f.page().accountId, 'acc-1');
  // The next start drops what the previous auto link still owns, then looks up again.
  assert.deepEqual({ ...f.result().dropAutoLink() }, { accountId: '', title: '' });
  f.result().check();
  f.render();
  assert.equal(f.page().accountId, '');
  assert.equal(f.page().title, '');
  assert.equal(f.calls.length, 2, 'the new start reads the calendar itself');
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /const released = calendarLink\.dropAutoLink\(\);[\s\S]*?createDraftMeeting\(released\);\s*calendarLink\.check\(\);/,
    'handleRecordingStart releases before creating its draft');
  assert.match(page, /if \(!pageIdle \|\| !autoSessionOpenRef\.current\) return;\s*autoSessionOpenRef\.current = false;\s*dropAutoLink\(\);/,
    'settling back to idle without a save releases too');
});

test('an MCP start titled like a retained auto title keeps it', async context => {
  const f = autoLinkFixture(context);
  f.start();
  await f.resolveCalendar();
  // The MCP branch order: release first, then write the requested values.
  f.result().dropAutoLink();
  f.result().markSource('mcp');
  f.page().setTitle('Synthetic customer call');
  f.page().setAccountId('acc-mcp');
  f.render();
  assert.equal(f.page().title, 'Synthetic customer call', 'the start gate sees its title');
  assert.equal(f.page().accountId, 'acc-mcp');
});

test('browsing references while auto-linked keeps the chip and title', async context => {
  const f = autoLinkFixture(context);
  f.start();
  await f.resolveCalendar();
  f.page().setAccountId('acc-browse'); // browseReferenceAccount while source is auto
  f.render();
  assert.equal(f.result().source(), 'auto');
  assert.equal(f.result().autoLink.accountId, 'acc-1');
  assert.equal(f.page().title, 'Synthetic customer call');
  assert.deepEqual({ ...f.result().dropAutoLink() }, { accountId: 'acc-browse', title: '' }, 'the browsed Account is the user\'s');
  const page = readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8');
  assert.match(page, /if \(autoLinkSource\(\) !== 'auto'\) markAccountSource\('user'\);/);
  assert.equal(page.split('onAccountChange={browseReferenceAccount}').length - 1, 3);
});
