// Local re-validation of skill packages (same rules as the TTOBAK API).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readZip } from '../dist/setup/zip.js';
import { validatePackage } from '../dist/setup/package.js';
import { demoPackage, makeZip } from './helpers/zip.mjs';

function rule(entries) {
  try {
    validatePackage(makeZip(entries));
  } catch (error) {
    assert.equal(error.code, 'PACKAGE_INVALID', String(error));
    return error.message.replace(/^PACKAGE_INVALID: /, '').split(':')[0];
  }
  assert.fail('package was accepted');
}

test('installer layout is read like the server', () => {
  const manifest = validatePackage(makeZip(demoPackage()));
  assert.equal(manifest.version, '9.9.9');
  assert.equal(manifest.root, 'pkg-v9.9.9');
  assert.deepEqual(manifest.skills, ['demo-skill']);
  assert.deepEqual(manifest.servers, {
    toolboxRegistries: ['s3://example-registry-one/tools.json', 's3://example-registry-two/tools.json'],
    toolboxServers: ['alpha-mcp', 'beta-mcp'], aimServers: ['gamma-mcp', 'delta-mcp'],
    httpServers: { 'docs-mcp': 'https://docs.example.com/mcp' },
  });
});

test('manifest wins over the script; version falls back to the folder', () => {
  const manifest = '{"version":"2.0.0","toolboxRegistries":[],"toolboxServers":["only-mcp"],"aimServers":[],"httpServers":{}}';
  const got = validatePackage(makeZip(demoPackage({ name: 'pkg-v9.9.9/ttobak-manifest.json', data: manifest })));
  assert.equal(got.version, '2.0.0');
  assert.deepEqual(got.servers.toolboxServers, ['only-mcp']);
  const bare = validatePackage(makeZip([{ name: 'pkg-v1.2.3/skills/a-skill/SKILL.md', data: 'x' }]));
  assert.equal(bare.version, 'pkg-v1.2.3');
});

test('zip reader rejects unsafe entries', () => {
  assert.equal(rule(demoPackage({ name: 'pkg-v9.9.9/skills/demo-skill/../../x', data: 'x' })), 'unsafe-path');
  assert.equal(rule(demoPackage({ name: '/abs/x', data: 'x' })), 'unsafe-path');
  assert.equal(rule(demoPackage({ name: 'pkg-v9.9.9\\skills\\x', data: 'x' })), 'unsafe-path');
  assert.equal(rule(demoPackage({ name: 'pkg-v9.9.9/skills/demo-skill/link', data: '/etc/passwd', mode: 0o120777 })), 'not-regular-file');
  assert.equal(rule(demoPackage({ name: 'other/skills/x/SKILL.md', data: 'x' })), 'top-level');
  assert.equal(rule(demoPackage({ name: 'loose.txt', data: 'x' })), 'top-level');
  assert.equal(rule([{ name: 'pkg/README.md', data: 'x' }]), 'no-skills');
  assert.equal(rule(demoPackage({ name: 'pkg-v9.9.9/skills/empty-skill/notes.md', data: 'x' })), 'skill-missing-md');
  assert.equal(rule(demoPackage({ name: 'pkg-v9.9.9/skills/demo-skill/x', data: 'x', flags: 1 })), 'not-regular-file');
});

test('zip reader stops at the uncompressed cap', () => {
  // A deflate entry that inflates past its declared size is refused when read.
  const entries = demoPackage({ name: 'pkg-v9.9.9/skills/demo-skill/big.txt', data: 'a'.repeat(5000), method: 'deflate', declared: 10 });
  const file = readZip(makeZip(entries)).find(entry => entry.name.endsWith('big.txt'));
  assert.throws(() => file.read(), /PACKAGE_INVALID/);
  assert.equal(rule([{ name: 'pkg/skills/a-skill/SKILL.md', data: 'x' }, { name: 'pkg/skills/a-skill/b', data: '', declared: 51 * 1024 * 1024 }]),
    'uncompressed-too-large');
  assert.equal(rule(Array.from({ length: 501 }, (_, i) => ({ name: `pkg/skills/a-skill/f${i}`, data: '' }))), 'too-many-entries');
  assert.throws(() => validatePackage(Buffer.alloc(20 * 1024 * 1024 + 1)), /zip-too-large/);
  assert.throws(() => validatePackage(Buffer.from('not a zip')), /not-zip/);
});

test('names, registries, URLs and script tokens follow the server rules', () => {
  const base = [{ name: 'pkg-v9.9.9/skills/demo-skill/SKILL.md', data: 'x' }];
  const script = body => ({ name: 'pkg-v9.9.9/setup-internal-mcp.sh', data: body });
  assert.equal(rule([{ name: 'pkg/skills/Bad_Name/SKILL.md', data: 'x' }]), 'bad-skill-name');
  assert.equal(rule([...base, script('AIM_MCPS_BASE=(Bad.Name)')]), 'bad-server-name');
  assert.equal(rule([...base, script('HTTP_MCPS=("docs=http://docs.example.com")')]), 'bad-http-url');
  assert.equal(rule([...base, script('TOOLBOX_REGISTRIES=("https://example.com/tools.json")')]), 'bad-registry');
  assert.equal(rule([...base, script('AIM_MCPS_BASE=("$HOME")')]), 'bad-server-list');
  assert.equal(rule([...base, script('AIM_MCPS_BASE=(`id`)')]), 'bad-server-list');
  assert.equal(rule([...base, script('AIM_MCPS_BASE=(alpha-mcp')]), 'bad-server-list');
  assert.equal(rule([...base, { name: 'pkg-v9.9.9/ttobak-manifest.json', data: '{' }]), 'bad-manifest');
});

test('the report skill needs its layout files', () => {
  const entries = demoPackage({ name: 'pkg-v9.9.9/skills/sa-performance-report/SKILL.md', data: 'x' },
    { name: 'pkg-v9.9.9/skills/sa-performance-report/scripts/calendar_count.py', data: 'x' });
  assert.equal(rule(entries), 'report-skill-layout');
  entries.push({ name: 'pkg-v9.9.9/skills/sa-performance-report/references/report-structure.md', data: 'x' });
  assert.deepEqual(validatePackage(makeZip(entries)).skills, ['demo-skill', 'sa-performance-report']);
});

test('null manifest lists read as empty, as on the server', () => {
  const manifest = '{"version":"1.0.0","toolboxRegistries":null,"toolboxServers":null,"aimServers":["a-mcp"],"httpServers":null}';
  const got = validatePackage(makeZip([{ name: 'pkg/skills/a-skill/SKILL.md', data: 'x' }, { name: 'pkg/ttobak-manifest.json', data: manifest }]));
  assert.deepEqual(got.servers, { toolboxRegistries: [], toolboxServers: [], aimServers: ['a-mcp'], httpServers: {} });
});
