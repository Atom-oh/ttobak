import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const helpers = {};
vm.runInNewContext(ts.transpileModule(readFileSync(new URL('../src/lib/calendarSharing.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: helpers });
const plain = value => JSON.parse(JSON.stringify(value));
const event = extra => ({
  eventId: 'e1', sourceId: 's1', provider: 'outlook', title: 'Sync', startsAt: '2026-11-10T01:00:00Z',
  endsAt: '2026-11-10T02:00:00Z', timeZone: 'Asia/Seoul', isAllDay: false, ...extra,
});
const direct = ['a1', 'a2'];

test('badge classifies shared, candidate, private and none', () => {
  assert.deepEqual(plain(helpers.sharingBadge(event({ shareAccountId: 'a1', shareEvidence: ['title', 'domain'], sharingMode: 'auto' }))),
    { kind: 'shared', accountId: 'a1', evidence: ['title', 'domain'] });
  assert.deepEqual(plain(helpers.sharingBadge(event({ shareAccountId: 'a1' }))), { kind: 'shared', accountId: 'a1', evidence: [] });
  assert.deepEqual(plain(helpers.sharingBadge(event({ candidateAccountIds: ['a2'] }))), { kind: 'candidate' });
  assert.deepEqual(plain(helpers.sharingBadge(event({ private: true }))), { kind: 'private' });
  // A granted share stays visible until it is actually removed.
  assert.deepEqual(plain(helpers.sharingBadge(event({ private: true, shareAccountId: 'a1' }))), { kind: 'shared', accountId: 'a1', evidence: [] });
  assert.deepEqual(plain(helpers.sharingBadge(event({}))), { kind: 'none' });
  assert.deepEqual(plain(helpers.sharingBadge(event({ sharingMode: 'hidden', candidateAccountIds: ['a2'] }))), { kind: 'none' });
  assert.deepEqual(plain(helpers.sharingBadge(event({ sharingMode: 'hidden', shareAccountId: 'a1', shareEvidence: ['domain'] }))),
    { kind: 'shared', accountId: 'a1', evidence: ['domain'] });
});

test('actions follow badge kind and manual mode', () => {
  assert.deepEqual(plain(helpers.sharingActions(event({ shareAccountId: 'a1' }), direct)), ['unshare']);
  assert.deepEqual(plain(helpers.sharingActions(event({ candidateAccountIds: ['a2'] }), direct)), ['assign']);
  assert.deepEqual(plain(helpers.sharingActions(event({}), direct)), ['assign']);
  assert.deepEqual(plain(helpers.sharingActions(event({ private: true }), direct)), []);
  assert.deepEqual(plain(helpers.sharingActions(event({ sharingMode: 'hidden' }), direct)), ['assign', 'reset']);
  assert.deepEqual(plain(helpers.sharingActions(event({ sharingMode: 'account', shareAccountId: 'a1' }), direct)), ['unshare', 'reset']);
  assert.deepEqual(plain(helpers.sharingActions(event({ private: true, sharingMode: 'hidden' }), direct)), ['reset']);
  // A hidden override whose share change failed keeps unshare so the owner can retry.
  assert.deepEqual(plain(helpers.sharingActions(event({ sharingMode: 'hidden', shareAccountId: 'a1' }), direct)), ['unshare', 'reset']);
  assert.deepEqual(plain(helpers.sharingActions(event({}), [])), []);
});

test('picker puts candidates first and names resolve through the directory', () => {
  const directory = [{ accountId: 'a1', name: 'Alpha' }, { accountId: 'a2', name: 'Beta' }, { accountId: 'a3', name: 'Gamma' }];
  assert.deepEqual(plain(helpers.orderAccountChoices(directory, ['a3', 'zz'])).map(a => a.accountId), ['a3', 'a1', 'a2']);
  assert.equal(helpers.accountLabel(directory, 'a2'), 'Beta');
  assert.equal(helpers.accountLabel(directory, 'zz'), 'zz');
});

test('replaceEvent swaps by id and copy is defined once', () => {
  const list = [event({}), event({ eventId: 'e2' })];
  const next = helpers.replaceEvent(list, event({ eventId: 'e2', sharingMode: 'hidden' }));
  assert.equal(next[1].sharingMode, 'hidden');
  assert.equal(next[0], list[0]);
  assert.equal(helpers.sharedByLabel(3), '3명이 공유');
  assert.ok(helpers.SHARING_COPY.assign && helpers.SHARING_COPY.unshare && helpers.SHARING_COPY.reset);
});

test('sharing overlay applies only while the replaced event object is current', () => {
  const loaded = [event({}), event({ eventId: 'e2' })];
  const shared = event({ shareAccountId: 'a1', sharingMode: 'account' });
  let overlay = helpers.recordSharingOverlay(loaded, [], shared);
  assert.equal(helpers.applySharingOverlay(loaded, overlay)[0], shared);
  assert.equal(helpers.applySharingOverlay(loaded, overlay)[1], loaded[1]);
  // Paging appends but keeps the loaded objects, so the response still shows.
  const paged = [...loaded, event({ eventId: 'e3' })];
  assert.equal(helpers.applySharingOverlay(paged, overlay)[0], shared);
  // A reload returns new objects: the fresh row wins, even with a stale shared response.
  const reloaded = [event({ title: 'Renamed', private: true }), event({ eventId: 'e2' })];
  assert.equal(helpers.applySharingOverlay(reloaded, overlay)[0], reloaded[0]);
  // A later response replaces the earlier one and drops entries no longer loaded.
  overlay = helpers.recordSharingOverlay(reloaded, overlay, event({ title: 'Renamed', sharingMode: 'hidden' }));
  assert.equal(overlay.length, 1);
  assert.equal(overlay[0].base, reloaded[0]);
  assert.equal(helpers.recordSharingOverlay(reloaded, overlay, event({ eventId: 'gone' })).length, 1);
});
