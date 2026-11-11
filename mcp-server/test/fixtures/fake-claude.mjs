// Fake `claude` CLI for report tests. A per-test wrapper script runs it as
// `node fake-claude.mjs <scenario.json> <claude argv...>`. It records what it
// was started with (argv, cwd, env, the MCP config and its mode, the files in
// its working directory), then prints the scenario's stream-json events.
import { mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';

const [scenarioPath, ...argv] = process.argv.slice(2);
const scenario = JSON.parse(readFileSync(scenarioPath, 'utf8'));
// The version probe the runner makes before the real run.
if (argv[0] === '--version') {
  if (scenario.versionSleepMs) await new Promise(resolve => setTimeout(resolve, scenario.versionSleepMs));
  process.stdout.write(`${scenario.version ?? '2.1.322 (Claude Code)'}\n`);
  process.exit(0);
}
const configIndex = argv.indexOf('--mcp-config');
let mcpConfig = null;
let mcpConfigMode = null;
if (configIndex >= 0) {
  mcpConfig = JSON.parse(readFileSync(argv[configIndex + 1], 'utf8'));
  mcpConfigMode = statSync(argv[configIndex + 1]).mode & 0o777;
}
const settingsIndex = argv.indexOf('--settings');
const settings = settingsIndex >= 0 ? JSON.parse(readFileSync(argv[settingsIndex + 1], 'utf8')) : null;
const settingsMode = settingsIndex >= 0 ? statSync(argv[settingsIndex + 1]).mode & 0o777 : null;
const cwd = process.cwd();
const files = {};
for (const name of readdirSync(cwd)) {
  const stat = statSync(`${cwd}/${name}`);
  files[name] = { mode: stat.mode & 0o777, dir: stat.isDirectory(),
    ...(stat.isFile() ? { text: readFileSync(`${cwd}/${name}`, 'utf8') } : {}) };
}
writeFileSync(scenario.record, JSON.stringify({ argv, cwd, cwdMode: statSync(cwd).mode & 0o777,
  env: process.env, mcpConfig, mcpConfigMode, settings, settingsMode, files }));
// Like the real CLI, a session directory under ~/.claude/projects named after the cwd.
const session = `${process.env.HOME}/.claude/projects/${cwd.replace(/[^a-zA-Z0-9]/g, '-')}`;
mkdirSync(`${session}/tool-results`, { recursive: true });
writeFileSync(`${session}/tool-results/result.txt`, 'TOOL_RESULT_SECRET');
for (const event of scenario.events ?? []) process.stdout.write(
  (typeof event === 'string' ? event : JSON.stringify(event)) + '\n');
if (scenario.sleepMs) await new Promise(resolve => setTimeout(resolve, scenario.sleepMs));
process.exit(scenario.exitCode ?? 0);
