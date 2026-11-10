import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

// Runs the Mac app's bundled caption page script against a minimal fake DOM.
const html = readFileSync(new URL('../../mac-app/src/caption-overlay.html', import.meta.url), 'utf8');
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);

class FakeElement {
  constructor(tag) {
    this.tagName = tag;
    this.className = '';
    this.textContent = '';
    this.style = { transform: '' };
    this.classList = { add() {}, remove() {} };
    this.parent = null;
    this.children = [];
  }
  get isConnected() { return this.parent !== null; }
  get offsetHeight() { return 0; }
  set innerHTML(_value) { throw new Error('innerHTML must not be used'); }
  addEventListener() {}
  getBoundingClientRect() { return { top: this.parent ? this.parent.children.indexOf(this) * 10 : 0 }; }
  appendChild(child) {
    if (child.parent) child.remove();
    child.parent = this;
    this.children.push(child);
    return child;
  }
  remove() {
    if (!this.parent) return;
    this.parent.children = this.parent.children.filter((c) => c !== this);
    this.parent = null;
  }
}

function loadPage(lang) {
  const box = new FakeElement('div');
  const properties = {};
  const listeners = [];
  const document = {
    documentElement: { lang: '', style: { setProperty: (k, v) => { properties[k] = v; } } },
    getElementById: (id) => (id === 'captions' ? box : null),
    createElement: (tag) => new FakeElement(tag),
  };
  const window = {
    __TAURI__: {
      event: {
        listen: (event, handler, options) => {
          listeners.push({ event, handler, options });
          return Promise.resolve(() => {});
        },
      },
    },
  };
  const context = { window, document, location: { search: `?lang=${lang}` }, URLSearchParams, Map, Set, Array, Object };
  for (const source of scripts) vm.runInNewContext(source, context);
  const emit = (event, payload) => {
    for (const l of listeners) if (l.event === event) l.handler({ event, payload });
  };
  const lines = () => box.children.map((c) => c.textContent);
  return { listeners, emit, lines, properties };
}

const entry = (lang, id, text, isFinal, order = 0) => ({ lang, kind: 'entry', id, order, text, isFinal });

test('listens only to events sent to its own window', () => {
  const page = loadPage('en');
  assert.deepEqual(page.listeners.map((l) => l.event).sort(), ['caption-style', 'caption-update']);
  for (const l of page.listeners) {
    assert.deepEqual(JSON.parse(JSON.stringify(l.options)), { target: { kind: 'WebviewWindow', label: 'caption-en' } });
  }
});

test('ignores payloads for another language', () => {
  const page = loadPage('en');
  page.emit('caption-update', entry('ko', 'u1', '안녕하세요', true));
  assert.deepEqual(page.lines(), ['Live captions will appear here.']);
  page.emit('caption-update', entry('en', 'u1', 'Hello', true));
  assert.deepEqual(page.lines(), ['Hello']);
  page.emit('caption-update', { lang: 'ko', kind: 'clear', id: '', order: 0, text: '', isFinal: false });
  assert.deepEqual(page.lines(), ['Hello']);
  page.emit('caption-update', { kind: 'clear', id: '', order: 0, text: '', isFinal: false });
  assert.deepEqual(page.lines(), ['Hello'], 'a payload without a language is ignored');
});

test('ignores styles for another language', () => {
  const page = loadPage('ko');
  page.emit('caption-style', { lang: 'en', fontSize: 90, fontColor: '#FF0000' });
  assert.equal(page.properties['--size'], undefined);
  page.emit('caption-style', { lang: 'ko', fontSize: 90, fontColor: '#FF0000' });
  assert.equal(page.properties['--size'], '90px');
  assert.equal(page.properties['--color'], '#FF0000');
});

test('empty text removes that line', () => {
  const page = loadPage('en');
  page.emit('caption-update', entry('en', 'u0', '', true, 0));
  assert.deepEqual(page.lines(), ['Live captions will appear here.'], 'the preview stays');
  page.emit('caption-update', entry('en', 'u1', 'First', true, 0));
  page.emit('caption-update', entry('en', 'u2', 'Second draft', false, 1));
  assert.deepEqual(page.lines(), ['First', 'Second draft']);
  page.emit('caption-update', entry('en', 'u2', '', true, 1));
  assert.deepEqual(page.lines(), ['First']);
  page.emit('caption-update', entry('en', 'u9', '', false, 9));
  assert.deepEqual(page.lines(), ['First'], 'an unknown id with empty text adds nothing');
});

test('a final line ignores later drafts for the same id', () => {
  const page = loadPage('en');
  page.emit('caption-update', entry('en', 'u1', 'Hello everyone', true));
  page.emit('caption-update', entry('en', 'u1', 'Hello', false));
  assert.deepEqual(page.lines(), ['Hello everyone']);
  page.emit('caption-update', { lang: 'en', kind: 'clear', id: '', order: 0, text: '', isFinal: false });
  page.emit('caption-update', entry('en', 'u1', 'New session draft', false));
  assert.deepEqual(page.lines(), ['New session draft'], 'clear starts a new session');
});

test('shows at most three lines, newest last', () => {
  const page = loadPage('en');
  for (let i = 0; i < 5; i++) page.emit('caption-update', entry('en', `u${i}`, `Line ${i}`, true, i));
  assert.deepEqual(page.lines(), ['Line 2', 'Line 3', 'Line 4']);
});
