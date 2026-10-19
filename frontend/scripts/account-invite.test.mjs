import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/accountInvite.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Error });
  return exports;
}

const { addMemberWithInvite, summarizeMemberOutcomes } = load();

const apiError = (status, code, message = code) => Object.assign(new Error(message), { status, code });

function deps({ isAdmin = true, adds = [], invite = async () => ({}) } = {}) {
  const calls = { add: [], invite: [] };
  return {
    calls,
    isAdmin,
    addMember: async (email, role) => {
      calls.add.push([email, role]);
      const next = adds.shift();
      if (next instanceof Error) throw next;
      return next;
    },
    inviteUser: async (email) => {
      calls.invite.push(email);
      return invite(email);
    },
  };
}

test('registered users are added without an invite', async () => {
  const d = deps({ adds: [{ userId: 'u1' }] });
  const out = await addMemberWithInvite(d, 'a@x.com', 'tam');
  assert.equal(out.status, 'added');
  assert.deepEqual(d.calls.invite, []);
});

test('admin invites an unregistered email and queues the pending add', async () => {
  const d = deps({ adds: [apiError(404, 'INVITATION_REQUIRED'), { pending: true }] });
  const out = await addMemberWithInvite(d, ' New@X.com ', 'ssa');
  assert.deepEqual(JSON.parse(JSON.stringify(out)), { email: 'new@x.com', status: 'pending', invited: true });
  assert.deepEqual(d.calls.invite, ['new@x.com']);
  assert.deepEqual(d.calls.add[1], ['new@x.com', 'ssa']);
});

test('non-admins are told to ask an admin and never call the invite endpoint', async () => {
  const d = deps({ isAdmin: false, adds: [apiError(404, 'INVITATION_REQUIRED')] });
  const out = await addMemberWithInvite(d, 'new@x.com', 'tam');
  assert.equal(out.status, 'needs-admin');
  assert.deepEqual(d.calls.invite, []);
});

test('other add errors, including a generic 404, do not trigger an invite', async () => {
  const d = deps({ adds: [apiError(404, 'NOT_FOUND', 'Account not found')] });
  const out = await addMemberWithInvite(d, 'new@x.com', 'tam');
  assert.equal(out.status, 'failed');
  assert.equal(out.message, 'Account not found');
  assert.deepEqual(d.calls.invite, []);
});

test('an existing Cognito user (409) still retries the add without claiming an invite', async () => {
  const d = deps({ adds: [apiError(404, 'INVITATION_REQUIRED'), { pending: true }], invite: async () => { throw apiError(409, 'BAD_REQUEST'); } });
  const out = await addMemberWithInvite(d, 'new@x.com', 'tam');
  assert.equal(out.status, 'pending');
  assert.equal(out.invited, false);
});

test('invite failures and post-invite add failures are reported separately', async () => {
  const inviteFails = deps({ adds: [apiError(404, 'INVITATION_REQUIRED')], invite: async () => { throw apiError(500, 'INTERNAL_ERROR', 'Failed to invite user'); } });
  const a = await addMemberWithInvite(inviteFails, 'new@x.com', 'tam');
  assert.equal(a.status, 'failed');
  assert.equal(a.invited, false);
  assert.equal(inviteFails.calls.add.length, 1);

  const addFails = deps({ adds: [apiError(404, 'INVITATION_REQUIRED'), apiError(500, 'INTERNAL_ERROR', 'boom')] });
  const b = await addMemberWithInvite(addFails, 'new@x.com', 'tam');
  assert.equal(b.status, 'failed');
  assert.equal(b.invited, true);
});

test('summary separates sent invites from emails that need attention', () => {
  const { notice, error } = summarizeMemberOutcomes([
    { email: 'a@x.com', status: 'added' },
    { email: 'b@x.com', status: 'pending', invited: true },
    { email: 'c@x.com', status: 'needs-admin' },
    { email: 'd@x.com', status: 'failed', invited: true, message: 'boom' },
  ]);
  assert.match(notice, /b@x\.com/);
  assert.doesNotMatch(notice, /a@x\.com/);
  assert.match(error, /c@x\.com/);
  assert.match(error, /d@x\.com: 초대 메일은 보냈지만/);
  assert.deepEqual({ ...summarizeMemberOutcomes([{ email: 'a@x.com', status: 'added' }]) }, { notice: '', error: '' });
});
