// `ttobak-mcp team-sync run` with a fake API and a fake directory in the environment.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { ApiResponseError } from '../dist/api.js';
import { IntakeError } from '../dist/intake/errors.js';
import { runTeamSync } from '../dist/team-sync/run.js';
import { DIRECTORY_TOOL, DIRECTORY_URL_PREFIX, openDirectory, parsePerson } from '../dist/team-sync/directory.js';

const FAKE_BUILDER = fileURLToPath(new URL('./fixtures/fake-builder-mcp.mjs', import.meta.url));
const NOW = new Date('2026-11-14T03:00:00Z');
const user = (userId, email, department = '', emailVerified = true) => ({ userId, email, emailVerified, department, name: 'Fake Name' });
const PREVIEW_ID = 'a'.repeat(32);

// The tool's text, with fake values and many fields the adapter must never keep.
const directoryText = (login, isManager, managerLogin) => JSON.stringify({ content: { status: 'success', content: {
  login, is_manager: isManager, ...(managerLogin ? { manager: { login: managerLogin, name: 'FAKE BOSS' } } : {}),
  job_title: 'FAKE TITLE', mobile: '+00 000 0000', name: 'Fake Person', building: 'FAKE-1' } } });

function setup(t, options = {}) {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-tstest-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const stdout = [];
  const stderr = [];
  const writes = [];
  const lookups = [];
  const people = options.people ?? {};
  const api = {
    adminListUsers: async () => {
      if (options.listError) throw options.listError;
      return { users: options.users ?? [], ...(options.truncated ? { truncated: true } : {}) };
    },
    syncTeams: async body => {
      writes.push(body);
      return options.syncResult?.(body) ?? { updated: body.items.map(item => item.userId), unchanged: 0, conflicts: [], rejected: [] };
    },
  };
  let closed = 0;
  const environment = {
    home, env: { PATH: process.env.PATH }, now: () => options.now ?? NOW, zone: 'UTC',
    stdout: line => stdout.push(line), stderr: line => stderr.push(line),
    api, scope: 'a'.repeat(32), me: { email: 'admin@amazon.com', userId: 'u0' },
    openDirectory: async () => ({
      lookup: async alias => {
        lookups.push(alias);
        if (options.directoryError) throw options.directoryError;
        return alias in people ? people[alias] : null;
      },
      close: async () => { closed++; },
    }),
  };
  const previewPath = join(home, '.ttobak', 'intake', 'team-sync-preview.json');
  const storePreview = (overrides = {}) => {
    mkdirSync(join(home, '.ttobak', 'intake'), { recursive: true, mode: 0o700 });
    writeFileSync(previewPath, JSON.stringify({ previewId: PREVIEW_ID, createdAt: NOW.toISOString(),
      changes: [{ userId: 'u1', alias: 'alice', from: 'old', to: 'boss' }], ...overrides }), { mode: 0o600 });
  };
  return { home, stdout, stderr, writes, lookups, environment, previewPath, storePreview, closed: () => closed,
    output: () => JSON.parse(stdout.at(-1)),
    run: args => runTeamSync(args, environment) };
}

test('parsePerson reads only the three fields', () => {
  const person = parsePerson('alice', directoryText('Alice', false, 'Boss'));
  assert.deepEqual(person, { login: 'alice', isManager: false, managerLogin: 'boss' });
  assert.deepEqual(parsePerson('alice', directoryText('alice', true, null)), { login: 'alice', isManager: true, managerLogin: null });
  assert.equal(parsePerson('alice', JSON.stringify({ content: { status: 'error', content: null } })), null);
  assert.equal(parsePerson('alice', 'not json'), null);
  assert.equal(parsePerson('alice', JSON.stringify({ content: { status: 'success', content: { login: 'alice' } } })), null);
  assert.throws(() => parsePerson('alice', 'Midway cookie expired'), error => error.code === 'AUTH_EXPIRED_MIDWAY');
});

test('preview stores changes and prints no directory extras', async t => {
  const ctx = setup(t, {
    users: [user('u1', 'alice@amazon.com', 'old'), user('u2', 'bob@amazon.com', 'boss'), user('u3', 'carol@amazon.com'),
      user('u4', 'x@gmail.com'), user('u5', 'dave@amazon.com', '', false)],
    people: { alice: { login: 'alice', isManager: false, managerLogin: 'boss' }, bob: { login: 'bob', isManager: false, managerLogin: 'boss' } },
    truncated: true,
  });
  assert.equal(await ctx.run(['run']), 0);
  const out = ctx.output();
  assert.deepEqual(out, { ok: true, previewId: out.previewId, changes: [{ alias: 'alice', from: 'old', to: 'boss' }], unchanged: 1,
    notFound: ['carol'], skippedNonDirectory: 2, truncated: true });
  assert.match(out.previewId, /^[a-f0-9]{32}$/);
  for (const text of ctx.stdout) assert.doesNotMatch(text, /job_title|mobile|FAKE|@/);
  assert.deepEqual(ctx.lookups, ['alice', 'bob', 'carol']);
  assert.equal(ctx.closed(), 1);
  assert.deepEqual(ctx.stderr, ['team-sync: 1/3', 'team-sync: 2/3', 'team-sync: 3/3']);
  assert.equal(statSync(ctx.previewPath).mode & 0o777, 0o600);
  const stored = JSON.parse(readFileSync(ctx.previewPath, 'utf8'));
  assert.deepEqual(Object.keys(stored).sort(), ['changes', 'createdAt', 'previewId']);
  assert.deepEqual(stored.changes, [{ userId: 'u1', alias: 'alice', from: 'old', to: 'boss' }]);
  assert.doesNotMatch(readFileSync(ctx.previewPath, 'utf8'), /@|FAKE|job_title/);
  assert.equal(ctx.writes.length, 0);
});

test('lookup URL is the fixed prefix plus the alias', async t => {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-tstest-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const bin = join(home, 'bin');
  mkdirSync(bin);
  const record = join(home, 'record.jsonl');
  writeFileSync(join(bin, 'builder-mcp'), `#!${process.execPath}\nprocess.argv.push(${JSON.stringify(record)});\n` +
    `import(${JSON.stringify(`file://${FAKE_BUILDER}`)});\n`);
  chmodSync(join(bin, 'builder-mcp'), 0o755);
  const environment = { home, env: { PATH: bin }, now: () => NOW, zone: 'UTC', stdout: () => {} };
  const directory = await openDirectory(environment);
  try {
    // The fake answers an upper-case login and manager; the parser lowercases both.
    assert.deepEqual(await directory.lookup('atomoh'), { login: 'atomoh', isManager: false, managerLogin: 'fakeboss' });
    assert.equal(await directory.lookup('gone'), null);
    await assert.rejects(directory.lookup('loggedout'), error => error instanceof IntakeError && error.code === 'AUTH_EXPIRED_MIDWAY');
  } finally {
    await directory.close();
  }
  assert.equal(DIRECTORY_TOOL, 'ReadInternalWebsites');
  assert.equal(DIRECTORY_URL_PREFIX, 'https://phonetool.amazon.com/users/');
  const calls = readFileSync(record, 'utf8').trim().split('\n').map(line => JSON.parse(line));
  assert.deepEqual(calls[0], { inputs: ['https://phonetool.amazon.com/users/atomoh'] });
});

test('a missing builder-mcp is DIRECTORY_UNAVAILABLE', async t => {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-tstest-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  mkdirSync(join(home, 'empty'));
  await assert.rejects(openDirectory({ home, env: { PATH: join(home, 'empty') }, now: () => NOW, zone: 'UTC', stdout: () => {} }),
    error => error.code === 'DIRECTORY_UNAVAILABLE');
});

test('apply sends the previewed value as expected', async t => {
  const ctx = setup(t);
  ctx.storePreview();
  assert.equal(await ctx.run(['run', '--apply', '--preview-id', PREVIEW_ID]), 0);
  assert.deepEqual(ctx.writes, [{ items: [{ userId: 'u1', team: 'boss', expected: 'old' }] }]);
  assert.deepEqual(ctx.output(), { ok: true, updated: ['u1'], unchanged: 0, conflicts: [], rejected: [] });
  assert.equal(existsSync(ctx.previewPath), false);
  assert.equal(ctx.lookups.length, 0);
});

test('apply posts chunks of 200 and merges the results', async t => {
  const ctx = setup(t, { syncResult: body => ({ updated: body.items.slice(1).map(item => item.userId), unchanged: 0,
    conflicts: [body.items[0].userId], rejected: [{ userId: '', code: 'X' }] }) });
  const changes = Array.from({ length: 450 }, (_, index) => ({ userId: `u${index}`, alias: `a${index}`, from: '', to: 'boss' }));
  ctx.storePreview({ changes });
  assert.equal(await ctx.run(['run', '--apply', '--preview-id', PREVIEW_ID]), 0);
  assert.deepEqual(ctx.writes.map(body => body.items.length), [200, 200, 50]);
  const out = ctx.output();
  assert.equal(out.updated.length, 447);
  assert.deepEqual(out.conflicts, ['u0', 'u200', 'u400']);
  assert.equal(out.rejected.length, 3);
});

test('apply refuses an old or unknown preview', async t => {
  const ctx = setup(t);
  ctx.storePreview({ createdAt: new Date(NOW.getTime() - 16 * 60_000).toISOString() });
  assert.equal(await ctx.run(['run', '--apply', '--preview-id', PREVIEW_ID]), 2);
  assert.deepEqual(ctx.output(), { ok: false, code: 'PREVIEW_EXPIRED' });
  ctx.storePreview();
  assert.equal(await ctx.run(['run', '--apply', '--preview-id', 'b'.repeat(32)]), 2);
  assert.equal(ctx.output().code, 'PREVIEW_EXPIRED');
  rmSync(ctx.previewPath);
  assert.equal(await ctx.run(['run', '--apply', '--preview-id', PREVIEW_ID]), 2);
  assert.equal(ctx.output().code, 'PREVIEW_EXPIRED');
  ctx.storePreview({ changes: [{ userId: 'u1', alias: 'alice', from: '', to: 'Bad Value!' }] });
  assert.equal(await ctx.run(['run', '--apply', '--preview-id', PREVIEW_ID]), 2);
  assert.equal(ctx.output().code, 'PREVIEW_EXPIRED');
  assert.equal(ctx.writes.length, 0);
});

test('argument errors', async t => {
  const ctx = setup(t);
  for (const args of [['run', '--apply'], ['run', '--preview-id', PREVIEW_ID], ['run', '--apply', '--preview-id', 'xyz'], ['run', '--x']]) {
    assert.equal(await ctx.run(args), 2, args.join(' '));
    assert.equal(ctx.output().code, 'INVALID_ARGUMENT');
  }
  assert.equal(await ctx.run(['--help']), 0);
  assert.match(ctx.stdout.at(-1), /ttobak-mcp team-sync run/);
});

test('auth failure stops before any API write', async t => {
  const ctx = setup(t, { users: [user('u1', 'alice@amazon.com')], directoryError: new IntakeError('AUTH_EXPIRED_MIDWAY') });
  assert.notEqual(await ctx.run(['run']), 0);
  assert.deepEqual(ctx.output(), { ok: false, code: 'AUTH_EXPIRED_MIDWAY' });
  assert.equal(existsSync(ctx.previewPath), false);
  assert.equal(ctx.writes.length, 0);
  assert.equal(ctx.closed(), 1);
});

test('non-admin login ends with NOT_ADMIN', async t => {
  const ctx = setup(t, { listError: new ApiResponseError('HTTP 403 FORBIDDEN: no', 403, 'FORBIDDEN') });
  assert.equal(await ctx.run(['run']), 2);
  assert.deepEqual(ctx.output(), { ok: false, code: 'NOT_ADMIN' });
  assert.equal(ctx.lookups.length, 0);
});

test('more than 500 directory users ends with TOO_MANY_USERS', async t => {
  const users = Array.from({ length: 501 }, (_, index) => user(`u${index}`, `a${index}@amazon.com`));
  const ctx = setup(t, { users });
  assert.equal(await ctx.run(['run']), 2);
  assert.deepEqual(ctx.output(), { ok: false, code: 'TOO_MANY_USERS' });
  assert.equal(ctx.lookups.length, 0);
});

test('apply with no changes sends nothing', async t => {
  const ctx = setup(t);
  ctx.storePreview({ changes: [] });
  assert.equal(await ctx.run(['run', '--apply', '--preview-id', PREVIEW_ID]), 0);
  assert.deepEqual(ctx.output(), { ok: true, updated: [], unchanged: 0, conflicts: [], rejected: [] });
  assert.equal(ctx.writes.length, 0);
});
