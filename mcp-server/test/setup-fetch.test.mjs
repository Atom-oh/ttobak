// Fetching the effective packages: TTOBAK metadata, sha256-checked downloads.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { fetchEffective } from '../dist/setup/fetch.js';
import { demoPackage, makeZip } from './helpers/zip.mjs';

const zip = makeZip(demoPackage());
const sha = createHash('sha256').update(zip).digest('hex');
const row = scope => ({ scope, version: '9.9.9', sha256: sha, size: zip.length, uploadedAt: '2026-11-12T00:00:00Z',
  skills: ['demo-skill'], servers: { toolboxRegistries: [], toolboxServers: [], aimServers: [], httpServers: {} } });
const view = overrides => ({ org: row('org'), personal: null,
  effective: { skills: [{ name: 'demo-skill', source: 'org' }], servers: row('org').servers, conflicts: [] },
  downloads: { org: 'https://bucket.example/org.zip' }, ...overrides });
const api = value => ({ skillPackages: async () => value });

test('a verified download is validated and returned by scope', async () => {
  const seen = [];
  const got = await fetchEffective(api(view()), async url => { seen.push(url); return zip; });
  assert.deepEqual(seen, ['https://bucket.example/org.zip']);
  assert.deepEqual(got.packages.org.skills, ['demo-skill']);
  assert.equal(got.packages.personal, undefined);
  assert.deepEqual(got.view.effective.skills, [{ name: 'demo-skill', source: 'org' }]);
});

test('sha mismatch fails before install', async () => {
  const tampered = Buffer.concat([zip, Buffer.from('x')]);
  await assert.rejects(fetchEffective(api(view()), async () => tampered), /PACKAGE_INTEGRITY/);
});

test('expired download URL fails', async () => {
  await assert.rejects(fetchEffective(api(view()), async () => { throw new Error('status 403'); }), /PACKAGE_DOWNLOAD/);
  await assert.rejects(fetchEffective(api(view({ downloads: { org: 'http://bucket.example/org.zip' } })), async () => zip), /PACKAGE_DOWNLOAD/);
});

test('no packages yields an empty effective set', async () => {
  const empty = { org: null, personal: null, effective: { skills: [], servers: row('org').servers, conflicts: [] }, downloads: {} };
  const got = await fetchEffective(api(empty), async () => assert.fail('no download'));
  assert.deepEqual(got.packages, {});
});

test('a malformed view is refused', async () => {
  await assert.rejects(fetchEffective(api({ org: 'x' }), async () => zip), /TTOBAK_UNAVAILABLE/);
  await assert.rejects(fetchEffective(api(view({ effective: { skills: [{ name: '../x', source: 'org' }], servers: row('org').servers, conflicts: [] } })),
    async () => zip), /TTOBAK_UNAVAILABLE/);
});
