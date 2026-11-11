import test from 'node:test';
import assert from 'node:assert/strict';
import { buildMatcher, DEFAULT_OWN_DOMAINS } from '../dist/intake/match.js';

const acct = (accountId, name, aliases = [], domains = []) => ({ accountId, name, aliases, domains });
const OWN = ['amazon.com'];

test('default own domains', () => {
  assert.deepEqual(DEFAULT_OWN_DOMAINS, ['amazon.com']);
});

test('Hangul alias followed by particle and Latin text matches', () => {
  const m = buildMatcher([acct('a1', '하나은행', ['하나은행'])], OWN);
  const r = m.match({ title: '하나은행과 AWS sync', domains: [] });
  assert.deepEqual(r, { kind: 'matched', accountId: 'a1', evidence: ['title'] });
});

test('Hangul alias matches inside Hangul text', () => {
  const m = buildMatcher([acct('a1', '하나은행', ['하나'])], OWN);
  assert.equal(m.match({ title: '하나은행미팅', domains: [] }).kind, 'matched');
});

test('HNBX sync does not match alias HNB', () => {
  const m = buildMatcher([acct('a1', 'Hana', ['HNB'])], OWN);
  assert.deepEqual(m.match({ title: 'HNBX sync', domains: [] }), { kind: 'none' });
});

test('HNB/AWS matches', () => {
  const m = buildMatcher([acct('a1', 'Hana', ['HNB'])], OWN);
  assert.equal(m.match({ title: 'HNB/AWS', domains: [] }).kind, 'matched');
});

test('subdomain attendee matches domain', () => {
  const m = buildMatcher([acct('a1', 'Hana', [], ['hanabank.com'])], OWN);
  assert.deepEqual(m.match({ title: 'x', domains: ['ops.hanabank.com'] }),
    { kind: 'matched', accountId: 'a1', evidence: ['domain'] });
});

test('domain normalization and non-subdomain suffix', () => {
  const m = buildMatcher([acct('a1', 'H', [], ['@Hanabank.com.'])], OWN);
  assert.equal(m.match({ title: '', domains: ['HANABANK.COM'] }).kind, 'matched');
  assert.equal(m.match({ title: '', domains: ['nothanabank.com'] }).kind, 'none');
});

test('title and domain give sorted evidence', () => {
  const m = buildMatcher([acct('a1', 'Hana', ['hana'], ['hanabank.com'])], OWN);
  const r = m.match({ title: 'Hana review', location: 'Seoul', domains: ['hanabank.com'] });
  assert.deepEqual(r, { kind: 'matched', accountId: 'a1', evidence: ['domain', 'title'] });
});

test('location evidence', () => {
  const m = buildMatcher([acct('a1', 'Hana', ['hana'])], OWN);
  assert.deepEqual(m.match({ title: 'x', location: 'Hana HQ', domains: [] }),
    { kind: 'matched', accountId: 'a1', evidence: ['location'] });
});

test('two accounts give candidates, sorted, capped at 5', () => {
  const accts = ['g', 'f', 'e', 'd', 'c', 'b', 'a'].map((x) => acct(x, 'n' + x, ['shared']));
  const r = buildMatcher(accts, OWN).match({ title: 'shared kickoff', domains: [] });
  assert.deepEqual(r, { kind: 'candidate', accountIds: ['a', 'b', 'c', 'd', 'e'] });
  const two = buildMatcher([acct('z', 'z', ['shared']), acct('y', 'y', ['shared'])], OWN)
    .match({ title: 'shared', domains: [] });
  assert.deepEqual(two, { kind: 'candidate', accountIds: ['y', 'z'] });
});

test('own domain never matches', () => {
  const m = buildMatcher([acct('a1', 'Amz', [], ['amazon.com'])], OWN);
  assert.deepEqual(m.match({ title: 'x', domains: ['amazon.com', 'corp.amazon.com'] }), { kind: 'none' });
});

test('one-letter alias ignored; NFKC and case folding apply', () => {
  const m = buildMatcher([acct('a1', 'X', ['A'])], OWN);
  assert.equal(m.match({ title: 'A plan', domains: [] }).kind, 'none');
  const n = buildMatcher([acct('a1', 'X', ['ＨＮＢ'])], OWN);
  assert.equal(n.match({ title: 'hnb   call', domains: [] }).kind, 'matched');
});

test('name is also a term; empty input is none', () => {
  const m = buildMatcher([acct('a1', 'Acme Corp')], OWN);
  assert.equal(m.match({ title: 'acme corp weekly', domains: [] }).kind, 'matched');
  assert.deepEqual(m.match({ title: '', domains: [] }), { kind: 'none' });
});
