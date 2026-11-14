import { test } from 'node:test';
import assert from 'node:assert/strict';
import { directoryAlias, teamFor, planTeams, TEAM_PATTERN } from '../dist/team-sync/rule.js';

const u = (userId, email, department = '', emailVerified = true) => ({ userId, email, emailVerified, department });

test('directoryAlias accepts only verified amazon.com addresses', () => {
  assert.equal(directoryAlias(u('1', 'A@Amazon.com')), 'a');
  assert.equal(directoryAlias(u('1', 'a@amazon.com', '', false)), null);
  assert.equal(directoryAlias(u('1', 'a@gmail.com')), null);
  assert.equal(directoryAlias(u('1', 'a.b+c@amazon.com')), null);
  assert.equal(directoryAlias(u('1', 'a@sub.amazon.com')), null);
});

test('manager is grouped with their own alias', () => {
  assert.equal(teamFor('mihong', { login: 'mihong', isManager: true, managerLogin: 'moonjong' }), 'mihong');
});

test('non-manager gets the manager alias', () => {
  assert.equal(teamFor('atomoh', { login: 'atomoh', isManager: false, managerLogin: 'mihong' }), 'mihong');
});

test('no manager yields notFound', () => {
  assert.equal(teamFor('atomoh', { login: 'atomoh', isManager: false, managerLogin: null }), null);
  assert.equal(teamFor('atomoh', null), null);
});

test('lookup rejects a mismatched login', () => {
  assert.equal(teamFor('atomoh', { login: 'other', isManager: false, managerLogin: 'mihong' }), null);
});

test('manager alias failing the pattern yields null', () => {
  assert.equal(teamFor('atomoh', { login: 'atomoh', isManager: false, managerLogin: 'Bad Alias' }), null);
  assert.ok(TEAM_PATTERN.test('a-b1'));
  assert.ok(!TEAM_PATTERN.test('-a'));
});

test('planTeams separates changes, unchanged, notFound and skipped', () => {
  const people = new Map([
    ['b', { login: 'b', isManager: false, managerLogin: 'm' }],
    ['a', { login: 'a', isManager: false, managerLogin: 'm' }],
    ['c', { login: 'c', isManager: false, managerLogin: 'n' }],
  ]);
  const plan = planTeams([
    u('2', 'b@amazon.com', 'old'), u('1', 'a@amazon.com', 'm'), u('3', 'x@amazon.com'),
    u('4', 'c@gmail.com'), u('5', 'c@amazon.com', ''),
  ], people);
  assert.deepEqual(plan.changes, [
    { userId: '2', alias: 'b', from: 'old', to: 'm' },
    { userId: '5', alias: 'c', from: '', to: 'n' },
  ]);
  assert.equal(plan.unchanged, 1);
  assert.deepEqual(plan.notFound, ['x']);
  assert.equal(plan.skippedNonDirectory, 1);
});
