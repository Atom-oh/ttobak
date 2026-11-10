// `ttobak-mcp intake setup`: picks the known server for each connector from the
// host lists, verifies it exactly as `intake select` does, stores the
// selections, dry-runs, optionally syncs once and optionally installs the
// LaunchAgent. Prompts and progress go to stderr in Korean; stdout keeps the
// one-JSON-line contract. Only host and server names are ever shown.

import { resolve } from 'node:path';
import type { ConnectorId } from './connectors.js';
import { IntakeError, intakeCode, type IntakeCode } from './errors.js';
import { HOSTS, listHosts, parsePathList, type HostId } from './hosts.js';
import { runConnectors, selectConnector, SYNC_CONNECTORS, type IntakeEnvironment, type RunOutput } from './run.js';
import { applySchedule, defaultLaunchctl, parseSchedule, scheduleLabel, type Schedule } from './schedule.js';
import type { IntakeStore } from './store.js';

const ORDER: readonly ConnectorId[] = ['calendar', 'sfdc', 'meetings', 'specreq', 'slack'];

/** The only server name probed per connector (ADR-065); unrelated servers never start. */
export const DEFAULT_SERVERS: Record<ConnectorId, string> = {
  calendar: 'aws-outlook-mcp',
  sfdc: 'aws-sentral-mcp',
  meetings: 'amazon-meetings-mcp',
  specreq: 'spec-req-mcp',
  slack: 'slack-mcp',
};

const LABELS: Record<ConnectorId, string> = {
  calendar: '캘린더', sfdc: 'SFDC', meetings: '회의', specreq: 'SpecReq', slack: 'Slack',
};

type ConnectorStatus = { status: 'selected'; server: string; host: HostId } | { status: 'skipped'; code: IntakeCode };

interface SetupOptions {
  yes: boolean;
  acceptUnknownSensitivity: boolean;
  autoAck: boolean;
  skip: Set<ConnectorId>;
  schedule?: Schedule;
}

function parseArgs(args: string[]): SetupOptions {
  const options: SetupOptions = { yes: false, acceptUnknownSensitivity: false, autoAck: true, skip: new Set() };
  const seen = new Set<string>();
  for (let index = 0; index < args.length; index++) {
    const name = args[index];
    if (name !== '--skip' && seen.has(name)) throw new IntakeError('INVALID_ARGUMENT', `${name} repeated`);
    seen.add(name);
    if (name === '--yes') options.yes = true;
    else if (name === '--accept-unknown-sensitivity') options.acceptUnknownSensitivity = true;
    else if (name === '--skip' || name === '--schedule' || name === '--auto-ack') {
      const value = args[++index];
      if (value === undefined || value.startsWith('--')) throw new IntakeError('INVALID_ARGUMENT', `${name} needs a value`);
      if (name === '--schedule') options.schedule = parseSchedule(value);
      else if (name === '--auto-ack') {
        if (value !== 'on' && value !== 'off') throw new IntakeError('INVALID_ARGUMENT', '--auto-ack must be on or off');
        options.autoAck = value === 'on';
      } else {
        if (!(ORDER as readonly string[]).includes(value)) throw new IntakeError('INVALID_ARGUMENT', '--skip takes a connector name');
        options.skip.add(value as ConnectorId);
      }
    } else throw new IntakeError('INVALID_ARGUMENT', `unexpected argument ${JSON.stringify(name.slice(0, 40))}`);
  }
  return options;
}

const yes = (answer: string) => /^(y|yes|예|네|ㅇ)$/i.test(answer.trim());

function countsLine(results: RunOutput['results']): string {
  const parts: string[] = [];
  const calendar = results.calendar;
  if (calendar) {
    const counts = calendar.counts as { upserts?: number; removals?: number } | undefined;
    parts.push(calendar.result === 'ok' ? `캘린더 추가/변경 ${counts?.upserts ?? 0}건, 삭제 ${counts?.removals ?? 0}건`
      : `캘린더 실패(${calendar.result})`);
  }
  const sfdc = results.sfdc;
  if (sfdc) {
    const counts = sfdc.counts as { assignments?: { upserts?: number }; tasks?: { upserts?: number } } | undefined;
    parts.push(sfdc.result === 'ok' ? `SFDC 담당 계정 ${counts?.assignments?.upserts ?? 0}건, 활동 ${counts?.tasks?.upserts ?? 0}건`
      : `SFDC 실패(${sfdc.result})`);
  }
  return parts.join(', ');
}

export async function setup(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  const options = parseArgs(args);
  const ask = environment.prompt;
  if (!options.yes && !ask) throw new IntakeError('INVALID_ARGUMENT', 'interactive setup needs a terminal; pass --yes');
  const say = (line: string) => environment.stderr?.(line);
  const question = async (text: string) => (await ask!(text)).trim();

  const connectors: Partial<Record<ConnectorId, ConnectorStatus>> = {};
  let dryRun: RunOutput['results'] = {};
  let synced = false;
  let sync: RunOutput['results'] | undefined;
  let schedule = 'none';
  const stop = (code: IntakeCode, message: string) => {
    if (code === 'AUTH_EXPIRED_MIDWAY') say('Midway 인증이 만료되었습니다. 터미널에서 `mwinit -o`를 실행한 뒤 다시 시도하세요.');
    return { ok: false, code, message, connectors, dryRun, synced, schedule, ...(sync ? { sync } : {}) };
  };

  // 1. Discover: enabled stdio servers whose command is found.
  const reports = listHosts(environment.home, parsePathList(environment.env.PATH));
  for (const connector of ORDER) {
    const label = LABELS[connector];
    const server = DEFAULT_SERVERS[connector];
    if (options.skip.has(connector)) {
      connectors[connector] = { status: 'skipped', code: 'SKIPPED_BY_USER' };
      say(`${label}: 건너뜀 (--skip)`);
      continue;
    }
    const candidates = HOSTS.filter(host => reports.find(report => report.host === host)?.servers
      .some(item => item.name === server && item.transport === 'stdio' && !item.disabled && item.commandFound));
    if (!candidates.length) {
      connectors[connector] = { status: 'skipped', code: 'SERVER_NOT_CONFIGURED' };
      say(`${label}: ${server} 서버를 찾지 못해 건너뜁니다.`);
      continue;
    }
    // 2. Calendar consent: private items are included (ADR-065/068).
    let accept = false;
    if (connector === 'calendar') {
      accept = options.acceptUnknownSensitivity || (!options.yes &&
        yes(await question('캘린더 원본은 비공개 표시를 알려주지 않아 비공개 일정도 TTOBAK 개인 캘린더로 들어갑니다. 동의할까요? [y/N]')));
      if (!accept) {
        connectors[connector] = { status: 'skipped', code: 'SENSITIVITY_UNKNOWN' };
        say(`${label}: 비공개 일정 포함에 동의하지 않아 건너뜁니다 (--accept-unknown-sensitivity 필요).`);
        continue;
      }
    }
    // Claude, then Codex, then Kiro, unless the user picks another.
    let host = candidates[0];
    if (candidates.length > 1 && !options.yes) {
      const answer = await question(`${label}: ${server} 서버가 여러 호스트에 있습니다. ${candidates
        .map((item, index) => `${index + 1}) ${item}`).join(' ')} 중 번호를 고르세요 [1]:`);
      host = candidates[Number(answer) - 1] ?? candidates[0];
    }
    try {
      await selectConnector(connector, { host, server, autoAck: options.autoAck, acceptUnknownSensitivity: accept,
        identityAuthFatal: true }, environment, store);
      connectors[connector] = { status: 'selected', server, host };
      say(`${label}: ${host}의 ${server} 확인 완료`);
    } catch (error) {
      const code = intakeCode(error);
      if (code === 'AUTH_EXPIRED_MIDWAY') return stop(code, `${server} reported an authentication failure`);
      connectors[connector] = { status: 'skipped', code };
      say(`${label}: ${server} 확인 실패(${code}), 건너뜁니다.`);
    }
  }

  // 3-5. Dry run, then the first sync of the connectors selected now.
  const selected = SYNC_CONNECTORS.filter(id => connectors[id]?.status === 'selected');
  if (selected.length) {
    say('가져올 항목 수를 확인합니다 (드라이런)...');
    const dry = await runConnectors({ dryRun: true, noninteractive: true, only: selected }, environment, store);
    dryRun = dry.results;
    say(`드라이런: ${countsLine(dry.results)}`);
    if (dry.code === 'AUTH_EXPIRED_MIDWAY') return stop(dry.code, dry.message ?? '');
    if (dry.ok && (options.yes || yes(await question('첫 수집을 실행할까요? [y/N]')))) {
      say('첫 수집을 실행합니다...');
      const real = await runConnectors({ dryRun: false, noninteractive: false, only: selected }, environment, store);
      sync = real.results;
      synced = real.ok;
      say(`수집 결과: ${countsLine(real.results)}`);
      if (real.code === 'AUTH_EXPIRED_MIDWAY') return stop(real.code, real.message ?? '');
    }
  }

  // 6. Schedule: only with --schedule or an explicit interactive answer.
  let wanted = options.schedule;
  if (!wanted && !options.yes) {
    const answer = await question('자동 수집 일정을 설정할까요? hourly, daily@HH:MM, twice-daily@HH:MM,HH:MM 또는 off (빈 칸: 건너뜀):');
    if (answer) {
      try { wanted = parseSchedule(answer); } catch { say('일정 형식이 올바르지 않아 일정 설정을 건너뜁니다.'); }
    }
  }
  if (wanted) {
    const uid = environment.uid ?? process.getuid?.();
    const cliPath = environment.cliPath ?? (process.argv[1] ? resolve(process.argv[1]) : '');
    if (uid === undefined) return stop('SCHEDULE_FAILED', 'no user ID for launchctl');
    try {
      await applySchedule(wanted, { home: environment.home, uid, nodePath: environment.nodePath ?? process.execPath, cliPath,
        launchctl: environment.launchctl ?? defaultLaunchctl });
    } catch (error) {
      say('자동 수집 일정을 설정하지 못했습니다.');
      return stop(intakeCode(error), error instanceof IntakeError ? error.message : 'schedule failed');
    }
    schedule = scheduleLabel(wanted);
    say(wanted.kind === 'off' ? '자동 수집 일정을 제거했습니다.' : `자동 수집 일정을 설정했습니다: ${schedule}`);
  }
  return { ok: true, connectors, dryRun, synced, schedule, ...(sync ? { sync } : {}) };
}
