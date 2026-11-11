// SFDC contact sync step of the local intake. Emails and phones are used only
// on the Mac for matching; no request body, status file or output carries them.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { matchContacts, normalizeName, runSfdcContacts } from '../dist/intake/sfdc-contacts.js';
import { SFDC_CONTACTS_TOOL, SFDC_TOOLS } from '../dist/intake/sfdc.js';
import { IntakeStore } from '../dist/intake/store.js';
import { runIntake } from '../dist/intake/run.js';
import { ApiResponseError } from '../dist/api.js';
import { canonicalSfdcId } from '../dist/intake/sfdc-id.js';

const NOW = new Date('2026-11-09T03:00:00Z');
const SENTRAL = fileURLToPath(new URL('./fixtures/intake-sentral-server.mjs', import.meta.url));
const SCOPE = 'c'.repeat(32);
/** The canonical 18-character form of a 15-character fixture ID. */
const id = base => canonicalSfdcId(base);
const ACCOUNT_A = id('001XX0000000A01');
const sfdcId = (prefix, n) => id(`${prefix}XX${String(n).padStart(10, '0')}`);

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-sfdc-contacts-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return home;
}

function allFiles(directory) {
  const files = [];
  for (const name of readdirSync(directory)) {
    const path = join(directory, name);
    if (statSync(path).isDirectory()) files.push(...allFiles(path));
    else files.push(path);
  }
  return files;
}

function contactRecord(id, firstName, lastName, extra = {}) {
  return { id, firstName, lastName, title: `T-${id.slice(-4)}`, accountId: extra.accountId ?? ACCOUNT_A,
    recordType: { name: 'Sales/BD' }, email: `${id.toLowerCase()}@customer.example`, phone: '+82-10-0000-0000', ...extra };
}

/** In-memory search_contacts: `pages[sfdcAccountId]` is a list of record pages; `fail` names "account:page" to fail. */
function contactSource(pages, { fail = {} } = {}) {
  const calls = [];
  return {
    calls,
    async call(tool, args) {
      assert.equal(tool, 'search_contacts');
      calls.push(structuredClone(args));
      assert.equal(args.limit, 500);
      assert.equal(args.condition.field, 'accountId');
      assert.equal(args.condition.operator, 'EXACT_MATCH');
      const account = args.condition.value;
      const list = pages[account] ?? [[]];
      const index = args.after === undefined ? 0 : Number(args.after.slice(1));
      const failure = fail[`${account}:${index}`];
      if (failure) throw failure;
      const total = list.reduce((sum, page) => sum + page.length, 0);
      const more = index < list.length - 1;
      return { data: { resultRecords: list[index], totalCount: String(total), hasNextPage: more, cursor: more ? `p${index + 1}` : null } };
    },
  };
}

/** Fake TTOBAK API that enforces the sfdc-sync request contract and answers like the server. */
function contactsApi({ mappings = [], contacts = {}, forbidden = [], answer, truncated = false } = {}) {
  const syncs = [];
  const lists = [];
  return {
    syncs,
    lists,
    async performanceSources() { return { sources: [] }; },
    async performanceMappings() {
      return { items: mappings.map(([sfdcAccountId, accountId]) => ({ sfdcAccountId, name: 'n', role: 'REGISTRY',
        mapped: !!accountId, manual: false, ...(accountId ? { accountId } : {}) })), truncated };
    },
    async syncPerformance(_source, body) {
      return { revision: body.expectedRevision + 1, completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length, removed: body.mutations.filter(m => m.op === 'remove').length };
    },
    async listAccountContacts(accountId, options) {
      lists.push(accountId);
      if (forbidden.includes(accountId)) throw new ApiResponseError('HTTP 403 FORBIDDEN: denied', 403, 'FORBIDDEN');
      assert.ok(options.limit >= 1 && options.limit <= 25);
      const all = contacts[accountId] ?? [];
      const start = options.cursor ? Number(options.cursor) : 0;
      const page = all.slice(start, start + options.limit);
      const next = start + options.limit < all.length ? String(start + options.limit) : undefined;
      return { contacts: page, ...(next ? { nextCursor: next } : {}) };
    },
    async syncSfdcContacts(accountId, body) {
      const text = JSON.stringify(body);
      syncs.push({ accountId, body: structuredClone(body), text });
      assert.ok(Buffer.byteLength(text) <= 512 * 1024, 'body within 512 KiB');
      assert.deepEqual(Object.keys(body).sort(), body.snapshotComplete
        ? ['items', 'present', 'sfdcAccountIds', 'snapshotComplete'] : ['items', 'sfdcAccountIds', 'snapshotComplete']);
      assert.ok(body.sfdcAccountIds.length >= 1 && body.sfdcAccountIds.length <= 20);
      assert.equal(new Set(body.sfdcAccountIds).size, body.sfdcAccountIds.length);
      assert.ok(body.items.length <= 200);
      assert.equal(new Set(body.items.map(item => item.sfdcContactId)).size, body.items.length);
      if (body.snapshotComplete) {
        assert.deepEqual(Object.keys(body.present).sort(), [...body.sfdcAccountIds].sort());
        let total = 0;
        for (const ids of Object.values(body.present)) { assert.ok(ids.length <= 5000); total += ids.length; }
        assert.ok(total <= 20000);
      }
      for (const item of body.items) {
        assert.match(item.sfdcContactId, /^[A-Za-z0-9]{15,18}$/);
        assert.ok(body.sfdcAccountIds.includes(item.sfdcAccountId));
        if (body.snapshotComplete) assert.ok(body.present[item.sfdcAccountId].includes(item.sfdcContactId));
        if (item.action === 'link') {
          assert.deepEqual(Object.keys(item).sort(), ['action', 'contactId', 'name', 'revision', 'sfdcAccountId', 'sfdcContactId', 'title']);
        } else {
          assert.equal(item.action, 'create');
          assert.deepEqual(Object.keys(item).sort(), ['action', 'name', 'sfdcAccountId', 'sfdcContactId', 'title']);
        }
      }
      if (forbidden.includes(`sync:${accountId}`)) throw new ApiResponseError('HTTP 403 FORBIDDEN: denied', 403, 'FORBIDDEN');
      const results = body.items.map(item => answer?.(item) ?? (item.action === 'create'
        ? { sfdcContactId: item.sfdcContactId, result: 'created', contactId: `new-${item.sfdcContactId}` }
        : { sfdcContactId: item.sfdcContactId, result: 'filled', contactId: item.contactId }));
      return { results, marked: body.snapshotComplete ? 1 : 0, unmarked: 0, skipped: 0 };
    },
  };
}

const existing = (contactId, name, extra = {}) => ({ contactId, revision: `r-${contactId}`, name, company: '', title: '',
  department: '', email: '', address: '', workPhone: '', fax: '', phone: '', cardRegisteredAt: '', cardBook: '', group: '',
  notes: '', ...extra });

// ---------- pure helpers ----------

test('normalize_name_folds_width_case_whitespace_and_punctuation', () => {
  assert.equal(normalizeName('  Jane   Q.  Doe, '), 'jane q doe');
  assert.equal(normalizeName('ＪＡＮＥ　Doe'), 'jane doe');
  assert.equal(normalizeName('홍\t길동'), '홍 길동');
});

test('email_match_beats_name_mismatch', () => {
  const sfdc = [{ id: id('003XX0000000E01'), name: 'Jane Customer', title: 'CTO', email: 'Jane@Customer.Example', accountId: ACCOUNT_A }];
  const current = [existing('card-1', '제인 커스터머', { email: 'jane@customer.example ' }),
    existing('card-2', 'Jane Customer', { email: 'someone.else@customer.example' })];
  const { items, ambiguous } = matchContacts(sfdc, current, new Set());
  assert.equal(ambiguous, 0);
  assert.deepEqual(items, [{ action: 'link', sfdcContactId: id('003XX0000000E01'), sfdcAccountId: ACCOUNT_A,
    name: 'Jane Customer', title: 'CTO', contactId: 'card-1', revision: 'r-card-1' }]);
});

test('a contact whose email two SFDC people share is not name-linked to a third', () => {
  const sfdc = [
    { id: id('003XX0000000E01'), name: 'Ha Kim', title: '', email: 'shared@customer.example', accountId: ACCOUNT_A },
    { id: id('003XX0000000E02'), name: 'Jo Kim', title: '', email: 'shared@customer.example', accountId: ACCOUNT_A },
    { id: id('003XX0000000E03'), name: 'Ha Kim', title: '', email: '', accountId: ACCOUNT_A },
  ];
  const current = [existing('card-1', 'Ha Kim', { email: 'shared@customer.example' })];
  const result = matchContacts(sfdc, current, new Set());
  assert.equal(result.ambiguous, 2);
  assert.ok(!result.items.some(item => item.contactId === 'card-1'), 'the shared-email contact stays unlinked');
  assert.deepEqual(result.items.map(item => [item.action, item.sfdcContactId]), [['create', id('003XX0000000E03')]]);
});

test('candidates of an ambiguous name are contested, so no other person claims one uniquely', () => {
  const sfdc = [
    { id: id('003XX0000000E01'), name: 'Na Kim', title: '', email: '', accountId: ACCOUNT_A },
    { id: id('003XX0000000E02'), name: 'Na Kim', title: '', email: 'q@customer.example', accountId: ACCOUNT_A },
  ];
  const current = [existing('a', 'Na Kim'), existing('b', 'Na Kim', { email: 'b@customer.example' })];
  const result = matchContacts(sfdc, current, new Set());
  assert.deepEqual(result.items, [], 'Q never links to A, which P also matched');
  assert.equal(result.ambiguous, 2);
});

test('a contact holding a linked or suppressed person\'s email is never name-linked', () => {
  const linkedId = id('003XX0000000E01');
  const suppressedId = id('003XX0000000E02');
  const sfdc = [
    { id: linkedId, name: 'Linked Kim', title: '', email: 'old@customer.example', accountId: ACCOUNT_A },
    { id: suppressedId, name: 'Gone Kim', title: '', email: 'gone@customer.example', accountId: ACCOUNT_A },
    { id: id('003XX0000000E03'), name: 'Card Kim', title: '', email: '', accountId: ACCOUNT_A },
    { id: id('003XX0000000E04'), name: 'Other Kim', title: '', email: '', accountId: ACCOUNT_A },
  ];
  const current = [
    existing('linked', 'Linked Kim', { sfdcContactId: linkedId, title: 'T', company: 'Co' }),
    existing('card', 'Card Kim', { email: 'old@customer.example' }),
    existing('other', 'Other Kim', { email: 'gone@customer.example' }),
  ];
  const result = matchContacts(sfdc, current, new Set([suppressedId]));
  assert.ok(!result.items.some(item => item.action === 'link'), 'neither card is name-linked');
  assert.deepEqual(result.items.map(item => item.sfdcContactId), [id('003XX0000000E03'), id('003XX0000000E04')]);
});

test('a record with a malformed ID makes the read incomplete', async () => {
  const records = [contactRecord('001A000001aBcDe', 'New', 'Person'), contactRecord('003AB00000abcdEZZZ', 'Bad', 'Suffix')];
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']], contacts: { 'acct-1': [] } });
  const result = await runSfdcContacts({ session: contactSource({ [ACCOUNT_A]: [records] }), api, dryRun: false });
  assert.equal(result.counts.invalidIds, 1);
  assert.equal(result.counts.incomplete, 1);
  assert.ok(api.syncs.every(sync => !sync.body.snapshotComplete), 'no departure snapshot');
});

test('whitespace-only title or company is not fillable, matching the server', () => {
  const sfdc = [{ id: id('003XX0000000E01'), name: 'Bo Kim', title: 'VP', email: '', accountId: ACCOUNT_A }];
  const blank = [existing('linked', 'Bo Kim', { sfdcContactId: id('003XX0000000E01'), title: ' ', company: '  ' })];
  const result = matchContacts(sfdc, blank, new Set());
  assert.deepEqual(result.items, []);
  assert.equal(result.unchanged, 1);
  const empty = [existing('linked', 'Bo Kim', { sfdcContactId: id('003XX0000000E01'), title: ' ', company: '' })];
  assert.deepEqual(matchContacts(sfdc, empty, new Set()).items.map(item => item.contactId), ['linked']);
});

test('existing_link_wins', () => {
  const sfdc = [{ id: id('003XX0000000E01'), name: 'Jane Customer', title: 'CTO', email: 'jane@customer.example', accountId: ACCOUNT_A },
    { id: id('003XX0000000E02'), name: 'Bo Kim', title: 'VP', email: '', accountId: ACCOUNT_A }];
  const current = [
    existing('linked-1', 'Different Name', { sfdcContactId: id('003XX0000000E01'), title: 'Old', company: 'Co' }),
    existing('card-1', 'Jane Customer', { email: 'jane@customer.example' }),
    existing('linked-2', 'Bo Kim', { sfdcContactId: id('003XX0000000E02'), title: '', company: 'Co' }),
  ];
  const result = matchContacts(sfdc, current, new Set());
  // A fully filled linked contact needs no request; one with an empty title is filled through its link.
  assert.deepEqual(result.items, [{ action: 'link', sfdcContactId: id('003XX0000000E02'), sfdcAccountId: ACCOUNT_A,
    name: 'Bo Kim', title: 'VP', contactId: 'linked-2', revision: 'r-linked-2' }]);
  assert.equal(result.unchanged, 1);
  assert.equal(result.ambiguous, 0);
});

test('ambiguous_name_is_skipped_and_counted', () => {
  const sfdc = [
    { id: id('003XX0000000E01'), name: 'Min Lee', title: '', email: '', accountId: ACCOUNT_A },
    { id: id('003XX0000000E02'), name: 'Ann Park', title: '', email: '', accountId: ACCOUNT_A },
    { id: id('003XX0000000E03'), name: 'Ann  Park.', title: '', email: '', accountId: ACCOUNT_A },
    { id: id('003XX0000000E04'), name: 'New Person', title: '', email: '', accountId: ACCOUNT_A },
    { id: id('003XX0000000E05'), name: 'Sam Choi', title: '', email: 'sam@one.example', accountId: ACCOUNT_A },
  ];
  const current = [existing('a', 'min lee'), existing('b', 'Min Lee'), existing('c', 'ann park'),
    existing('d', 'Sam Choi', { email: 'sam@two.example' })];
  const result = matchContacts(sfdc, current, new Set([id('003XX0000000E09')]));
  // Two existing Min Lee, and two SFDC Ann Park for one contact: nobody is linked or created for them.
  assert.equal(result.ambiguous, 3);
  // A same-name contact with a conflicting email is another person.
  assert.deepEqual(result.items.map(item => [item.action, item.sfdcContactId]),
    [['create', id('003XX0000000E04')], ['create', id('003XX0000000E05')]]);
});

test('suppressed_ids_are_never_recreated_and_korean_names_match_without_spaces', () => {
  const sfdc = [
    { id: id('003XX0000000E01'), name: '이민호', names: ['이민호', '민호 이'], title: '', email: '', accountId: ACCOUNT_A },
    { id: id('003XX0000000E02'), name: 'Gone', title: '', email: '', accountId: ACCOUNT_A },
  ];
  const result = matchContacts(sfdc, [existing('k', '이 민호', { company: 'Co', title: '' })], new Set([id('003XX0000000E02')]));
  assert.equal(result.suppressed, 1);
  assert.deepEqual(result.items.map(item => [item.action, item.contactId]), [['link', 'k']]);
});

test('canonical_sfdc_id_matches_the_server', () => {
  for (const [short, full] of [['001A000001aBcDe', '001A000001aBcDeIAK'], ['003AB00000abcdE', '003AB00000abcdEYAQ'],
    ['0B0DE0B0DE0B0DE', '0B0DE0B0DE0B0DE000'], ['ABCDEFGHIJKLMNO', 'ABCDEFGHIJKLMNO555']]) {
    assert.equal(canonicalSfdcId(short), full);
    assert.equal(canonicalSfdcId(full), full);
  }
  for (const bad of ['003AB00000abcdEyaq', '003AB00000abcdEYAR', '003AB00000abcd', '003AB00000abcdEYA', '003AB00000abcdE-AQ',
    '003AB00000abcdEYAQ1', '', null, 42]) assert.equal(canonicalSfdcId(bad), null, String(bad));
});

test('latin_names_match_only_in_given_order', () => {
  const sfdc = [{ id: id('003XX0000000E01'), name: 'Min Lee', names: ['Min Lee'], title: '', email: '', accountId: ACCOUNT_A }];
  const result = matchContacts(sfdc, [existing('x', 'Lee Min')], new Set());
  assert.deepEqual(result.items.map(item => item.action), ['create']);
});

// ---------- the step ----------

test('fifteen_char_sentral_id_matches_stored_18_char_link', async () => {
  const records = [
    contactRecord('003AB00000abcdE', 'Linked', 'Person'),
    contactRecord('001A000001aBcDe', 'New', 'Person'),
    contactRecord('0B0DE0B0DE0B0DE', 'Min', 'Lee'),
  ];
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']], contacts: { 'acct-1': [
    existing('linked', 'Linked Person', { sfdcContactId: '003AB00000abcdEYAQ', title: 'T', company: 'Co' }),
    existing('broken', 'Broken', { sfdcContactId: 'not-an-sfdc-id' }),
    existing('swapped', 'Lee Min'),
  ] },
  // The server may echo the 15-character form; the canonical comparison accepts it.
  answer: item => ({ sfdcContactId: item.sfdcContactId.slice(0, 15), result: 'created', contactId: 'n' }) });
  const result = await runSfdcContacts({ session: contactSource({ [ACCOUNT_A]: [records] }), api, dryRun: false });
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.unchanged, 1, 'the 15-character record matched its 18-character link');
  assert.equal(result.counts.invalidIds, 1, 'a malformed stored link');
  assert.equal(result.counts.created, 2);
  const { body } = api.syncs[0];
  assert.deepEqual(body.items.map(item => [item.action, item.sfdcContactId]),
    [['create', '001A000001aBcDeIAK'], ['create', '0B0DE0B0DE0B0DE000']], 'Latin names never match in swapped order');
  assert.deepEqual(body.present[ACCOUNT_A], ['003AB00000abcdEYAQ', '001A000001aBcDeIAK', '0B0DE0B0DE0B0DE000'],
    'present holds canonical IDs in read order');
});

test('one_failed_sfdc_account_of_two_sends_items_without_a_snapshot', async () => {
  const B = id('001XX0000000A02');
  const { IntakeError } = await import('../dist/intake/errors.js');
  // F01 is already linked, so its fill is still sent; the unlinked F03 is deferred.
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1'], [B, 'acct-1']],
    contacts: { 'acct-1': [existing('linked', 'A B', { sfdcContactId: id('003XX0000000F01') })] } });
  const source = contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B'), contactRecord(id('003XX0000000F03'), 'E', 'F')]],
    [B]: [[contactRecord(id('003XX0000000F02'), 'C', 'D', { accountId: B })]] },
  { fail: { [`${B}:0`]: new IntakeError('SOURCE_TIMEOUT', 'search_contacts call failed') } });
  const result = await runSfdcContacts({ session: source, api, dryRun: false });
  assert.equal(result.result, 'SOURCE_TIMEOUT');
  assert.equal(result.counts.deferredIncomplete, 1);
  assert.equal(api.syncs.length, 1);
  const { body } = api.syncs[0];
  assert.equal(body.snapshotComplete, false);
  assert.equal(body.present, undefined);
  assert.deepEqual(body.sfdcAccountIds, [ACCOUNT_A, B]);
  assert.deepEqual(body.items.map(item => [item.action, item.sfdcContactId]), [['link', id('003XX0000000F01')]]);
  assert.equal(result.counts.incomplete, 1);
});

test('truncated_mappings_send_no_snapshot', async () => {
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']], truncated: true });
  const result = await runSfdcContacts({ session: contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B')]] }),
    api, dryRun: false });
  assert.equal(api.syncs.length, 0, 'no create is inferred and nothing is linked, so nothing is sent');
  assert.equal(result.counts.deferredIncomplete, 1);
  assert.equal(result.counts.incomplete, 1);
});

test('corrupt_ledger_is_reset_and_later_accounts_still_run', async t => {
  const store = new IntakeStore(tempHome(t));
  const B = id('001XX0000000A02');
  const directory = join(store.directory, 'sfdc-contacts', SCOPE);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const file = account => join(directory, `${createHash('sha256').update(account).digest('hex').slice(0, 32)}.json`);
  writeFileSync(file('acct-1'), 'not json', { mode: 0o600 });
  writeFileSync(file('acct-2'), JSON.stringify({ version: 1, suppressed: ['bad id', id('003XX0000000F09')] }), { mode: 0o600 });
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1'], [B, 'acct-2']] });
  const source = contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B')]],
    [B]: [[contactRecord(id('003XX0000000F02'), 'C', 'D', { accountId: B })]] });
  const result = await runSfdcContacts({ session: source, api, dryRun: false, store, scope: SCOPE });
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.ledgerReset, 2);
  assert.deepEqual(api.syncs.map(sync => sync.accountId), ['acct-1', 'acct-2']);
});

test('filters_internal_record_types_and_aws_domains', async () => {
  const records = [
    contactRecord(id('003XX0000000F01'), 'Keep', 'Me'),
    contactRecord(id('003XX0000000F02'), 'In', 'Side', { recordType: { name: 'Stakeholder Map - Internal Contacts' } }),
    contactRecord(id('003XX0000000F03'), 'Pro', 'Res', { recordType: { name: 'Project Resource' } }),
    contactRecord(id('003XX0000000F04'), 'A', 'Z', { email: 'a@amazon.com' }),
    contactRecord(id('003XX0000000F05'), 'B', 'Z', { email: 'b@corp.Amazon.COM' }),
    contactRecord(id('003XX0000000F06'), 'C', 'Z', { email: 'c@amazon.co.kr' }),
    contactRecord(id('003XX0000000F07'), 'D', 'Z', { email: 'd@aws.com' }),
    contactRecord(id('003XX0000000F08'), 'Look', 'Alike', { email: 'e@notamazon.com' }),
  ];
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']] });
  const result = await runSfdcContacts({ session: contactSource({ [ACCOUNT_A]: [records] }), api, dryRun: false });
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.fetched, 8);
  assert.equal(result.counts.skippedInternal, 6);
  assert.equal(result.counts.created, 2);
  const [sync] = api.syncs;
  assert.deepEqual(sync.body.items.map(item => item.sfdcContactId), [id('003XX0000000F01'), id('003XX0000000F08')]);
  assert.deepEqual(sync.body.present, { [ACCOUNT_A]: [id('003XX0000000F01'), id('003XX0000000F08')] });
  assert.equal(sync.body.items[0].name, 'Keep Me');
});

test('cap_limits_items_to_200', async () => {
  const records = Array.from({ length: 260 }, (_, n) => contactRecord(sfdcId('003', 1000 + n), `P${n}`, 'Q'));
  // The 40 highest IDs match existing contacts by email: links go first, so they all fit.
  const current = records.slice(220).map((item, n) => existing(`card-${n}`, `Card ${n}`, { email: item.email }));
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']], contacts: { 'acct-1': current } });
  const result = await runSfdcContacts({ session: contactSource({ [ACCOUNT_A]: [records.slice(0, 130), records.slice(130)] }),
    api, dryRun: false });
  const items = api.syncs[0].body.items;
  assert.equal(items.length, 200);
  assert.deepEqual(items.slice(0, 40).map(item => item.action), Array(40).fill('link'));
  assert.deepEqual(items.slice(40).map(item => item.sfdcContactId), records.slice(0, 160).map(item => item.id));
  assert.equal(result.counts.skippedCap, 60);
  assert.equal(result.counts.filled, 40);
  assert.equal(result.counts.created, 160);
  // Skipped people still count as present, so the departure snapshot stays complete.
  assert.equal(api.syncs[0].body.present[ACCOUNT_A].length, 260);
  assert.deepEqual(api.lists, ['acct-1', 'acct-1'], 'all 40 TTOBAK contacts are read in pages of 25');
});

test('forbidden_account_is_skipped', async () => {
  const B = id('001XX0000000A02'), C = id('001XX0000000A03');
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1'], [B, 'acct-2'], [C, 'acct-3'], [id('001XX0000000A04'), null]],
    forbidden: ['acct-1', 'sync:acct-3'] });
  const source = contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B')]],
    [B]: [[contactRecord(id('003XX0000000F02'), 'C', 'D', { accountId: B })]],
    [C]: [[contactRecord(id('003XX0000000F03'), 'E', 'F', { accountId: C })]] });
  const result = await runSfdcContacts({ session: source, api, dryRun: false });
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.accounts, 3);
  assert.equal(result.counts.forbidden, 2);
  assert.equal(result.counts.created, 1);
  assert.deepEqual(api.syncs.map(sync => sync.accountId), ['acct-2', 'acct-3']);
});

test('more_than_20_sfdc_accounts_sends_no_snapshot_and_reads_only_the_first_20', async () => {
  const ids = Array.from({ length: 23 }, (_, n) => sfdcId('001', 500 + n));
  const pages = Object.fromEntries(ids.map((id, n) => [id, [[contactRecord(sfdcId('003', 700 + n), `N${n}`, 'X', { accountId: id })]]]));
  const api = contactsApi({ mappings: [...ids].reverse().map(id => [id, 'acct-1']) });
  const source = contactSource(pages);
  const result = await runSfdcContacts({ session: source, api, dryRun: false });
  assert.equal(source.calls.length, 20);
  assert.equal(api.syncs.length, 0, 'an incomplete Account infers no links or creates');
  assert.equal(result.counts.deferredIncomplete, 20);
  assert.equal(result.counts.skippedAccountCap, 3);
  assert.equal(result.counts.incomplete, 1);
});

test('read_cap_of_5000_makes_the_snapshot_incomplete', async () => {
  const pages = Array.from({ length: 11 }, (_, page) => Array.from({ length: page === 10 ? 1 : 500 },
    (_, n) => contactRecord(sfdcId('003', page * 500 + n), `N${page}-${n}`, 'X')));
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']] });
  const source = contactSource({ [ACCOUNT_A]: pages });
  const result = await runSfdcContacts({ session: source, api, dryRun: false });
  assert.equal(source.calls.length, 10, 'at most 5,000 contacts are read per SFDC account');
  assert.equal(api.syncs.length, 0);
  assert.equal(result.counts.deferredIncomplete, 5000);
  assert.equal(result.counts.incomplete, 1);
  assert.equal(result.result, 'ok', 'a capped read is incomplete, not a failure');
});

test('server_results_are_counted_and_suppressed_ids_are_remembered_locally', async t => {
  const store = new IntakeStore(tempHome(t));
  const records = ['G01', 'G02', 'G03', 'G04', 'G05'].map(g => contactRecord(id(`003XX0000000${g}`), g, 'X'));
  const outcome = { G01: 'suppressed', G02: 'alreadyLinked', G03: 'conflict', G04: 'invalid', G05: 'unchanged' };
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']],
    answer: item => ({ sfdcContactId: item.sfdcContactId, result: outcome[item.sfdcContactId.slice(12, 15)] }) });
  const session = contactSource({ [ACCOUNT_A]: [records] });
  const first = await runSfdcContacts({ session, api, dryRun: false, store, scope: SCOPE });
  assert.deepEqual([first.counts.suppressed, first.counts.alreadyLinked, first.counts.conflict, first.counts.invalid,
    first.counts.unchanged, first.counts.marked], [1, 1, 1, 1, 1, 1]);
  const second = await runSfdcContacts({ session, api, dryRun: false, store, scope: SCOPE });
  assert.deepEqual(api.syncs[1].body.items.map(item => item.sfdcContactId.slice(12, 15)), ['G02', 'G03', 'G04', 'G05']);
  assert.equal(second.counts.suppressed, 1);
  // The suppressed ID stays in the departure snapshot; the ledger holds only IDs.
  assert.ok(api.syncs[1].body.present[ACCOUNT_A].includes(id('003XX0000000G01')));
  const ledger = allFiles(store.directory).map(path => readFileSync(path, 'utf8')).join('\n');
  assert.ok(ledger.includes(id('003XX0000000G01')));
  assert.ok(!ledger.includes('@') && !ledger.includes('acct-1'));
});

test('time budget defers the remaining accounts and the result stays ok', async () => {
  const B = id('001XX0000000A02');
  const C = id('001XX0000000A03');
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1'], [B, 'acct-2'], [C, 'acct-3']] });
  const source = contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B')]],
    [B]: [[contactRecord(id('003XX0000000F02'), 'C', 'D', { accountId: B })]] });
  // Start at 0; the first Account starts at 0, the second just before 8 minutes, the third after.
  const ticks = [0, 0, 8 * 60_000 - 1, 8 * 60_000];
  const now = () => ticks.length > 1 ? ticks.shift() : ticks[0];
  const result = await runSfdcContacts({ session: source, api, dryRun: false, now });
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.accounts, 2);
  assert.equal(result.counts.deferred, 1);
  assert.deepEqual(api.syncs.map(sync => sync.accountId), ['acct-1', 'acct-2']);
  const fresh = await runSfdcContacts({ session: source, api: contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']] }), dryRun: true });
  assert.equal(fresh.counts.deferred, 0, 'the real clock stays within the budget');
});

test('dry_run_sends_nothing_and_reports_counts', async () => {
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']] });
  const result = await runSfdcContacts({ session: contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B')]] }),
    api, dryRun: true });
  assert.equal(api.syncs.length, 0);
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.created, 1, 'planned creates are reported');
});

test('a failed read is a step failure but other accounts still sync', async () => {
  const B = id('001XX0000000A02');
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1'], [B, 'acct-2']] });
  const { IntakeError } = await import('../dist/intake/errors.js');
  const source = contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B')], [contactRecord(id('003XX0000000F09'), 'C', 'D')]],
    [B]: [[contactRecord(id('003XX0000000F02'), 'C', 'D', { accountId: B })]] },
  { fail: { [`${ACCOUNT_A}:1`]: new IntakeError('SOURCE_ERROR', 'search_contacts returned an error') } });
  const result = await runSfdcContacts({ session: source, api, dryRun: false });
  assert.equal(result.result, 'SOURCE_ERROR');
  assert.deepEqual(api.syncs.map(sync => [sync.accountId, sync.body.snapshotComplete]), [['acct-2', true]]);
  assert.equal(result.counts.deferredIncomplete, 1);
});

test('incomplete_read_infers_no_link_from_a_partial_name_match', async () => {
  // Two same-named SFDC people on different pages and one unlinked TTOBAK contact:
  // a complete read counts both ambiguous; when page two fails, nothing is linked.
  const { IntakeError } = await import('../dist/intake/errors.js');
  const first = contactRecord(id('003XX0000000H01'), 'Jane', 'Doe', { email: undefined });
  const second = contactRecord(id('003XX0000000H02'), 'Jane', 'Doe', { email: undefined });
  const contacts = { 'acct-1': [existing('card-1', 'Jane Doe'), existing('linked', 'Old Link', { sfdcContactId: id('003XX0000000H03') })] };
  const third = contactRecord(id('003XX0000000H03'), 'Old', 'Link');
  const pages = { [ACCOUNT_A]: [[first, third], [second]] };

  const complete = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']], contacts });
  const full = await runSfdcContacts({ session: contactSource(pages), api: complete, dryRun: false });
  assert.equal(full.counts.ambiguous, 2);
  assert.deepEqual(complete.syncs[0].body.items.map(item => item.contactId), ['linked']);

  const partial = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']], contacts });
  const result = await runSfdcContacts({ session: contactSource(pages,
    { fail: { [`${ACCOUNT_A}:1`]: new IntakeError('SOURCE_ERROR', 'search_contacts returned an error') } }), api: partial, dryRun: false });
  assert.equal(result.result, 'SOURCE_ERROR');
  assert.equal(result.counts.deferredIncomplete, 1);
  assert.equal(result.counts.ambiguous, 0);
  const { body } = partial.syncs[0];
  assert.equal(body.snapshotComplete, false);
  assert.deepEqual(body.items.map(item => [item.action, item.sfdcContactId, item.contactId]),
    [['link', id('003XX0000000H03'), 'linked']], 'only the existing link is filled; card-1 is not linked');
});

test('uneven_pages_over_5000_ids_make_the_read_incomplete', async () => {
  // One short page, then ten full pages: 5,001 kept IDs, ending on a terminal page.
  const pages = [[contactRecord(sfdcId('003', 1), 'S', 'X')],
    ...Array.from({ length: 10 }, (_, page) => Array.from({ length: 500 }, (_, n) => contactRecord(sfdcId('003', 10 + page * 500 + n), `N${page}-${n}`, 'X')))];
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']] });
  const source = contactSource({ [ACCOUNT_A]: pages });
  const result = await runSfdcContacts({ session: source, api, dryRun: false });
  assert.equal(source.calls.length, 11);
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.incomplete, 1);
  assert.equal(result.counts.deferredIncomplete, 5000, 'at most 5,000 people are kept for item building');
  assert.equal(api.syncs.length, 0, 'no present list above 5,000 is ever sent');
});

/** A source whose pages are produced by `page(index, args)`. */
function scriptedSource(page) {
  const calls = [];
  return { calls, async call(tool, args) { calls.push(structuredClone(args)); return page(calls.length - 1, args); } };
}

test('empty_pages_with_more_and_cycling_cursors_fail_closed', async () => {
  const empty = scriptedSource(index => ({ data: { resultRecords: [], totalCount: '9', hasNextPage: true, cursor: index % 2 ? 'a' : 'b' } }));
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']] });
  const first = await runSfdcContacts({ session: empty, api, dryRun: false });
  assert.equal(first.result, 'PARSE_FAILED');
  assert.equal(empty.calls.length, 1, 'an empty page that claims more stops the read');
  assert.equal(first.counts.incomplete, 1);

  // Fresh records on every page but a cursor cycle a -> b -> a.
  const cycling = scriptedSource(index => ({ data: { resultRecords: [contactRecord(sfdcId('003', 100 + index), `C${index}`, 'X')],
    totalCount: '99', hasNextPage: true, cursor: index % 2 ? 'b' : 'a' } }));
  const second = await runSfdcContacts({ session: cycling, api, dryRun: false });
  assert.equal(second.result, 'PARSE_FAILED');
  assert.equal(cycling.calls.length, 3, 'the first repeated cursor stops the read');
  assert.equal(second.counts.deferredIncomplete, 3);

  // Distinct cursors with short pages stop at the page bound.
  const endless = scriptedSource(index => ({ data: { resultRecords: [contactRecord(sfdcId('003', 300 + index), `E${index}`, 'X')],
    totalCount: '99', hasNextPage: true, cursor: `c${index}` } }));
  const third = await runSfdcContacts({ session: endless, api, dryRun: false });
  assert.equal(third.result, 'PARSE_FAILED');
  assert.equal(endless.calls.length, 11);
  assert.equal(api.syncs.length, 0);
});

test('records_of_another_account_are_dropped', async () => {
  const B = id('001XX0000000A02');
  const api = contactsApi({ mappings: [[ACCOUNT_A, 'acct-1']] });
  const source = contactSource({ [ACCOUNT_A]: [[contactRecord(id('003XX0000000F01'), 'A', 'B'),
    contactRecord(id('003XX0000000F02'), 'C', 'D', { accountId: B }),
    contactRecord(id('003XX0000000F03'), 'E', 'F', { accountId: '001XX0000000A01' }),
    contactRecord(id('003XX0000000F04'), 'G', 'H', { accountId: null })]] });
  const result = await runSfdcContacts({ session: source, api, dryRun: false });
  assert.equal(result.result, 'ok');
  assert.equal(result.counts.accountMismatch, 2);
  assert.deepEqual(api.syncs[0].body.present[ACCOUNT_A], [id('003XX0000000F01'), id('003XX0000000F03')],
    'a 15-character account ID matches its canonical form');
});

// ---------- CLI ----------

const EMAIL_OR_PHONE = /canary@|\+82-10-555/i;

function writeSentral(home, contacts = 'ok', mode = 'ok') {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    'aws-sentral-mcp': { command: process.execPath, args: [SENTRAL], env: { FAKE_SENTRAL_MODE: mode, FAKE_SENTRAL_CONTACTS: contacts } } } }));
}

async function cli(t, { contacts = 'ok', existingContacts = [], selectContacts = 'ok' } = {}) {
  const home = tempHome(t);
  writeSentral(home, selectContacts);
  const lines = [];
  const errors = [];
  const environment = {
    home, now: () => NOW, zone: 'Asia/Seoul', stdout: line => lines.push(line), stderr: line => errors.push(line),
    env: { PATH: '/usr/bin:/bin', TTOBAK_API_URL: 'https://api.example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.example.invalid',
      TTOBAK_CLIENT_ID: 'fixture' },
    scope: SCOPE,
  };
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 0, lines.at(-1));
  writeSentral(home, contacts);
  // The mapping holds the 15-character form; requests use the canonical one.
  const api = contactsApi({ mappings: [['001XX0000000A01', 'acct-1']], contacts: { 'acct-1': existingContacts } });
  environment.api = api;
  return { home, lines, errors, environment, api };
}

test('run_includes_sfdc_contacts_after_sfdc', async t => {
  const { lines, environment, api, home } = await cli(t);
  assert.equal(SFDC_CONTACTS_TOOL.name, 'search_contacts');
  assert.ok(!SFDC_TOOLS.includes(SFDC_CONTACTS_TOOL), 'the SFDC sync never requires the contacts tool');
  assert.equal(await runIntake(['run', '--noninteractive', '--only', 'sfdc'], environment), 0, lines.at(-1));
  const output = JSON.parse(lines.at(-1));
  assert.equal(output.ok, true);
  assert.equal(output.results.sfdc.result, 'ok');
  assert.equal(output.results.sfdc.sfdcContacts.result, 'ok');
  assert.equal(output.results.sfdc.sfdcContacts.counts.created, 2);
  assert.equal(output.results.sfdc.sfdcContacts.counts.skippedInternal, 4);
  assert.deepEqual(api.syncs[0].body.items.map(item => [item.sfdcContactId, item.name]),
    [[id('003XX0000000C01'), 'Jane Customer'], [id('003XX0000000C04'), '이민호']]);
  const status = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'status.json'), 'utf8'));
  assert.deepEqual(Object.keys(status.sfdcContacts).sort(), ['counts', 'lastRunAt', 'lastSuccessAt', 'result']);
  assert.equal(status.sfdcContacts.result, 'ok');
});

test('no_email_or_phone_leaves_the_mac', async t => {
  // One business-card contact holds a customer email; it must be linked, and its email never echoed.
  const { lines, errors, environment, api, home } = await cli(t, {
    existingContacts: [existing('card-1', '제인', { email: '003XX0000000C01.canary@customer.example', phone: '+82-10-5559-9999' })] });
  assert.equal(await runIntake(['sfdc', '--noninteractive'], environment), 0, lines.at(-1));
  assert.equal(JSON.parse(lines.at(-1)).sfdcContacts.counts.filled, 1);
  assert.equal(api.syncs[0].body.items[0].contactId, 'card-1');
  assert.equal(await runIntake(['status'], environment), 0);
  const everything = [...api.syncs.map(sync => sync.text), ...lines, ...errors,
    ...allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8'))].join('\n');
  assert.ok(!EMAIL_OR_PHONE.test(everything), 'no SFDC or TTOBAK email or phone in bodies, output or state');
  assert.ok(!everything.includes('5559-9999'));
});

test('incomplete_snapshot_sends_no_present_list', async t => {
  // C01 is already linked, so its fill is still sent; nothing is inferred from the partial read.
  const { lines, environment, api, home } = await cli(t, { contacts: 'page2-error',
    existingContacts: [existing('card-1', 'Jane Customer', { sfdcContactId: id('003XX0000000C01') })] });
  assert.equal(await runIntake(['run', '--noninteractive', '--only', 'sfdc'], environment), 2);
  const output = JSON.parse(lines.at(-1));
  assert.equal(output.code, 'PARTIAL_FAILURE');
  assert.equal(output.results.sfdc.result, 'ok', 'the SFDC sources keep their result');
  assert.equal(output.results.sfdc.sfdcContacts.result, 'SOURCE_ERROR');
  assert.equal(api.syncs.length, 1);
  assert.equal(api.syncs[0].body.snapshotComplete, false);
  assert.equal(api.syncs[0].body.present, undefined);
  assert.deepEqual(api.syncs[0].body.items.map(item => item.sfdcContactId), [id('003XX0000000C01')]);
  const status = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'status.json'), 'utf8'));
  assert.equal(status.sfdcContacts.result, 'SOURCE_ERROR');
  assert.equal(status.sfdcContacts.lastSuccessAt, undefined);
  assert.equal(status.sfdc.result, 'ok');
});

test('a missing contacts tool fails only the contacts step', async t => {
  // Selection does not require search_contacts either.
  const { lines, environment, api, home } = await cli(t, { contacts: 'missing', selectContacts: 'missing' });
  assert.equal(await runIntake(['run', '--noninteractive', '--only', 'sfdc'], environment), 2);
  const output = JSON.parse(lines.at(-1));
  assert.equal(output.code, 'PARTIAL_FAILURE');
  assert.equal(output.results.sfdc.result, 'ok', 'the SFDC performance sync still succeeds');
  assert.equal(output.results.sfdc.sfdcContacts.result, 'SCHEMA_CHANGED');
  assert.equal(api.syncs.length, 0);
  const status = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'status.json'), 'utf8'));
  assert.equal(status.sfdc.result, 'ok');
  assert.equal(status.sfdcContacts.result, 'SCHEMA_CHANGED');
});

test('auth_expired_midway_stops', async t => {
  const { lines, environment, api } = await cli(t, { contacts: 'page2-auth' });
  assert.equal(await runIntake(['run', '--noninteractive', '--only', 'sfdc'], environment), 2);
  const output = JSON.parse(lines.at(-1));
  assert.equal(output.code, 'AUTH_EXPIRED_MIDWAY');
  assert.equal(output.results.sfdc.sfdcContacts.result, 'AUTH_EXPIRED_MIDWAY');
  assert.equal(api.syncs.length, 0, 'nothing is sent after Midway expires');
});
