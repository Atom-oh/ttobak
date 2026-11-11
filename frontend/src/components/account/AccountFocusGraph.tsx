'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { accountApi, ApiError } from '@/lib/api';
import { roleLabel } from '@/lib/accountPeople';
import { TRUNCATED_NOTICE } from '@/lib/accountOverview';
import { useTheme } from '@/hooks/useTheme';
import {
  FOCUS_ANCHOR_Y, LEFT_BAND, MEETING_ROW, RIGHT_BAND, TECH_ROW,
  defaultFocus, focusOptions, focusParam, layoutFocusGraph, meetingRouteId, parseFocus, sameFocus,
  shortDate, techCountMax, techTone, type FocusOption,
} from '@/lib/accountFocusGraph';
import type { AccountFocusGraph as Graph, AccountPerson, FocusMeeting, FocusRef, FocusTech, PeopleRole } from '@/types/meeting';

type Props = { accountId: string; userId: string; projects: { id: string; name: string }[]; refreshKey: unknown };

const ROLE_ICONS: Record<PeopleRole, string> = {
  participant: 'person', speaker: 'record_voice_over', assignee: 'task_alt', mentions: 'forum',
};

const KIND_ICON: Record<string, string> = { contact: 'account_circle', member: 'groups', project: 'work' };
const KIND_LABEL: Record<string, string> = { contact: '고객', member: '우리 팀', project: '프로젝트' };

function readUrlFocus(): FocusRef | null {
  try { return parseFocus(new URLSearchParams(window.location.search).get('focus')); } catch { return null; }
}

function writeUrlFocus(ref: FocusRef) {
  try {
    const url = new URL(window.location.href);
    url.searchParams.set('focus', focusParam(ref));
    window.history.replaceState(window.history.state, '', url);
  } catch { /* URL sync is a convenience */ }
}

function errorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 404) return '선택한 사람이나 프로젝트를 볼 수 없습니다. 다른 대상을 고르세요.';
  if (e instanceof ApiError && e.status === 403) return '관계 그래프는 이 고객의 직접 멤버만 볼 수 있습니다.';
  return '관계 그래프를 불러오지 못했습니다.';
}

/** Person- or project-centered graph: focus → meetings → technologies. */
export function AccountFocusGraph({ accountId, userId, projects, refreshKey }: Props) {
  const { theme } = useTheme();
  const dark = theme === 'dark';
  const [people, setPeople] = useState<AccountPerson[]>([]);
  const [peopleLoaded, setPeopleLoaded] = useState(false);
  const [focus, setFocus] = useState<FocusRef | null>(null);
  const [graph, setGraph] = useState<Graph | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState('');
  const [retry, setRetry] = useState(0);
  const [tech, setTech] = useState<string | null>(null);
  const [hover, setHover] = useState<number | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [showOther, setShowOther] = useState(false);
  const generation = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    accountApi.people.list(accountId, '', { signal: controller.signal, expectedUserId: userId })
      .then(page => { if (!controller.signal.aborted) setPeople(page.people ?? []); })
      .catch(() => { /* the picker falls back to projects */ })
      .finally(() => { if (!controller.signal.aborted) setPeopleLoaded(true); });
    return () => controller.abort();
  }, [accountId, userId, refreshKey]);

  const options = useMemo(() => focusOptions(people, projects), [people, projects]);

  useEffect(() => {
    if (focus || !peopleLoaded) return;
    const initial = readUrlFocus() ?? defaultFocus(options);
    if (initial) setFocus(initial);
  }, [focus, peopleLoaded, options]);

  useEffect(() => {
    if (!focus) return;
    const controller = new AbortController();
    const run = ++generation.current;
    setLoading(true);
    setError('');
    setTech(null);
    setSelected(null);
    setShowOther(false);
    accountApi.people.focus(accountId, focusParam(focus), '', { signal: controller.signal, expectedUserId: userId })
      .then(g => { if (run === generation.current) setGraph(g); })
      .catch(e => { if (!controller.signal.aborted && run === generation.current) { setGraph(null); setError(errorText(e)); } })
      .finally(() => { if (run === generation.current) setLoading(false); });
    return () => controller.abort();
  }, [accountId, userId, focus, refreshKey, retry]);

  const choose = useCallback((ref: FocusRef) => {
    if (sameFocus(ref, focus)) return;
    writeUrlFocus(ref);
    setFocus(ref);
  }, [focus]);

  const loadMore = useCallback(async () => {
    if (!graph?.nextCursor || !focus) return;
    const run = generation.current;
    setLoadingMore(true);
    try {
      const next = await accountApi.people.focus(accountId, focusParam(focus), graph.nextCursor, { expectedUserId: userId });
      if (run !== generation.current) return;
      setGraph(prev => prev && { ...next, meetings: [...prev.meetings, ...next.meetings] });
    } catch (e) {
      if (run === generation.current) setError(errorText(e));
    } finally {
      if (run === generation.current) setLoadingMore(false);
    }
  }, [accountId, userId, focus, graph]);

  const layout = useMemo(() => layoutFocusGraph(graph ?? { meetings: [], techs: [] }), [graph]);
  const maxCount = techCountMax(graph?.techs ?? []);
  const techById = useMemo(() => new Map((graph?.techs ?? []).map(t => [t.id, t])), [graph]);
  const selectedMeeting = graph?.meetings.find(m => m.id === selected) ?? null;
  const dims = (m: FocusMeeting) => !!tech && !m.techs.includes(tech);

  if (peopleLoaded && options.length === 0) {
    return <p className="text-sm text-text-secondary">연락처나 프로젝트가 아직 없습니다. 고객 연락처를 추가하거나 프로젝트를 연결하면 관계 그래프가 그려집니다.</p>;
  }

  return <div className="space-y-4">
    <FocusPicker options={options} focus={focus} onChoose={choose} />
    {error && <div role="alert" className="flex items-center gap-3 text-sm text-error">
      {error}
      <button type="button" onClick={() => setRetry(n => n + 1)} className="rounded-md border border-border-subtle px-2 py-1 text-xs font-medium text-text-main hover:bg-surface-container">다시 시도</button>
    </div>}
    {loading && !graph && <div className="flex justify-center p-8"><div className="h-6 w-6 animate-spin rounded-full border-2 border-primary border-t-transparent" /></div>}
    {graph && <div className={`relative transition-opacity ${loading ? 'opacity-60' : ''}`} aria-busy={loading}>
      {graph.truncated && <p role="status" className="mb-2 text-xs text-text-secondary">{TRUNCATED_NOTICE}</p>}

      {/* Mobile: technology filter chips above the meeting list. */}
      {graph.techs.length > 0 && <div className="mb-3 flex flex-wrap gap-1.5 md:hidden">
        {graph.techs.map(t => <TechChip key={t.id} tech={t} dark={dark} active={tech === t.id} onClick={() => setTech(v => v === t.id ? null : t.id)} />)}
      </div>}

      <div className="md:grid md:items-start" style={{ gridTemplateColumns: `minmax(10rem,13rem) ${LEFT_BAND}px minmax(0,1fr) ${RIGHT_BAND}px minmax(9rem,12rem)` }}>
        <FocusCardView graph={graph} />

        <svg className="hidden md:block" width={LEFT_BAND} height={layout.height} aria-hidden="true">
          {layout.left.map(e => {
            const m = graph.meetings[e.meetingIndex];
            const active = hover === e.meetingIndex || selected === m.id;
            return <path key={e.meetingIndex} d={e.d} fill="none" strokeLinecap="round"
              className={active ? 'stroke-primary' : 'stroke-slate-300 dark:stroke-white/15'}
              strokeWidth={active ? 2 : 1.25} opacity={dims(m) ? 0.25 : 1} />;
          })}
          <circle cx={3} cy={FOCUS_ANCHOR_Y} r={3} className="fill-primary" />
        </svg>

        <ol className="mt-3 md:mt-0" aria-label="미팅">
          {graph.meetings.length === 0 && <li className="rounded-lg border border-dashed border-border-subtle p-4 text-sm text-text-secondary">
            {graph.focus.kind === 'project' ? '이 프로젝트에 연결된 미팅이 아직 없습니다.' : '이 사람이 참석자로 연결된 미팅이 아직 없습니다. 미팅의 참석자를 연결하면 여기에 나타납니다.'}
          </li>}
          {graph.meetings.map((m, i) => <MeetingRow key={m.id} meeting={m} techById={techById} dark={dark}
            selected={selected === m.id} dimmed={dims(m)} onHover={v => setHover(v ? i : null)}
            onSelect={() => setSelected(v => v === m.id ? null : m.id)} />)}
        </ol>

        <svg className="hidden md:block" width={RIGHT_BAND} height={layout.height} aria-hidden="true">
          {layout.right.map(e => {
            const m = graph.meetings[e.meetingIndex];
            const t = techById.get(e.techId!)!;
            const active = hover === e.meetingIndex || selected === m.id || tech === e.techId;
            const faded = (!!tech && tech !== e.techId) || (hover !== null && hover !== e.meetingIndex && !tech);
            return <path key={`${e.meetingIndex}-${e.techId}`} d={e.d} fill="none" strokeLinecap="round"
              stroke={techTone(t.group, dark)} strokeDasharray="4 4" strokeWidth={active ? 2 : 1.25}
              opacity={faded ? 0.12 : active ? 0.95 : 0.45} />;
          })}
        </svg>

        <div className="hidden md:block" role="group" aria-label="기술셋">
          {graph.techs.length === 0 && <p className="pt-3 text-xs text-text-secondary">태그된 기술이 없습니다.</p>}
          {graph.techs.map(t => <TechNode key={t.id} tech={t} max={maxCount} dark={dark}
            active={tech === t.id} onClick={() => setTech(v => v === t.id ? null : t.id)} />)}
        </div>
      </div>

      {graph.nextCursor && <button type="button" onClick={loadMore} disabled={loadingMore}
        className="mt-3 rounded-lg border border-border-subtle px-3 py-1.5 text-sm font-medium text-text-main hover:bg-surface-container disabled:opacity-50 md:ml-[calc(13rem+56px)]">
        {loadingMore ? '불러오는 중…' : '이전 미팅 더 보기'}
      </button>}

      <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-text-secondary">
        <span className="inline-flex items-center gap-1.5">
          <svg width="22" height="6" aria-hidden="true"><path d="M1 3H21" stroke="currentColor" strokeDasharray="4 4" /></svg>
          기술 연결은 미팅 요약에서 자동 추출한 태그 기준입니다
        </span>
        {graph.other.length > 0 && <button type="button" aria-expanded={showOther} onClick={() => setShowOther(v => !v)}
          className="underline decoration-dotted underline-offset-2 hover:text-text-main">
          목록에 없는 태그 {graph.other.length}개 {showOther ? '숨기기' : '보기'}
        </button>}
      </div>
      {showOther && <ul className="mt-2 flex flex-wrap gap-1.5" aria-label="목록에 없는 태그">
        {graph.other.map(t => <li key={t.tag} className="rounded-md bg-surface-container px-2 py-0.5 text-xs text-text-secondary">{t.tag} <span className="tabular-nums">{t.meetings}</span></li>)}
      </ul>}

      {selectedMeeting && <MeetingPanel meeting={selectedMeeting} techById={techById} dark={dark}
        onClose={() => setSelected(null)} onFocus={choose} />}
    </div>}
  </div>;
}

function FocusPicker({ options, focus, onChoose }: { options: FocusOption[]; focus: FocusRef | null; onChoose: (ref: FocusRef) => void }) {
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const q = query.trim().toLowerCase();
  const matches = (q ? options.filter(o => o.label.toLowerCase().includes(q)) : options).slice(0, 12);
  const quick = [
    ...options.filter(o => o.ref.kind === 'contact' && o.meetings > 0).slice(0, 3),
    ...options.filter(o => o.ref.kind === 'project').slice(0, 1),
  ];
  const pick = (o: FocusOption) => { onChoose(o.ref); setQuery(''); setOpen(false); };
  return <div className="flex flex-wrap items-center gap-2">
    <div className="relative w-full sm:w-64">
      <span className="material-symbols-outlined pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[18px] text-text-secondary" aria-hidden="true">search</span>
      <input type="search" value={query} placeholder="사람이나 프로젝트 찾기" aria-label="그래프 중심 찾기"
        role="combobox" aria-expanded={open} aria-controls="focus-picker-list"
        onChange={e => { setQuery(e.target.value); setOpen(true); }} onFocus={() => setOpen(true)}
        onBlur={() => setTimeout(() => setOpen(false), 120)}
        onKeyDown={e => { if (e.key === 'Enter' && matches[0]) { e.preventDefault(); pick(matches[0]); } if (e.key === 'Escape') setOpen(false); }}
        className="w-full rounded-lg border border-border-subtle bg-surface py-1.5 pl-8 pr-3 text-sm text-text-main placeholder:text-text-secondary focus:outline-2 focus:outline-primary" />
      {open && matches.length > 0 && <ul id="focus-picker-list" role="listbox"
        className="absolute z-20 mt-1 max-h-72 w-full overflow-auto rounded-lg border border-border-subtle bg-surface py-1 shadow-lg">
        {matches.map(o => <li key={focusParam(o.ref)} role="option" aria-selected={sameFocus(o.ref, focus)}>
          <button type="button" onMouseDown={e => e.preventDefault()} onClick={() => pick(o)}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-text-main hover:bg-surface-container">
            <span className="material-symbols-outlined text-[16px] text-text-secondary" aria-hidden="true">{KIND_ICON[o.ref.kind]}</span>
            <span className="min-w-0 flex-1 truncate">{o.label}</span>
            <span className="text-xs text-text-secondary">{KIND_LABEL[o.ref.kind]}{o.meetings > 0 ? ` · 미팅 ${o.meetings}` : ''}</span>
          </button>
        </li>)}
      </ul>}
    </div>
    {quick.map(o => <button key={focusParam(o.ref)} type="button" onClick={() => onChoose(o.ref)} aria-pressed={sameFocus(o.ref, focus)}
      className={`inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs font-medium transition-colors ${sameFocus(o.ref, focus)
        ? 'border-primary bg-primary text-white dark:text-[#0b0a1a]'
        : 'border-border-subtle text-text-main hover:border-primary/60'}`}>
      <span className="material-symbols-outlined text-[14px]" aria-hidden="true">{KIND_ICON[o.ref.kind]}</span>{o.label}
    </button>)}
  </div>;
}

function FocusCardView({ graph }: { graph: Graph }) {
  const f = graph.focus;
  const initial = Array.from(f.label.trim())[0] ?? '?';
  return <div className="rounded-xl border border-primary/30 bg-primary/[0.04] p-3 dark:border-primary/40 dark:bg-primary/[0.08]" style={{ minHeight: FOCUS_ANCHOR_Y * 2 }}>
    <div className="flex items-center gap-2.5">
      <span className={`flex h-10 w-10 shrink-0 items-center justify-center bg-primary text-base font-bold text-white dark:text-[#0b0a1a] ${f.kind === 'project' ? 'rounded-lg' : 'rounded-full'}`} aria-hidden="true">
        {f.kind === 'project' ? <span className="material-symbols-outlined text-[20px]">work</span> : initial}
      </span>
      <div className="min-w-0">
        <p className="truncate text-sm font-bold text-text-main">{f.label}</p>
        <p className="truncate text-xs text-text-secondary">{f.detail || KIND_LABEL[f.kind]}</p>
      </div>
    </div>
    <p className="mt-2.5 text-xs text-text-secondary">미팅 <span className="text-sm font-semibold tabular-nums text-text-main">{f.meetings}</span>회
      {graph.techs.length > 0 && <> · 기술 <span className="text-sm font-semibold tabular-nums text-text-main">{graph.techs.length}</span>개</>}</p>
  </div>;
}

function MeetingRow({ meeting, techById, dark, selected, dimmed, onHover, onSelect }: {
  meeting: FocusMeeting; techById: Map<string, FocusTech>; dark: boolean; selected: boolean; dimmed: boolean;
  onHover: (v: boolean) => void; onSelect: () => void;
}) {
  return <li style={{ height: MEETING_ROW }} className={`flex items-center transition-opacity ${dimmed ? 'opacity-35' : ''}`}
    onMouseEnter={() => onHover(true)} onMouseLeave={() => onHover(false)}>
    <button type="button" onClick={onSelect} onFocus={() => onHover(true)} onBlur={() => onHover(false)} aria-expanded={selected}
      className={`flex h-[52px] w-full min-w-0 items-center gap-3 rounded-lg border px-3 text-left transition-colors focus-visible:outline-2 focus-visible:outline-primary ${selected
        ? 'border-primary bg-primary/[0.06] dark:bg-primary/[0.12]'
        : 'border-transparent hover:border-border-subtle hover:bg-surface-container'}`}>
      <time dateTime={meeting.date} className="w-[4.25rem] shrink-0 text-xs tabular-nums text-text-secondary">{shortDate(meeting.date)}</time>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium text-text-main">{meeting.title || '제목 없는 미팅'}</span>
        <span className="mt-0.5 flex items-center gap-1.5 md:hidden">
          {meeting.techs.map(id => techById.get(id)).filter(Boolean).map(t => <span key={t!.id} className="text-[11px] font-medium" style={{ color: techTone(t!.group, dark) }}>{t!.label}</span>)}
        </span>
      </span>
      {meeting.roles.length > 0 && <span className="flex shrink-0 items-center gap-0.5 text-text-secondary" aria-label={meeting.roles.map(roleLabel).join(', ')}>
        {meeting.roles.map(r => <span key={r} title={roleLabel(r)} className="material-symbols-outlined text-[16px]" aria-hidden="true">{ROLE_ICONS[r] ?? 'person'}</span>)}
      </span>}
    </button>
  </li>;
}

function TechNode({ tech, max, dark, active, onClick }: { tech: FocusTech; max: number; dark: boolean; active: boolean; onClick: () => void }) {
  const tone = techTone(tech.group, dark);
  return <div style={{ height: TECH_ROW }} className="flex items-center">
    <button type="button" onClick={onClick} aria-pressed={active}
      className={`relative w-full overflow-hidden rounded-lg border px-2.5 py-1.5 text-left transition-colors focus-visible:outline-2 focus-visible:outline-primary ${active ? '' : 'border-border-subtle hover:bg-surface-container'}`}
      style={active ? { borderColor: tone, boxShadow: `inset 3px 0 0 ${tone}` } : undefined}>
      <span className="absolute inset-y-0 left-0 opacity-[0.12]" style={{ width: `${max ? (tech.meetings / max) * 100 : 0}%`, background: tone }} aria-hidden="true" />
      <span className="relative flex items-center gap-2">
        <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: tone }} aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-text-main">{tech.label}</span>
        <span className="text-xs tabular-nums text-text-secondary" aria-label={`미팅 ${tech.meetings}회`}>{tech.meetings}</span>
      </span>
    </button>
  </div>;
}

function TechChip({ tech, dark, active, onClick }: { tech: FocusTech; dark: boolean; active: boolean; onClick: () => void }) {
  const tone = techTone(tech.group, dark);
  return <button type="button" onClick={onClick} aria-pressed={active}
    className="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium text-text-main"
    style={{ borderColor: active ? tone : undefined }}>
    <span className="h-1.5 w-1.5 rounded-full" style={{ background: tone }} aria-hidden="true" />
    {tech.label} <span className="tabular-nums text-text-secondary">{tech.meetings}</span>
  </button>;
}

function MeetingPanel({ meeting, techById, dark, onClose, onFocus }: {
  meeting: FocusMeeting; techById: Map<string, FocusTech>; dark: boolean; onClose: () => void; onFocus: (ref: FocusRef) => void;
}) {
  const contacts = meeting.people.filter(p => p.kind === 'contact');
  const members = meeting.people.filter(p => p.kind === 'member');
  const routeId = meetingRouteId(meeting.id);
  const group = (title: string, list: typeof meeting.people) => list.length > 0 && <div>
    <h5 className="mb-1.5 text-xs font-semibold text-text-secondary">{title}</h5>
    <ul className="space-y-1">
      {list.map(p => <li key={`${p.kind}:${p.id}`}>
        <button type="button" onClick={() => onFocus({ kind: p.kind, id: p.id })} title="이 사람 중심으로 보기"
          className="group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-surface-container focus-visible:outline-2 focus-visible:outline-primary">
          <span className="min-w-0 flex-1">
            <span className="block truncate text-sm text-text-main">{p.label}</span>
            <span className="block truncate text-xs text-text-secondary">{p.roles.map(roleLabel).filter(Boolean).join(', ')}</span>
          </span>
          <span className="material-symbols-outlined text-[16px] text-text-secondary opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" aria-hidden="true">zoom_in</span>
        </button>
      </li>)}
    </ul>
  </div>;
  return <aside aria-label="미팅 상세" className="mt-4 rounded-xl border border-border-subtle bg-surface p-4 shadow-sm md:absolute md:right-0 md:top-0 md:mt-0 md:w-80 md:shadow-xl">
    <div className="mb-3 flex items-start gap-2">
      <div className="min-w-0 flex-1">
        <p className="text-xs tabular-nums text-text-secondary">{shortDate(meeting.date)}</p>
        <h4 className="text-sm font-bold text-text-main">{meeting.title || '제목 없는 미팅'}</h4>
      </div>
      <button type="button" onClick={onClose} aria-label="닫기" className="rounded-md p-1 text-text-secondary hover:bg-surface-container">
        <span className="material-symbols-outlined text-[18px]" aria-hidden="true">close</span>
      </button>
    </div>
    {meeting.techs.length > 0 && <div className="mb-3 flex flex-wrap gap-1.5">
      {meeting.techs.map(id => techById.get(id)).filter(Boolean).map(t => <span key={t!.id}
        className="inline-flex items-center gap-1.5 rounded-full border border-border-subtle px-2 py-0.5 text-xs text-text-main">
        <span className="h-1.5 w-1.5 rounded-full" style={{ background: techTone(t!.group, dark) }} aria-hidden="true" />{t!.label}
      </span>)}
    </div>}
    <div className="space-y-3">
      {group('고객 참석자', contacts)}
      {group('우리 팀', members)}
      {meeting.people.length === 0 && <p className="text-xs text-text-secondary">연결된 참석자가 없습니다.</p>}
    </div>
    {routeId && <a href={`/meeting/${encodeURIComponent(routeId)}`}
      className="mt-4 inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline">
      <span className="material-symbols-outlined text-[16px]" aria-hidden="true">open_in_new</span>미팅 열기
    </a>}
  </aside>;
}
