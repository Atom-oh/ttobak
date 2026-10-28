#!/usr/bin/env node
// Generates public/billing-app/ (not committed) from services/billing/page with
// its build_page.py, so `next build` exports it to out/billing-app/. Runs offline;
// it needs the page's vendor sources from `npm ci` in services/billing/page.
import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const page = join(here, '../../services/billing/page');
const vendor = ['d3/dist/d3.min.js', 'pretendard/dist/web/variable'];
const missing = vendor.filter(rel => !existsSync(join(page, 'node_modules', rel)));
if (missing.length) {
  console.error(`Billing page vendor sources missing (${missing.join(', ')}).\n`
    + 'Run: npm ci --prefix services/billing/page');
  process.exit(1);
}
const out = join(here, '../public/billing-app');
const run = spawnSync('python3', ['-I', join(page, 'build_page.py'), '--out', out], { stdio: 'inherit' });
if (run.error) {
  console.error(`python3 failed to start: ${run.error.message}`);
  process.exit(1);
}
process.exit(run.status ?? 1);
