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

const display = loadModule('lib/calendarDisplay.ts');
const ids = events => [...events].map(event => event.eventId);
const event = (eventId, startsAt, extra = {}) => ({
  eventId, sourceId: 's', provider: 'outlook', title: eventId, startsAt,
  endsAt: startsAt, timeZone: 'Asia/Seoul', isAllDay: false, ...extra,
});

test('customer_events_first_order_kept', () => {
  const events = [
    event('a', '2026-11-11T01:00:00Z'),
    event('b', '2026-11-11T02:00:00Z', { accountId: 'acc-1' }),
    event('c', '2026-11-11T03:00:00Z'),
    event('d', '2026-11-11T04:00:00Z', { accountId: 'acc-2' }),
  ];
  const { customer, internal } = display.partitionCustomerEvents(events);
  assert.deepEqual(ids(customer), ['b', 'd']);
  assert.deepEqual(ids(internal), ['a', 'c']);
});

test('all_day_events_are_internal', () => {
  const { customer, internal } = display.partitionCustomerEvents([
    event('x', '2026-11-11T00:00:00Z', { isAllDay: true, accountId: 'acc-1' }),
    event('y', '2026-11-11T05:00:00Z', { accountId: 'acc-1' }),
  ]);
  assert.deepEqual(ids(customer), ['y']);
  assert.deepEqual(ids(internal), ['x']);
});

test('empty_lists', () => {
  assert.deepEqual(JSON.parse(JSON.stringify(display.partitionCustomerEvents([]))), { customer: [], internal: [] });
  const onlyInternal = display.partitionCustomerEvents([event('a', '2026-11-11T01:00:00Z', { accountId: '' })]);
  assert.deepEqual(ids(onlyInternal.customer), []);
  assert.deepEqual(ids(onlyInternal.internal), ['a']);
});

test('internal toggle label uses the fixed copy', () => {
  assert.equal(display.internalEventsToggleLabel(3), '사내 일정 3건');
});

test('collapse preference defaults to collapsed and survives storage errors', () => {
  const store = new Map();
  const storage = { getItem: key => store.get(key) ?? null, setItem: (key, value) => store.set(key, value) };
  assert.equal(display.readInternalEventsCollapsed(storage), true);
  display.writeInternalEventsCollapsed(false, storage);
  assert.equal(display.readInternalEventsCollapsed(storage), false);
  display.writeInternalEventsCollapsed(true, storage);
  assert.equal(display.readInternalEventsCollapsed(storage), true);
  const broken = { getItem: () => { throw new Error('denied'); }, setItem: () => { throw new Error('denied'); } };
  assert.equal(display.readInternalEventsCollapsed(broken), true);
  assert.doesNotThrow(() => display.writeInternalEventsCollapsed(false, broken));
  assert.equal(display.readInternalEventsCollapsed(null), true);
});
