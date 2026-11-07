import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const SRC = new URL('../src/', import.meta.url).pathname;

function loadDialog(context) {
  const source = readFileSync(join(SRC, 'lib/dialog.ts'), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports,
    require: (id) => {
      assert.equal(id, '@/lib/tauri');
      return { isTauri: () => context.tauri };
    },
    window: context.window,
    document: context.document,
    Promise,
    setTimeout,
    HTMLElement: class {},
  });
  return exports;
}

function sourceFiles(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.(ts|tsx)$/.test(name) ? [path] : [];
  });
}

test('browsers keep the native confirm and alert', async () => {
  const calls = [];
  const window = {
    confirm: (m) => (calls.push(['confirm', m]), true),
    alert: (m) => calls.push(['alert', m]),
  };
  const dialog = loadDialog({ tauri: false, window });
  assert.equal(await dialog.confirmDialog('delete?'), true);
  await dialog.alertDialog('failed');
  assert.deepEqual(calls, [['confirm', 'delete?'], ['alert', 'failed']]);
});

test('the Mac app never relies on the WebView confirm, which returns false', async () => {
  const window = { confirm: () => assert.fail('native confirm used in the app') };
  const appended = [];
  const document = {
    activeElement: null,
    createElement: () => {
      const el = {
        children: [],
        listeners: {},
        setAttribute() {},
        appendChild(child) { this.children.push(child); },
        addEventListener(type, fn) { this.listeners[type] = fn; },
        querySelectorAll() { return this.children; },
        focus() {},
        remove() {},
      };
      return el;
    },
    addEventListener() {},
    removeEventListener() {},
    body: { appendChild: (el) => appended.push(el) },
  };
  const dialog = loadDialog({ tauri: true, window, document });
  const answer = dialog.confirmDialog('delete?');
  await new Promise((r) => setTimeout(r, 0));
  const row = appended[0].children[0].children[1];
  const accept = row.children.find((b) => b.textContent === '확인');
  accept.listeners.click();
  assert.equal(await answer, true);
});

test('in-app dialogs open one at a time', async () => {
  const appended = [];
  const document = {
    activeElement: null,
    createElement: () => ({
      children: [], listeners: {},
      setAttribute() {}, appendChild(child) { this.children.push(child); },
      addEventListener(type, fn) { this.listeners[type] = fn; },
      querySelectorAll() { return this.children; }, focus() {}, remove() {},
    }),
    addEventListener() {}, removeEventListener() {},
    body: { appendChild: (el) => appended.push(el) },
  };
  const dialog = loadDialog({ tauri: true, window: {}, document });
  const first = dialog.confirmDialog('first');
  const second = dialog.confirmDialog('second');
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(appended.length, 1);
  const buttons = (i) => appended[i].children[0].children[1].children;
  buttons(0).find((b) => b.textContent === '취소').listeners.click();
  assert.equal(await first, false);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(appended.length, 2);
  buttons(1).find((b) => b.textContent === '확인').listeners.click();
  assert.equal(await second, true);
});

test('leftover actions reserve the meeting flow before their confirmation', () => {
  const page = readFileSync(join(SRC, 'app/record/page.tsx'), 'utf8');
  for (const name of ['handleLeftoverUpload', 'handleLeftoverDelete']) {
    const start = page.indexOf(`const ${name} = async`);
    assert.ok(start >= 0, name);
    const body = page.slice(start, page.indexOf('\n  };\n', start));
    const reserve = body.indexOf('reserveLeftoverFlow()');
    assert.ok(reserve >= 0 && reserve < body.indexOf('confirmDialog('), `${name} must reserve before confirming`);
  }
});

test('components use the shared dialog instead of window.confirm/alert', () => {
  const offenders = sourceFiles(SRC)
    .filter((path) => !path.endsWith('lib/dialog.ts'))
    .filter((path) => /(^|[^\w.])(window\.)?(confirm|alert)\(\s*[^)\s]/m.test(readFileSync(path, 'utf8')));
  assert.deepEqual(offenders, []);
});
