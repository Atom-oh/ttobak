// Slack links connector: channel listing, selection and collection against a
// fake session and API, plus the synthetic stdio server. All data is synthetic.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { collectLinks, listChannels, selectChannels } from '../dist/intake/links.js';
import { LINKS_TOOLS } from '../dist/intake/connectors.js';
import { IntakeStore } from '../dist/intake/store.js';
import { McpSession } from '../dist/intake/mcp-client.js';
import { ApiResponseError } from '../dist/api.js';

const NOW = new Date('2026-11-10T03:00:00Z');
const SCOPE = 'a'.repeat(32);
const SECRET = '비밀 메시지 SECRET-TEXT-7c1f';
const FIXTURE = fileURLToPath(new URL('./fixtures/intake-work-server.mjs', import.meta.url));
const C1 = 'C0AAAAAAA1';
const C2 = 'C0BBBBBBB2';

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-intake-links-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return home;
}

function allFiles(directory) {
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) files.push(...allFiles(path));
    else files.push(path);
  }
  return files;
}

const allText = home => allFiles(home).map(path => readFileSync(path, 'utf8')).join('\n');
const json = value => ({ content: [{ type: 'text', text: JSON.stringify(value) }] });
const ts = date => (new Date(date).getTime() / 1000).toFixed(6);
const iso = date => new Date(date).toISOString();

/** The real server's `text`: every whitespace datamarked as U+E000, then wrapped. */
const slackText = text => `<slack-user-content ts="1794619610.336109">\n${text.replace(/\s/g, '\uE000')}\n</slack-user-content>`;

/** A message in the real `get_messages` shape; `text` is given as written. */
function message(time, text, blocks) {
  return { text, timestamp: iso(time), user: { id: 'U0SECRET', name: SECRET }, replyToken: SECRET,
    permalink: `https://team.slack.com/archives/C0AAAAAAA1/p${new Date(time).getTime()}`, isThreadParent: false,
    replyCount: 0, ...(blocks ? { blocks } : {}) };
}

const section = (...elements) => ({ type: 'rich_text_section', elements });
const richText = (...elements) => [{ type: 'rich_text', block_id: SECRET, elements }];

/** Messages with one public link each (in text and blocks), minutes apart, newest first like Slack. */
function linkMessages(count, start = '2026-11-09T00:00:00Z', prefix = 'n') {
  const base = new Date(start).getTime();
  return Array.from({ length: count }, (_, index) => {
    const url = `https://news.example.com/${prefix}${index}`;
    return message(base + index * 60_000, `${SECRET} ${url}`,
      richText(section({ type: 'text', text: `${SECRET} ` }, { type: 'link', url, text: SECRET })));
  }).reverse();
}

/** Turns a test result into the real shape: datamarked, wrapped text and paging fields. */
function realResult(entry, channel) {
  if (!entry || !Array.isArray(entry.messages)) return entry;
  return { channel: { id: channel, name: 'fixture' }, hasMore: false, nextCursor: '', ...entry,
    messages: entry.messages.map(item => item && typeof item.text === 'string' ? { ...item, text: slackText(item.text) } : item) };
}

function fakeSession(byChannel) {
  const calls = [];
  return {
    calls,
    async call(name, args) {
      calls.push({ name, args: structuredClone(args) });
      if (name !== 'get_messages') throw new Error('unexpected tool');
      const entry = byChannel[args.channel];
      if (entry instanceof Error) throw entry;
      if (typeof entry === 'function') return json(realResult(entry(args), args.channel));
      return json(realResult(entry ?? { messages: [] }, args.channel));
    },
  };
}

function fakeApi({ sources = [], failUploads = {}, addFailures = [], deleteFailures = {}, patchFailures = {} } = {}) {
  const calls = [];
  const cloud = new Map(sources.map(source => [source.sourceId, source]));
  let uploads = 0;
  let next = 0;
  return {
    calls, cloud,
    async listCustomerUpdateSources() {
      calls.push(['list']);
      return { sources: [...cloud.values()].map(source => structuredClone(source)) };
    },
    async addCustomerUpdateSource(body) {
      calls.push(['add', structuredClone(body)]);
      const failure = addFailures.shift();
      if (failure) throw failure;
      const source = { sourceId: `s${++next}`, kind: body.kind, label: body.label, localSourceId: body.localSourceId,
        status: 'active' };
      cloud.set(source.sourceId, source);
      return structuredClone(source);
    },
    async patchCustomerUpdateSource(sourceId, body) {
      calls.push(['patch', sourceId, structuredClone(body)]);
      const failure = patchFailures[sourceId]?.shift();
      if (failure) throw failure;
      return { status: 'updated' };
    },
    async deleteCustomerUpdateSource(sourceId) {
      calls.push(['delete', sourceId]);
      const failure = deleteFailures[sourceId]?.shift();
      if (failure) throw failure;
      cloud.delete(sourceId);
      return {};
    },
    async uploadCustomerUpdateLinks(body) {
      calls.push(['upload', structuredClone(body)]);
      uploads++;
      const failure = failUploads[uploads];
      if (failure) throw failure;
      return { accepted: body.links.length, duplicate: 0, rejected: 0 };
    },
  };
}

function selectionOf(home) {
  return JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
}

function cursorsOf(store) {
  return store.read('links', SCOPE, 'cursors.json');
}

async function selected(t, channels = [[C1, 'whats-new'], [C2, 'blogs']]) {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const api = fakeApi();
  await selectChannels(store, api, channels.map(([id, label]) => ({ id, label })), NOW);
  api.calls.length = 0;
  return { home, store, api };
}

const uploads = api => api.calls.filter(call => call[0] === 'upload').map(call => call[1]);

test('LINKS_TOOLS names only the two read tools', () => {
  assert.deepEqual(LINKS_TOOLS, [
    { name: 'list_my_channels', properties: [] },
    { name: 'get_messages', properties: ['channel', 'since', 'until', 'limit', 'cursor', 'includeThreadReplies'] },
  ]);
});

test('listChannels flattens sections and rejects an unknown shape', async () => {
  const calls = [];
  const session = { async call(name, args) {
    calls.push({ name, args });
    return json({ sections: [
      { sectionId: 'S1', name: 'Starred', channels: [{ id: C1, name: 'whats-new', topic: SECRET }] },
      { sectionId: 'S2', name: 'Other', channels: [{ id: C2, name: 'blogs' }, { id: C1, name: 'whats-new' }, { id: 'bad id', name: 'x' }] },
    ], sectionCount: 2, totalChannels: 3 });
  } };
  assert.deepEqual(await listChannels(session), [{ id: C1, name: 'whats-new' }, { id: C2, name: 'blogs' }]);
  assert.deepEqual(calls, [{ name: 'list_my_channels', args: { excludeArchived: true, compactOutput: true } }]);
  const broken = { async call() { return json({ channels: [] }); } };
  await assert.rejects(listChannels(broken), error => error.code === 'SCHEMA_CHANGED');
});

test('selectChannels adds and removes sources and never sends the channel ID', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const api = fakeApi();
  const first = await selectChannels(store, api, [{ id: C1, label: ' whats-new ' }, { id: C2, label: 'blogs' }], NOW);
  assert.equal(first.channels.length, 2);
  const adds = api.calls.filter(call => call[0] === 'add').map(call => call[1]);
  assert.equal(adds.length, 2);
  for (const body of adds) {
    assert.deepEqual(Object.keys(body).sort(), ['kind', 'label', 'localSourceId']);
    assert.equal(body.kind, 'slack');
    assert.match(body.localSourceId, /^[0-9a-f-]{36}$/);
    assert.ok(!JSON.stringify(body).includes(C1) && !JSON.stringify(body).includes(C2));
  }
  assert.equal(adds[0].label, 'whats-new');
  assert.notEqual(adds[0].localSourceId, adds[1].localSourceId);
  const stored = selectionOf(home);
  assert.equal(stored.version, 1);
  assert.deepEqual(stored.links.channels.map(c => [c.id, c.label, c.sourceId]), [[C1, 'whats-new', 's1'], [C2, 'blogs', 's2']]);

  api.calls.length = 0;
  const C3 = 'C0CCCCCCC3';
  await selectChannels(store, api, [{ id: C1, label: 'whats-new' }, { id: C3, label: '새 채널' }], NOW);
  assert.deepEqual(api.calls.map(call => call[0]), ['list', 'delete', 'add']);
  assert.equal(api.calls[1][1], 's2');
  assert.deepEqual(selectionOf(home).links.channels.map(c => [c.id, c.sourceId]), [[C1, 's1'], [C3, 's3']]);
});

test('selectChannels rejects invalid choices before any call', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const api = fakeApi();
  const many = Array.from({ length: 21 }, (_, index) => ({ id: `C0${String(index).padStart(8, '0')}`, label: `c${index}` }));
  const invalid = [
    many,
    [{ id: 'c0lowercase', label: 'x' }],
    [{ id: 'C1', label: 'x' }],
    [{ id: C1, label: '   ' }],
    [{ id: C1, label: '가'.repeat(101) }],
    [{ id: C1, label: 'a' }, { id: C1, label: 'b' }],
  ];
  for (const choices of invalid) {
    await assert.rejects(selectChannels(store, api, choices, NOW), error => error.code === 'INVALID_ARGUMENT');
  }
  assert.deepEqual(api.calls, []);
  // 100 code points, counted as code points (emoji are two UTF-16 units).
  await selectChannels(store, api, [{ id: C1, label: '😀'.repeat(100) }], NOW);
  assert.equal(selectionOf(home).links.channels.length, 1);
});

test('selectChannels retries SOURCES_BUSY and stores only created sources at the cap', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const busy = () => new ApiResponseError('HTTP 503 SOURCES_BUSY: busy', 503, 'SOURCES_BUSY');
  const api = fakeApi({ addFailures: [busy(), null, new ApiResponseError('HTTP 409 CONFLICT: Conflict', 409, 'CONFLICT')] });
  const sleeps = [];
  await assert.rejects(selectChannels(store, api, [{ id: C1, label: 'a' }, { id: C2, label: 'b' }], NOW,
    { sleep: async ms => { sleeps.push(ms); } }), error => error.code === 'TTOBAK_CONFLICT' && /limit/.test(error.message));
  assert.equal(sleeps.length, 1);
  const adds = api.calls.filter(call => call[0] === 'add');
  assert.equal(adds.length, 3);
  // The retry reuses the same local ID; the capped channel is not stored.
  assert.equal(adds[0][1].localSourceId, adds[1][1].localSourceId);
  const links = selectionOf(home).links;
  assert.deepEqual(links.channels.map(c => c.id), [C1]);
  // A definitive 409 created nothing, so nothing waits for cleanup.
  assert.deepEqual(links.pendingCleanup, []);
});

test('selectChannels removes a channel locally when its delete fails and cleans it up later', async t => {
  const { home, store } = await selected(t);
  const api = fakeApi({ sources: [{ sourceId: 's1', kind: 'slack', localSourceId: selectionOf(home).links.channels[0].localSourceId },
    { sourceId: 's2', kind: 'slack', localSourceId: selectionOf(home).links.channels[1].localSourceId }],
  deleteFailures: { s2: [new Error('socket hang up')] } });
  const result = await selectChannels(store, api, [{ id: C1, label: 'whats-new' }], NOW);
  assert.deepEqual(result.channels.map(c => c.id), [C1]);
  assert.equal(result.pendingCleanup.length, 1);
  assert.deepEqual(selectionOf(home).links.channels.map(c => c.id), [C1]);
  assert.ok(api.cloud.has('s2'));
  // The next selection retries the delete of the orphan, matched by its local ID.
  const again = await selectChannels(store, api, [{ id: C1, label: 'whats-new' }], NOW);
  assert.deepEqual(again.pendingCleanup, []);
  assert.ok(!api.cloud.has('s2'));
});

test('selectChannels re-registers a channel whose cloud source was deleted', async t => {
  const { home, store } = await selected(t, [[C1, 'whats-new']]);
  const api = fakeApi();
  await selectChannels(store, api, [{ id: C1, label: 'whats-new' }], NOW);
  assert.deepEqual(api.calls.map(call => call[0]), ['list', 'add']);
  assert.equal(selectionOf(home).links.channels[0].sourceId, 's1');
});

test('first run reads 3 days back, keeps the 50 newest links oldest first and sets the cursor', async t => {
  const { home, store, api } = await selected(t, [[C1, 'whats-new']]);
  const session = fakeSession({ [C1]: { messages: linkMessages(60) } });
  const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  assert.deepEqual(session.calls, [{ name: 'get_messages',
    args: { channel: C1, since: '2026-11-07T03:00:00.000Z', limit: 200, includeThreadReplies: false } }]);
  const [body] = uploads(api);
  assert.equal(body.sourceId, 's1');
  assert.equal(body.links.length, 50);
  assert.equal(body.links[0].url, 'https://news.example.com/n10');
  assert.equal(body.links[49].url, 'https://news.example.com/n59');
  assert.equal(body.links[0].seenAt, '2026-11-09T00:10:00.000Z');
  assert.deepEqual(Object.keys(body.links[0]).sort(), ['seenAt', 'url']);
  assert.deepEqual(result.channels, [{ label: 'whats-new', result: 'ok', messages: 60, links: 50, uploaded: 50, rejected: 0 }]);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-09T00:59:00.000Z' });
  assert.ok(!JSON.stringify(uploads(api)).includes(C1));
  assert.ok(!allText(home).includes('SECRET-TEXT'));
});

test('a later run sends the stored cursor, caps at 200 and continues after the cap', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  store.write({ [C1]: '2026-11-08T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  const session = fakeSession({ [C1]: { messages: [
    ...linkMessages(250),
    // At the cursor (already processed), with the `ts` fallback time field.
    { ts: ts('2026-11-08T00:00:00Z'), text: 'https://news.example.com/old' },
  ] } });
  const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false, batchSize: 100 });
  assert.equal(session.calls[0].args.since, '2026-11-08T00:00:00.000Z');
  const bodies = uploads(api);
  assert.deepEqual(bodies.map(body => body.links.length), [100, 100]);
  assert.ok(!bodies.flatMap(body => body.links).some(link => link.url.endsWith('/old')));
  assert.equal(result.channels[0].links, 200);
  // The 201st link's message is the first one the next run reads.
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-09T03:19:59.999Z' });
  api.calls.length = 0;
  const next = fakeSession({ [C1]: { messages: linkMessages(250) } });
  await collectLinks({ store, api, session: next, scope: SCOPE, now: NOW, dryRun: false });
  const urls = uploads(api).flatMap(body => body.links.map(link => link.url));
  assert.equal(urls[0], 'https://news.example.com/n200');
  assert.equal(urls.length, 50);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-09T04:09:00.000Z' });
});

test('unwrapText undoes datamarking and the server wrapper', async () => {
  const { unwrapText } = await import('../dist/intake/links.js');
  assert.equal(unwrapText(slackText('see https://example.com/x now')), '\nsee https://example.com/x now\n');
  assert.equal(unwrapText('<slack-user-content\uE000ts="1">a\uE000b</slack-user-content>'), 'a b');
  assert.equal(unwrapText('plain https://example.com/y'), 'plain https://example.com/y');
});

test('text links, block links and nested blocks yield each public link once, oldest first', async t => {
  const { home, store, api } = await selected(t, [[C1, 'whats-new']]);
  const session = fakeSession({ [C1]: { messages: [
    // Newest first, as the real server returns them.
    message('2026-11-09T05:00:00Z', `${SECRET}`, richText(
      { type: 'rich_text_list', style: 'bullet', elements: [
        section({ type: 'text', text: SECRET }, { type: 'link', url: 'https://example.com/nested' }),
      ] },
      section({ type: 'link', url: 'https://team.slack.com/archives/C0AAAAAAA1/p1', text: SECRET },
        { type: 'link', url: 42 }, { type: 'text', text: 'https://example.com/plain-text-element' }),
    )),
    message('2026-11-09T04:00:00Z', `${SECRET} 소식:https://example.com/glued 다음`),
    message('2026-11-09T03:00:00Z', 'see <https://example.com/a|read this> and https://example.com/b',
      richText(section({ type: 'link', url: 'https://example.com/a', text: 'read this' }))),
    message('2026-11-09T02:00:00Z', '', richText(section({ type: 'text', text: `${SECRET} ` },
      { type: 'link', url: 'https://example.com/block-only' }))),
    message('2026-11-09T01:30:00Z', 'again https://example.com/a <https://team.slack.com/archives/C1/p1> http://example.com/c'),
    { timestamp: '2026-11-09T01:00:00Z' },
    { text: 'https://example.com/no-time' },
    'not an object',
  ] } });
  const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  // The oldest message holding a link sets its seenAt; later repeats are dropped.
  assert.deepEqual(uploads(api)[0].links, [
    { url: 'https://example.com/a', seenAt: '2026-11-09T01:30:00.000Z' },
    { url: 'https://example.com/block-only', seenAt: '2026-11-09T02:00:00.000Z' },
    { url: 'https://example.com/b', seenAt: '2026-11-09T03:00:00.000Z' },
    { url: 'https://example.com/glued', seenAt: '2026-11-09T04:00:00.000Z' },
    { url: 'https://example.com/nested', seenAt: '2026-11-09T05:00:00.000Z' },
  ]);
  assert.equal(result.channels[0].messages, 5);
  assert.equal(result.channels[0].links, 5);
  assert.ok(!allText(home).includes('SECRET-TEXT'));
  assert.ok(!JSON.stringify(api.calls).includes('SECRET-TEXT'));
});

test('hasMore pages are followed newest first and reading stops at the window', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  const pages = {
    '': { messages: [message('2026-11-09T02:00:00Z', 'https://example.com/p2'), message('2026-11-08T02:00:00Z', 'https://example.com/p1')],
      hasMore: true, nextCursor: 'page2' },
    page2: { messages: [message('2026-11-07T04:00:00Z', 'https://example.com/p0'),
      message('2026-11-06T00:00:00Z', 'https://example.com/too-old')], hasMore: true, nextCursor: 'page3' },
  };
  const session = fakeSession({ [C1]: args => pages[args.cursor ?? ''] });
  await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  assert.deepEqual(session.calls.map(call => call.args.cursor), [undefined, 'page2']);
  assert.deepEqual(uploads(api)[0].links.map(link => link.url),
    ['https://example.com/p0', 'https://example.com/p1', 'https://example.com/p2']);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-09T02:00:00.000Z' });
});

test('pages stop when hasMore is false', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  const done = fakeSession({ [C1]: () => ({ messages: [message('2026-11-09T01:00:00Z', 'https://example.com/x')],
    hasMore: false, nextCursor: 'ignored' }) });
  await collectLinks({ store, api, session: done, scope: SCOPE, now: NOW, dryRun: true });
  assert.equal(done.calls.length, 1);
});

/** A channel served like the real tool: newest first, `since`/`until` inclusive, 200 per page. */
function simulatedChannel(messages) {
  const sorted = [...messages].sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
  return args => {
    const since = Date.parse(args.since);
    const until = args.until ? Date.parse(args.until) : Infinity;
    const inRange = sorted.filter(item => Date.parse(item.timestamp) >= since && Date.parse(item.timestamp) <= until);
    const offset = args.cursor ? Number(args.cursor) : 0;
    const page = inRange.slice(offset, offset + args.limit);
    const hasMore = offset + args.limit < inRange.length;
    return { messages: page, hasMore, nextCursor: hasMore ? String(offset + args.limit) : '' };
  };
}

function minuteMessages(count, startMs, prefix) {
  return Array.from({ length: count }, (_, index) =>
    message(startMs + (index + 1) * 60_000, `${SECRET} https://news.example.com/${prefix}${index + 1}`));
}

test('a later run over 1,500 new messages reads the oldest window only and later runs drain the rest', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  const cursorMs = NOW.getTime() - 2 * 24 * 60 * 60 * 1000;
  store.write({ [C1]: iso(cursorMs) }, 'links', SCOPE, 'cursors.json');
  const all = minuteMessages(1500, cursorMs, 'm');
  const session = fakeSession({ [C1]: simulatedChannel(all) });
  const first = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  // Full read (5 pages) and 24 h are truncated; the 12 h window (720 messages) is complete.
  assert.deepEqual(session.calls.map(call => call.args.until), [
    ...Array(5).fill(undefined), ...Array(5).fill(iso(cursorMs + 24 * 3600_000)), ...Array(4).fill(iso(cursorMs + 12 * 3600_000))]);
  assert.equal(first.channels[0].result, 'ok');
  const firstUrls = uploads(api).flatMap(body => body.links.map(link => link.url));
  assert.deepEqual(firstUrls, all.slice(0, 200).map((_, index) => `https://news.example.com/m${index + 1}`));
  // Never past an unread message: the 201st message is read next time.
  assert.ok(Date.parse(cursorsOf(store)[C1]) < Date.parse(all[200].timestamp));

  for (let run = 0; run < 20; run++) {
    session.calls.length = 0;
    const before = uploads(api).length;
    const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
    assert.equal(result.channels[0].result, 'ok');
    if (uploads(api).length === before) break;
  }
  const urls = uploads(api).flatMap(body => body.links.map(link => link.url));
  assert.equal(urls.length, 1500);
  assert.equal(new Set(urls).size, 1500);
  assert.deepEqual(cursorsOf(store), { [C1]: all[1499].timestamp });
});

test('a busy first run uploads at most 50 links and the next run only newer messages', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  const windowStart = NOW.getTime() - 3 * 24 * 60 * 60 * 1000;
  const all = minuteMessages(1500, windowStart, 'f');
  const session = fakeSession({ [C1]: simulatedChannel(all) });
  const first = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  // One truncated read of 5 pages (the newest 1,000), never narrowed.
  assert.equal(session.calls.length, 5);
  assert.ok(session.calls.every(call => call.args.until === undefined));
  assert.equal(first.channels[0].result, 'ok');
  const urls = uploads(api).flatMap(body => body.links.map(link => link.url));
  // The 50 newest of what was read, uploaded oldest first, as on an untruncated first run.
  assert.deepEqual(urls, Array.from({ length: 50 }, (_, index) => `https://news.example.com/f${1451 + index}`));
  assert.equal(cursorsOf(store)[C1], all[1499].timestamp);

  api.calls.length = 0;
  session.calls.length = 0;
  const newer = minuteMessages(10, Date.parse(all[1499].timestamp), 'g');
  const next = fakeSession({ [C1]: simulatedChannel([...all, ...newer]) });
  await collectLinks({ store, api, session: next, scope: SCOPE, now: NOW, dryRun: false });
  assert.equal(next.calls[0].args.since, all[1499].timestamp);
  assert.deepEqual(uploads(api).flatMap(body => body.links.map(link => link.url)),
    Array.from({ length: 10 }, (_, index) => `https://news.example.com/g${index + 1}`));
});

test('a window that never becomes readable uploads nothing and keeps the cursor', async t => {
  const { store, api } = await selected(t);
  store.write({ [C1]: '2026-11-08T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  let page = 0;
  // Ignores `until`, so every read stays truncated.
  const endless = { [C1]: () => {
    page++;
    return { messages: [message(NOW.getTime() - page * 1000, `https://example.com/p${page}`)], hasMore: true, nextCursor: `page${page}` };
  }, [C2]: { messages: linkMessages(1) } };
  const session = fakeSession(endless);
  const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  assert.deepEqual(result.channels.map(c => c.result), ['SOURCE_TOO_LARGE', 'ok']);
  assert.equal(session.calls.filter(call => call.args.channel === C1).length, 35);
  assert.deepEqual(uploads(api).map(body => body.sourceId), ['s2']);
  assert.equal(cursorsOf(store)[C1], '2026-11-08T00:00:00.000Z');
});

test('extractUrls keeps 20 by default and takes a larger max', async () => {
  const { extractUrls } = await import('../dist/intake/link-extract.js');
  const text = Array.from({ length: 30 }, (_, index) => `https://example.com/${index}`).join(' ');
  assert.equal(extractUrls(text).length, 20);
  assert.equal(extractUrls(text, 200).length, 30);
});

test('internal links do not crowd a public link out of the per-message cap', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  const internal = Array.from({ length: 20 }, (_, index) => `https://w.amazon.com/page${index}`).join(' ');
  const many = Array.from({ length: 25 }, (_, index) => `https://example.com/many${index}`).join(' ');
  const session = fakeSession({ [C1]: { messages: [
    message('2026-11-09T02:00:00Z', many),
    message('2026-11-09T01:00:00Z', `${internal} https://example.com/public`),
  ] } });
  await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  const urls = uploads(api)[0].links.map(link => link.url);
  assert.equal(urls[0], 'https://example.com/public');
  assert.equal(urls.length, 21);
  assert.equal(urls.at(-1), 'https://example.com/many19');
});

test('selectChannels relabels a kept channel and keeps the old label when that fails', async t => {
  const { home, store } = await selected(t);
  const [first, second] = selectionOf(home).links.channels;
  const sources = [first, second].map(c => ({ sourceId: c.sourceId, kind: 'slack', localSourceId: c.localSourceId }));
  const api = fakeApi({ sources, patchFailures: { [second.sourceId]: [new ApiResponseError('HTTP 500', 500)] } });
  const C3 = 'C0CCCCCCC3';
  const result = await selectChannels(store, api,
    [{ id: C1, label: '새 이름' }, { id: C2, label: 'renamed' }, { id: C3, label: 'third' }], NOW);
  assert.deepEqual(api.calls.filter(call => call[0] === 'patch'),
    [['patch', first.sourceId, { label: '새 이름' }], ['patch', second.sourceId, { label: 'renamed' }]]);
  assert.deepEqual(result.relabelFailed, [C2]);
  assert.deepEqual(selectionOf(home).links.channels.map(c => [c.id, c.label]),
    [[C1, '새 이름'], [C2, 'blogs'], [C3, 'third']]);
  // An unchanged label sends no PATCH.
  api.calls.length = 0;
  await selectChannels(store, api, [{ id: C1, label: '새 이름' }], NOW);
  assert.ok(!api.calls.some(call => call[0] === 'patch'));
});

test('an upload failing on the second batch keeps the cursor and a re-run sends the same links', async t => {
  const { store } = await selected(t);
  store.write({ [C1]: '2026-11-08T00:00:00.000Z', [C2]: '2026-11-08T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  const api = fakeApi({ failUploads: { 2: new Error('socket hang up') } });
  const messages = { [C1]: { messages: linkMessages(5) }, [C2]: { messages: linkMessages(2, '2026-11-09T00:00:00Z', 'b') } };
  const result = await collectLinks({ store, api, session: fakeSession(messages), scope: SCOPE, now: NOW, dryRun: false, batchSize: 3 });
  assert.deepEqual(result.channels.map(c => [c.result, c.uploaded]), [['TTOBAK_UNAVAILABLE', 3], ['ok', 2]]);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-08T00:00:00.000Z', [C2]: '2026-11-09T00:01:00.000Z' });
  const firstUrls = uploads(api).filter(body => body.sourceId === 's1').flatMap(body => body.links.map(l => l.url));
  const retry = fakeApi();
  await collectLinks({ store, api: retry, session: fakeSession(messages), scope: SCOPE, now: NOW, dryRun: false, batchSize: 3 });
  const again = uploads(retry).filter(body => body.sourceId === 's1').flatMap(body => body.links.map(l => l.url));
  assert.deepEqual(again, firstUrls);
  assert.equal(again.length, 5);
  assert.equal(cursorsOf(store)[C1], '2026-11-09T00:04:00.000Z');
});

test('a rejected upload records its code for that channel only', async t => {
  const { store } = await selected(t);
  const api = fakeApi({ failUploads: { 1: new ApiResponseError('HTTP 409 CONFLICT: Conflict', 409, 'CONFLICT') } });
  const result = await collectLinks({ store, api, scope: SCOPE, now: NOW, dryRun: false,
    session: fakeSession({ [C1]: { messages: linkMessages(1) }, [C2]: { messages: linkMessages(1) } }) });
  assert.deepEqual(result.channels.map(c => c.result), ['TTOBAK_CONFLICT', 'ok']);
  assert.deepEqual(Object.keys(cursorsOf(store)), [C2]);
});

test('cursors of deselected channels are dropped on the next write', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  store.write({ C0DESELECT1: '2026-11-01T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  await collectLinks({ store, api, session: fakeSession({ [C1]: { messages: linkMessages(1) } }), scope: SCOPE, now: NOW, dryRun: false });
  assert.deepEqual(Object.keys(cursorsOf(store)), [C1]);
});

test('a collection drops the cursor of an unselected channel even when no channel advances', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  store.write({ C0DESELECT1: '2026-11-01T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  const result = await collectLinks({ store, api, session: fakeSession({ [C1]: new Error('down') }), scope: SCOPE, now: NOW,
    dryRun: false });
  assert.equal(result.channels[0].result, 'SOURCE_ERROR');
  assert.deepEqual(cursorsOf(store), {});
});

test('a cleared and reselected channel starts a fresh first run in every account scope', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  const OTHER = 'b'.repeat(32);
  store.write({ [C1]: '2026-10-01T00:00:00.000Z' }, 'links', OTHER, 'cursors.json');
  await collectLinks({ store, api, session: fakeSession({ [C1]: { messages: linkMessages(1) } }), scope: SCOPE, now: NOW, dryRun: false });
  assert.ok(cursorsOf(store)[C1]);

  await selectChannels(store, api, [], NOW);
  assert.deepEqual(cursorsOf(store), {});
  assert.deepEqual(store.read('links', OTHER, 'cursors.json'), {});

  await selectChannels(store, api, [{ id: C1, label: 'whats-new' }], NOW);
  api.calls.length = 0;
  const later = new Date('2026-12-10T03:00:00Z');
  const session = fakeSession({ [C1]: { messages: linkMessages(60, '2026-12-09T00:00:00Z') } });
  await collectLinks({ store, api, session, scope: SCOPE, now: later, dryRun: false });
  assert.equal(session.calls[0].args.since, '2026-12-07T03:00:00.000Z');
  assert.equal(uploads(api)[0].links.length, 50);
});

test('a corrupt ledger in another scope does not block selecting channels', async t => {
  const { store, api } = await selected(t);
  store.write({ [C1]: '2026-11-08T00:00:00.000Z', [C2]: '2026-11-08T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  store.writeText('not json', 'links', 'b'.repeat(32), 'cursors.json');
  await selectChannels(store, api, [{ id: C1, label: 'whats-new' }], NOW);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-08T00:00:00.000Z' });
  assert.equal(store.readText('links', 'b'.repeat(32), 'cursors.json'), 'not json', 'the corrupt ledger is left as is');
});

test('a channel failing with SOURCE_ERROR reopens the session for the next channel', async t => {
  const { store, api } = await selected(t);
  const { IntakeError } = await import('../dist/intake/errors.js');
  const dead = { async call() { throw new IntakeError('SOURCE_ERROR', 'get_messages call failed'); } };
  const fresh = fakeSession({ [C2]: { messages: linkMessages(1) } });
  let reopened = 0;
  const result = await collectLinks({ store, api, session: dead, scope: SCOPE, now: NOW, dryRun: false,
    reopen: async () => { reopened++; return fresh; } });
  assert.equal(reopened, 1);
  assert.deepEqual(result.channels.map(c => [c.result, c.uploaded]), [['SOURCE_ERROR', 0], ['ok', 1]]);
  assert.deepEqual(fresh.calls.map(call => call.args.channel), [C2]);
});

test('a failed reopen gives the remaining channels its code and the run ends normally', async t => {
  const { store, api } = await selected(t, [[C1, 'a'], [C2, 'b'], ['C0CCCCCCC3', 'c']]);
  const { IntakeError } = await import('../dist/intake/errors.js');
  const dead = { async call() { throw new IntakeError('SOURCE_TIMEOUT', 'get_messages call failed'); } };
  let reopened = 0;
  const result = await collectLinks({ store, api, session: dead, scope: SCOPE, now: NOW, dryRun: false,
    reopen: async () => { reopened++; throw new IntakeError('SERVER_START_FAILED', 'slack did not start'); } });
  assert.equal(reopened, 1);
  assert.deepEqual(result.channels.map(c => c.result), ['SOURCE_TIMEOUT', 'SERVER_START_FAILED', 'SERVER_START_FAILED']);
});

test('the last failing channel does not reopen the session', async t => {
  const { store, api } = await selected(t, [[C1, 'a']]);
  let reopened = 0;
  await collectLinks({ store, api, session: fakeSession({ [C1]: new Error('down') }), scope: SCOPE, now: NOW, dryRun: false,
    reopen: async () => { reopened++; return fakeSession({}); } });
  assert.equal(reopened, 0);
});

test('a kept channel keeps its cursor while another one is deselected', async t => {
  const { store, api } = await selected(t);
  store.write({ [C1]: '2026-11-08T00:00:00.000Z', [C2]: '2026-11-08T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  await selectChannels(store, api, [{ id: C1, label: 'whats-new' }], NOW);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-08T00:00:00.000Z' });
});

test('selecting channels keeps a links connector that setup disabled', async t => {
  const { home, store, api } = await selected(t, [[C1, 'whats-new']]);
  const stored = selectionOf(home);
  store.write({ ...stored, links: { ...stored.links, disabled: true } }, 'selection.json');
  await selectChannels(store, api, [{ id: C1, label: 'whats-new' }, { id: C2, label: 'blogs' }], NOW);
  const after = selectionOf(home).links;
  assert.equal(after.disabled, true);
  assert.deepEqual(after.channels.map(c => c.id), [C1, C2]);
});

test('a later read with hasMore but no usable cursor is truncated and narrowed', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  store.write({ [C1]: '2026-11-09T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  const session = fakeSession({ [C1]: args => args.until
    ? { messages: [message('2026-11-09T01:00:00Z', 'https://example.com/early')] }
    : { messages: [message('2026-11-10T02:00:00Z', 'https://example.com/late')], hasMore: true, nextCursor: '' } });
  const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  assert.equal(result.channels[0].result, 'ok');
  assert.deepEqual(session.calls.map(call => call.args.until), [undefined, '2026-11-09T13:30:00.000Z']);
  assert.deepEqual(uploads(api)[0].links.map(link => link.url), ['https://example.com/early']);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-09T13:29:59.999Z' });
});

test('an empty narrowed window moves the cursor past it and uploads nothing', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  store.write({ [C1]: '2026-11-09T00:00:00.000Z' }, 'links', SCOPE, 'cursors.json');
  let page = 0;
  const session = fakeSession({ [C1]: args => args.until ? { messages: [] }
    : { messages: [message(NOW.getTime() - ++page * 1000, `https://example.com/p${page}`)], hasMore: true, nextCursor: `c${page}` } });
  const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  assert.equal(result.channels[0].result, 'ok');
  assert.equal(result.channels[0].links, 0);
  assert.deepEqual(uploads(api), []);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-09T13:29:59.999Z' });
});

test('a TTOBAK login failure stops the run', async t => {
  const { store } = await selected(t);
  const api = fakeApi({ failUploads: { 1: new ApiResponseError('HTTP 401', 401) } });
  await assert.rejects(collectLinks({ store, api, scope: SCOPE, now: NOW, dryRun: false,
    session: fakeSession({ [C1]: { messages: linkMessages(1) } }) }), error => error.code === 'TTOBAK_LOGIN_REQUIRED');
});

test('dryRun uploads nothing and keeps the cursor', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  const result = await collectLinks({ store, api, session: fakeSession({ [C1]: { messages: linkMessages(3) } }),
    scope: SCOPE, now: NOW, dryRun: true });
  assert.deepEqual(api.calls, []);
  assert.equal(cursorsOf(store), null);
  assert.deepEqual(result.channels, [{ label: 'whats-new', result: 'ok', messages: 3, links: 3, uploaded: 0, rejected: 0 }]);
});

test('a result without messages is SCHEMA_CHANGED for that channel and the others still run', async t => {
  const { home, store, api } = await selected(t);
  const session = fakeSession({ [C1]: { items: [{ text: SECRET }] }, [C2]: { messages: linkMessages(1) } });
  const result = await collectLinks({ store, api, session, scope: SCOPE, now: NOW, dryRun: false });
  assert.deepEqual(result.channels.map(c => c.result), ['SCHEMA_CHANGED', 'ok']);
  assert.ok(!JSON.stringify(result).includes('SECRET-TEXT'));
  assert.ok(!allText(home).includes('SECRET-TEXT'));
});

test('a channel with no messages on its first run starts its cursor at the window', async t => {
  const { store, api } = await selected(t, [[C1, 'whats-new']]);
  await collectLinks({ store, api, session: fakeSession({}), scope: SCOPE, now: NOW, dryRun: false });
  assert.deepEqual(api.calls, []);
  assert.deepEqual(cursorsOf(store), { [C1]: '2026-11-07T03:00:00.000Z' });
});

test('stdio server: a failing channel gets its own code and Midway expiry stops without retry', async t => {
  const { home, store, api } = await selected(t);
  const log = join(home, 'calls.log');
  const open = async messages => {
    const session = await McpSession.open({ command: process.execPath, args: [FIXTURE],
      env: { FAKE_WORK_LOG: log, FAKE_WORK_MESSAGES: JSON.stringify(messages) } }, 'work',
    { pathDirs: ['/usr/bin', '/bin'], allowedTools: LINKS_TOOLS.map(tool => tool.name) });
    t.after(() => session.close());
    return session;
  };
  const healthy = await open({ [C1]: 'error', [C2]: { messages: linkMessages(1).map(m => ({ ...m, text: `${SECRET} https://example.com/x` })) } });
  const result = await collectLinks({ store, api, session: healthy, scope: SCOPE, now: NOW, dryRun: false });
  assert.deepEqual(result.channels.map(c => c.result), ['SOURCE_ERROR', 'ok']);
  assert.deepEqual(uploads(api).map(body => body.sourceId), ['s2']);

  rmSync(log, { force: true });
  const expired = await open({ [C1]: 'auth', [C2]: { messages: [] } });
  let thrown;
  await assert.rejects(collectLinks({ store, api, session: expired, scope: SCOPE, now: NOW, dryRun: false }),
    error => { thrown = error; return error.code === 'AUTH_EXPIRED_MIDWAY'; });
  assert.equal(readFileSync(log, 'utf8'), 'get_messages\n');
  assert.ok(!thrown.message.includes('SECRET-TEXT'));
  assert.ok(!allText(home).includes('SECRET-TEXT'));
});
