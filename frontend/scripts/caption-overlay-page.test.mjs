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

class FakeGrip extends FakeElement {
  constructor() {
    super('div');
    this.handlers = {};
    this.captured = null;
  }
  addEventListener(type, handler) { this.handlers[type] = handler; }
  setPointerCapture(id) { this.captured = id; }
  fire(type, e = {}) {
    this.handlers[type]({ button: 0, pointerId: 1, preventDefault() {}, stopPropagation() {}, ...e });
  }
}

function loadPage(lang, { tauriWindow = true, invokeFails = false } = {}) {
  const box = new FakeElement('div');
  const grip = new FakeGrip();
  const properties = {};
  const listeners = [];
  const sizes = [];
  const frames = [];
  const rootClasses = new Set();
  const invokes = [];
  const frameEl = new FakeElement('div');
  const frameClasses = new Set();
  frameEl.classList = {
    add: (c) => frameClasses.add(c),
    remove: (c) => frameClasses.delete(c),
    toggle: (c, on) => (on ? frameClasses.add(c) : frameClasses.delete(c)),
  };
  const document = {
    documentElement: {
      lang: '',
      style: { setProperty: (k, v) => { properties[k] = v; } },
      classList: { add: (c) => rootClasses.add(c), remove: (c) => rootClasses.delete(c) },
    },
    getElementById: (id) => (id === 'captions' ? box : id === 'grip' ? grip : id === 'frame' ? frameEl : null),
    createElement: (tag) => new FakeElement(tag),
  };
  class LogicalSize {
    constructor(width, height) { this.type = 'Logical'; this.width = width; this.height = height; }
  }
  const window = {
    innerWidth: 800,
    innerHeight: 160,
    requestAnimationFrame: (cb) => { frames.push(cb); return frames.length; },
    __TAURI__: {
      core: {
        invoke: (cmd, args) => {
          invokes.push([cmd, JSON.parse(JSON.stringify(args))]);
          return invokeFails ? Promise.reject(new Error('denied')) : Promise.resolve();
        },
      },
      event: {
        listen: (event, handler, options) => {
          listeners.push({ event, handler, options });
          return Promise.resolve(() => {});
        },
      },
    },
  };
  if (tauriWindow) {
    window.__TAURI__.window = {
      LogicalSize,
      getCurrentWindow: () => ({
        setSize: (size) => { sizes.push(size); return Promise.resolve(); },
      }),
    };
  }
  const context = { window, document, location: { search: `?lang=${lang}` }, URLSearchParams, Map, Set, Array, Object };
  for (const source of scripts) vm.runInNewContext(source, context);
  const emit = (event, payload) => {
    for (const l of listeners) if (l.event === event) l.handler({ event, payload });
  };
  const lines = () => box.children.map((c) => c.textContent);
  const runFrames = () => { for (const cb of frames.splice(0)) cb(); };
  return { listeners, emit, lines, properties, grip, sizes, runFrames, rootClasses, frames, context, invokes, frameClasses };
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

test('resize size is the start size plus the delta, clamped to 320x80', () => {
  const { context } = loadPage('en');
  const size = (...a) => JSON.parse(JSON.stringify(context.captionResizeSize(...a)));
  assert.deepEqual(size(800, 160, 40.4, -20.6), { width: 840, height: 139 });
  assert.deepEqual(size(400, 100, -500, -500), { width: 320, height: 80 });
  assert.deepEqual(size(800, 160, NaN, Infinity), { width: 800, height: 160 });
  assert.deepEqual(size(undefined, null, 0, 0), { width: 320, height: 80 });
});

test('the grip resizes the window once per animation frame', () => {
  const page = loadPage('en');
  page.grip.fire('pointerdown', { screenX: 100, screenY: 100 });
  assert.equal(page.grip.captured, 1);
  assert.ok(page.rootClasses.has('resizing'));
  page.grip.fire('pointermove', { screenX: 110, screenY: 105 });
  page.grip.fire('pointermove', { screenX: 150, screenY: 130 });
  assert.equal(page.frames.length, 1, 'one frame per burst of moves');
  page.runFrames();
  assert.deepEqual(page.sizes.map((s) => [s.type, s.width, s.height]), [['Logical', 850, 190]]);
  page.grip.fire('pointermove', { screenX: -900, screenY: -900 });
  page.runFrames();
  assert.deepEqual([page.sizes[1].width, page.sizes[1].height], [320, 80]);
  page.grip.fire('pointerup');
  assert.ok(!page.rootClasses.has('resizing'));
  page.grip.fire('pointermove', { screenX: 300, screenY: 300 });
  page.runFrames();
  assert.equal(page.sizes.length, 2, 'no resize after release');
  assert.deepEqual(page.lines(), ['Live captions will appear here.'], 'captions are untouched');
});

test('the grip ignores non-primary buttons and works without the window API', () => {
  const page = loadPage('en');
  page.grip.fire('pointerdown', { button: 2, screenX: 0, screenY: 0 });
  page.grip.fire('pointermove', { screenX: 50, screenY: 50 });
  page.runFrames();
  assert.equal(page.sizes.length, 0);
  const bare = loadPage('en', { tauriWindow: false });
  assert.deepEqual(Object.keys(bare.grip.handlers), []);
  assert.deepEqual(bare.lines(), ['Live captions will appear here.']);
});

test('page chrome is a drag handle and a non-drag grip, with no text', () => {
  assert.match(html, /<div id="move" class="chrome" data-tauri-drag-region aria-hidden="true">⠿<\/div>/);
  assert.match(html, /<div id="grip" class="chrome" data-tauri-drag-region="false"/);
  assert.match(html, /<body data-tauri-drag-region="deep">/);
  assert.match(html, /html:hover \.chrome/);
  assert.doesNotMatch(html, /console\.|fetch\(|XMLHttpRequest|innerHTML/);
});

const clear = (lang) => ({ lang, kind: 'clear', id: '', order: 0, text: '', isFinal: false });
const idleReports = (page) => page.invokes.filter(([cmd]) => cmd === 'caption_overlay_idle').map(([, a]) => a.idle);

test('an idle window reports idle so clicks pass through, and takes them back with text', () => {
  const page = loadPage('en');
  assert.deepEqual(idleReports(page), [false], 'the preview takes clicks');
  page.emit('caption-update', entry('en', 'u1', 'Hello', false));
  assert.deepEqual(idleReports(page), [false], 'replacing the preview does not flip pass-through');
  page.emit('caption-update', clear('en'));
  assert.ok(page.frameClasses.has('idle'));
  assert.deepEqual(idleReports(page), [false, true]);
  page.emit('caption-update', clear('en'));
  assert.deepEqual(idleReports(page), [false, true], 'unchanged state is not re-sent');
  page.emit('caption-update', entry('en', 'u2', 'Back', true));
  assert.ok(!page.frameClasses.has('idle'));
  assert.deepEqual(idleReports(page), [false, true, false]);
  page.emit('caption-update', entry('en', 'u2', '', true));
  assert.deepEqual(idleReports(page), [false, true, false, true], 'removing the last line goes idle');
  page.emit('caption-update', { lang: 'en', kind: 'preview', id: '', order: 0, text: '', isFinal: false });
  assert.deepEqual(idleReports(page), [false, true, false, true, false]);
  page.emit('caption-update', clear('ko'));
  assert.equal(idleReports(page).length, 5, 'another language changes nothing');
  assert.deepEqual(page.invokes.filter(([cmd]) => cmd !== 'caption_overlay_idle'), [], 'no other command');
});

test('a failed idle report is retried on the next update', async () => {
  const page = loadPage('en', { invokeFails: true });
  await new Promise((r) => setImmediate(r));
  page.emit('caption-update', entry('en', 'u1', 'Hello', false));
  assert.deepEqual(idleReports(page), [false, false]);
});
