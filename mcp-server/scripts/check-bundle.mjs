import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const cwd = fileURLToPath(new URL('../', import.meta.url));
const npm = process.platform === 'win32' ? 'npm.cmd' : 'npm';
const bundle = () => execFileSync(npm, ['run', 'bundle'], { cwd, stdio: 'inherit' });
bundle();
const first = readFileSync(new URL('../dist/ttobak-mcp.mjs', import.meta.url));
bundle();
assert.deepEqual(readFileSync(new URL('../dist/ttobak-mcp.mjs', import.meta.url)), first,
  'Repeated bundle builds must be byte-identical');
execFileSync(process.execPath, ['--test', 'test/reading.test.mjs'], {
  cwd, stdio: 'inherit', env: { ...process.env, TTOBAK_TEST_BUNDLE: '1' },
});
console.log('Bundle reproducibility and reading protocol tests passed.');
