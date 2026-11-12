// Skill install from validated packages: backup first, never outside ~/.claude/skills.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { validatePackage } from '../dist/setup/package.js';
import { installSkills, planSkills } from '../dist/setup/skills.js';
import { makeZip } from './helpers/zip.mjs';

const NOW = new Date('2026-11-12T03:04:05Z');

function home(t) {
  const dir = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-skills-')));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

function pkg(files, root = 'pkg') {
  return validatePackage(makeZip(Object.entries(files).map(([name, data]) => ({ name: `${root}/skills/${name}`, data,
    ...(name.endsWith('.sh') ? { mode: 0o100755 } : {}) }))));
}

const skillDir = (h, name) => join(h, '.claude', 'skills', name);
const backups = h => existsSync(join(h, '.claude', 'skills-backup')) ? readdirSync(join(h, '.claude', 'skills-backup')) : [];

test('a missing skill is installed, then reported current with no backup', t => {
  const h = home(t);
  const packages = { org: pkg({ 'demo/SKILL.md': '# demo', 'demo/scripts/run.sh': 'echo' }) };
  const effective = [{ name: 'demo', source: 'org' }];
  assert.deepEqual(planSkills(h, effective, packages).map(p => p.state), ['install']);
  const first = installSkills(h, planSkills(h, effective, packages), packages, NOW);
  assert.equal(first.status, 'ok');
  assert.equal(readFileSync(join(skillDir(h, 'demo'), 'SKILL.md'), 'utf8'), '# demo');
  assert.equal(statSync(join(skillDir(h, 'demo'), 'scripts', 'run.sh')).mode & 0o111, 0o111);
  // identical skill is current and makes no backup
  const again = installSkills(h, planSkills(h, effective, packages), packages, NOW);
  assert.equal(again.status, 'current');
  assert.deepEqual(backups(h), []);
});

test('changed skill is backed up then replaced; local-only files are kept', t => {
  const h = home(t);
  mkdirSync(skillDir(h, 'demo'), { recursive: true });
  writeFileSync(join(skillDir(h, 'demo'), 'SKILL.md'), '# old');
  writeFileSync(join(skillDir(h, 'demo'), 'notes.md'), 'mine');
  const packages = { org: pkg({ 'demo/SKILL.md': '# new' }) };
  const result = installSkills(h, planSkills(h, [{ name: 'demo', source: 'org' }], packages), packages, NOW);
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.items, ['demo']);
  assert.equal(readFileSync(join(skillDir(h, 'demo'), 'SKILL.md'), 'utf8'), '# new');
  assert.equal(readFileSync(join(skillDir(h, 'demo'), 'notes.md'), 'utf8'), 'mine');
  assert.deepEqual(backups(h), ['demo-20261112-030405']);
  assert.equal(readFileSync(join(h, '.claude', 'skills-backup', 'demo-20261112-030405', 'SKILL.md'), 'utf8'), '# old');
});

test('symlinked skills dir is refused and nothing is written', t => {
  const h = home(t);
  const elsewhere = join(h, 'elsewhere');
  mkdirSync(elsewhere);
  mkdirSync(join(h, '.claude', 'skills'), { recursive: true });
  symlinkSync(elsewhere, skillDir(h, 'demo'));
  const packages = { org: pkg({ 'demo/SKILL.md': '# demo' }) };
  const result = installSkills(h, planSkills(h, [{ name: 'demo', source: 'org' }], packages), packages, NOW);
  assert.equal(result.status, 'failed');
  assert.equal(result.code, 'SKILL_PATH_UNSAFE');
  assert.deepEqual(readdirSync(elsewhere), []);
  // The skills root itself as a link is refused too.
  const h2 = home(t);
  mkdirSync(join(h2, '.claude'), { recursive: true });
  symlinkSync(elsewhere, join(h2, '.claude', 'skills'));
  assert.equal(installSkills(h2, planSkills(h2, [{ name: 'demo', source: 'org' }], packages), packages, NOW).code, 'SKILL_PATH_UNSAFE');
  assert.deepEqual(readdirSync(elsewhere), []);
});

test('personal source is used when both exist', t => {
  const h = home(t);
  const packages = { org: pkg({ 'shared/SKILL.md': 'org' }), personal: pkg({ 'shared/SKILL.md': 'mine' }, 'mine') };
  installSkills(h, planSkills(h, [{ name: 'shared', source: 'personal' }], packages), packages, NOW);
  assert.equal(readFileSync(join(skillDir(h, 'shared'), 'SKILL.md'), 'utf8'), 'mine');
});

test('an effective skill missing from its package fails without writing', t => {
  const h = home(t);
  const packages = { org: pkg({ 'other/SKILL.md': 'x' }) };
  const result = installSkills(h, planSkills(h, [{ name: 'demo', source: 'org' }], packages), packages, NOW);
  assert.equal(result.status, 'failed');
  assert.equal(result.code, 'PACKAGE_INVALID');
  assert.equal(existsSync(skillDir(h, 'demo')), false);
});

test('no effective skills is skipped', t => {
  assert.equal(installSkills(home(t), [], {}, NOW).status, 'skipped');
});
