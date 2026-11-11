// SA performance report schema v1: period keys and the adapter mirror of the
// server validation (backend/internal/model/performance_report.go). The
// adapter validates before upload; the server validates again. Validation also
// rebuilds the object from known fields only, so the upload never carries a
// null, an unknown field or a numeric table cell.

import { ReportError } from './errors.js';

export const SCHEMA_VERSION = 1;
export const MAX_REPORT_BYTES = 256 * 1024;
const SHORT = 200;
const LONG = 4000;
const MAX_SECTIONS = 40;
const MAX_ACTIONS = 200;
const MAX_HIGHLIGHTS = 20;
const MAX_MISSING = 20;
const MAX_LEDGER = 50;
const MAX_PARAGRAPHS = 100;
const MAX_TABLES = 20;
const MAX_COLUMNS = 50;
const MAX_ROWS = 500;
const MAX_CARDS = 50;
const MAX_DATA_BASIS = 50;
const MAX_CUSTOM_DAYS = 400;
const MIN_YEAR = 2000;
const MAX_YEAR = 2100;
const DAY_MS = 86_400_000;

const WEEK_KEY = /^W(\d{4})-(0[1-9]|[1-4]\d|5[0-3])$/;
const QUARTER_KEY = /^Q(\d{4})-([1-4])$/;
const YEAR_KEY = /^Y(\d{4})$/;
const CUSTOM_KEY = /^C(\d{4}-\d{2}-\d{2})_(\d{4}-\d{2}-\d{2})$/;
const SECTION_ID = /^[A-Za-z0-9_-]{1,64}$/;
const DATE = /^(\d{4})-(\d{2})-(\d{2})$/;
// Go's `<[A-Za-z/]|\]\(|(?i)slack\.com`; `iu` matches Go's Unicode case folding.
const TAG_OR_LINK = /<[A-Za-z/]|\]\(/;
const SLACK = /slack\.com/iu;
const LONE_SURROGATE = /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/;
// RFC3339 as Go's time.Parse(time.RFC3339) accepts it.
/** The server's generatedAt rule: at most 35 bytes, a 1-9 digit fraction, case-sensitive T and Z, a real date. */
const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d{1,9})?(Z|[+-](\d{2}):(\d{2}))$/;
const MAX_GENERATED_AT_BYTES = 35;

/**
 * Go `time.Parse(time.RFC3339Nano)` ranges: a day that exists in the month
 * (leap years included), hour 0-23, minute and second 0-59 (no leap second),
 * offset hours 0-23 and minutes 0-59.
 */
export function validGeneratedAt(value: unknown): value is string {
  if (typeof value !== 'string' || Buffer.byteLength(value) > MAX_GENERATED_AT_BYTES) return false;
  const match = RFC3339.exec(value);
  if (!match) return false;
  const [year, month, dayOfMonth, hour, minute, second] = match.slice(1, 7).map(Number);
  const leap = (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1];
  if (days === undefined || dayOfMonth < 1 || dayOfMonth > days || hour > 23 || minute > 59 || second > 59) return false;
  if (match[9] !== undefined && (Number(match[9]) > 23 || Number(match[10]) > 59)) return false;
  return !Number.isNaN(Date.parse(value));
}
export const SEVERITIES = ['hi', 'mid', 'lo'] as const;

export type PeriodKind = 'week' | 'quarter' | 'year' | 'custom';
export interface Period { periodKey: string; start: string; end: string; label: string }
export interface Highlights { projects: string[]; initiatives: string[]; channels: string[]; programs: string[] }
export interface LedgerItem { label: string; value: string; unit: string; note?: string }
export interface Table { columns: string[]; rows: string[][] }
export interface Card { title: string; body: string }
export interface Section { id: string; title: string; lede: string; paragraphs: string[]; tables?: Table[]; cards?: Card[] }
export interface Action { severity: typeof SEVERITIES[number]; title: string; detail: string; source: string }
export interface DataBasis { source: string; query: string; exclusions: string; estimates?: string }
export interface PerformanceReport {
  schemaVersion: 1;
  periodKey: string;
  period: { start: string; end: string; label: string };
  alias: string;
  generatedAt: string;
  model: string;
  partial: boolean;
  missingSources?: string[];
  highlights?: Highlights;
  summary: string;
  ledger: LedgerItem[];
  sections: Section[];
  actions: Action[];
  dataBasis: DataBasis[];
}

function invalid(field: string): never {
  throw new ReportError('REPORT_INVALID', field);
}

function day(value: string): number | null {
  const match = DATE.exec(value);
  if (!match) return null;
  const [year, month, date] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const time = Date.UTC(year, month - 1, date);
  if (year < MIN_YEAR || year > MAX_YEAR || new Date(time).toISOString().slice(0, 10) !== value) return null;
  return time;
}

const iso = (time: number) => new Date(time).toISOString().slice(0, 10);
const validYear = (value: string) => Number(value) >= MIN_YEAR && Number(value) <= MAX_YEAR;

/** Monday of ISO week 1 of `year`. */
function isoWeekOne(year: number): number {
  const january4 = Date.UTC(year, 0, 4);
  return january4 - ((new Date(january4).getUTCDay() + 6) % 7) * DAY_MS;
}

/** The inclusive first and last day of a period key, as the server computes them. */
export function periodBounds(key: string): { start: string; end: string } {
  let match = WEEK_KEY.exec(key);
  if (match) {
    const year = Number(match[1]);
    if (!validYear(match[1])) invalid('period key');
    const monday = isoWeekOne(year) + (Number(match[2]) - 1) * 7 * DAY_MS;
    if (monday >= isoWeekOne(year + 1)) invalid('period key'); // week 53 of a 52-week year
    return { start: iso(monday), end: iso(monday + 6 * DAY_MS) };
  }
  match = QUARTER_KEY.exec(key);
  if (match) {
    if (!validYear(match[1])) invalid('period key');
    const year = Number(match[1]);
    const quarter = Number(match[2]);
    return { start: iso(Date.UTC(year, 3 * (quarter - 1), 1)), end: iso(Date.UTC(year, 3 * quarter, 0)) };
  }
  match = YEAR_KEY.exec(key);
  if (match) {
    if (!validYear(match[1])) invalid('period key');
    return { start: `${match[1]}-01-01`, end: `${match[1]}-12-31` };
  }
  match = CUSTOM_KEY.exec(key);
  if (match) {
    const start = day(match[1]);
    const end = day(match[2]);
    if (start === null || end === null || end < start || end - start >= MAX_CUSTOM_DAYS * DAY_MS) invalid('period key');
    return { start: match[1], end: match[2] };
  }
  return invalid('period key');
}

/**
 * The period of a run: the ISO week, calendar quarter or year containing
 * `today` (YYYY-MM-DD in the owner's zone), or a custom inclusive range.
 */
export function periodFor(kind: string, today: string, custom: { start?: string; end?: string } = {}, utcYear?: number): Period {
  const bad = (detail: string): never => { throw new ReportError('INVALID_ARGUMENT', detail); };
  const now = day(today);
  if (now === null) return bad('today is invalid');
  const year = new Date(now).getUTCFullYear();
  if (kind !== 'custom' && (custom.start !== undefined || custom.end !== undefined)) bad('--start and --end need --period custom');
  if (kind === 'week') {
    const monday = now - ((new Date(now).getUTCDay() + 6) % 7) * DAY_MS;
    const thursday = new Date(monday + 3 * DAY_MS);
    const isoYear = thursday.getUTCFullYear();
    const week = 1 + Math.round((monday - isoWeekOne(isoYear)) / (7 * DAY_MS));
    const periodKey = `W${isoYear}-${String(week).padStart(2, '0')}`;
    const { start, end } = periodBounds(periodKey);
    return { periodKey, start, end, label: `${isoYear}년 ${week}주차 (${start} ~ ${end})` };
  }
  if (kind === 'quarter') {
    const quarter = Math.floor(new Date(now).getUTCMonth() / 3) + 1;
    const periodKey = `Q${year}-${quarter}`;
    return { periodKey, ...periodBounds(periodKey), label: `${year}년 ${quarter}분기` };
  }
  if (kind === 'year') return { periodKey: `Y${year}`, ...periodBounds(`Y${year}`), label: `${year}년` };
  if (kind === 'custom') {
    if (typeof custom.start !== 'string' || typeof custom.end !== 'string') return bad('--period custom needs --start and --end');
    const start = day(custom.start);
    const end = day(custom.end);
    if (start === null || end === null) return bad('--start and --end must be YYYY-MM-DD');
    if (end < start) return bad('--end is before --start');
    if (end - start >= MAX_CUSTOM_DAYS * DAY_MS) return bad(`a custom period spans at most ${MAX_CUSTOM_DAYS} days`);
    // The server keeps a report until January 1 of (end year + 3) and refuses one already past it.
    // Against the current UTC year, like the server, the app and the web dialog.
    if (new Date(end).getUTCFullYear() + 3 <= (utcYear ?? year)) {
      throw new ReportError('REPORT_PERIOD_EXPIRED', 'the period ended more than two calendar years ago');
    }
    return { periodKey: `C${custom.start}_${custom.end}`, start: custom.start, end: custom.end,
      label: `${custom.start} ~ ${custom.end}` };
  }
  return bad('--period must be week, quarter, year or custom');
}

/** Plain text: valid UTF-16 (no lone surrogate), code-point bounded, no controls, tags, Markdown or Slack links. */
export function plainText(value: unknown, max: number): value is string {
  if (typeof value !== 'string' || LONE_SURROGATE.test(value) || TAG_OR_LINK.test(value) || SLACK.test(value)) return false;
  let count = 0;
  for (const character of value) {
    const code = character.codePointAt(0)!;
    if ((code < 0x20 && code !== 0x09 && code !== 0x0a && code !== 0x0d) || code === 0x7f) return false;
    if (++count > max) return false;
  }
  return true;
}

function record(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) invalid(field);
  return value as Record<string, unknown>;
}

function list(value: unknown, max: number, field: string): unknown[] {
  if (!Array.isArray(value) || value.length > max) invalid(field);
  return value;
}

function text(value: unknown, max: number, field: string, required = false): string {
  if (!plainText(value, max) || (required && value === '')) invalid(field);
  return value;
}

/** A string, or a finite number stringified (table cells and ledger values). */
function cell(value: unknown, max: number, field: string): string {
  return text(typeof value === 'number' && Number.isFinite(value) ? String(value) : value, max, field);
}

function texts(value: unknown, maxItems: number, maxRunes: number, field: string): string[] {
  return list(value, maxItems, field).map(item => text(item, maxRunes, field));
}

/** An optional field: absent stays absent; `null` is invalid. */
function optional<T>(source: Record<string, unknown>, name: string, parse: (value: unknown) => T): { [key: string]: T } {
  return name in source ? { [name]: parse(source[name]) } : {};
}

function highlights(value: unknown, field: string, code: 'REPORT_INVALID' | 'INVALID_ARGUMENT'): Highlights {
  try {
    const source = record(value, field);
    const known = ['projects', 'initiatives', 'channels', 'programs'] as const;
    if (Object.keys(source).some(key => !(known as readonly string[]).includes(key))) invalid(field);
    const result = {} as Highlights;
    for (const key of known) result[key] = key in source ? texts(source[key], MAX_HIGHLIGHTS, SHORT, field) : [];
    return result;
  } catch (error) {
    if (code === 'INVALID_ARGUMENT' && error instanceof ReportError) throw new ReportError(code, 'highlights file is invalid');
    throw error;
  }
}

/** The `--highlights-file` content: each list at most 20 items of at most 200 characters. */
export function validateHighlights(value: unknown): Highlights {
  return highlights(value, 'highlights', 'INVALID_ARGUMENT');
}

function section(value: unknown): Section {
  const source = record(value, 'section');
  if (typeof source.id !== 'string' || !SECTION_ID.test(source.id)) invalid('section id');
  return {
    id: source.id,
    title: text(source.title, SHORT, 'section text', true),
    lede: text(source.lede, LONG, 'section text'),
    paragraphs: texts(source.paragraphs, MAX_PARAGRAPHS, LONG, 'section text'),
    ...optional(source, 'tables', tables => list(tables, MAX_TABLES, 'section size').map(item => {
      const table = record(item, 'table');
      const columns = texts(table.columns, MAX_COLUMNS, SHORT, 'table');
      if (!columns.length) invalid('table');
      const rows = list(table.rows, MAX_ROWS, 'table').map(row => {
        const cells = list(row, MAX_COLUMNS, 'table row');
        if (cells.length !== columns.length) invalid('table row');
        return cells.map(entry => cell(entry, LONG, 'table row'));
      });
      return { columns, rows };
    })),
    ...optional(source, 'cards', cards => list(cards, MAX_CARDS, 'section size').map(item => {
      const card = record(item, 'card');
      return { title: text(card.title, SHORT, 'card'), body: text(card.body, LONG, 'card') };
    })),
  } as Section;
}

/** Compact JSON byte size as Go's encoder (HTML escaping off) writes it: U+2028/2029 become 6-byte escapes. */
export function encodedSize(value: unknown): number {
  const json = JSON.stringify(value);
  return Buffer.byteLength(json) + 3 * (json.match(/[\u2028\u2029]/g)?.length ?? 0);
}

/**
 * The report as the server encodes it for its size check: Go decoding fills
 * absent strings with "" and NormalizePerformanceReport absent lists with [].
 */
export function withServerDefaults(report: PerformanceReport): unknown {
  const empty = { projects: [], initiatives: [], channels: [], programs: [] };
  return {
    ...report,
    missingSources: report.missingSources ?? [],
    highlights: { ...empty, ...(report.highlights ?? {}) },
    ledger: report.ledger.map(item => ({ note: '', ...item })),
    sections: report.sections.map(section => ({ tables: [], cards: [], ...section })),
    dataBasis: report.dataBasis.map(basis => ({ estimates: '', ...basis })),
  };
}

/**
 * Validates a complete report against its period key exactly as the server
 * does, and returns a rebuilt copy holding only schema fields.
 */
export function validateReport(key: string, value: unknown): PerformanceReport {
  const { start, end } = periodBounds(key);
  const source = record(value, 'report');
  if (source.schemaVersion !== SCHEMA_VERSION) invalid('schemaVersion');
  const period = record(source.period, 'period');
  if (source.periodKey !== key || period.start !== start || period.end !== end) invalid('period');
  if (!validGeneratedAt(source.generatedAt)) invalid('generatedAt');
  if (typeof source.partial !== 'boolean') invalid('partial');
  const sections = list(source.sections, MAX_SECTIONS, 'sections').map(section);
  if (new Set(sections.map(item => item.id)).size !== sections.length) invalid('section id');
  const report: PerformanceReport = {
    schemaVersion: SCHEMA_VERSION,
    periodKey: key,
    period: { start, end, label: text(period.label, SHORT, 'period.label', true) },
    alias: text(source.alias, SHORT, 'alias or model'),
    generatedAt: source.generatedAt as string,
    model: text(source.model, SHORT, 'alias or model'),
    partial: source.partial,
    ...optional(source, 'missingSources', items => texts(items, MAX_MISSING, SHORT, 'missingSources')),
    ...optional(source, 'highlights', item => highlights(item, 'highlights', 'REPORT_INVALID')),
    summary: text(source.summary, LONG, 'summary'),
    ledger: list(source.ledger, MAX_LEDGER, 'ledger').map(item => {
      const entry = record(item, 'ledger');
      return { label: text(entry.label, SHORT, 'ledger', true), value: cell(entry.value, SHORT, 'ledger'),
        unit: text(entry.unit, SHORT, 'ledger'), ...optional(entry, 'note', note => text(note, LONG, 'ledger')) };
    }),
    sections,
    actions: list(source.actions, MAX_ACTIONS, 'actions').map(item => {
      const action = record(item, 'actions');
      if (!(SEVERITIES as readonly unknown[]).includes(action.severity)) invalid('actions');
      return { severity: action.severity as Action['severity'], title: text(action.title, SHORT, 'actions', true),
        detail: text(action.detail, LONG, 'actions'), source: text(action.source, SHORT, 'actions') };
    }),
    dataBasis: list(source.dataBasis, MAX_DATA_BASIS, 'dataBasis').map(item => {
      const basis = record(item, 'dataBasis');
      return { source: text(basis.source, SHORT, 'dataBasis', true), query: text(basis.query, LONG, 'dataBasis'),
        exclusions: text(basis.exclusions, LONG, 'dataBasis'),
        ...optional(basis, 'estimates', estimates => text(estimates, LONG, 'dataBasis')) };
    }),
  } as PerformanceReport;
  if (encodedSize(withServerDefaults(report)) > MAX_REPORT_BYTES) invalid('size');
  return report;
}

const str = (maxLength: number) => ({ type: 'string', maxLength });
const strings = (maxItems: number, maxLength: number) => ({ type: 'array', maxItems, items: str(maxLength) });
const object = (properties: Record<string, unknown>, required: string[]) =>
  ({ type: 'object', additionalProperties: false, properties, required });

/**
 * JSON Schema of the model's part of the report, passed to `--json-schema`.
 * The CLI owns schemaVersion, periodKey, period, alias, generatedAt, model and
 * highlights and overwrites them; `validateReport` is the gate before upload.
 */
export const REPORT_JSON_SCHEMA = object({
  partial: { type: 'boolean' },
  missingSources: strings(MAX_MISSING, SHORT),
  summary: str(LONG),
  ledger: { type: 'array', maxItems: MAX_LEDGER, items: object(
    { label: str(SHORT), value: str(SHORT), unit: str(SHORT), note: str(LONG) }, ['label', 'value', 'unit']) },
  sections: { type: 'array', maxItems: MAX_SECTIONS, items: object({
    id: { type: 'string', pattern: SECTION_ID.source },
    title: str(SHORT),
    lede: str(LONG),
    paragraphs: strings(MAX_PARAGRAPHS, LONG),
    tables: { type: 'array', maxItems: MAX_TABLES, items: object({
      columns: { ...strings(MAX_COLUMNS, SHORT), minItems: 1 },
      rows: { type: 'array', maxItems: MAX_ROWS, items: strings(MAX_COLUMNS, LONG) },
    }, ['columns', 'rows']) },
    cards: { type: 'array', maxItems: MAX_CARDS, items: object({ title: str(SHORT), body: str(LONG) }, ['title', 'body']) },
  }, ['id', 'title', 'lede', 'paragraphs']) },
  actions: { type: 'array', maxItems: MAX_ACTIONS, items: object({
    severity: { type: 'string', enum: [...SEVERITIES] }, title: str(SHORT), detail: str(LONG), source: str(SHORT),
  }, ['severity', 'title', 'detail', 'source']) },
  dataBasis: { type: 'array', maxItems: MAX_DATA_BASIS, items: object({
    source: str(SHORT), query: str(LONG), exclusions: str(LONG), estimates: str(LONG),
  }, ['source', 'query', 'exclusions']) },
}, ['partial', 'missingSources', 'summary', 'ledger', 'sections', 'actions', 'dataBasis']);
