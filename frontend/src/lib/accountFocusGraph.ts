import type { AccountFocusGraph, AccountPerson, FocusKind, FocusRef, FocusTech } from '@/types/meeting';

const FOCUS_KINDS: readonly FocusKind[] = ['contact', 'member', 'project'];

/** Parses `contact:{id}`, `member:{id}` or `project:{id}`; anything else is null. */
export function parseFocus(value: string | null | undefined): FocusRef | null {
  const text = String(value ?? '');
  const at = text.indexOf(':');
  if (at <= 0) return null;
  const kind = text.slice(0, at) as FocusKind;
  const id = text.slice(at + 1);
  if (!FOCUS_KINDS.includes(kind) || !id || /[\s#:]/.test(id) || id.length > 128) return null;
  return { kind, id };
}

export function focusParam(ref: FocusRef): string {
  return `${ref.kind}:${ref.id}`;
}

export function sameFocus(a: FocusRef | null, b: FocusRef | null): boolean {
  return !!a && !!b && a.kind === b.kind && a.id === b.id;
}

export interface FocusOption { ref: FocusRef; label: string; meetings: number }

/** Picker options: contacts, then members (each by meeting count), then projects. */
export function focusOptions(people: AccountPerson[], projects: { id: string; name: string }[]): FocusOption[] {
  const byCount = (a: FocusOption, b: FocusOption) => b.meetings - a.meetings || a.label.localeCompare(b.label, 'ko');
  const persons = (kind: 'contact' | 'member') => people
    .filter(p => p.kind === kind && p.label)
    .map(p => ({ ref: { kind, id: p.id }, label: p.label, meetings: Number(p.meetings) || 0 }))
    .sort(byCount);
  const seen = new Set<string>();
  const projectOptions = projects
    .filter(p => p.id && p.name && !seen.has(p.id) && (seen.add(p.id), true))
    .map(p => ({ ref: { kind: 'project' as const, id: p.id }, label: p.name, meetings: 0 }));
  return [...persons('contact'), ...persons('member'), ...projectOptions];
}

/** The contact with the most meetings, else the busiest member, else the first project. */
export function defaultFocus(options: FocusOption[]): FocusRef | null {
  const person = options.find(o => o.ref.kind !== 'project' && o.meetings > 0 && o.ref.kind === 'contact')
    ?? options.find(o => o.ref.kind === 'member' && o.meetings > 0);
  return (person ?? options.find(o => o.ref.kind === 'project'))?.ref ?? null;
}

// Fixed geometry: rows have fixed heights so connectors need no DOM measuring.
export const FOCUS_ANCHOR_Y = 44;
export const MEETING_ROW = 60;
export const TECH_ROW = 44;
export const LEFT_BAND = 56;
export const RIGHT_BAND = 96;

export interface FocusEdge { d: string; meetingIndex: number; techId?: string }
export interface FocusLayout {
  height: number;
  left: FocusEdge[];
  right: FocusEdge[];
  techY: Record<string, number>;
}

function curve(width: number, y1: number, y2: number): string {
  const c = width / 2;
  return `M0 ${y1} C${c} ${y1} ${c} ${y2} ${width} ${y2}`;
}

/** Connector geometry for the focus → meetings and meetings → techs bands. */
export function layoutFocusGraph(graph: Pick<AccountFocusGraph, 'meetings' | 'techs'>): FocusLayout {
  const meetings = graph.meetings ?? [];
  const techs = graph.techs ?? [];
  const techY: Record<string, number> = {};
  techs.forEach((t, i) => { techY[t.id] = i * TECH_ROW + TECH_ROW / 2; });
  const meetingY = (i: number) => i * MEETING_ROW + MEETING_ROW / 2;
  const left = meetings.map((_, i) => ({ d: curve(LEFT_BAND, FOCUS_ANCHOR_Y, meetingY(i)), meetingIndex: i }));
  const right: FocusEdge[] = [];
  meetings.forEach((m, i) => {
    for (const id of m.techs ?? []) {
      if (techY[id] === undefined) continue;
      right.push({ d: curve(RIGHT_BAND, meetingY(i), techY[id]), meetingIndex: i, techId: id });
    }
  });
  const height = Math.max(meetings.length * MEETING_ROW, techs.length * TECH_ROW, FOCUS_ANCHOR_Y * 2);
  return { height, left, right, techY };
}

/** Tech group color: [light, dark]. Unknown groups use slate. */
const TONES: Record<string, [string, string]> = {
  containers: ['#0f766e', '#2dd4bf'],
  compute: ['#0369a1', '#7dd3fc'],
  ai: ['#6d28d9', '#c4b5fd'],
  database: ['#b45309', '#fcd34d'],
  analytics: ['#0e7490', '#67e8f9'],
  migration: ['#be123c', '#fda4af'],
  platform: ['#475569', '#cbd5e1'],
};

export function techTone(group: string, dark: boolean): string {
  const tone = TONES[group] ?? TONES.platform;
  return dark ? tone[1] : tone[0];
}

/** `2026-11-03T…` → `26.11.03`; invalid input → ''. */
export function shortDate(iso: string): string {
  const m = /^(\d{2})(\d{2})-(\d{2})-(\d{2})/.exec(String(iso ?? ''));
  return m ? `${m[2]}.${m[3]}.${m[4]}` : '';
}

/** Meeting route id: the part after the owner of `{ownerUserId}:{meetingId}`. */
export function meetingRouteId(linkId: string): string {
  const at = String(linkId ?? '').indexOf(':');
  return at >= 0 ? linkId.slice(at + 1) : '';
}

export function techCountMax(techs: FocusTech[]): number {
  return techs.reduce((max, t) => Math.max(max, t.meetings), 0);
}
