'use client';

import { useState, useRef, useEffect, useCallback, useSyncExternalStore } from 'react';
import { qaApi } from '@/lib/api';
import { RealtimeWebSocket, type WebSocketMessage } from '@/lib/websocket';
import {
  claimedProactiveQuestions,
  proactiveGuard,
  proactiveBatchKey,
  proactiveSearchStore,
  rollbackProactiveClaimState,
} from '@/lib/proactiveSearch';
import { QAChatMessage, QASuggestedQuestions, QAEmptyState } from '@/components/qa';

interface LiveQAPanelProps {
  transcriptContext?: string;
  meetingId?: string;
  onDetectedQuestionsChange?: (count: number) => void;
  serverDetectedQuestions?: string[];
  /** Detected questions flagged as search-answerable — the panel auto-fires the first new one */
  proactiveQuestions?: string[];
  onAskedQuestion?: (question: string) => void;
  /** Save a Q&A entry into the meeting notes */
  onSaveToNotes?: (question: string, answer: string) => void;
}

interface QAEntry {
  id: string;
  question: string;
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  isStreaming?: boolean;
  /** AI-suggested follow-up questions for this answer */
  followUps?: string[];
  /** Auto-fired from a detected question (not typed by the user) */
  isProactive?: boolean;
}

const suggestedQuestions = [
  '주요 논의 사항은?',
  '결정된 액션 아이템은?',
  '핵심 키워드 정리해줘',
];

const WS_URL = process.env.NEXT_PUBLIC_WEBSOCKET_URL || '';

// Cross-instance proactive-search state (claim set, batch/in-flight guards,
// opt-in store) lives in lib/proactiveSearch.ts — both panel instances (the
// desktop aside and the mobile bottom sheet) stay mounted simultaneously
// during recording, so every one of these must be shared, and non-component
// callers (record page's recording-start reset, auth.ts's logout opt-in
// clear) need it without importing a component module.

/** Tail-truncate to at most maxBytes of UTF-8, without splitting a multi-byte char. */
function truncateToUtf8ByteLimit(text: string | undefined, maxBytes: number): string | undefined {
  if (!text) return text;
  const bytes = new TextEncoder().encode(text);
  if (bytes.length <= maxBytes) return text;
  let start = bytes.length - maxBytes;
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++; // skip stray continuation byte
  return new TextDecoder().decode(bytes.slice(start));
}

export function LiveQAPanel({ transcriptContext, meetingId, onDetectedQuestionsChange, serverDetectedQuestions, proactiveQuestions, onAskedQuestion, onSaveToNotes }: LiveQAPanelProps) {
  const [question, setQuestion] = useState('');
  const [qaHistory, setQaHistory] = useState<QAEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedEntryIds, setSavedEntryIds] = useState<Set<string>>(new Set());
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [detectedQuestions, setDetectedQuestions] = useState<string[]>([]);
  const [askedQuestions, setAskedQuestions] = useState<string[]>([]);
  const wsRef = useRef<RealtimeWebSocket | null>(null);
  const activeEntryIdRef = useRef<string | null>(null);
  // Proactive-search opt-in, synchronized across panel instances via the
  // module-level store (see proactiveSearchStore's comment). Server snapshot
  // is false so the static export renders the safe default.
  const proactiveSearchEnabled = useSyncExternalStore(
    proactiveSearchStore.subscribe,
    proactiveSearchStore.get,
    () => false,
  );
  const toggleProactiveSearch = useCallback(() => {
    proactiveSearchStore.set(!proactiveSearchStore.get());
  }, []);
  // entryId → proactive question, so failure paths (watchdog timeout,
  // answer_error, socket error, HTTP catch) can roll the claim back and let
  // a later detection batch retry the question instead of losing it forever.
  const proactiveClaimByEntryRef = useRef<Map<string, string>>(new Map());
  const rollbackProactiveClaim = useCallback((entryId: string | null) => {
    if (!entryId) return;
    const claimed = proactiveClaimByEntryRef.current.get(entryId);
    if (claimed) {
      proactiveClaimByEntryRef.current.delete(entryId);
      // Also un-consumes the (content-keyed) batch marker — see
      // rollbackProactiveClaimState's comment for why a rollback that left
      // the marker set would permanently consume the question.
      rollbackProactiveClaimState(claimed);
    }
  }, []);
  // A proactive question is recorded as "asked" only on SUCCESS: recording
  // it up front (like manual asks) would survive every failure path via
  // askedQuestions AND the parent's askedQuestionsRef (detect-questions'
  // previousQuestions), so the backend would never re-suggest it and the
  // `!askedQuestions.includes(q)` guard would block a retry — silently
  // defeating the claim rollback above.
  const recordProactiveAsked = useCallback((entryId: string) => {
    const q = proactiveClaimByEntryRef.current.get(entryId);
    if (!q) return;
    proactiveClaimByEntryRef.current.delete(entryId);
    proactiveGuard.askInFlight = false;
    setAskedQuestions(prev => (prev.includes(q) ? prev : [...prev, q]));
    onAskedQuestion?.(q);
  }, [onAskedQuestion]);
  // Watchdog for the WS streaming path: if no message arrives for the active
  // entry within this window, surface an error and unlock the input instead of
  // spinning forever on a stalled stream.
  const watchdogRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const [sessionId, setSessionId] = useState(() => `qa-${meetingId || 'live'}-${Date.now()}`);
  useEffect(() => {
    setSessionId(`qa-${meetingId || 'live'}-${Date.now()}`);
  }, [meetingId]);

  const clearWatchdog = useCallback(() => {
    if (watchdogRef.current) {
      clearTimeout(watchdogRef.current);
      watchdogRef.current = null;
    }
  }, []);

  const armWatchdog = useCallback(() => {
    clearWatchdog();
    watchdogRef.current = setTimeout(() => {
      watchdogRef.current = null;
      const entryId = activeEntryIdRef.current;
      if (!entryId) return;
      setQaHistory(prev =>
        prev.map(e =>
          e.id === entryId
            ? {
                ...e,
                // A partial answer must not silently pass for a complete one
                // -- later questions build on this history.
                answer: e.answer
                  ? e.answer + '\n\n> ⚠️ 응답 시간 초과 — 답변이 여기서 잘렸을 수 있습니다.'
                  : '응답 시간 초과 — 다시 시도해주세요.',
                isStreaming: false,
              }
            : e
        )
      );
      rollbackProactiveClaim(entryId);
      setIsAsking(false);
      activeEntryIdRef.current = null;
      inputRef.current?.focus();
      // The stalled request/socket is still live server-side — a late
      // delta/complete for it would otherwise land on whatever entry becomes
      // active next (there's no per-request id, only activeEntryIdRef).
      // Dropping the socket here stops that at the source; ensureWebSocket
      // opens a fresh one for the next question.
      wsRef.current?.disconnect();
      wsRef.current = null;
      // The zombie Lambda invocation also keeps running server-side and
      // will save_session (a whole-item put) when it eventually finishes.
      // Rotating the session id means the NEXT question writes a different
      // session item, so the zombie's late save can't race/clobber it.
      // Conversation continuity for the timed-out thread is deliberately
      // sacrificed — correctness over context.
      setSessionId(`qa-${meetingId || 'live'}-${Date.now()}`);
    }, 60_000);
  }, [clearWatchdog, meetingId, rollbackProactiveClaim]);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [qaHistory]);

  // Merge server-detected questions
  useEffect(() => {
    if (serverDetectedQuestions && serverDetectedQuestions.length > 0) {
      const newQuestions = serverDetectedQuestions.filter(q => !askedQuestions.includes(q));
      if (newQuestions.length > 0) {
        setDetectedQuestions(newQuestions);
        onDetectedQuestionsChange?.(newQuestions.length);
      }
    }
  }, [serverDetectedQuestions, askedQuestions, onDetectedQuestionsChange]);

  // Fetch follow-up question suggestions for each completed answer.
  // Reuses the detect-questions endpoint (its prompt already generates
  // "questions a practitioner would ask next" from context). Fetched at
  // most once per entry; failures are silent (cards simply don't appear).
  const followUpFetchedRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    const target = qaHistory.find(
      (e) => !e.isStreaming && e.answer && !followUpFetchedRef.current.has(e.id),
    );
    if (!target) return;
    followUpFetchedRef.current.add(target.id);
    // Skip error placeholders — no useful context to suggest from
    if (target.answer.startsWith('답변을 가져오지') || target.answer.startsWith('답변 생성 중 오류')) return;

    const context = `질문: ${target.question}\n답변: ${target.answer}`.slice(0, 2000);
    qaApi.detectQuestions(context, askedQuestions)
      .then((res) => {
        if (res.questions.length > 0) {
          setQaHistory((prev) =>
            prev.map((e) =>
              e.id === target.id ? { ...e, followUps: res.questions.slice(0, 3) } : e,
            ),
          );
        }
      })
      .catch(() => {}); // silent fail
  }, [qaHistory, askedQuestions]);

  const handleStreamMessage = useCallback((msg: WebSocketMessage) => {
    const entryId = activeEntryIdRef.current;
    if (!entryId) return;

    switch (msg.type) {
      case 'answer_delta':
        armWatchdog(); // stream is alive — push the stall deadline out
        setQaHistory(prev =>
          prev.map(e =>
            e.id === entryId ? { ...e, answer: e.answer + (msg.text || '') } : e
          )
        );
        break;
      case 'tool_progress':
        // Tool execution (KB retrieve, research kickoff, etc.) produces no
        // answer text, but the round is still alive server-side — rearm
        // without touching the answer so a long tool round doesn't trip the
        // stall watchdog.
        armWatchdog();
        break;
      case 'answer_complete':
        clearWatchdog();
        // Success — the claim sticks; record the proactive question as asked
        // (deferred from ask time, see recordProactiveAsked).
        recordProactiveAsked(entryId);
        setQaHistory(prev =>
          prev.map(e =>
            e.id === entryId
              ? {
                  ...e,
                  answer: msg.answer || e.answer,
                  sources: msg.sources,
                  usedKB: msg.usedKB,
                  usedDocs: msg.usedDocs,
                  toolsUsed: msg.toolsUsed,
                  isStreaming: false,
                }
              : e
          )
        );
        setIsAsking(false);
        activeEntryIdRef.current = null;
        inputRef.current?.focus();
        break;
      case 'answer_error':
        clearWatchdog();
        rollbackProactiveClaim(entryId);
        setQaHistory(prev =>
          prev.map(e =>
            e.id === entryId
              ? { ...e, answer: msg.error || '답변 생성 중 오류가 발생했습니다.', isStreaming: false }
              : e
          )
        );
        setIsAsking(false);
        activeEntryIdRef.current = null;
        inputRef.current?.focus();
        break;
      case 'error':
        // Socket-level error frame: without clearing the watchdog here the
        // user stares at a spinner for the full 60s before the timeout
        // banner appears on top of this error.
        clearWatchdog();
        rollbackProactiveClaim(entryId);
        setError(msg.error || 'WebSocket error');
        setQaHistory(prev =>
          prev.map(e => (e.id === entryId ? { ...e, isStreaming: false } : e))
        );
        setIsAsking(false);
        activeEntryIdRef.current = null;
        // Mirrors the watchdog-timeout path: this frame type doesn't
        // guarantee no server-side request is still in flight for every
        // current and future backend error path, so a late delta/complete
        // could otherwise land on whatever entry becomes active next. Same
        // disconnect+rotate, same tradeoff (continuity for correctness).
        wsRef.current?.disconnect();
        wsRef.current = null;
        setSessionId(`qa-${meetingId || 'live'}-${Date.now()}`);
        break;
    }
  }, [armWatchdog, clearWatchdog, meetingId, rollbackProactiveClaim, recordProactiveAsked]);

  const ensureWebSocket = useCallback(async (): Promise<RealtimeWebSocket | null> => {
    if (!WS_URL) return null;
    if (wsRef.current?.isConnected) return wsRef.current;

    const ws = new RealtimeWebSocket(WS_URL, handleStreamMessage, () => {
      wsRef.current = null;
    });
    try {
      await ws.connect();
      wsRef.current = ws;
      return ws;
    } catch {
      return null;
    }
  }, [handleStreamMessage]);

  // Cleanup WebSocket + watchdog on unmount. Pending proactive claims are
  // rolled back too: the mobile sheet unmounts on close (conditional
  // render), and a claim whose answer will never arrive (its socket is
  // being dropped right here) must not block the question — or the
  // module-level in-flight flag — for the rest of the session.
  useEffect(() => {
    const pendingClaims = proactiveClaimByEntryRef.current;
    return () => {
      wsRef.current?.disconnect();
      if (watchdogRef.current) clearTimeout(watchdogRef.current);
      pendingClaims.forEach((q) => rollbackProactiveClaimState(q));
      pendingClaims.clear();
    };
  }, []);

  const handleAsk = useCallback(async (q: string, opts?: { proactive?: boolean }) => {
    if (!q.trim() || isAsking) return;

    setQuestion('');
    if (!opts?.proactive) {
      // Manual asks are recorded immediately. Proactive asks defer this to
      // answer_complete / HTTP success (recordProactiveAsked): recording up
      // front would make a FAILED auto-ask unretryable — askedQuestions and
      // the parent's askedQuestionsRef (fed to detect-questions as
      // previousQuestions) both suppress the question forever, nullifying
      // the claim rollback.
      setAskedQuestions(prev => [...prev, q.trim()]);
      onAskedQuestion?.(q.trim());
    }
    setDetectedQuestions(prev => {
      const next = prev.filter(dq => dq !== q.trim());
      onDetectedQuestionsChange?.(next.length);
      return next;
    });
    setError(null);
    setIsAsking(true);

    const entryId = Date.now().toString();
    const newEntry: QAEntry = {
      id: entryId,
      question: q.trim(),
      answer: '',
      isStreaming: true,
      isProactive: opts?.proactive,
    };
    setQaHistory((prev) => [...prev, newEntry]);
    activeEntryIdRef.current = entryId;

    // Claim the proactive question SYNCHRONOUSLY (before the first await) so
    // a sibling panel instance's effect running in the same flush sees it as
    // taken. Registered per-entry so every failure path can roll it back.
    if (opts?.proactive) {
      claimedProactiveQuestions.add(q.trim());
      proactiveClaimByEntryRef.current.set(entryId, q.trim());
      proactiveGuard.askInFlight = true;
    }

    // Try WebSocket streaming first
    const ws = await ensureWebSocket();
    if (ws) {
      // API Gateway WebSocket has a 32KB per-frame limit (separate from, and
      // much smaller than, the 128KB message limit) that the browser can't
      // control the framing around — a char-count cap on Korean-heavy text
      // can still serialize past it and die silently. Budget by the size of
      // the ACTUAL serialized frame, not the raw context: JSON escaping
      // (quotes, newlines → \n) plus the question/session envelope can push
      // a raw-28KB context past the limit, so shrink until the whole frame
      // fits with headroom.
      let wsContext = truncateToUtf8ByteLimit(transcriptContext, 28_000);
      const frameBytes = () =>
        new TextEncoder().encode(
          JSON.stringify({ action: 'ask_live', question: q.trim(), context: wsContext, meetingId, sessionId }),
        ).length;
      while (wsContext && frameBytes() > 30_000) {
        wsContext = truncateToUtf8ByteLimit(
          wsContext,
          Math.floor(new TextEncoder().encode(wsContext).length * 0.85),
        );
      }
      if (frameBytes() > 30_000) {
        // Context is already empty and the frame STILL exceeds the budget --
        // the question itself is too large. Sending anyway would die
        // silently at the gateway's 32KB frame limit and burn a 60s
        // watchdog wait; reject up front instead.
        rollbackProactiveClaim(entryId);
        setQaHistory(prev => prev.filter(e => e.id !== entryId));
        activeEntryIdRef.current = null;
        setIsAsking(false);
        setError('질문이 너무 깁니다 — 내용을 줄여서 다시 시도해주세요.');
        return;
      }
      ws.askLive(q.trim(), wsContext, meetingId, sessionId);
      armWatchdog();
      return;
    }

    // Fallback to HTTP sync
    try {
      setQaHistory(prev => prev.map(e => e.id === entryId ? { ...e, isStreaming: false } : e));
      const response = await qaApi.ask(q.trim(), transcriptContext, sessionId);
      recordProactiveAsked(entryId);
      setQaHistory((prev) =>
        prev.map((entry) =>
          entry.id === entryId
            ? {
                ...entry,
                answer: response.answer,
                sources: response.sources,
                usedKB: response.usedKB,
                usedDocs: response.usedDocs,
                toolsUsed: response.toolsUsed,
              }
            : entry
        )
      );
    } catch (err) {
      rollbackProactiveClaim(entryId);
      setError(err instanceof Error ? err.message : 'Failed to get answer');
      setQaHistory((prev) =>
        prev.map((entry) =>
          entry.id === entryId
            ? { ...entry, answer: '답변을 가져오지 못했습니다. 다시 시도해주세요.' }
            : entry
        )
      );
    } finally {
      setIsAsking(false);
      activeEntryIdRef.current = null;
      inputRef.current?.focus();
    }
  }, [
    isAsking, onAskedQuestion, onDetectedQuestionsChange, ensureWebSocket,
    transcriptContext, meetingId, sessionId, armWatchdog,
    rollbackProactiveClaim, recordProactiveAsked,
  ]);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    handleAsk(question);
  };

  // Panel visibility, kept REACTIVE via IntersectionObserver: both panel
  // instances stay mounted while CSS-hidden (mobile ↔ desktop breakpoint,
  // ReferenceTabs' qa ↔ ref tab), and a plain offsetParent check inside the
  // effect wouldn't re-run when the user switches back to this tab — the
  // observer flips state on every visibility change, so a held batch fires
  // as soon as the panel is actually shown again.
  const [isPanelVisible, setIsPanelVisible] = useState(false);
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    if (typeof IntersectionObserver === 'undefined') {
      // No observer support → fail OPEN (treat as visible): the alternative
      // silently disables proactive search forever on such browsers, and
      // the module-level claim/in-flight guards still bound double-firing.
      setIsPanelVisible(true);
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      setIsPanelVisible(entries[entries.length - 1]?.isIntersecting ?? false);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // Proactive search: when the question detector flags a question as
  // search-answerable, fire it automatically so the answer is already on
  // screen by the time someone would have typed it. OFF by default —
  // auto-firing sends conversation-derived queries to an external web
  // search provider, so it requires the explicit header toggle (persisted
  // opt-in). Guards keep it from becoming spam: at most ONE auto-ask per
  // detection batch (a batch is consumed the moment it fires — the rest
  // stay as tappable suggestion chips), each question auto-fires at most
  // once per recording session ACROSS panel instances
  // (claimedProactiveQuestions, module-level, claimed inside handleAsk,
  // rolled back on failure, cleared on recording start via
  // resetProactiveClaims),
  // only the visible panel fires (isPanelVisible above), and nothing fires
  // while another answer is in flight or while the user is composing their
  // own question (an auto-ask would steal the single active-entry streaming
  // slot). A batch arriving while blocked is held, not dropped: it stays
  // unconsumed until an eligible render fires it. The consumed marker and
  // the proactive in-flight flag are MODULE-level (see their declaration):
  // an instance-local ref would let the OTHER mounted panel instance
  // re-consume the same batch — a second, possibly stale auto-fire.
  useEffect(() => {
    if (!proactiveSearchEnabled) return;
    if (!proactiveQuestions || proactiveQuestions.length === 0) return;
    if (proactiveGuard.consumedBatchKey === proactiveBatchKey(proactiveQuestions)) return;
    if (proactiveGuard.askInFlight || isAsking || question.trim() || !isPanelVisible) return;
    const next = proactiveQuestions.find(
      (q) =>
        q.trim() &&
        !claimedProactiveQuestions.has(q.trim()) &&
        !askedQuestions.includes(q.trim()),
    );
    proactiveGuard.consumedBatchKey = proactiveBatchKey(proactiveQuestions);
    if (!next) return;
    handleAsk(next, { proactive: true });
    // handleAsk identity changes are harmless as a dep: the module-level
    // consumed-batch marker prevents a re-run from refiring the same batch.
  }, [proactiveSearchEnabled, proactiveQuestions, isAsking, question, askedQuestions, isPanelVisible, handleAsk]);

  return (
    <div className="flex flex-col h-full bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800">
      {/* Header */}
      <div className="flex items-center gap-2 px-4 py-3 border-b border-slate-100 dark:border-slate-800">
        <span className="material-symbols-outlined text-primary">auto_awesome</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">AI 어시스턴트 · KB Q&A</h3>
        {/* Proactive-search opt-in: auto-fires detected questions, which sends
            conversation-derived queries to an external web search — default off. */}
        <button
          type="button"
          onClick={toggleProactiveSearch}
          title="대화에서 감지된 질문을 자동으로 검색해 답을 미리 띄웁니다. 회의 내용에서 추출된 검색어가 외부 웹 검색으로 전송되므로 기본 꺼짐입니다."
          className={`ml-auto flex items-center gap-1 text-[10px] font-medium px-2 py-1 rounded-full border transition-colors ${
            proactiveSearchEnabled
              ? 'bg-sky-50 dark:bg-sky-900/20 border-sky-300 dark:border-sky-700 text-sky-600 dark:text-sky-400'
              : 'bg-slate-50 dark:bg-slate-800 border-slate-200 dark:border-slate-700 text-slate-400 dark:text-slate-500'
          }`}
        >
          <span className="material-symbols-outlined text-xs">travel_explore</span>
          선제 검색 {proactiveSearchEnabled ? 'ON' : 'OFF'}
        </button>
        <span className="text-[10px] text-slate-400 dark:text-slate-500 font-mono">⌘K</span>
      </div>

      {/* Chat History */}
      <div ref={containerRef} className="flex-1 overflow-y-auto p-4 space-y-4 min-h-0">
        {qaHistory.length === 0 ? (
          <div className="space-y-4">
            <QAEmptyState isLive />
            <QASuggestedQuestions
              questions={detectedQuestions.length > 0 ? detectedQuestions : suggestedQuestions}
              isDetected={detectedQuestions.length > 0}
              onAsk={handleAsk}
              disabled={isAsking}
            />
          </div>
        ) : (
          qaHistory.map((entry) => (
            <QAChatMessage
              key={entry.id}
              question={entry.question}
              answer={entry.answer}
              sources={entry.sources}
              usedKB={entry.usedKB}
              usedDocs={entry.usedDocs}
              toolsUsed={entry.toolsUsed}
              isStreaming={entry.isStreaming}
              isProactive={entry.isProactive}
              onSaveToNotes={
                onSaveToNotes
                  ? () => {
                      onSaveToNotes(entry.question, entry.answer);
                      setSavedEntryIds((prev) => new Set(prev).add(entry.id));
                    }
                  : undefined
              }
              isSavedToNotes={savedEntryIds.has(entry.id)}
              followUps={entry.followUps}
              onAskFollowUp={handleAsk}
              followUpsDisabled={isAsking}
            />
          ))
        )}
      </div>

      {/* Error */}
      {error && (
        <div className="px-4 py-2 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm">
          {error}
        </div>
      )}

      {/* Detected Questions */}
      {detectedQuestions.length > 0 && qaHistory.length > 0 && (
        <div className="px-4 py-3 border-t border-slate-100 dark:border-slate-800">
          <QASuggestedQuestions
            questions={detectedQuestions}
            isDetected
            onAsk={handleAsk}
            disabled={isAsking}
          />
        </div>
      )}

      {/* Input */}
      <form onSubmit={handleSubmit} className="p-4 border-t border-slate-100 dark:border-slate-800">
        <div className="flex items-center gap-2">
          <input
            ref={inputRef}
            type="text"
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            placeholder="질문을 입력하세요..."
            className="flex-1 px-4 py-2.5 text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg focus:ring-2 focus:ring-primary/20 placeholder:text-slate-400"
            disabled={isAsking}
          />
          <button
            type="submit"
            disabled={!question.trim() || isAsking}
            className="flex items-center justify-center w-10 h-10 rounded-lg bg-primary text-white hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            <span className="material-symbols-outlined text-xl">send</span>
          </button>
        </div>
      </form>
    </div>
  );
}
