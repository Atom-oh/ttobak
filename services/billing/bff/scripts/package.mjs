// Builds dist/: index.js, app.js, lib/ and production node_modules (npm ci --omit=dev in a temp copy).
import { cpSync, mkdtempSync, rmSync, mkdirSync, existsSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const dist = join(root, 'dist');
const tmpBase = process.env.TMPDIR || join(process.env.HOME || root, '.tmp');
mkdirSync(tmpBase, { recursive: true });
const work = mkdtempSync(join(tmpBase, 'billing-bff-'));
try {
  for (const f of ['package.json', 'package-lock.json']) cpSync(join(root, f), join(work, f));
  execFileSync('npm', ['ci', '--omit=dev', '--no-audit', '--no-fund'], { cwd: work, stdio: 'inherit' });
  rmSync(dist, { recursive: true, force: true });
  mkdirSync(dist, { recursive: true });
  for (const f of ['index.js', 'app.js', 'package.json']) cpSync(join(root, f), join(dist, f));
  cpSync(join(root, 'lib'), join(dist, 'lib'), { recursive: true });
  cpSync(join(work, 'node_modules'), join(dist, 'node_modules'), { recursive: true });
  if (!existsSync(join(dist, 'node_modules', 'express'))) throw new Error('express missing from dist');
} finally {
  rmSync(work, { recursive: true, force: true });
}
