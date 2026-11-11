// Pure view helpers for Mac local intake (settings section and the
// /performance collect control). Wire types mirror
// mac-app/src-tauri/src/intake_runner.rs. No imports, so node:test can load
// this file in isolation. Copy is fixed in
// docs/superpowers/plans/2026-11-11-mac-intake-ui-copy.json.

export const INTAKE_COPY = {
  collectNow: '지금 수집',
  connectSetup: '연결 설정',
  ttobakLogin: 'TTOBAK 로그인',
  update: '업데이트',
  sectionTitle: 'Mac 로컬 수집',
  browserHint: 'Mac 앱에서 수집할 수 있어요',
  connectorSelected: '선택됨',
  connectorSkipped: '건너뜀',
  cancel: '취소',
} as const;

const CODE_LABELS: Record<string, string> = {
  AUTH_EXPIRED_MIDWAY: 'Midway 인증이 만료됐어요. 터미널에서 `mwinit -o`를 실행한 뒤 다시 시도하세요.',
  TTOBAK_LOGIN_REQUIRED: 'TTOBAK 로그인이 필요해요.',
  RUN_IN_PROGRESS: '이미 수집 중이에요.',
  PARTIAL_FAILURE: '일부 소스만 수집됐어요.',
  SENSITIVITY_UNKNOWN: '비공개 일정 포함에 동의해야 캘린더를 연결할 수 있어요.',
  ADAPTER_UPDATING: '업데이트 중이에요. 잠시 후 다시 시도하세요.',
  ADAPTER_MISSING: 'TTOBAK 수집 도구가 설치돼 있지 않아요. [업데이트]를 눌러 설치하세요.',
  NODE_MISSING: 'Node.js를 찾을 수 없어요. node 경로를 지정하세요.',
  WAIT_FAILED: '실행 상태를 확인하지 못했어요. 다시 시도하세요.',
  HOME_MISSING: '홈 폴더를 찾지 못했어요.',
  SERVER_NOT_CONFIGURED: '이 Mac의 Claude/Codex/Kiro 설정에서 해당 MCP 서버를 찾지 못했어요.',
  SKIPPED_BY_USER: '건너뛰도록 선택했어요.',
  SCHEMA_CHANGED: 'MCP 서버의 도구 형식이 바뀌어 연결하지 못했어요.',
};

export type IntakeScheduleKind = 'off' | 'hourly' | 'daily' | 'twiceDaily';
export interface IntakeSchedule {
  kind: IntakeScheduleKind;
  times: string[];
}

export type IntakeConnector = 'calendar' | 'sfdc' | 'meetings' | 'specreq' | 'slack';
export const INTAKE_CONNECTORS: { connector: IntakeConnector; label: string }[] = [
  { connector: 'calendar', label: '캘린더' },
  { connector: 'sfdc', label: 'SFDC' },
  { connector: 'meetings', label: '회의' },
  { connector: 'specreq', label: 'SpecReq' },
  { connector: 'slack', label: 'Slack' },
];

export interface IntakeSelectionEntry {
  connector: string;
  server: string;
  host: string;
  disabled: boolean;
}

export type IntakeAction = 'run' | 'setup' | 'login';
export interface IntakeFinished {
  action: IntakeAction | string;
  exitCode?: number | null;
  result?: unknown;
  code?: string | null;
}

export interface IntakeStateView {
  adapter: { present: boolean; updateAvailable: boolean | null };
  nodePath: string | null;
  nodeMissing: boolean;
  schedule: IntakeSchedule;
  running: IntakeAction | 'scheduleOff' | null;
  lastResult: IntakeFinished | null;
  status: unknown;
  selection: IntakeSelectionEntry[];
  launchAgentInstalled: boolean;
  progress: string[];
}

export interface IntakeSourceRow {
  source: 'calendar' | 'sfdc' | 'tasks';
  label: string;
  lastRunAt: string | null;
  lastSuccessAt: string | null;
  /** `ok` or an intake code; null when unknown. */
  result: string | null;
  counts: string | null;
}

/** Korean text for an intake or runner code; unknown codes show as-is. */
export function intakeCodeLabel(code: string): string {
  return Object.prototype.hasOwnProperty.call(CODE_LABELS, code) ? CODE_LABELS[code] : code;
}

/** The code of an invoke rejection (Tauri commands reject with a string code). */
export function errorCode(error: unknown): string {
  const message = record(error)?.message;
  return typeof message === 'string' ? message : String(error);
}

/** One-line Korean text for a failed state read (not command-not-found). */
export function loadErrorText(message: string): string {
  const oneLine = message.replace(/\s+/g, ' ').trim().slice(0, 200);
  return `Mac 로컬 수집 상태를 읽지 못했어요: ${oneLine || '알 수 없는 오류'}`;
}

export const SCHEDULE_OPTIONS: { kind: IntakeScheduleKind; label: string }[] = [
  { kind: 'off', label: '끄기' },
  { kind: 'hourly', label: '매시간' },
  { kind: 'daily', label: '하루 한 번' },
  { kind: 'twiceDaily', label: '하루 두 번' },
];

const TIME_PATTERN = /^([01]\d|2[0-3]):[0-5]\d$/;

export function scheduleLabel(schedule: IntakeSchedule | null | undefined): string {
  const option = SCHEDULE_OPTIONS.find(item => item.kind === schedule?.kind);
  if (!option || !schedule) return SCHEDULE_OPTIONS[0].label;
  const times = Array.isArray(schedule.times) ? schedule.times.filter(time => TIME_PATTERN.test(time)) : [];
  return times.length && option.kind !== 'off' && option.kind !== 'hourly' ? `${option.label} (${times.join(', ')})` : option.label;
}

/** The wire schedule for the form, or null when the times do not fit the kind. */
export function scheduleFromForm(kind: string, first: string, second: string): IntakeSchedule | null {
  if (kind === 'off' || kind === 'hourly') return { kind, times: [] };
  if (kind === 'daily') return TIME_PATTERN.test(first) ? { kind, times: [first] } : null;
  if (kind === 'twiceDaily') {
    return TIME_PATTERN.test(first) && TIME_PATTERN.test(second) && first !== second ? { kind, times: [first, second] } : null;
  }
  return null;
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

const TIMESTAMP_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?(Z|[+-]\d{2}:\d{2})$/;
const RESULT_PATTERN = /^(ok|[A-Z0-9_]{1,64})$/;

const timestamp = (value: unknown) => typeof value === 'string' && TIMESTAMP_PATTERN.test(value) ? value : null;
const count = (value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.floor(value) : null;

function summary(parts: [string, unknown][]): string | null {
  const shown = parts.flatMap(([label, value]) => {
    const n = count(value);
    return n === null ? [] : [`${label} ${n}`];
  });
  return shown.length ? shown.join(' · ') : null;
}

function countsText(source: IntakeSourceRow['source'], raw: unknown): string | null {
  const counts = record(raw);
  if (!counts) return null;
  if (source === 'calendar') return summary([['가져옴', counts.fetched], ['반영', counts.upserts], ['삭제', counts.removals]]);
  if (source === 'sfdc') return summary([['담당 고객', record(counts.assignments)?.fetched], ['활동', record(counts.tasks)?.fetched]]);
  return summary([['할 일', counts.tasks], ['생성', counts.created]]);
}

const SOURCES: { source: IntakeSourceRow['source']; label: string }[] = [
  { source: 'calendar', label: '캘린더' },
  { source: 'sfdc', label: 'SFDC' },
  { source: 'tasks', label: '할 일' },
];

/** One row per sync source that ran or is selected. `status` is the local
 * status.json and is treated as untrusted: bad fields become null. */
export function sourceRows(status: unknown, selection: unknown): IntakeSourceRow[] {
  const entries = record(status) ?? {};
  const selected = new Set((Array.isArray(selection) ? selection : [])
    .map(record)
    .filter(item => item && item.disabled !== true && typeof item.connector === 'string')
    .map(item => item!.connector as string));
  return SOURCES.flatMap(({ source, label }) => {
    const entry = record(entries[source]);
    if (!entry && !selected.has(source)) return [];
    const result = typeof entry?.result === 'string' && RESULT_PATTERN.test(entry.result) ? entry.result : null;
    return [{
      source, label,
      lastRunAt: timestamp(entry?.lastRunAt),
      lastSuccessAt: timestamp(entry?.lastSuccessAt),
      result,
      counts: result === 'ok' ? countsText(source, entry?.counts) : null,
    }];
  });
}

const LOGIN_CODE = 'TTOBAK_LOGIN_REQUIRED';

/** Show [TTOBAK 로그인] when the last run or the status reports a missing
 * TTOBAK login; a later successful login clears it. */
export function needsTtobakLogin(last: IntakeFinished | null | undefined, status: unknown): boolean {
  if (last?.action === 'login' && !last.code) return false;
  if (last?.code === LOGIN_CODE) return true;
  const results = record(record(last?.result)?.results);
  if (results && Object.values(results).some(item => record(item)?.result === LOGIN_CODE)) return true;
  const entries = record(status);
  return !!entries && Object.values(entries).some(item => record(item)?.result === LOGIN_CODE);
}

/** The CLI LaunchAgent and the app schedule would both run intake. */
export function showLaunchAgentWarning(state: Pick<IntakeStateView, 'launchAgentInstalled' | 'schedule'> | null | undefined): boolean {
  return !!state?.launchAgentInstalled && !!state.schedule && state.schedule.kind !== 'off';
}

/** Message for a finished run; empty on success. */
export function finishedMessage(finished: IntakeFinished | null | undefined): string {
  return finished?.code ? intakeCodeLabel(finished.code) : '';
}

export interface IntakeSetupRow {
  connector: IntakeConnector;
  label: string;
  status: 'selected' | 'skipped';
  /** Skip reason code; null when selected or unknown. */
  code: string | null;
  server: string | null;
  host: string | null;
}

const NAME_PATTERN = /^[A-Za-z0-9._@-]{1,128}$/;
const safeName = (value: unknown): string | null => typeof value === 'string' && NAME_PATTERN.test(value) ? value : null;

/** Per-connector rows of the last finished setup (`result.connectors`), in
 * connector order; empty for any other action. The result is untrusted:
 * unknown connectors and malformed fields are dropped. */
export function setupConnectorRows(finished: IntakeFinished | null | undefined): IntakeSetupRow[] {
  if (finished?.action !== 'setup') return [];
  const connectors = record(record(finished.result)?.connectors);
  if (!connectors) return [];
  return INTAKE_CONNECTORS.flatMap(({ connector, label }) => {
    const entry = record(connectors[connector]);
    if (!entry || (entry.status !== 'selected' && entry.status !== 'skipped')) return [];
    const code = entry.status === 'skipped' && typeof entry.code === 'string' && RESULT_PATTERN.test(entry.code) && entry.code !== 'ok'
      ? entry.code : null;
    return [{ connector, label, status: entry.status, code, server: safeName(entry.server), host: safeName(entry.host) }];
  });
}
