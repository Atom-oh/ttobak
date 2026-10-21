import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TtobakApi } from '../dist/api.js';

function metadataPages(value, size = 1700) {
  const points = Array.from(JSON.stringify(value));
  const pages = [];
  for (let start = 0; start < points.length; start += size) {
    const end = Math.min(points.length, start + size), complete = end === points.length;
    pages.push({ kind: 'project', projectId: value.projectId, section: 'metadata', phase: 'metadata',
      consistency: 'current_group', revision: 'a'.repeat(64), dataJson: points.slice(start, end).join(''),
      fragment: { unit: 'unicode_code_points', startOffset: start, endOffset: end, totalCodePoints: points.length, complete },
      page: { complete, nextCursor: complete ? null : `server-page-${pages.length + 1}` } });
  }
  return pages;
}
function fixture(pages) {
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  const reads = [], writes = [];
  api.getProject = async () => { throw new Error('Unbounded legacy prefetch'); };
  api.readProjectSection = async options => { reads.push({ ...options }); return pages.shift(); };
  api.put = async (path, body) => { writes.push({ path, body }); return { projectId: 'p' }; };
  return { api, reads, writes };
}
test('project update restores large escaped metadata before preserving omitted fields', async () => {
  const current = { projectId: 'p', description: '원문😀"\\\n'.repeat(20000), sfdcOpptyId: 'opp', sfdcUrl: 'https://example.invalid/opportunity', stage: 'draft' };
  const pages = metadataPages(current), count = pages.length;
  const { api, reads, writes } = fixture(pages);
  await api.updateProject('p', { name: 'New name', stage: '' });
  assert.equal(reads.length, count);
  assert.ok(reads.every(o => o.section === 'metadata' && o.projectId === 'p'));
  assert.equal(reads[1].cursor, 'server-page-1');
  assert.deepEqual(writes, [{ path: '/api/projects/p', body: { name: 'New name', description: current.description,
    sfdcOpptyId: 'opp', sfdcUrl: current.sfdcUrl, stage: '' } }]);
});
test('authorization-only pages retain server cursors before a metadata prefetch', async () => {
  const { api, reads, writes } = fixture([{ kind: 'project', projectId: 'p', section: 'metadata', phase: 'authorization',
    authorizationPending: true, page: { complete: false, nextCursor: 'authorization-next' } }, ...metadataPages({ projectId: 'p', stage: 'current' })]);
  await api.updateProject('p', { name: 'Name' });
  assert.equal(reads[1].cursor, 'authorization-next'); assert.equal(writes[0].body.stage, 'current');
});
test('stale, missing or repeated metadata continuation never sends an update', async () => {
  for (const kind of ['revision', 'offset', 'cycle', 'identity']) {
    const pages = metadataPages({ projectId: 'p', description: 'x'.repeat(5000) });
    if (kind === 'revision') pages[1].revision = 'b'.repeat(64);
    if (kind === 'offset') { pages[1].fragment.startOffset++; pages[1].dataJson = pages[1].dataJson.slice(1); }
    if (kind === 'cycle') pages[1].page.nextCursor = pages[0].page.nextCursor;
    if (kind === 'identity') pages[0].projectId = 'other';
    const { api, writes } = fixture(pages);
    await assert.rejects(api.updateProject('p', { name: 'Name' }));
    assert.equal(writes.length, 0, kind);
  }
});
test('fully specified project updates do not introduce unnecessary reads', async () => {
  const { api, reads, writes } = fixture([]);
  await api.updateProject('p', { name: 'Name', description: '', sfdcOpptyId: '', sfdcUrl: '', stage: '' });
  assert.equal(reads.length, 0); assert.equal(writes.length, 1);
});
