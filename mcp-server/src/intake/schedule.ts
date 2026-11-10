// The user LaunchAgent that runs `intake run --noninteractive` on a schedule.
// The plist holds fixed arguments only: the node executable, the CLI script and
// the command. launchctl is reached through an injected runner (argv, never a
// shell), so tests never touch the real launchd.

import { execFile } from 'node:child_process';
import { chmodSync, mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';
import { IntakeError } from './errors.js';

export const LAUNCH_AGENT_LABEL = 'click.atomai.ttobak.intake';

export type Schedule =
  | { kind: 'off' }
  | { kind: 'hourly' }
  | { kind: 'daily' | 'twice-daily'; times: { hour: number; minute: number }[] };

/** Exit code of `launchctl <args>`. */
export type LaunchctlRunner = (args: string[]) => Promise<number>;

const TIME = /^([01]\d|2[0-3]):([0-5]\d)$/;

export function parseSchedule(value: string): Schedule {
  if (value === 'off' || value === 'hourly') return { kind: value };
  const match = /^(daily|twice-daily)@(.+)$/.exec(value);
  const parts = match ? match[2].split(',') : [];
  const times = parts.map(part => TIME.exec(part)).map(time => time && { hour: Number(time[1]), minute: Number(time[2]) });
  const expected = match?.[1] === 'daily' ? 1 : 2;
  if (!match || times.length !== expected || times.some(time => !time) || new Set(parts).size !== parts.length) {
    throw new IntakeError('INVALID_ARGUMENT', '--schedule must be off, hourly, daily@HH:MM or twice-daily@HH:MM,HH:MM');
  }
  return { kind: match[1] as 'daily' | 'twice-daily', times: times as { hour: number; minute: number }[] };
}

export function scheduleLabel(schedule: Schedule): string {
  if (schedule.kind === 'off' || schedule.kind === 'hourly') return schedule.kind;
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${schedule.kind}@${schedule.times.map(time => `${pad(time.hour)}:${pad(time.minute)}`).join(',')}`;
}

function escapeXml(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&apos;');
}

function absolutePath(value: string): string {
  // eslint-disable-next-line no-control-regex
  if (!isAbsolute(value) || /[\u0000-\u001f\u007f]/.test(value) || value.length > 1024) {
    throw new IntakeError('INVALID_ARGUMENT', 'the schedule needs absolute node and CLI paths');
  }
  return value;
}

export function buildLaunchAgentPlist(schedule: Schedule, nodePath: string, cliPath: string, home: string): string {
  if (schedule.kind === 'off') throw new IntakeError('INVALID_ARGUMENT', 'an off schedule has no plist');
  const string = (value: string) => `<string>${escapeXml(value)}</string>`;
  const program = [absolutePath(nodePath), absolutePath(cliPath), 'intake', 'run', '--noninteractive'];
  const logs = join(absolutePath(home), 'Library', 'Logs', 'ttobak');
  const when = schedule.kind === 'hourly'
    ? '  <key>StartInterval</key><integer>3600</integer>'
    : `  <key>StartCalendarInterval</key><array>${schedule.times.map(time =>
      `<dict><key>Hour</key><integer>${time.hour}</integer><key>Minute</key><integer>${time.minute}</integer></dict>`).join('')}</array>`;
  return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key>${string(LAUNCH_AGENT_LABEL)}
  <key>ProgramArguments</key><array>${program.map(string).join('')}</array>
${when}
  <key>StandardOutPath</key>${string(join(logs, 'intake.log'))}
  <key>StandardErrorPath</key>${string(join(logs, 'intake.err'))}
  <key>Nice</key><integer>10</integer>
</dict></plist>
`;
}

export function launchAgentPath(home: string): string {
  return join(home, 'Library', 'LaunchAgents', `${LAUNCH_AGENT_LABEL}.plist`);
}

export const defaultLaunchctl: LaunchctlRunner = args => new Promise(resolve => {
  if (process.platform !== 'darwin') { resolve(127); return; }
  execFile('/bin/launchctl', args, { timeout: 30_000 }, error => {
    const code = (error as { code?: unknown } | null)?.code;
    resolve(error ? (typeof code === 'number' ? code : 1) : 0);
  });
});

export interface ScheduleTarget { home: string; uid: number; nodePath: string; cliPath: string; launchctl: LaunchctlRunner }

/**
 * Writes (or for `off`, removes) the plist and reloads the job. An old job is
 * booted out first; its failure is ignored because the job may not exist.
 */
export async function applySchedule(schedule: Schedule, target: ScheduleTarget): Promise<void> {
  const path = launchAgentPath(target.home);
  const service = `gui/${target.uid}/${LAUNCH_AGENT_LABEL}`;
  if (schedule.kind === 'off') {
    await target.launchctl(['bootout', service]);
    rmSync(path, { force: true });
    return;
  }
  const plist = buildLaunchAgentPlist(schedule, target.nodePath, target.cliPath, target.home);
  const temporary = `${path}.${process.pid}.tmp`;
  try {
    mkdirSync(join(target.home, 'Library', 'LaunchAgents'), { recursive: true });
    mkdirSync(join(target.home, 'Library', 'Logs', 'ttobak'), { recursive: true, mode: 0o700 });
    writeFileSync(temporary, plist, { mode: 0o644 });
    chmodSync(temporary, 0o644);
    renameSync(temporary, path);
  } catch {
    rmSync(temporary, { force: true });
    throw new IntakeError('SCHEDULE_FAILED', 'the LaunchAgent plist could not be written');
  }
  await target.launchctl(['bootout', service]);
  if (await target.launchctl(['bootstrap', `gui/${target.uid}`, path]) !== 0) {
    throw new IntakeError('SCHEDULE_FAILED', 'launchctl bootstrap failed');
  }
}

/** The schedule seams of an intake environment; defaults are the real process values. */
export interface ScheduleEnvironment { home: string; uid?: number; nodePath?: string; cliPath?: string; launchctl?: LaunchctlRunner }

/** `intake schedule <value>` and the last step of `intake setup`: plist and launchctl only. */
export async function installSchedule(schedule: Schedule, environment: ScheduleEnvironment): Promise<string> {
  const uid = environment.uid ?? process.getuid?.();
  if (uid === undefined) throw new IntakeError('SCHEDULE_FAILED', 'no user ID for launchctl');
  await applySchedule(schedule, { home: environment.home, uid, nodePath: environment.nodePath ?? process.execPath,
    cliPath: environment.cliPath ?? (process.argv[1] ? resolve(process.argv[1]) : ''),
    launchctl: environment.launchctl ?? defaultLaunchctl });
  return scheduleLabel(schedule);
}
