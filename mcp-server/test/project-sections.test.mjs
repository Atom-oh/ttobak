import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { projectSectionOptions, projectSectionResult } from '../dist/project-sections.js';

function result(options, phase, dataJson, complete = true) {
  const count = Array.from(dataJson).length;
  return {
    kind: 'project', projectId: options.projectId, section: options.section, phase,
    consistency: 'current_group', revision: 'a'.repeat(64), dataJson,
    fragment: { unit: 'unicode_code_points', startOffset: 0, endOffset: count, totalCodePoints: count, complete: true },
    page: { complete, nextCursor: complete ? null : 'opaque-next-group' },
  };
}

test('project section API forwards fixed selection and caps original HTTP bytes', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  const seen = [];
  api.request = async (...args) => { seen.push(args); return {}; };
  await api.readProjectSection(projectSectionOptions({
    projectId: 'acct', cursor: 'opaque+/cursor', from: '2026-10-01T00:00:00Z', types: ['need'],
  }, 'brief'));
  const [method, path, body, limit] = seen[0];
  assert.equal(method, 'GET');
  assert.equal(body, undefined);
  assert.equal(limit, 32000);
  const url = new URL(path, 'https://example.invalid');
  assert.equal(url.pathname, '/api/projects/acct/reading');
  assert.deepEqual(Object.fromEntries(url.searchParams), {
    section: 'brief', pageSize: '4000', cursor: 'opaque+/cursor', from: '2026-10-01T00:00:00Z', types: 'need',
  });
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: project tools keep every brief phase available without full-read fallback`, async t => {
    const seen = [];
    const server = createMcpServer({
      mode, auth: { isAuthenticated: () => true, getIdToken: async () => 'fixture' },
      api: {
        getProject() { throw new Error('Unbounded project fallback'); },
        getProjectBrief() { throw new Error('Unbounded brief fallback'); },
        getProjectMeetings() { throw new Error('Unbounded meeting fallback'); },
        getProjectInsights() { throw new Error('Unbounded insight fallback'); },
        async readProjectSection(options) {
          seen.push(options);
          const metadata = options.section === 'detail' || options.section === 'brief';
          return result(options, metadata ? 'metadata' : options.section, metadata ? '{"name":"계정😀"}' : '[]', !metadata);
        },
      },
      apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture',
    });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    const client = new Client({ name: 'project-sections', version: '1' });
    await server.connect(remote);
    await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    for (const name of ['ttobak_get_project', 'ttobak_get_project_brief', 'ttobak_get_project_insights']) {
      const tool = (await client.listTools()).tools.find(tool => tool.name === name);
      assert.ok(tool.inputSchema.properties.cursor);
      const response = await client.callTool({ name, arguments: { projectId: 'acct', cursor: 'server-owned' } });
      assert.notEqual(response.isError, true, response.content[0].text);
    }
    assert.deepEqual(seen.map(value => value.section), ['detail', 'brief', 'insights']);
    assert.ok(seen.every(value => value.cursor === 'server-owned'));
  });
}

test('project JSON fragments are preserved and incomplete groups cannot masquerade as a whole brief', () => {
  const options = projectSectionOptions({ projectId: 'acct' }, 'brief');
  const page = result(options, 'metadata', '{"name":"계정"}', false);
  assert.deepEqual(JSON.parse(projectSectionResult(page, options).content[0].text), page);
  for (const broken of [
    { ...page, projectId: 'other' },
    { ...page, page: { complete: true, nextCursor: null } },
    { ...page, dataJson: '{}' },
    { ...page, fragment: { ...page.fragment, complete: false } },
    { ...page, page: { complete: false, nextCursor: null } },
    { ...page, extra: 'x'.repeat(32000) },
  ]) assert.throws(() => projectSectionResult(broken, options));
  for (const args of [
    { projectId: '../bad' }, { projectId: 'acct', pageSize: 0 }, { projectId: 'acct', cursor: '' },
    { projectId: 'acct', types: ['unsupported'] }, { projectId: 'acct', from: 'yesterday' },
  ]) assert.throws(() => projectSectionOptions(args, 'brief'));
});


test('project access discovery has no premature data and retains the server cursor', () => {
  const options = projectSectionOptions({ projectId: 'p' }, 'brief');
  const pending = { kind: 'project', projectId: 'p', section: 'brief', phase: 'authorization',
    authorizationPending: true, page: { complete: false, nextCursor: 'server-owned' } };
  assert.deepEqual(JSON.parse(projectSectionResult(pending, options).content[0].text), pending);
  assert.throws(() => projectSectionResult({ ...pending, dataJson: '{}' }, options));
  assert.throws(() => projectSectionResult({ ...pending, page: { complete: true, nextCursor: null } }, options));
  const research = projectSectionOptions({ projectId: 'p', section: 'research' }, 'brief');
  assert.equal(research.section, 'research');
  assert.doesNotThrow(() => projectSectionResult(result(research, 'research', '[]'), research));
});
