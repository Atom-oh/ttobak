// `ttobak-mcp report run`: the owner's SA performance report, generated on the
// Mac by the owner's own Claude CLI with the unchanged sa-performance-report
// skill, validated here and uploaded to the owner-private report route.
// stdout gets exactly one JSON line; stderr gets Korean progress and tool names.

import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { deliveryFailure } from '../intake/batch.js';
import { IntakeError } from '../intake/errors.js';
import { signIn, type IntakeEnvironment, type Selection } from '../intake/run.js';
import { IntakeStore } from '../intake/store.js';
import { MIN_CLAUDE_VERSION, SAFE_PATH, claudeArgs, claudeProjectDir, claudeVersion, findClaude, findSkill, homePythonPrefix,
  reportEnv, reportMcpConfig, reportSettings, runClaude, sandboxSupported, versionAtLeast } from './claude.js';
import { ReportError, reportCode } from './errors.js';
import { reportPrompt } from './prompt.js';
import { REPORT_JSON_SCHEMA, periodFor, validateHighlights, validateReport, type Highlights, type Period } from './schema.js';
import { RUN_DIR_PREFIX, sweepStaleRuns } from './sweep.js';
import { TOOL_SERVERS, allowedTools } from './tools.js';

/** Init-event MCP states that mean the source was not read; `pending` may still connect. */
const UNAVAILABLE = ['failed', 'needs-auth', 'disabled'];
export const REPORT_TIMEOUT_MS = 45 * 60 * 1000;
const MAX_HIGHLIGHTS_FILE_BYTES = 64 * 1024;
const ALIAS = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const ID = /^[A-Za-z0-9]{15,18}$/;
const MAX_MISSING_SOURCES = 20;

export interface ReportEnvironment extends IntakeEnvironment {
  /** Test seam: the run timeout (default 45 minutes). */
  reportTimeoutMs?: number;
  /** Cancels the run (the CLI wires SIGINT/SIGTERM to it). */
  signal?: AbortSignal;
  /** Test seam: whether this platform can run Claude Code's sandbox. */
  sandboxSupported?: () => boolean;
  /** Test seam: where the run directory is created (default `os.tmpdir()`). */
  tempRoot?: string;
  /** Test seam: temp roots swept for stale run directories (default the temp root and `/tmp`). */
  sweepRoots?: string[];
}

const USAGE = `Usage:
  ttobak-mcp report run --period week|quarter|year|custom [--start YYYY-MM-DD --end YYYY-MM-DD]
    [--highlights-file <path>] [--exclude-slack-channel <name>]... [--noninteractive]
  ttobak-mcp report slack-channels [--days 1-180]`;
const CHANNEL = /^[\p{Ll}\p{Lo}\p{Nd}][\p{Ll}\p{Lo}\p{Nd}._-]{0,79}$/u;
const MAX_EXCLUDED_CHANNELS = 100;

function flags(args: string[]): Record<string, string | true> & { excluded: string[] } {
  const allowed: Record<string, 'value' | 'flag'> = { '--period': 'value', '--start': 'value', '--end': 'value',
    '--highlights-file': 'value', '--noninteractive': 'flag' };
  const result: Record<string, string | true> = {};
  const excluded: string[] = [];
  for (let index = 0; index < args.length; index++) {
    const name = args[index];
    if (name === '--exclude-slack-channel') {
      const channel = args[++index];
      if (channel === undefined || !CHANNEL.test(channel) || excluded.length >= MAX_EXCLUDED_CHANNELS) {
        throw new ReportError('INVALID_ARGUMENT', '--exclude-slack-channel needs a Slack channel name');
      }
      if (!excluded.includes(channel)) excluded.push(channel);
      continue;
    }
    const kind = allowed[name];
    if (!kind || name in result) throw new ReportError('INVALID_ARGUMENT', `unexpected argument ${JSON.stringify(name.slice(0, 40))}`);
    if (kind === 'flag') result[name] = true;
    else {
      const value = args[++index];
      if (value === undefined || value.startsWith('--')) throw new ReportError('INVALID_ARGUMENT', `${name} needs a value`);
      result[name] = value;
    }
  }
  if (typeof result['--period'] !== 'string') throw new ReportError('INVALID_ARGUMENT', '--period is required');
  return Object.assign(result, { excluded });
}

function today(now: Date, zone: string): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }).format(now);
}

function readHighlights(path: string): Highlights {
  let text: string;
  try {
    const stat = statSync(path);
    if (!stat.isFile() || stat.size > MAX_HIGHLIGHTS_FILE_BYTES) throw new Error('rejected');
    text = readFileSync(path, 'utf8');
  } catch { throw new ReportError('INVALID_ARGUMENT', 'highlights file is unreadable'); }
  let value: unknown;
  try { value = JSON.parse(text); } catch { throw new ReportError('INVALID_ARGUMENT', 'highlights file is not JSON'); }
  return validateHighlights(value);
}

/** A literal Python `re` pattern of a name; whitespace runs match any spacing, as the skill's example does. */
const namePattern = (value: string) => value.replace(/[.*+?^${}()|[\]\\/-]/g, '\\$&').replace(/\s+/g, '\\s*');

/** The skill's example defaults (`partner_domains`, `internal_team_meeting_regex`), if readable. */
const DEFAULT_PARTNER_DOMAINS = ['anthropic.com', 'openai.com', 'github.com', 'mongodb.com', 'megazone.com', 'mz.co.kr'];
function exampleDefaults(skillDir: string): { partner_domains: string[]; internal_team_meeting_regex: string } {
  try {
    const path = join(skillDir, 'config', 'customers.example.json');
    if (statSync(path).size <= MAX_HIGHLIGHTS_FILE_BYTES) {
      const example = JSON.parse(readFileSync(path, 'utf8')) as Record<string, unknown>;
      const domains = example.partner_domains;
      if (Array.isArray(domains) && domains.every(item => typeof item === 'string' && item.length <= 253) && domains.length <= 200) {
        return { partner_domains: domains as string[],
          internal_team_meeting_regex: typeof example.internal_team_meeting_regex === 'string' ? example.internal_team_meeting_regex : '' };
      }
    }
  } catch { /* defaults below */ }
  return { partner_domains: DEFAULT_PARTNER_DOMAINS, internal_team_meeting_regex: '' };
}

/** `[display name, regex]` pairs of the owner's synced SFDC assignments. */
async function customers(api: { performanceMappings(): Promise<unknown> }): Promise<[string, string][]> {
  let value: unknown;
  try { value = await api.performanceMappings(); } catch (error) {
    if (deliveryFailure(error).kind === 'login') throw new ReportError('TTOBAK_LOGIN_REQUIRED');
    throw new ReportError('TTOBAK_UNAVAILABLE', 'performance assignments could not be read');
  }
  const items = value && typeof value === 'object' && Array.isArray((value as { items?: unknown }).items)
    ? (value as { items: unknown[] }).items : null;
  if (!items) throw new ReportError('TTOBAK_UNAVAILABLE', 'performance assignments have an unexpected shape');
  const names = new Map<string, [string, string]>();
  for (const item of items) {
    const entry = item as { sfdcAccountId?: unknown; name?: unknown };
    if (!entry || typeof entry.sfdcAccountId !== 'string' || !ID.test(entry.sfdcAccountId) || typeof entry.name !== 'string') continue;
    const name = entry.name.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, 200);
    if (name && !names.has(name)) names.set(name, [name, namePattern(name)]);
  }
  return [...names.values()];
}

function writePrivate(path: string, data: string): void {
  writeFileSync(path, data, { mode: 0o600, flag: 'wx' });
}

async function report(args: string[], environment: ReportEnvironment, store: IntakeStore) {
  // A cancel before the spawn must not start claude: its own process group would outlive node.
  const cancelled = () => { if (environment.signal?.aborted) throw new ReportError('REPORT_CANCELLED'); };
  cancelled();
  const options = flags(args);
  const custom = { ...(typeof options['--start'] === 'string' ? { start: options['--start'] } : {}),
    ...(typeof options['--end'] === 'string' ? { end: options['--end'] } : {}) };
  const period: Period = periodFor(options['--period'] as string, today(environment.now(), environment.zone), custom,
    environment.now().getUTCFullYear());
  const highlights = typeof options['--highlights-file'] === 'string' ? readHighlights(options['--highlights-file']) : undefined;
  const progress = (line: string) => environment.stderr?.(line);
  const bin = findClaude(environment.home, environment.env);
  const skillDir = findSkill(environment.home);
  // Bash runs only sandboxed; without a sandbox there is no run (the CLI also refuses at startup).
  if (!(environment.sandboxSupported ?? sandboxSupported)()) throw new ReportError('REPORT_SANDBOX_UNAVAILABLE');
  // A CLI older than the verified schema may ignore the sandbox keys: refuse it before the run.
  const childEnv = reportEnv(environment.home, environment.env);
  const version = await claudeVersion(bin, childEnv, environment.signal);
  cancelled();
  if (!version || !versionAtLeast(version)) {
    throw new ReportError('REPORT_SANDBOX_UNAVAILABLE', `claude ${MIN_CLAUDE_VERSION.join('.')} or newer is required`);
  }

  let release: () => void;
  try { release = store.lock('report'); } catch (error) { throw new ReportError(reportCode(error) === 'RUN_IN_PROGRESS' ? 'RUN_IN_PROGRESS' : 'TTOBAK_UNAVAILABLE'); }
  let directory: string | undefined;
  let sessionDir: string | undefined;
  // Synchronous removal, also from the abort path: the app allows a cancelled run about 2 s.
  const cleanup = () => {
    if (directory) rmSync(directory, { recursive: true, force: true });
    // Raw tool results and the session transcript of this run only.
    if (sessionDir && sessionDir.includes(`-${RUN_DIR_PREFIX}`)) {
      try { rmSync(sessionDir, { recursive: true, force: true }); } catch { /* reported in finally */ }
    }
  };
  // After runClaude's own abort listener has sent claude's group SIGTERM.
  const onAbort = () => queueMicrotask(() => { try { cleanup(); } catch { /* finally retries */ } });
  const tempRoot = environment.tempRoot ?? tmpdir();
  try {
    // The lock is held, so every run directory left in a temp root is from an ended run.
    sweepStaleRuns(environment.home, environment.sweepRoots ?? [tempRoot, '/tmp']);
    progress(`리포트 기간: ${period.label}`);
    const selection = store.read<Selection>('selection.json');
    const { api, me } = await signIn(selection, environment, false, options['--noninteractive'] === true);
    cancelled();
    if (!api || !me) throw new ReportError('TTOBAK_LOGIN_REQUIRED');
    const alias = me.email.split('@')[0] ?? '';
    if (!ALIAS.test(alias)) throw new ReportError('TTOBAK_LOGIN_REQUIRED', 'the signed-in alias could not be read');
    const accounts = await customers(api);
    cancelled();
    if (!accounts.length) throw new ReportError('REPORT_NO_SFDC', 'no synced SFDC assignments');
    progress(`SFDC 담당 고객 ${accounts.length}곳을 확인했어요.`);

    directory = realpathSync(mkdtempSync(join(tempRoot, RUN_DIR_PREFIX)));
    environment.signal?.addEventListener('abort', onAbort, { once: true });
    // The CLI runs in work/; its config files stay outside, in the run directory.
    // work/ is the CLI's project settings root, so it is not writable: the run
    // writes only in work/out/ (file tools and the sandbox alike).
    const workDir = join(directory, 'work');
    mkdirSync(workDir, { mode: 0o700 });
    const outDir = join(workDir, 'out');
    mkdirSync(outDir, { mode: 0o700 });
    // Large tool results are saved beside the CLI session of the working directory; the run may read them there.
    sessionDir = claudeProjectDir(environment.home, workDir);
    for (const path of [directory, workDir, outDir, sessionDir]) {
      if (!SAFE_PATH.test(path)) throw new ReportError('INVALID_ARGUMENT', 'the run directory path has unsupported characters');
    }
    // The owner's own mapping in the skill wins; otherwise one in the
    // customers.example.json format goes into the work directory.
    const ownMapping = join(skillDir, 'config', `customers.${alias}.json`);
    let customersFile = ownMapping;
    try { if (!statSync(ownMapping).isFile()) throw new Error('not a file'); } catch {
      customersFile = join(workDir, `customers.${alias}.json`);
      writePrivate(customersFile, JSON.stringify({ customers: accounts, domain_map: {}, organizer_name_map: [],
        ...exampleDefaults(skillDir) }, null, 2) + '\n');
    }
    const mcp = reportMcpConfig(environment.home);
    const mcpConfigPath = join(directory, 'mcp.json');
    writePrivate(mcpConfigPath, JSON.stringify(mcp.config));
    const settingsPath = join(directory, 'settings.json');
    writePrivate(settingsPath, JSON.stringify(reportSettings({ home: environment.home, workDir, outDir, sessionDir, skillDir,
      privateFiles: [mcpConfigPath, settingsPath], pythonPrefix: homePythonPrefix(environment.home, childEnv.PATH) })));
    if (mcp.missing.length) progress(`설정에 없는 소스: ${mcp.missing.join(', ')}`);

    const argv = claudeArgs({
      prompt: reportPrompt({ skillDir, outDir, alias, period, customersFile, ...(highlights ? { highlights } : {}),
        excludedChannels: options.excluded }),
      schema: REPORT_JSON_SCHEMA, mcpConfigPath, settingsPath, skillDir, workDir,
      allowedTools: allowedTools({ skillDir, outDir, readDirs: [skillDir, workDir, sessionDir], servers: mcp.present }),
    });
    cancelled();
    progress('Claude Code로 리포트를 만드는 중이에요.');
    const outcome = await runClaude(bin, argv, { cwd: workDir, env: childEnv,
      timeoutMs: environment.reportTimeoutMs ?? REPORT_TIMEOUT_MS, signal: environment.signal,
      onTool: name => progress(`도구 호출: ${name}`) });
    if (outcome.timedOut) throw new ReportError('REPORT_TIMEOUT');
    if (outcome.cancelled) throw new ReportError('REPORT_CANCELLED');
    if (outcome.sandboxUnavailable) throw new ReportError('REPORT_SANDBOX_UNAVAILABLE');
    if (outcome.authFailed) throw new ReportError('CLAUDE_CLI_AUTH');
    const result = outcome.result;
    // A Bedrock request that never answers (no or wrong AWS profile) ends as a timed-out error result.
    if (result?.is_error === true && childEnv.CLAUDE_CODE_USE_BEDROCK === '1' && typeof result.result === 'string'
      && /timed out/i.test(result.result)) throw new ReportError('CLAUDE_CLI_BEDROCK', 'the Bedrock request timed out');
    if (!result || result.subtype !== 'success' || result.is_error === true) throw new ReportError('REPORT_INVALID', 'no successful result');
    let content: unknown = result.structured_output;
    if (content === undefined && typeof result.result === 'string') {
      // Only a result that is entirely one JSON object; prose around it is invalid.
      try { content = JSON.parse(result.result); } catch { content = undefined; }
    }
    if (!content || typeof content !== 'object' || Array.isArray(content)) throw new ReportError('REPORT_INVALID', 'no JSON object');

    progress('리포트를 검증하고 있어요.');
    // Servers absent from the config or failed at start, in server order, after the model's own gaps.
    const failed = outcome.servers.filter(server => UNAVAILABLE.includes(server.status) && mcp.present.includes(server.name))
      .map(server => server.name);
    const serverMissing = TOOL_SERVERS.filter(name => mcp.missing.includes(name) || failed.includes(name));
    const { schemaVersion: _v, periodKey: _k, period: _p, alias: _a, generatedAt: _g, model: _m, highlights: _h,
      missingSources: modelMissing, ...modelContent } = content as Record<string, unknown>;
    if (typeof modelContent.partial !== 'boolean') throw new ReportError('REPORT_INVALID', 'partial');
    if (modelMissing !== undefined && !Array.isArray(modelMissing)) throw new ReportError('REPORT_INVALID', 'missingSources');
    // The model's own gaps first; the server gaps always fit within the cap of 20.
    const missingSources = [...new Set([...((modelMissing as unknown[] | undefined) ?? [])
      .slice(0, MAX_MISSING_SOURCES - serverMissing.length), ...serverMissing])];
    const candidate = {
      ...modelContent,
      schemaVersion: 1, periodKey: period.periodKey,
      period: { start: period.start, end: period.end, label: period.label },
      alias, generatedAt: environment.now().toISOString(), model: outcome.model ?? 'unknown',
      partial: modelContent.partial === true || missingSources.length > 0,
      missingSources,
      ...(highlights ? { highlights } : {}),
    };
    const validated = validateReport(period.periodKey, candidate);
    if (mentionsChannel(validated, options.excluded)) throw new ReportError('REPORT_EXCLUDED_CHANNEL');

    try { await api.putPerformanceReport(period.periodKey, validated); } catch (error) {
      const failure = deliveryFailure(error);
      if (failure.kind === 'login') throw new ReportError('TTOBAK_LOGIN_REQUIRED');
      if (failure.kind === 'rejected' && failure.status === 400) throw new ReportError('REPORT_INVALID', 'rejected by TTOBAK');
      throw new ReportError('TTOBAK_UNAVAILABLE', 'the report upload was not confirmed');
    }
    progress('리포트를 저장했어요.');
    return { ok: true, periodKey: period.periodKey, partial: validated.partial, missingSources: validated.missingSources ?? [] };
  } finally {
    environment.signal?.removeEventListener('abort', onAbort);
    cleanup();
    if (sessionDir && existsSync(sessionDir)) environment.stderr?.('경고: 이번 실행의 Claude 세션 폴더를 지우지 못했어요. ~/.claude/projects 에서 직접 지우세요.');
    release();
  }
}

/** A string equal to an excluded channel name or containing `#<name>` as a whole name. */
function mentionsChannel(value: unknown, excluded: string[]): boolean {
  if (!excluded.length) return false;
  const patterns = excluded.map(name => new RegExp(`#${name.replace(/[.]/g, '\\.')}(?![\\p{L}\\p{N}._-])`, 'u'));
  const visit = (item: unknown): boolean => {
    if (typeof item === 'string') return excluded.includes(item.trim()) || patterns.some(pattern => pattern.test(item));
    if (Array.isArray(item)) return item.some(visit);
    return !!item && typeof item === 'object' && Object.values(item).some(visit);
  };
  return visit(value);
}

export async function runReport(args: string[], environment: ReportEnvironment): Promise<number> {
  const store = new IntakeStore(environment.home);
  const [command, ...rest] = args;
  if (command === 'slack-channels') {
    const { slackChannels } = await import('./slack-channels.js');
    try {
      environment.stdout(JSON.stringify(await slackChannels(rest, environment, store)));
      return 0;
    } catch (error) {
      const code = error instanceof IntakeError ? error.code : reportCode(error);
      environment.stdout(JSON.stringify({ ok: false, code, message: error instanceof Error ? error.message.slice(0, 200) : code }));
      return 2;
    }
  }
  if (command !== 'run') {
    environment.stdout(USAGE);
    return command === '--help' || command === 'help' ? 0 : 2;
  }
  try {
    environment.stdout(JSON.stringify(await report(rest, environment, store)));
    return 0;
  } catch (error) {
    const code = reportCode(error);
    const message = error instanceof ReportError ? error.message : code;
    environment.stdout(JSON.stringify({ ok: false, code, message }));
    return 2;
  }
}
