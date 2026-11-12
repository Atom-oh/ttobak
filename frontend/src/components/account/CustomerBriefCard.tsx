'use client';

import Link from 'next/link';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { accountApi, dictionaryApi } from '@/lib/api';
import {
  BRIEF_COPY, GENERIC_FAILURE, POLL_INTERVAL_MS, SECTION_TITLES,
  addTermInput, addTermOutcome, applyResponse, formatGeneratedAt, formatLocalDateTime, hidesCard, pollDecision, postErrorAction,
  sourceHref, sourceKindLabel,
  type AddTermOutcome, type Brief, type BriefSource, type BriefState, type BriefStatement,
} from '@/lib/customerBrief';

type View =
  | { phase: 'loading' }
  | { phase: 'hidden' }
  | { phase: 'loadError' }
  | { phase: 'ready'; state: BriefState; receivedAt: number };

const errorStatus = (failure: unknown) => (failure as { status?: unknown } | null)?.status;

const primaryButton = 'min-h-11 rounded-lg bg-primary px-4 text-sm font-semibold text-white dark:text-[#0b0a1a] hover:bg-primary-hover disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary';
const secondaryButton = 'min-h-11 rounded-lg border border-border-subtle px-4 text-sm font-semibold text-text-main hover:bg-surface-container disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary';

/**
 * The caller's private pre-meeting brief. Renders nothing when the API
 * answers 404 (not a direct member, or the backend is not deployed yet).
 * All brief text is model output rendered as plain React text.
 */
const REQUEST_TIMEOUT_MS = 30_000;

export function CustomerBriefCard({ accountId, userId }: { accountId: string; userId: string }) {
  const [view, setView] = useState<View>({ phase: 'loading' });
  const [busy, setBusy] = useState(false);
  const [pollStartedAt, setPollStartedAt] = useState<number | null>(null);
  const [timedOut, setTimedOut] = useState(false);
  const [pollTick, setPollTick] = useState(0);
  const [visible, setVisible] = useState(() => typeof document === 'undefined' || !document.hidden);
  const busyRef = useRef(false);
  const getRef = useRef<AbortController | null>(null);
  const postRef = useRef<AbortController | null>(null);
  // The last accepted brief; POST answers never carry one, so it is retained.
  const briefRef = useRef<Brief | null>(null);

  const accept = useCallback((state: BriefState) => {
    const now = Date.now();
    briefRef.current = state.brief;
    setView({ phase: 'ready', state, receivedAt: now });
    if (state.active) {
      setPollStartedAt(previous => previous ?? now);
    } else {
      setPollStartedAt(null);
      setTimedOut(false);
    }
  }, []);

  const load = useCallback(async (kind: 'initial' | 'polling' | 'manual') => {
    getRef.current?.abort();
    const controller = new AbortController();
    getRef.current = controller;
    // A request that never settles must not hold up the next poll or the
    // ten-minute limit, so each read gives up after REQUEST_TIMEOUT_MS.
    let expired = false;
    const timer = setTimeout(() => {
      expired = true;
      controller.abort();
    }, REQUEST_TIMEOUT_MS);
    try {
      const response = await accountApi.customerBrief.get(accountId, { signal: controller.signal, expectedUserId: userId });
      if (controller.signal.aborted) return;
      accept(applyResponse(briefRef.current, response, 'get'));
    } catch (failure) {
      if (controller.signal.aborted && !expired) return;
      if (hidesCard(errorStatus(failure))) {
        setView({ phase: 'hidden' });
      } else if (kind === 'initial') {
        setView({ phase: 'loadError' });
      }
    } finally {
      clearTimeout(timer);
      if (getRef.current === controller) getRef.current = null;
      if ((!controller.signal.aborted || expired) && kind === 'polling') setPollTick(tick => tick + 1);
    }
  }, [accountId, userId, accept]);

  useEffect(() => {
    void load('initial');
    return () => {
      getRef.current?.abort();
      postRef.current?.abort();
    };
  }, [load]);

  useEffect(() => {
    const onVisibility = () => setVisible(!document.hidden);
    document.addEventListener('visibilitychange', onVisibility);
    return () => document.removeEventListener('visibilitychange', onVisibility);
  }, []);

  const active = view.phase === 'ready' && view.state.active;
  useEffect(() => {
    if (!active || pollStartedAt === null || timedOut || !visible) return;
    const timer = window.setTimeout(() => {
      const decision = pollDecision({ active: true, visible: !document.hidden, startedAt: pollStartedAt, now: Date.now() });
      if (decision === 'timeout') setTimedOut(true);
      else if (decision === 'pollNext') void load('polling');
    }, POLL_INTERVAL_MS);
    return () => window.clearTimeout(timer);
  }, [active, pollStartedAt, timedOut, visible, pollTick, load]);

  const requestBrief = async () => {
    if (busyRef.current || active) return;
    busyRef.current = true;
    setBusy(true);
    const controller = new AbortController();
    postRef.current = controller;
    const kept = briefRef.current;
    try {
      const response = await accountApi.customerBrief.request(accountId, { signal: controller.signal, expectedUserId: userId });
      if (controller.signal.aborted) return;
      setPollStartedAt(null);
      setTimedOut(false);
      accept(applyResponse(kept, response, 'request'));
    } catch (failure) {
      if (controller.signal.aborted) return;
      const action = postErrorAction(errorStatus(failure));
      if (action === 'hideCard') {
        setView({ phase: 'hidden' });
      } else if (action === 'awaitRun') {
        setPollStartedAt(null);
        setTimedOut(false);
        accept({ status: 'queued', active: true, brief: kept, failure: null });
      } else {
        accept({ status: 'failed', active: false, brief: kept, failure: GENERIC_FAILURE });
      }
    } finally {
      if (postRef.current === controller) postRef.current = null;
      busyRef.current = false;
      setBusy(false);
    }
  };

  const recheck = async () => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    setTimedOut(false);
    setPollStartedAt(Date.now());
    try {
      await load('manual');
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  if (view.phase === 'hidden') return null;

  const state = view.phase === 'ready' ? view.state : null;
  const brief = state?.brief ?? null;
  const statusLine = timedOut ? BRIEF_COPY.slow : state?.active ? BRIEF_COPY.running : state?.failure ?? null;
  const generated = brief && view.phase === 'ready' ? formatGeneratedAt(brief.generatedAt, view.receivedAt) : null;
  const disabled = busy || !!state?.active;

  return <section className="glass-panel mb-8 min-w-0 rounded-xl p-4" aria-labelledby="customer-brief-heading"
    aria-busy={view.phase === 'loading' || undefined}>
    <h3 id="customer-brief-heading" className="mb-3 text-base font-bold text-text-main">{BRIEF_COPY.title}</h3>

    <div aria-live="polite" className="empty:hidden">
      {statusLine && <div className="mb-3 flex flex-wrap items-center gap-2 text-sm text-text-secondary">
        <p className={state?.failure && !timedOut && !state.active ? 'text-error' : undefined}>{statusLine}</p>
        {timedOut && <button type="button" onClick={() => void recheck()} disabled={busy} className={secondaryButton}>{BRIEF_COPY.recheck}</button>}
      </div>}
    </div>

    {view.phase === 'loading' && <div className="space-y-2" aria-hidden="true">
      <div className="h-4 w-2/3 animate-pulse rounded bg-slate-200 dark:bg-white/5" />
      <div className="h-4 w-full animate-pulse rounded bg-slate-200 dark:bg-white/5" />
      <div className="h-4 w-5/6 animate-pulse rounded bg-slate-200 dark:bg-white/5" />
    </div>}

    {view.phase === 'loadError' && <div className="flex flex-wrap items-center gap-2 text-sm text-text-secondary">
      <p>{BRIEF_COPY.loadFailed}</p>
      <button type="button" className={secondaryButton} onClick={() => { setView({ phase: 'loading' }); void load('initial'); }}>{BRIEF_COPY.reload}</button>
    </div>}

    {state && !brief && !state.active && <div className="space-y-3">
      <p className="text-sm text-text-secondary">{BRIEF_COPY.intro}</p>
      <button type="button" className={primaryButton} onClick={() => void requestBrief()} disabled={disabled}>{BRIEF_COPY.create}</button>
    </div>}

    {brief && <BriefBody brief={brief} accountId={accountId} />}

    {brief && <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-3">
      <span className="text-xs text-text-secondary">{generated ? `${generated} 생성` : ''}</span>
      <button type="button" className={secondaryButton} onClick={() => void requestBrief()} disabled={disabled}>{BRIEF_COPY.regenerate}</button>
    </div>}
  </section>;
}

/** Adds one unfamiliar term to this customer's dictionary layer. */
function AddTermButton({ accountId, term }: { accountId: string; term: string }) {
  const [phase, setPhase] = useState<'idle' | 'saving' | AddTermOutcome>('idle');
  const input = addTermInput(term);
  if (!input) return null;
  const add = async () => {
    if (phase === 'saving' || phase === 'added') return;
    setPhase('saving');
    try {
      await dictionaryApi.createLayerTerm(`account:${accountId}`, input);
      setPhase('added');
    } catch (failure) {
      setPhase(addTermOutcome(failure));
    }
  };
  if (phase === 'added') return <span role="status" className="text-xs text-text-secondary">{BRIEF_COPY.termAdded}</span>;
  const note = phase === 'full' ? BRIEF_COPY.termFull : phase === 'denied' ? BRIEF_COPY.termDenied : phase === 'failed' ? BRIEF_COPY.termFailed : '';
  return <>
    <button type="button" disabled={phase === 'saving' || phase === 'full' || phase === 'denied'} onClick={() => void add()}
      aria-label={`${term} ${BRIEF_COPY.addTerm}`}
      className="rounded-sm text-xs font-semibold text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 disabled:opacity-50">
      {phase === 'saving' ? BRIEF_COPY.addingTerm : BRIEF_COPY.addTerm}</button>
    {note && <span role="alert" className="text-xs text-error">{note}</span>}
  </>;
}

function BriefBody({ brief, accountId }: { brief: Brief; accountId: string }) {
  const { nextMeeting, situation, sinceLast, news, questions, terms } = brief.content;
  const startsAt = nextMeeting ? formatLocalDateTime(nextMeeting.startsAt) : null;
  return <div className="space-y-4 text-sm text-text-main">
    {nextMeeting && <BriefSection title={SECTION_TITLES.nextMeeting}>
      <p className="font-semibold">{nextMeeting.title}</p>
      {startsAt && <p className="text-text-secondary">{startsAt}</p>}
      {nextMeeting.source && <SourceChips sources={[nextMeeting.source]} />}
    </BriefSection>}
    {situation && <BriefSection title={SECTION_TITLES.situation}>
      <p className="font-semibold leading-relaxed">{situation.text}</p>
      <SourceChips sources={situation.sources} />
    </BriefSection>}
    {sinceLast.length > 0 && <BriefSection title={SECTION_TITLES.sinceLast}><StatementList items={sinceLast} /></BriefSection>}
    {news.length > 0 && <BriefSection title={SECTION_TITLES.news}>
      <ul className="space-y-3">
        {news.map((item, index) => <li key={index} className="min-w-0">
          <p className="break-words">{item.text}</p>
          {item.why && <p className="mt-0.5 break-words text-text-secondary"><span className="font-semibold">{BRIEF_COPY.why}</span> · {item.why}</p>}
          <SourceChips sources={item.sources} />
        </li>)}
      </ul>
    </BriefSection>}
    {questions.length > 0 && <BriefSection title={SECTION_TITLES.questions}><StatementList items={questions} /></BriefSection>}
    {terms.length > 0 && <BriefSection title={SECTION_TITLES.terms}>
      <ul className="space-y-3">
        {terms.map((item, index) => <li key={index} className="min-w-0">
          <p className="flex flex-wrap items-center gap-2">
            <span className="font-semibold">{item.term}</span>
            <span className="rounded-full border border-border-subtle px-2 py-0.5 text-xs text-text-secondary">
              {item.inDictionary ? BRIEF_COPY.inDictionary : BRIEF_COPY.notInDictionary}
            </span>
            {!item.inDictionary && <AddTermButton accountId={accountId} term={item.term} />}
          </p>
          {item.gloss && <p className="mt-0.5 break-words text-text-secondary">{item.gloss}</p>}
          <SourceChips sources={item.sources} />
        </li>)}
      </ul>
    </BriefSection>}
  </div>;
}

function BriefSection({ title, children }: { title: string; children: ReactNode }) {
  return <div className="min-w-0">
    <h4 className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-text-secondary">{title}</h4>
    {children}
  </div>;
}

function StatementList({ items }: { items: BriefStatement[] }) {
  return <ul className="list-disc space-y-2 pl-5">
    {items.map((item, index) => <li key={index} className="min-w-0">
      <p className="break-words">{item.text}</p>
      <SourceChips sources={item.sources} />
    </li>)}
  </ul>;
}

function SourceChips({ sources }: { sources: BriefSource[] }) {
  if (sources.length === 0) return null;
  return <ul className="mt-1 flex min-w-0 flex-wrap gap-1" aria-label={BRIEF_COPY.sourcesLabel}>
    {sources.map((source, index) => <li key={`${source.kind}:${source.id}:${index}`} className="min-w-0 max-w-full">
      <SourceChip source={source} />
    </li>)}
  </ul>;
}

function SourceChip({ source }: { source: BriefSource }) {
  const kind = sourceKindLabel(source.kind);
  const href = sourceHref(source);
  const body = <>
    <span className="shrink-0 font-semibold">{kind}</span>
    <span className="min-w-0 max-w-[14rem] truncate">{source.label}</span>
  </>;
  const className = 'inline-flex max-w-full items-center gap-1 rounded-full bg-surface-container px-2 py-0.5 text-xs text-text-secondary';
  const title = `${kind} · ${source.label}`;
  return href
    ? <Link href={href} title={title} className={`${className} hover:text-primary focus-visible:outline-2 focus-visible:outline-primary`}>{body}</Link>
    : <span title={title} className={className}>{body}</span>;
}
