import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

// Keeps the in-app release notes aligned with the package version, the root
// CHANGELOG.md headings and the routes the static export actually serves.
// Release procedure: docs/runbooks/releases.md.

const root = new URL('../../', import.meta.url);
const read = (path) => readFileSync(new URL(path, root), 'utf8');

function loadNotes(window) {
  const { outputText } = ts.transpileModule(read('frontend/src/lib/releaseNotes.ts'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, window, require: (id) => assert.fail(id) });
  return exports;
}

const notes = loadNotes(undefined);
const { RELEASES, LATEST_RELEASE } = notes;

const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const parts = (v) => v.split('.').map(Number);
function newer(a, b) {
  const [x, y] = [parts(a), parts(b)];
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] > y[i];
  return false;
}

function memoryStorage() {
  const map = new Map();
  return { getItem: (k) => map.get(k) ?? null, setItem: (k, v) => map.set(k, String(v)) };
}

test('releases are unique semver versions, newest first, with ISO dates', () => {
  assert.ok(RELEASES.length > 0);
  assert.equal(LATEST_RELEASE, RELEASES[0]);
  for (const [i, release] of RELEASES.entries()) {
    assert.match(release.version, SEMVER);
    assert.match(release.date, /^\d{4}-\d{2}-\d{2}$/);
    assert.ok(!Number.isNaN(Date.parse(`${release.date}T00:00:00Z`)));
    assert.match(release.summary, /[가-힣]/);
    if (i > 0) {
      assert.ok(newer(RELEASES[i - 1].version, release.version), `${RELEASES[i - 1].version} > ${release.version}`);
      assert.ok(RELEASES[i - 1].date >= release.date);
    }
  }
});

test('the latest release matches the frontend package and lockfile versions', () => {
  const pkg = JSON.parse(read('frontend/package.json'));
  const lock = JSON.parse(read('frontend/package-lock.json'));
  assert.equal(pkg.version, LATEST_RELEASE.version);
  assert.equal(lock.version, LATEST_RELEASE.version);
  assert.equal(lock.packages[''].version, LATEST_RELEASE.version);
});

test('every in-app release has the same version and date in CHANGELOG.md, in order', () => {
  const changelog = read('CHANGELOG.md');
  const headings = [...changelog.matchAll(/^## \[(\d+\.\d+\.\d+)\] - (\d{4}-\d{2}-\d{2})$/gm)]
    .map(([, version, date]) => ({ version, date }));
  assert.match(changelog, /^## \[Unreleased\]$/m);
  // JSON round trip: the notes come from another vm realm.
  assert.deepEqual(headings.slice(0, RELEASES.length), JSON.parse(JSON.stringify(RELEASES.map(({ version, date }) => ({ version, date })))));
  assert.ok(changelog.indexOf('## [Unreleased]') < changelog.indexOf(`## [${LATEST_RELEASE.version}]`));
});

test('items have a known kind, Korean copy, PR references and served routes', () => {
  const router = read('infra/lib/frontend-stack.ts');
  const knownPages = JSON.parse(router.match(/var knownPages = (\[[^\]]*\]);/)[1].replaceAll("'", '"'));
  const titles = new Set();
  for (const release of RELEASES) {
    assert.ok(release.items.length > 0);
    for (const item of release.items) {
      assert.ok(['new', 'improved', 'fixed'].includes(item.kind), item.title);
      assert.match(item.title, /[가-힣]/);
      assert.match(item.body, /[가-힣]/);
      assert.ok(item.where.length > 0, item.title);
      assert.ok(!titles.has(`${release.version}:${item.title}`), `duplicate ${item.title}`);
      titles.add(`${release.version}:${item.title}`);
      assert.ok(item.refs.length > 0 && item.refs.every((n) => Number.isInteger(n) && n > 0), item.title);
      if (item.to === null) continue;
      const path = item.to.split('?')[0];
      // A static page, served by CloudFront and present in the app directory.
      assert.ok(path === '/' || knownPages.includes(path), `${item.to} is not in knownPages`);
      const page = path === '/' ? 'frontend/src/app/page.tsx' : `frontend/src/app${path}/page.tsx`;
      assert.ok(existsSync(new URL(page, root)), `${page} is missing`);
    }
  }
});

test('the changelog route is a served static page', () => {
  assert.equal(notes.CHANGELOG_PATH, '/changelog');
  assert.ok(existsSync(new URL('frontend/src/app/changelog/page.tsx', root)));
  assert.match(read('infra/lib/frontend-stack.ts'), /'\/changelog'/);
});

test('seen state survives missing or throwing storage and notifies subscribers', () => {
  const storage = memoryStorage();
  assert.equal(notes.hasUnseenRelease(storage), true);
  let calls = 0;
  const unsubscribe = notes.subscribeReleaseSeen(() => { calls += 1; });
  notes.markSeen(LATEST_RELEASE.version, storage);
  assert.equal(storage.getItem(notes.SEEN_KEY), LATEST_RELEASE.version);
  assert.equal(notes.hasUnseenRelease(storage), false);
  assert.equal(calls, 1);
  unsubscribe();
  notes.markSeen('0.0.1', storage);
  assert.equal(calls, 1);
  assert.equal(notes.hasUnseenRelease(storage), true);

  const throwing = { getItem() { throw new Error('SecurityError'); }, setItem() { throw new Error('QuotaExceeded'); } };
  assert.equal(notes.seenVersion(throwing), null);
  assert.doesNotThrow(() => notes.markSeen(LATEST_RELEASE.version, throwing));
  assert.equal(notes.hasUnseenRelease(throwing), true);
  // No window and no storage during the static prerender.
  assert.equal(notes.seenVersion(), null);
});

test('another tab marking the release seen notifies through the storage event', () => {
  const handlers = new Map();
  const window = {
    addEventListener: (type, fn) => handlers.set(type, fn),
    removeEventListener: (type, fn) => { if (handlers.get(type) === fn) handlers.delete(type); },
  };
  const scoped = loadNotes(window);
  let calls = 0;
  const unsubscribe = scoped.subscribeReleaseSeen(() => { calls += 1; });
  handlers.get('storage')({ key: 'other' });
  assert.equal(calls, 0);
  handlers.get('storage')({ key: scoped.SEEN_KEY });
  handlers.get('storage')({ key: null });
  assert.equal(calls, 2);
  unsubscribe();
  assert.equal(handlers.has('storage'), false);
});
