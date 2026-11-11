// The owner's Claude Code CLI as the report runner: binary and skill lookup,
// the temporary MCP config, the minimal environment, the argv, and parsing of
// `--output-format stream-json`. Host configuration values are copied into the
// 0600 config file only; they are never printed or logged (ADR-003, ADR-023).

import { execFile, spawn } from 'node:child_process';
import { accessSync, constants, existsSync, lstatSync, readFileSync, readdirSync, realpathSync, statSync } from 'node:fs';
import { userInfo } from 'node:os';
import { delimiter, dirname, isAbsolute, join, relative, sep } from 'node:path';
import { ReportError } from './errors.js';
import { TOOL_SERVERS, deniedTools } from './tools.js';

const SKILL = 'sa-performance-report';
const MAX_CONFIG_BYTES = 16 * 1024 * 1024;
/** One stream-json line; larger lines (big tool results) are skipped, never buffered. */
const MAX_LINE_BYTES = 8 * 1024 * 1024;
const MAX_STDERR_BYTES = 8 * 1024;
const KILL_GRACE_MS = 5_000;
/** A cancel (the app's SIGTERM, which allows about 2 s) SIGKILLs claude's group sooner. */
const ABORT_KILL_GRACE_MS = 1_500;
/** Absolute paths that are safe inside permission rules and a comma-joined list. */
export const SAFE_PATH = /^\/[A-Za-z0-9_./@+-]+$/;
const TOOL_NAME = /^[A-Za-z0-9_.:-]{1,128}$/;

function executable(path: string): boolean {
  try {
    accessSync(path, constants.X_OK);
    return statSync(path).isFile();
  } catch { return false; }
}

/** `$TTOBAK_CLAUDE_BIN` (exclusive when set), then the usual install locations. */
export function findClaude(home: string, env: NodeJS.ProcessEnv): string {
  if (env.TTOBAK_CLAUDE_BIN) {
    if (env.TTOBAK_CLAUDE_BIN.startsWith('/') && executable(env.TTOBAK_CLAUDE_BIN)) return env.TTOBAK_CLAUDE_BIN;
    throw new ReportError('CLAUDE_CLI_MISSING', 'TTOBAK_CLAUDE_BIN is not an executable file');
  }
  for (const candidate of [join(home, '.local/bin/claude'), '/opt/homebrew/bin/claude', '/usr/local/bin/claude',
    join(home, '.claude/local/claude')]) {
    if (executable(candidate)) return candidate;
  }
  throw new ReportError('CLAUDE_CLI_MISSING', 'claude was not found');
}

function skillAt(directory: string): { path: string; mtime: number } | null {
  try {
    const file = join(directory, 'SKILL.md');
    if (!statSync(file).isFile()) return null;
    const path = realpathSync(directory);
    return SAFE_PATH.test(path) ? { path, mtime: statSync(file).mtimeMs } : null;
  } catch { return null; }
}

/** Plugin cache directories `<...>/skills/sa-performance-report` within a few levels. */
function pluginSkills(root: string, depth: number, found: { path: string; mtime: number }[]): void {
  if (depth < 0) return;
  let entries: string[];
  try { entries = readdirSync(root); } catch { return; }
  for (const name of entries) {
    const path = join(root, name);
    try { if (!lstatSync(path).isDirectory()) continue; } catch { continue; }
    if (name === 'skills') {
      const skill = skillAt(join(path, SKILL));
      if (skill) found.push(skill);
      continue;
    }
    if (name !== 'node_modules' && !name.startsWith('.')) pluginSkills(path, depth - 1, found);
  }
}

/** Files that mark the v1.4.0 layout (internal-mcp-setup, 2026-11-07) the run is built for. */
export const SKILL_LAYOUT = ['scripts/calendar_count.py', 'references/report-structure.md'] as const;

/** The user's skill, else the newest plugin cache copy; an older layout fails before any run. */
export function findSkill(home: string): string {
  let path = skillAt(join(home, '.claude', 'skills', SKILL))?.path;
  if (!path) {
    const found: { path: string; mtime: number }[] = [];
    pluginSkills(join(home, '.claude', 'plugins', 'cache'), 5, found);
    found.sort((a, b) => b.mtime - a.mtime);
    path = found[0]?.path;
  }
  if (!path) throw new ReportError('SKILL_MISSING', `${SKILL} is not installed`);
  for (const file of SKILL_LAYOUT) {
    try { if (statSync(join(path, file)).isFile()) continue; } catch { /* falls through */ }
    throw new ReportError('SKILL_OUTDATED', `${SKILL} lacks ${file}; update it with the internal-mcp-setup v1.4.0 installer`);
  }
  return path;
}

/** The Claude CLI's session directory of a working directory (tool-result files land there). */
export function claudeProjectDir(home: string, cwd: string): string {
  return join(home, '.claude', 'projects', cwd.replace(/[^a-zA-Z0-9]/g, '-'));
}

/**
 * The report servers with allowed tools present in `~/.claude.json` top-level
 * `mcpServers`, copied unchanged; `missing` lists the absent ones.
 */
export function reportMcpConfig(home: string): { config: { mcpServers: Record<string, unknown> }; present: string[]; missing: string[] } {
  let servers: Record<string, unknown> = {};
  try {
    const path = join(home, '.claude.json');
    if (statSync(path).size <= MAX_CONFIG_BYTES) {
      const parsed = JSON.parse(readFileSync(path, 'utf8')) as { mcpServers?: unknown };
      if (parsed && typeof parsed.mcpServers === 'object' && parsed.mcpServers && !Array.isArray(parsed.mcpServers)) {
        servers = parsed.mcpServers as Record<string, unknown>;
      }
    }
  } catch { /* an unreadable config means no servers; never echo it */ }
  const mcpServers: Record<string, unknown> = {};
  for (const name of TOOL_SERVERS) {
    const entry = Object.hasOwn(servers, name) ? servers[name] : undefined;
    if (entry && typeof entry === 'object' && !Array.isArray(entry)) mcpServers[name] = entry;
  }
  const present = Object.keys(mcpServers);
  return { config: { mcpServers }, present, missing: TOOL_SERVERS.filter(name => !present.includes(name)) };
}

/** Model-auth keys carried over from the user's `~/.claude/settings.json` `env`. */
export const AUTH_ENV_KEYS = ['CLAUDE_CODE_USE_BEDROCK', 'CLAUDE_CODE_USE_VERTEX', 'AWS_PROFILE', 'AWS_REGION',
  'ANTHROPIC_BASE_URL', 'ANTHROPIC_MODEL', 'ANTHROPIC_DEFAULT_OPUS_MODEL', 'ANTHROPIC_DEFAULT_SONNET_MODEL',
  'ANTHROPIC_DEFAULT_HAIKU_MODEL'] as const;

const ENV_VALUE = /^[\x20-\x7e]{1,512}$/;

/**
 * HOME, PATH, LANG, USER, LOGNAME (a Keychain login needs them) and the allowlisted model-auth keys. User settings are not
 * a setting source of the run, so their auth keys are carried explicitly. The
 * app's `TTOBAK_REPORT_USE_BEDROCK` (1/0) and `TTOBAK_REPORT_AWS_PROFILE` win.
 */
export function reportEnv(home: string, env: NodeJS.ProcessEnv): Record<string, string> {
  const result: Record<string, string> = { HOME: home, PATH: env.PATH || '/usr/bin:/bin:/usr/sbin:/sbin',
    LANG: env.LANG && ENV_VALUE.test(env.LANG) ? env.LANG : 'en_US.UTF-8' };
  let user = env.USER;
  if (!user || !ENV_VALUE.test(user)) { try { user = userInfo().username; } catch { user = undefined; } }
  if (user && ENV_VALUE.test(user)) {
    result.USER = user;
    result.LOGNAME = env.LOGNAME && ENV_VALUE.test(env.LOGNAME) ? env.LOGNAME : user;
  }
  try {
    const path = join(home, '.claude', 'settings.json');
    if (statSync(path).size <= MAX_CONFIG_BYTES) {
      const settings = JSON.parse(readFileSync(path, 'utf8')) as { env?: Record<string, unknown> };
      const source = settings && typeof settings.env === 'object' && settings.env ? settings.env : {};
      for (const key of AUTH_ENV_KEYS) {
        const value = Object.hasOwn(source, key) ? source[key] : undefined;
        if (typeof value === 'string' && ENV_VALUE.test(value)) result[key] = value;
      }
    }
  } catch { /* no user settings: the CLI's own login decides */ }
  if (env.TTOBAK_REPORT_USE_BEDROCK === '1') result.CLAUDE_CODE_USE_BEDROCK = '1';
  else if (env.TTOBAK_REPORT_USE_BEDROCK === '0') delete result.CLAUDE_CODE_USE_BEDROCK;
  if (env.TTOBAK_REPORT_AWS_PROFILE && ENV_VALUE.test(env.TTOBAK_REPORT_AWS_PROFILE)) result.AWS_PROFILE = env.TTOBAK_REPORT_AWS_PROFILE;
  return result;
}

/**
 * Lowest Claude Code version whose bundled settings schema was checked to
 * have every sandbox key `reportSettings` relies on (`sandbox.enabled`,
 * `failIfUnavailable`, `allowUnsandboxedCommands`, `autoAllowBashIfSandboxed`,
 * `network.allowedDomains`, `filesystem.allowWrite/denyWrite/denyRead/allowRead`).
 * An older CLI may ignore unknown keys and run Bash unsandboxed, so it is refused.
 */
export const MIN_CLAUDE_VERSION = [2, 1, 322] as const;
const VERSION_TIMEOUT_MS = 10_000;

/** The `claude --version` semver, or null when it cannot be read within 10 seconds. */
export function claudeVersion(bin: string, env: Record<string, string>, signal?: AbortSignal): Promise<[number, number, number] | null> {
  return new Promise(resolve => {
    if (signal?.aborted) { resolve(null); return; }
    execFile(bin, ['--version'], { env, timeout: VERSION_TIMEOUT_MS, maxBuffer: 4096, killSignal: 'SIGKILL', signal }, (error, stdout) => {
      const match = error ? null : /^\s*(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?![\d.-])/.exec(String(stdout));
      resolve(match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null);
    });
  });
}

export function versionAtLeast(version: readonly number[], minimum: readonly number[] = MIN_CLAUDE_VERSION): boolean {
  for (let index = 0; index < 3; index++) {
    if (version[index] !== minimum[index]) return version[index] > minimum[index];
  }
  return true;
}

/** Home folders of Python version managers, re-allowed whole because their shims read their own tree. */
const PYTHON_MANAGERS = ['.pyenv', '.asdf', '.rye', '.mise', '.conda', 'miniconda3', 'anaconda3', 'miniforge3'];

/**
 * The read-allowed install of the first `python3` on PATH when it lives under
 * HOME (pyenv, asdf, ...): a manager folder whole, else the prefix above `bin`.
 * `~/.local` is shared with unrelated tools, so there only `bin` and
 * `lib/python3*` are re-allowed. Empty when it is outside HOME (no re-allow
 * needed) or absent.
 */
export function homePythonPrefix(home: string, path: string): string[] {
  const inHome = (target: string) => {
    const rest = relative(home, target);
    return rest !== '' && !rest.startsWith('..') && !isAbsolute(rest) ? rest : null;
  };
  for (const directory of path.split(delimiter)) {
    if (!directory.startsWith('/')) continue;
    const candidate = join(directory, 'python3');
    if (!executable(candidate)) continue;
    let real: string;
    try { real = realpathSync(candidate); } catch { return []; }
    for (const rest of [inHome(candidate), inHome(real)]) {
      const top = rest?.split(sep)[0];
      if (top && PYTHON_MANAGERS.includes(top)) return [join(home, top)];
    }
    // An install under HOME: the prefix above its bin directory.
    if (inHome(real)) {
      const prefix = dirname(dirname(real));
      if (!inHome(prefix)) return [];
      if (prefix === join(home, '.local')) {
        // A shared prefix: only its bin and python library folders.
        let libs: string[] = [];
        try { libs = readdirSync(join(prefix, 'lib')).filter(name => /^python3/.test(name)).map(name => join(prefix, 'lib', name)); } catch { /* no lib */ }
        return [join(prefix, 'bin'), ...libs];
      }
      return [prefix];
    }
    // A link under HOME to an install outside it: only the link's directory.
    return inHome(candidate) ? [dirname(candidate)] : [];
  }
  return [];
}

/**
 * Run-specific settings for `--settings`. Bash runs only inside Claude Code's
 * sandbox: the CLI exits at startup when the sandbox cannot start
 * (`failIfUnavailable`), ignores `dangerouslyDisableSandbox`
 * (`allowUnsandboxedCommands: false`) and still applies the allowlist to
 * sandboxed commands (`autoAllowBashIfSandboxed: false`). Sandboxed commands
 * reach no network and write only the output directory, never the skill. They
 * cannot read HOME except the skill, this run's session folder and a
 * home-installed python3, nor the run's own config files.
 */
export function reportSettings(options: { home: string; workDir: string; outDir: string; sessionDir: string; skillDir: string;
  privateFiles: string[]; pythonPrefix?: string[] }) {
  return {
    sandbox: {
      enabled: true,
      failIfUnavailable: true,
      autoAllowBashIfSandboxed: false,
      allowUnsandboxedCommands: false,
      network: { allowedDomains: [] as string[] },
      filesystem: {
        // Not the working directory itself: its .claude/ is a settings source of the run.
        allowWrite: [options.outDir],
        // The working directory stays in the default write scope, so its .claude/ (a settings source) is denied outright.
        denyWrite: [options.skillDir, `${options.workDir}/.claude`],
        denyRead: [options.home, ...options.privateFiles],
        // The work directory is readable even when a TMPDIR under HOME falls inside the HOME deny.
        allowRead: [options.skillDir, options.sessionDir, options.workDir, ...(options.pythonPrefix ?? [])],
      },
    },
  };
}

/**
 * Whether this platform can run Claude Code's sandbox: Seatbelt
 * (`/usr/bin/sandbox-exec`) on macOS, bubblewrap on Linux. The CLI's own
 * `failIfUnavailable` remains the enforcing check.
 */
export function sandboxSupported(platform: NodeJS.Platform = process.platform, path = process.env.PATH ?? ''): boolean {
  if (platform === 'darwin') return existsSync('/usr/bin/sandbox-exec');
  if (platform === 'linux') return path.split(':').some(directory => directory.startsWith('/') && executable(join(directory, 'bwrap')));
  return false;
}

export interface ClaudeArgs { prompt: string; schema: object; mcpConfigPath: string; settingsPath: string; allowedTools: string[]; skillDir: string;
  workDir: string }

/**
 * Headless, read-only argv. No user settings (their allow rules would admit
 * SFDC writes), only the given MCP config, an explicit allowlist and denial of
 * everything else, Bash only in the sandbox (`--settings`), and no skills or
 * slash commands. The session is persisted
 * because large tool results are saved as files beside it; the runner removes
 * that session directory after the run.
 */
export function claudeArgs(options: ClaudeArgs): string[] {
  return [
    '-p', options.prompt,
    '--output-format', 'stream-json', '--verbose',
    '--json-schema', JSON.stringify(options.schema),
    '--permission-mode', 'dontAsk',
    '--setting-sources', 'project,local',
    '--settings', options.settingsPath,
    '--strict-mcp-config', '--mcp-config', options.mcpConfigPath,
    '--allowedTools', options.allowedTools.join(','),
    '--disallowedTools', deniedTools(options.skillDir, options.workDir).join(','),
    '--add-dir', options.skillDir,
    '--disable-slash-commands',
  ];
}

export interface ClaudeOutcome {
  /** The last `result` event, if any. */
  result?: Record<string, unknown>;
  /** Model named by the init event. */
  model?: string;
  /** MCP server status names from the init event. */
  servers: { name: string; status: string }[];
  /** An authentication failure was reported in the stream or on stderr. */
  authFailed: boolean;
  /** The CLI refused to start (or to run a command) because the sandbox is unavailable. */
  sandboxUnavailable: boolean;
  timedOut: boolean;
  cancelled: boolean;
  exitCode: number | null;
}

const SANDBOX_FAILURE = /sandbox required but unavailable|sandbox_required_unavailable|sandbox_exec_lost_and_required|sandbox\.failIfUnavailable/i;
const AUTH_FAILURE = /not logged in|authentication_failed|invalid api key|please run \/login|api error: 401|oauth token (has )?expired/i;

function textOf(value: unknown): string {
  return typeof value === 'string' ? value.slice(0, 4096) : '';
}

/**
 * Runs the CLI in its own process group and reads stream-json. `onTool` gets
 * tool names only, and only when they are plain identifiers.
 */
export function runClaude(bin: string, args: string[], options: { cwd: string; env: Record<string, string>; timeoutMs: number;
  onTool: (name: string) => void; signal?: AbortSignal }): Promise<ClaudeOutcome> {
  return new Promise((resolve, reject) => {
    const outcome: ClaudeOutcome = { servers: [], authFailed: false, sandboxUnavailable: false, timedOut: false, cancelled: false, exitCode: null };
    // Cancelled before the spawn: claude never starts (its own group would outlive node).
    if (options.signal?.aborted) { resolve({ ...outcome, cancelled: true }); return; }
    const child = spawn(bin, args, { cwd: options.cwd, env: options.env, stdio: ['ignore', 'pipe', 'pipe'], detached: true });
    let pending: Buffer[] = [];
    let pendingBytes = 0;
    let skipping = false;
    let stderr = '';
    let settled = false;
    let killTimer: NodeJS.Timeout | undefined;
    const killGroup = (signal: NodeJS.Signals) => {
      try { if (child.pid) process.kill(-child.pid, signal); } catch { /* already gone */ }
    };
    // SIGTERM once; a later, shorter grace (a cancel after a timeout) replaces the pending SIGKILL.
    const stop = (grace = KILL_GRACE_MS) => {
      if (killTimer) clearTimeout(killTimer);
      else killGroup('SIGTERM');
      killTimer = setTimeout(() => killGroup('SIGKILL'), grace);
      killTimer.unref();
    };
    const timer = setTimeout(() => { outcome.timedOut = true; stop(); }, options.timeoutMs);
    const onAbort = () => { outcome.cancelled = true; clearTimeout(timer); stop(ABORT_KILL_GRACE_MS); };
    options.signal?.addEventListener('abort', onAbort, { once: true });

    const handle = (line: string) => {
      let event: Record<string, unknown>;
      try { event = JSON.parse(line); } catch { return; }
      if (!event || typeof event !== 'object') return;
      if (event.type === 'system' && event.subtype === 'init') {
        if (typeof event.model === 'string') outcome.model = event.model.slice(0, 200);
        if (Array.isArray(event.mcp_servers)) {
          outcome.servers = event.mcp_servers.flatMap(item => item && typeof item.name === 'string' && typeof item.status === 'string'
            ? [{ name: item.name, status: item.status }] : []);
        }
      } else if (event.type === 'assistant') {
        if (event.error === 'authentication_failed') outcome.authFailed = true;
        const message = event.message as { content?: unknown } | undefined;
        if (message && Array.isArray(message.content)) {
          for (const block of message.content) {
            if (block && block.type === 'tool_use') onToolName(block.name);
          }
        }
      } else if (event.type === 'result') {
        outcome.result = event;
        if (event.is_error === true && AUTH_FAILURE.test(textOf(event.result))) outcome.authFailed = true;
        if (SANDBOX_FAILURE.test(textOf(event.result)) || SANDBOX_FAILURE.test(textOf(event.error))) outcome.sandboxUnavailable = true;
      }
    };
    const onToolName = (name: unknown) => {
      if (typeof name === 'string' && TOOL_NAME.test(name)) options.onTool(name);
    };
    child.stdout.on('data', (chunk: Buffer) => {
      let start = 0;
      for (let index = chunk.indexOf(10); index !== -1; index = chunk.indexOf(10, start)) {
        if (!skipping) {
          pending.push(chunk.subarray(start, index));
          handle(Buffer.concat(pending).toString('utf8'));
        }
        pending = [];
        pendingBytes = 0;
        skipping = false;
        start = index + 1;
      }
      if (start < chunk.length && !skipping) {
        pendingBytes += chunk.length - start;
        if (pendingBytes > MAX_LINE_BYTES) { skipping = true; pending = []; pendingBytes = 0; }
        else pending.push(chunk.subarray(start));
      }
    });
    child.stderr.on('data', (chunk: Buffer) => {
      if (stderr.length < MAX_STDERR_BYTES) stderr += chunk.toString('utf8').slice(0, MAX_STDERR_BYTES - stderr.length);
    });
    const finish = (error?: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      options.signal?.removeEventListener('abort', onAbort);
      if (error) {
        if (killTimer) clearTimeout(killTimer);
        reject(error);
        return;
      }
      if (!skipping && pending.length) handle(Buffer.concat(pending).toString('utf8'));
      if (AUTH_FAILURE.test(stderr)) outcome.authFailed = true;
      if (SANDBOX_FAILURE.test(stderr)) outcome.sandboxUnavailable = true;
      // Anything left in the group (an MCP server, a sub-agent) goes with the run.
      killGroup('SIGKILL');
      if (killTimer) clearTimeout(killTimer);
      resolve(outcome);
    };
    child.on('error', error => {
      const code = (error as NodeJS.ErrnoException).code;
      finish(code === 'ENOENT' || code === 'EACCES' ? new ReportError('CLAUDE_CLI_MISSING', 'claude could not start') : error);
    });
    child.on('close', code => { outcome.exitCode = code; finish(); });
  });
}
