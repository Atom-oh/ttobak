#!/usr/bin/env node
// Reuse the public guide's allowlist and media hash checks. Validate a new
// package before replacing generated output, keeping failed rebuilds harmless.
import { spawnSync } from 'node:child_process';
import { mkdtempSync, renameSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const publicDir = join(here, '../public');
const out = join(publicDir, 'user-guide');
const staging = mkdtempSync(join(publicDir, '.user-guide-build-'));

try {
  const site = join(staging, 'site');
  const args = ['-I', join(here, '../../scripts/docs/build_user_guide.py'), '--output', site];
  if (process.env.TTOBAK_GUIDE_SCREENSHOT_ARCHIVE) {
    args.push('--screenshot-archive', process.env.TTOBAK_GUIDE_SCREENSHOT_ARCHIVE);
  }
  const run = spawnSync('python3', args, { stdio: 'inherit' });
  if (run.error) {
    console.error(`Guide packager failed to start: ${run.error.message}`);
    process.exitCode = 1;
  } else if (run.status !== 0) {
    process.exitCode = run.status ?? 1;
  } else {
    rmSync(out, { recursive: true, force: true });
    renameSync(site, out);
    console.log('Packaged the verified user guide in public/user-guide/');
  }
} finally {
  rmSync(staging, { recursive: true, force: true });
}
