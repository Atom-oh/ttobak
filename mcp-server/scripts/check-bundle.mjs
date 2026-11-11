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
execFileSync(process.execPath, ['--test', 'test/reading.test.mjs', 'test/calendar.test.mjs', 'test/http-artifact.test.mjs'], {
  cwd, stdio: 'inherit', env: { ...process.env, TTOBAK_TEST_BUNDLE: '1' },
});
// The published artifact must run `intake`: its stdio client pulls CommonJS
// dependencies that call require().
const help = execFileSync(process.execPath, [fileURLToPath(new URL('../dist/ttobak-mcp.mjs', import.meta.url)), 'intake', '--help'], {
  cwd, encoding: 'utf8', env: { PATH: process.env.PATH ?? '' },
});
assert.match(help, /ttobak-mcp intake hosts/, 'The bundled intake command must start');
const reportHelp = execFileSync(process.execPath, [fileURLToPath(new URL('../dist/ttobak-mcp.mjs', import.meta.url)), 'report', '--help'], {
  cwd, encoding: 'utf8', env: { PATH: process.env.PATH ?? '' },
});
assert.match(reportHelp, /ttobak-mcp report run --period/, 'The bundled report command must start');
console.log('Bundle reproducibility, stdio reading/calendar, intake/report start and HTTP artifact tests passed.');
