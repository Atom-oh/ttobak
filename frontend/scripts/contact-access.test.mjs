import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const tick = async () => { for (let i = 0; i < 5; i++) await new Promise((resolve) => setImmediate(resolve)); };
const jsx = (type, props) => ({ type, props });
function fixture(confirmDialog = async () => true) {
  const slots = []; let cursor = 0; let effects = [];
  const same = (a, b) => a?.length === b?.length && a.every((x, i) => Object.is(x, b[i]));
  const react = {
    useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = typeof initial === 'function' ? initial() : initial; return [slots[i], (v) => { slots[i] = typeof v === 'function' ? v(slots[i]) : v; }]; },
    useRef(initial) { const i = cursor++; return slots[i] ||= { current: initial }; },
    useCallback(fn, deps) { const i = cursor++; if (!same(slots[i]?.deps, deps)) slots[i] = { deps, fn }; return slots[i].fn; },
    useEffect(fn, deps) { const i = cursor++; if (!same(slots[i]?.deps, deps)) effects.push(() => { slots[i]?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; }); },
  };
  const source = readFileSync(new URL('../src/components/AccountContacts.tsx', import.meta.url), 'utf8');
  const output = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText;
  const denied = { status: 403 };
  const deletions = [];
  const api = { contacts: async () => ({ contacts: [{ contactId: 'id', revision: 'rev', name: 'Private Name', notes: 'Private Note' }] }), updateContact: async () => { throw denied; }, deleteContact: async (...args) => { deletions.push(args); throw denied; } };
  const ContactImport = () => null;
  const modules = { react, 'react/jsx-runtime': { jsx, jsxs: jsx }, '@/lib/api': { accountApi: api }, '@/lib/dialog': { confirmDialog }, '@/lib/contactImport': { CONTACT_FIELDS: [['name', '이름']], isContactAccessDenied: (e) => e?.status === 403 || e?.status === 401 }, '@/lib/contactSfdc': { BADGE_LABELS: {}, contactBadges: () => [], sortContactsForDisplay: (list) => list, leftSfdcTooltip: () => '' }, '@/components/ContactImport': { ContactImport } };
  const exports = {};
  vm.runInNewContext(output, { exports, require: (name) => modules[name], AbortController, window: { confirm: () => true } });
  const draw = () => { cursor = 0; effects = []; const result = exports.AccountContacts({ accountId: 'a', userId: 'u' }); effects.forEach((fn) => fn()); return result; };
  return { draw, ContactImport, deletions, unmount: () => slots.forEach((slot) => slot?.cleanup?.()) };
}
function nodes(value) {
  if (!value || typeof value !== 'object') return [];
  return [value, ...[value.props?.children].flat(Infinity).flatMap(nodes)];
}
function text(value) {
  if (typeof value === 'string') return value;
  if (Array.isArray(value)) return value.map(text).join('');
  return value?.props ? text(value.props.children) : '';
}
test('a revoked member loses the visible list, edit form and import controls after a denied write', async () => {
  const f = fixture(); f.draw(); await tick();
  let tree = f.draw();
  assert.match(text(tree), /Private Name/);
  nodes(tree).find((n) => n.type === 'button' && text(n) === '수정').props.onClick();
  tree = f.draw();
  nodes(tree).find((n) => n.type === 'button' && text(n) === '저장').props.onClick();
  await tick(); tree = f.draw();
  assert.match(text(tree), /접근 권한이 없습니다/);
  assert.doesNotMatch(text(tree), /Private Name|Private Note/);
  assert.equal(nodes(tree).some((n) => n.type === f.ContactImport || n.type === 'input'), false);
});

test('contact deletion waits for confirmation and cancellation preserves the contact', async () => {
  let finish;
  let confirmations = 0;
  const screen = fixture(() => { confirmations++; return new Promise((resolve) => { finish = resolve; }); });
  screen.draw(); await tick();
  const remove = nodes(screen.draw()).find((node) => node.type === 'button' && text(node) === '삭제').props.onClick;
  remove(); remove();
  await tick();
  assert.equal(screen.deletions.length, 0);
  assert.equal(confirmations, 1);
  finish(false); await tick();
  assert.equal(screen.deletions.length, 0);
  assert.match(text(screen.draw()), /Private Name/);
});

test('a late contact confirmation cannot delete after the component unmounts', async () => {
  let finish;
  const screen = fixture(() => new Promise((resolve) => { finish = resolve; }));
  screen.draw(); await tick();
  nodes(screen.draw()).find((node) => node.type === 'button' && text(node) === '삭제').props.onClick();
  await tick();
  assert.equal(screen.deletions.length, 0);
  screen.unmount(); finish(true); await tick();
  assert.equal(screen.deletions.length, 0);
});
