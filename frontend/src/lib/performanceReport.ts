// Pure helpers for the SA performance report tab on /performance (ADR-023).
// Wire types mirror backend/internal/model/performance_report.go and the
// report commands of mac-app/src-tauri/src/report_runner.rs. No imports, so
// node:test can load this file in isolation. Copy is fixed in
// docs/superpowers/plans/2026-11-11-mac-intake-ui-copy.json.

export const REPORT_COPY = {
  reportTab: '리포트',
  dashboardTab: '대시보드',
  makeReport: '리포트 만들기',
  cancel: '취소',
  actions: '정리할 항목',
  dataBasis: '데이터 기준',
} as const;

export type ReportPeriodKind = 'week' | 'quarter' | 'year' | 'custom';

export const REPORT_PERIOD_OPTIONS: { value: ReportPeriodKind; label: string }[] = [
  { value: 'week', label: '이번 주' },
  { value: 'quarter', label: '이번 분기' },
  { value: 'year', label: '올해' },
  { value: 'custom', label: '직접 지정' },
];

const KIND_LABELS: Record<ReportPeriodKind, string> = { week: '주간', quarter: '분기', year: '연간', custom: '직접 지정' };

const PERIOD_EXPIRED = '보관 기간(기간 종료 후 2년)이 지난 기간이에요.';

const CODE_LABELS: Record<string, string> = {
  CLAUDE_CLI_MISSING: 'Claude Code CLI를 찾을 수 없어요. 설치 후 다시 시도하세요.',
  CLAUDE_CLI_AUTH: 'Claude Code 로그인이 필요해요. 터미널에서 `claude`를 실행해 `/login` 하세요.',
  SKILL_MISSING: 'sa-performance-report 스킬이 설치돼 있지 않아요.',
  ADAPTER_OUTDATED: 'TTOBAK 수집 도구가 예전 버전이라 리포트를 만들 수 없어요. 설정의 수집 섹션에서 [업데이트]를 누른 뒤 다시 시도하세요.',
  SKILL_OUTDATED: 'sa-performance-report 스킬이 예전 버전이에요. internal-mcp-setup v1.4.0 설치 스크립트로 스킬을 갱신하세요.',
  REPORT_SANDBOX_UNAVAILABLE: 'Claude Code 샌드박스를 쓸 수 없어 리포트를 만들지 않았어요. Claude Code를 최신 버전으로 업데이트하세요.',
  REPORT_NO_SFDC: 'SFDC 데이터가 없어 리포트를 만들 수 없어요. 먼저 [지금 수집]을 실행하세요.',
  REPORT_INVALID: '리포트 형식이 올바르지 않아 저장하지 않았어요. 다시 시도하세요.',
  REPORT_CANCELLED: '리포트 만들기를 취소했어요.',
  REPORT_TIMEOUT: '시간이 너무 오래 걸려 멈췄어요. 기간을 줄여 다시 시도하세요.',
  RUN_IN_PROGRESS: '이미 리포트를 만들고 있어요.',
  TTOBAK_LOGIN_REQUIRED: 'TTOBAK 로그인이 필요해요.',
  INVALID_ARGUMENT: '입력값이 올바르지 않아요.',
  REPORT_PERIOD_EXPIRED: PERIOD_EXPIRED,
  TTOBAK_UNAVAILABLE: 'TTOBAK 서버에 연결하지 못했어요. 잠시 후 다시 시도하세요.',
  EXIT_FAILED: '리포트 실행이 실패했어요. 다시 시도하세요.',
  SPAWN_FAILED: '리포트 실행을 시작하지 못했어요. 다시 시도하세요.',
  WAIT_FAILED: '실행 상태를 확인하지 못했어요. 다시 시도하세요.',
  HOME_MISSING: '홈 폴더를 찾지 못했어요.',
  'save-failed': '강조 항목을 임시 파일에 저장하지 못했어요. 다시 시도하세요.',
  'profile-missing': '선택한 AWS 프로필을 이 Mac에서 찾을 수 없어요.',
  // docs/superpowers/plans/2026-11-12-one-click-intake-setup-ui-copy.json
  CLAUDE_CLI_BEDROCK: 'Bedrock 호출이 시간 안에 끝나지 않았어요. 리포트 설정에서 AWS 프로필을 고르거나 Bedrock을 끄세요.',
  'profile-required': 'Bedrock을 쓰려면 AWS 프로필을 골라야 해요.',
  REPORT_EXCLUDED_CHANNEL: '리포트에 제외한 Slack 채널 이름이 남아 있어 저장하지 않았어요. 강조할 항목에 그 채널이 있다면 지우고 다시 만드세요.',
  SLACK_NOT_SELECTED: 'Slack 채널을 불러오려면 수집 설정에서 Slack을 먼저 연결하세요.',
};

/** Korean text for a report CLI or runner code; unknown codes show as-is. */
export function reportCodeLabel(code: string): string {
  return Object.prototype.hasOwnProperty.call(CODE_LABELS, code) ? CODE_LABELS[code] : code;
}

/** Bedrock is on without a profile: such a setting cannot be saved or run. */
export function bedrockNeedsProfile(settings: ReportSettings | null | undefined): boolean {
  return Boolean(settings?.useBedrock && !settings.awsProfile);
}

/** Profile to save with a Bedrock toggle: the stored one only while it still
 * exists on this Mac, so a removed profile never blocks turning Bedrock off. */
export function bedrockToggleProfile(settings: ReportSettingsView | null | undefined): string | null {
  const profile = settings?.awsProfile ?? null;
  return profile && settings?.profiles.includes(profile) ? profile : null;
}

// ---------------------------------------------------------------- wire types

export interface PerformanceReportSummary {
  periodKey: string;
  label: string;
  generatedAt: string;
  partial: boolean;
}

export interface PerformanceReportList {
  items: PerformanceReportSummary[];
  truncated: boolean;
}

export interface PerformanceReportHighlights {
  projects: string[];
  initiatives: string[];
  channels: string[];
  programs: string[];
}

export interface PerformanceReportAction {
  severity: 'hi' | 'mid' | 'lo' | string;
  title: string;
  detail: string;
  source: string;
}

export interface PerformanceReport {
  schemaVersion: number;
  periodKey: string;
  period: { start: string; end: string; label: string };
  alias: string;
  generatedAt: string;
  model: string;
  partial: boolean;
  missingSources: string[];
  highlights: PerformanceReportHighlights;
  summary: string;
  ledger: { label: string; value: string; unit: string; note: string }[];
  sections: {
    id: string;
    title: string;
    lede: string;
    paragraphs: string[];
    tables: { columns: string[]; rows: string[][] }[];
    cards: { title: string; body: string }[];
  }[];
  actions: PerformanceReportAction[];
  dataBasis: { source: string; query: string; exclusions: string; estimates: string }[];
}

export interface ReportFinished {
  exitCode?: number | null;
  result?: unknown;
  code?: string | null;
}

export interface ReportSettings {
  useBedrock: boolean;
  awsProfile: string | null;
}

/** Cached `report slack-channels` result: the owner's channels of the last 90 days. */
export interface SlackChannelCandidates {
  channels: { name: string; count: number }[];
  partial?: boolean;
  since?: string;
}

export interface SlackChannelRow {
  name: string;
  /** Posts in the window; null for an exclusion no longer among the candidates. */
  count: number | null;
  included: boolean;
}

/** Every candidate is included unless excluded; exclusions that dropped out
 * of the candidates stay listed (unchecked) so they can be undone. */
export function channelRows(candidates: SlackChannelCandidates | null | undefined, excluded: string[]): SlackChannelRow[] {
  const listed = (candidates?.channels ?? []).map(channel => ({ name: channel.name, count: channel.count, included: !excluded.includes(channel.name) }));
  const stale = excluded.filter(name => !listed.some(row => row.name === name)).map(name => ({ name, count: null, included: false }));
  return [...listed, ...stale];
}

export function toggleExcluded(excluded: string[], name: string): string[] {
  return excluded.includes(name) ? excluded.filter(item => item !== name) : [...excluded, name];
}

export interface ReportSettingsView extends ReportSettings {
  profiles: string[];
  /** Absent in apps before Slack channel exclusions. */
  excludedSlackChannels?: string[];
  slackChannels?: SlackChannelCandidates | null;
}

export interface ReportStateView {
  running: boolean;
  lastResult: ReportFinished | null;
  progress: string[];
  settings: ReportSettings;
  /** Bedrock is on without a profile (saved before the guard); absent in older apps. */
  settingsAttention?: boolean;
}

export interface ReportGenerateInput {
  period: ReportPeriodKind;
  start?: string;
  end?: string;
  highlights?: Partial<PerformanceReportHighlights>;
}

export const HIGHLIGHT_FIELDS: { field: keyof PerformanceReportHighlights; label: string; hint: string }[] = [
  { field: 'projects', label: '프로젝트', hint: '강조할 프로젝트' },
  { field: 'initiatives', label: '이니셔티브', hint: '강조할 이니셔티브' },
  { field: 'channels', label: '채널', hint: '강조할 Slack 채널 이름' },
  { field: 'programs', label: '프로그램', hint: '강조할 프로그램' },
];

// ---------------------------------------------------------------- periods

const WEEK_KEY = /^W(\d{4})-(0[1-9]|[1-4]\d|5[0-3])$/;
const QUARTER_KEY = /^Q(\d{4})-([1-4])$/;
const YEAR_KEY = /^Y(\d{4})$/;
const CUSTOM_KEY = /^C(\d{4}-\d{2}-\d{2})_(\d{4}-\d{2}-\d{2})$/;
const DAY_MS = 24 * 60 * 60 * 1000;
const MAX_CUSTOM_DAYS = 400;

const validYear = (value: string) => Number(value) >= 2000 && Number(value) <= 2100;

/** Milliseconds of a real YYYY-MM-DD date in 2000-2100, or null. */
function day(value: unknown): number | null {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value) || !validYear(value.slice(0, 4))) return null;
  const parsed = Date.parse(`${value}T00:00:00Z`);
  return Number.isNaN(parsed) || new Date(parsed).toISOString().slice(0, 10) !== value ? null : parsed;
}

/** The period kind of a stored period key, or null for a malformed key. */
export function periodKind(key: unknown): ReportPeriodKind | null {
  if (typeof key !== 'string') return null;
  let match = WEEK_KEY.exec(key) ?? QUARTER_KEY.exec(key) ?? YEAR_KEY.exec(key);
  if (match) {
    if (!validYear(match[1])) return null;
    return key[0] === 'W' ? 'week' : key[0] === 'Q' ? 'quarter' : 'year';
  }
  match = CUSTOM_KEY.exec(key);
  if (!match) return null;
  const start = day(match[1]);
  const end = day(match[2]);
  return start !== null && end !== null && end >= start && end - start < MAX_CUSTOM_DAYS * DAY_MS ? 'custom' : null;
}

/** Korean label for a period key; a malformed key shows as-is. */
export function periodLabel(key: string): string {
  const kind = periodKind(key);
  if (kind === 'week') {
    const [, year, week] = WEEK_KEY.exec(key)!;
    return `${year}년 ${Number(week)}주차`;
  }
  if (kind === 'quarter') {
    const [, year, quarter] = QUARTER_KEY.exec(key)!;
    return `${year}년 ${quarter}분기`;
  }
  if (kind === 'year') return `${key.slice(1)}년`;
  if (kind === 'custom') {
    const [, start, end] = CUSTOM_KEY.exec(key)!;
    return `${start} ~ ${end}`;
  }
  return key;
}

/**
 * Error text for a custom range, or null when the CLI and server accept it.
 * The server keeps a report until January 1 (UTC) of (end year + 3) and
 * refuses one already past that.
 */
export function customRangeError(start: string, end: string, now = Date.now()): string | null {
  const from = day(start);
  const to = day(end);
  if (from === null || to === null) return '시작일과 종료일을 2000-2100년의 날짜로 입력하세요.';
  if (to < from) return '종료일이 시작일보다 앞서요.';
  if (to - from >= MAX_CUSTOM_DAYS * DAY_MS) return '기간은 최대 400일까지 지정할 수 있어요.';
  if (new Date(to).getUTCFullYear() + 3 <= new Date(now).getUTCFullYear()) return PERIOD_EXPIRED;
  return null;
}

// ---------------------------------------------------------------- list

export interface ReportSummaryRow {
  periodKey: string;
  label: string;
  kind: ReportPeriodKind;
  kindLabel: string;
  generatedAt: string;
  generatedText: string;
  partial: boolean;
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

const SEOUL_OFFSET_MS = 9 * 60 * 60 * 1000; // Asia/Seoul has no DST.

/** `YYYY-MM-DD HH:mm` in Asia/Seoul, or empty for an unparsable time. */
export function formatGeneratedAt(iso: unknown): string {
  const parsed = typeof iso === 'string' ? Date.parse(iso) : NaN;
  if (Number.isNaN(parsed)) return '';
  const shifted = new Date(parsed + SEOUL_OFFSET_MS).toISOString();
  return `${shifted.slice(0, 10)} ${shifted.slice(11, 16)}`;
}

const timeOf = (iso: string) => {
  const parsed = Date.parse(iso);
  return Number.isNaN(parsed) ? -Infinity : parsed;
};

/** List rows newest `generatedAt` first; items with a malformed key are dropped. */
export function reportSummaryRows(list: unknown): ReportSummaryRow[] {
  const items = record(list)?.items;
  if (!Array.isArray(items)) return [];
  const rows = items.flatMap((raw): ReportSummaryRow[] => {
    const item = record(raw);
    const kind = periodKind(item?.periodKey);
    if (!item || !kind) return [];
    const periodKey = item.periodKey as string;
    const generatedAt = typeof item.generatedAt === 'string' ? item.generatedAt : '';
    return [{
      periodKey,
      label: typeof item.label === 'string' && item.label.trim() ? item.label : periodLabel(periodKey),
      kind,
      kindLabel: KIND_LABELS[kind],
      generatedAt,
      generatedText: formatGeneratedAt(generatedAt),
      partial: item.partial === true,
    }];
  });
  return rows.sort((a, b) => timeOf(b.generatedAt) - timeOf(a.generatedAt));
}

/** The newest stored report of a period kind (rows are newest first). */
export function newestReportOfKind(rows: ReportSummaryRow[], kind: ReportPeriodKind): string | null {
  return rows.find(row => row.kind === kind)?.periodKey ?? null;
}

// ---------------------------------------------------------------- view

const SEVERITIES: { severity: 'hi' | 'mid' | 'lo'; label: string }[] = [
  { severity: 'hi', label: '높음' },
  { severity: 'mid', label: '중간' },
  { severity: 'lo', label: '낮음' },
];

/** Non-empty action groups in hi, mid, lo order; stored order within a group. */
export function groupActions(actions: PerformanceReportAction[] | null | undefined) {
  const list = Array.isArray(actions) ? actions : [];
  return SEVERITIES
    .map(({ severity, label }) => ({ severity, label, items: list.filter(action => action?.severity === severity) }))
    .filter(group => group.items.length > 0);
}

// ---------------------------------------------------------------- dialog

const MAX_HIGHLIGHTS = 20;
const MAX_HIGHLIGHT_CHARS = 200;
// Mirrors the Rust check before spawn: no tags, Markdown links or Slack links.
const FORBIDDEN = /<[A-Za-z/]|\]\(|slack\.com/i;

/** One highlight per line: trimmed, blank lines dropped, duplicates removed. */
export function parseHighlightLines(text: string): { items: string[]; error: string | null } {
  const items = [...new Set(text.split('\n').map(line => line.trim()).filter(Boolean))];
  if (items.length > MAX_HIGHLIGHTS) return { items, error: `최대 ${MAX_HIGHLIGHTS}개까지 입력할 수 있어요.` };
  for (const item of items) {
    if ([...item].length > MAX_HIGHLIGHT_CHARS) return { items, error: `한 줄은 최대 ${MAX_HIGHLIGHT_CHARS}자예요.` };
    if (FORBIDDEN.test(item) || /[\u0000-\u001f\u007f]/.test(item)) {
      return { items, error: '태그, 링크, 제어 문자는 사용할 수 없는 값이에요.' };
    }
  }
  return { items, error: null };
}

export function highlightsText(list: string[] | null | undefined): string {
  return Array.isArray(list) ? list.join('\n') : '';
}

/**
 * The period key to open and a message for a finished run. Codes without a
 * report label go to `fallback` (the shared intake runner labels, such as
 * NODE_MISSING or ADAPTER_MISSING).
 */
export function reportFinishedOutcome(finished: ReportFinished | null | undefined,
  fallback: (code: string) => string = code => code): { periodKey: string | null; message: string } {
  if (!finished) return { periodKey: null, message: '' };
  if (finished.code) {
    const label = reportCodeLabel(finished.code);
    return { periodKey: null, message: label !== finished.code ? label : fallback(finished.code) };
  }
  const result = record(finished.result);
  if (result?.ok !== true || !periodKind(result.periodKey)) return { periodKey: null, message: reportCodeLabel('REPORT_INVALID') };
  const missing = Array.isArray(result.missingSources) ? result.missingSources.filter(item => typeof item === 'string') : [];
  return {
    periodKey: result.periodKey as string,
    message: result.partial === true && missing.length ? `일부 소스 없이 만들었어요: ${missing.join(', ')}` : '',
  };
}

/** `m:ss` elapsed time. */
export function formatElapsed(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}
