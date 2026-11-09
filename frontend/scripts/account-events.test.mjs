import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const transpile = path => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const helpers = {};
vm.runInNewContext(transpile('../src/lib/accountEvents.ts'), { exports: helpers, Date, TextEncoder });
const plain = value => JSON.parse(JSON.stringify(value));
const form = overrides => ({ ...helpers.EMPTY_EVENT_FORM, title: '리뷰', start: '2026-11-10T14:00', ...overrides });

test('local datetime inputs round-trip through UTC RFC 3339', () => {
  const utc = helpers.fromLocalInput('2026-11-10T14:00');
  assert.match(utc, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:00Z$/);
  assert.equal(helpers.toLocalInput(utc), '2026-11-10T14:00');
  assert.equal(helpers.toLocalInput('2026-11-10T05:00:00+09:00'), helpers.toLocalInput('2026-11-09T20:00:00Z'));
  for (const bad of ['', 'tomorrow', '2026-11-10']) assert.equal(helpers.fromLocalInput(bad), null);
  assert.equal(helpers.toLocalInput(undefined), '');
});

test('event input mirrors the API limits and never sends server-owned fields', () => {
  const { input } = helpers.eventInputFromForm(form({ end: '2026-11-10T15:30', location: ' 본사 ', attendees: [{ contactId: 'c1', name: '홍길동' }, { name: ' 외부 ', email: ' a@example.com ' }] }), 'rev');
  assert.deepEqual(plain(input), {
    revision: 'rev', title: '리뷰', startAt: helpers.fromLocalInput('2026-11-10T14:00'), endAt: helpers.fromLocalInput('2026-11-10T15:30'),
    allDay: false, location: '본사', attendees: [{ contactId: 'c1', name: '홍길동' }, { name: '외부', email: 'a@example.com' }], notes: '',
  });
  for (const [overrides, message] of [
    [{ title: ' ' }, /제목/], [{ start: '' }, /시작/], [{ end: '2026-11-10T13:00' }, /이후/],
    [{ end: '2026-12-20T14:00' }, /31일/], [{ notes: 'x'.repeat(8193) }, /메모/],
    [{ attendees: [{ name: 'a', email: 'nope' }] }, /참석자/], [{ attendees: Array.from({ length: 51 }, (_, i) => ({ name: `n${i}` })) }, /50명/],
    [{ notes: 'x'.repeat(8000), attendees: Array.from({ length: 20 }, (_, i) => ({ name: 'n'.repeat(200) + i })) }, /너무 깁니다/],
  ]) {
    const { input: rejected, error } = helpers.eventInputFromForm(form(overrides));
    assert.equal(rejected, undefined);
    assert.match(error, message);
  }
});

test('attendees dedupe by contact or by name and email', () => {
  let list = helpers.addAttendee([], { contactId: 'c1', name: 'A' });
  list = helpers.addAttendee(list, { contactId: 'c1', name: 'A again' });
  list = helpers.addAttendee(list, { name: 'Guest', email: 'g@example.com' });
  list = helpers.addAttendee(list, { name: ' guest ', email: 'G@example.com ' });
  assert.equal(list.length, 2);
});

// Renders the shipped component with a minimal hook runtime.
function fixture(api) {
  const slots = []; let cursor = 0; let effects = [];
  const same = (a, b) => a?.length === b?.length && a.every((x, i) => Object.is(x, b[i]));
  const react = {
    useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = typeof initial === 'function' ? initial() : initial; return [slots[i], v => { slots[i] = typeof v === 'function' ? v(slots[i]) : v; }]; },
    useRef(initial) { const i = cursor++; return slots[i] ||= { current: initial }; },
    useCallback(fn, deps) { const i = cursor++; if (!same(slots[i]?.deps, deps)) slots[i] = { deps, fn }; return slots[i].fn; },
    useEffect(fn, deps) { const i = cursor++; if (!same(slots[i]?.deps, deps)) effects.push(() => { slots[i]?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; }); },
  };
  const jsx = (type, props) => ({ type, props });
  const exports = {};
  const modules = {
    react, 'react/jsx-runtime': { jsx, jsxs: jsx }, '@/lib/api': { accountApi: api },
    '@/lib/dialog': { confirmDialog: async () => true },
    '@/lib/contactImport': { isContactAccessDenied: e => e?.status === 403 || e?.status === 401 },
    '@/lib/accountEvents': helpers,
    '@/lib/accountOverview': { LINK_KIND_EVENT: 'event' },
    '@/components/account/LinkToOpportunity': { LinkToOpportunity: () => null },
  };
  vm.runInNewContext(transpile('../src/components/AccountEvents.tsx'), { exports, require: name => modules[name], AbortController, Date, console });
  const draw = () => { cursor = 0; effects = []; const tree = exports.AccountEvents({ accountId: 'a', userId: 'u' }); effects.forEach(fn => fn()); return tree; };
  return { draw };
}
const tick = async () => { for (let i = 0; i < 6; i++) await new Promise(resolve => setImmediate(resolve)); };
const nodes = value => (!value || typeof value !== 'object') ? [] : [value, ...[value.props?.children].flat(Infinity).flatMap(nodes)];
const text = value => typeof value === 'string' ? value : Array.isArray(value) ? value.map(text).join('') : value?.props ? text(value.props.children) : '';
const button = (tree, label) => nodes(tree).find(n => n.type === 'button' && text(n) === label);

test('upcoming and past lists, and the past bound stays fixed across pages', async () => {
  const calls = [];
  const f = fixture({
    events: async (_id, _user, options) => { calls.push(options); return options.cursor ? { events: [] } : { events: [{ eventId: 'e1', revision: 'r', title: '분기 리뷰', startAt: '2026-11-10T05:00:00Z', attendees: [{ name: '홍길동' }] }], nextCursor: 'next' }; },
    contacts: async () => ({ contacts: [] }),
  });
  f.draw(); await tick();
  let tree = f.draw();
  assert.match(text(tree), /분기 리뷰/);
  assert.match(text(tree), /참석자: 홍길동/);
  assert.deepEqual(plain(calls[0]), { cursor: '' });
  button(tree, '지난 일정').props.onClick();
  f.draw(); await tick(); tree = f.draw();
  const past = calls.at(-1);
  assert.equal(past.order, 'desc');
  button(tree, '일정 더 보기').props.onClick();
  await tick();
  assert.equal(calls.at(-1).cursor, 'next');
  assert.equal(calls.at(-1).to, past.to, 'a continuation reuses the bound its cursor was issued for');
});

test('a denied write hides the schedule and its controls', async () => {
  const f = fixture({
    events: async () => ({ events: [{ eventId: 'e1', revision: 'r', title: 'Private Title', startAt: '2026-11-10T05:00:00Z', attendees: [] }] }),
    contacts: async () => ({ contacts: [] }),
    deleteEvent: async () => { throw { status: 403 }; },
  });
  f.draw(); await tick();
  let tree = f.draw();
  button(tree, '삭제').props.onClick();
  await tick(); tree = f.draw();
  assert.match(text(tree), /접근 권한이 없습니다/);
  assert.doesNotMatch(text(tree), /Private Title/);
  assert.equal(nodes(tree).some(n => n.type === 'button'), false);
});

test('creating sends only the validated input and reloads', async () => {
  const created = [];
  const f = fixture({
    events: async () => ({ events: [] }),
    contacts: async () => ({ contacts: [{ contactId: 'c1', revision: 'r', name: '홍길동', email: 'h@example.com' }] }),
    createEvent: async (_id, input) => { created.push(input); return { eventId: 'new' }; },
  });
  f.draw(); await tick();
  let tree = f.draw();
  button(tree, '일정 추가').props.onClick();
  tree = f.draw();
  const inputs = nodes(tree).filter(n => n.type === 'input');
  inputs[0].props.onChange({ target: { value: '킥오프' } });
  tree = f.draw();
  nodes(tree).filter(n => n.type === 'input')[1].props.onChange({ target: { value: '2026-11-12T10:00' } });
  tree = f.draw();
  nodes(tree).find(n => n.type === 'select').props.onChange({ target: { value: 'c1' } });
  tree = f.draw();
  button(tree, '저장').props.onClick();
  await tick();
  assert.equal(created.length, 1);
  assert.equal(created[0].title, '킥오프');
  assert.deepEqual(plain(created[0].attendees), [{ contactId: 'c1', name: '홍길동', email: 'h@example.com' }]);
  assert.equal('createdBy' in created[0] || 'eventId' in created[0], false);
});

test('the range buttons cannot abort a pending save or blank the active list', async () => {
  let release;
  const f = fixture({
    events: async () => ({ events: [{ eventId: 'e1', revision: 'r', title: '유지되는 일정', startAt: '2026-11-10T05:00:00Z', attendees: [] }] }),
    contacts: async () => ({ contacts: [] }),
    deleteEvent: () => new Promise(resolve => { release = resolve; }),
  });
  f.draw(); await tick();
  let tree = f.draw();
  button(tree, '다가오는 일정').props.onClick();
  tree = f.draw();
  assert.match(text(tree), /유지되는 일정/, 're-selecting the active range keeps the list');
  button(tree, '삭제').props.onClick();
  await tick(); tree = f.draw();
  assert.equal(button(tree, '지난 일정').props.disabled, true, 'range switching waits for the pending delete');
  release();
  await tick(); tree = f.draw();
  assert.equal(button(tree, '지난 일정').props.disabled, false);
  assert.equal(button(tree, '일정 추가').props.disabled, false, 'controls are usable again after the delete');
});
