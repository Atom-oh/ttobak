'use client';

import { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { qaApi, chatApi, meetingsApi, QA_POLL_DEADLINE_MS } from '@/lib/api';
import { assistantCards } from '@/lib/assistantSuggestions';
import { getRuntimeConfig, runtimeWebSocketUrl } from '@/lib/runtimeConfig';
import { RealtimeWebSocket, isTerminalModelError, type WebSocketMessage } from '@/lib/websocket';
import { QAChatMessage } from '@/components/qa';
import type { ChatSession, Meeting } from '@/types/meeting';

interface ChatEntry {
  id: string;
  question: string;
  answer: string;
  sources?: string[];
  sourceDetails?: import('@/types/meeting').QASourceDetail[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  isStreaming?: boolean;
}

// Shared QA Lambda: 300 seconds, plus bounded transport grace.
const ANSWER_TIMEOUT_MS = 330_000;

export function ChatClient() {
  const router = useRouter();
  const { user, isLoading, isAuthenticated } = useAuth();
  const [wsUrl, setWsUrl] = useState('');

  const [question, setQuestion] = useState('');
  const [chatHistory, setChatHistory] = useState<ChatEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sessions, setSessions] = useState<ChatSession[]>([]);
  const [showSessions, setShowSessions] = useState(false);
  const [recentMeetings, setRecentMeetings] = useState<{ userId: string; meetings: Meeting[] } | null>(null);
  const cards = useMemo(() => assistantCards(
    recentMeetings?.userId === user?.userId ? recentMeetings?.meetings || [] : [],
  ), [recentMeetings, user?.userId]);

  useEffect(() => {
    if (!isAuthenticated || !user?.userId) return;
    const userId = user.userId;
    const controller = new AbortController();
    meetingsApi.list({ limit: 30 }, { signal: controller.signal }).then(result => {
      if (!controller.signal.aborted) setRecentMeetings({ userId, meetings: result.meetings || [] });
    }).catch(() => {
      // Keep useful generic cards when recent metadata is unavailable.
    });
    return () => controller.abort();
  }, [isAuthenticated, user?.userId]);

  const currentSessionId = useMemo(() => {
    const ts = Date.now();
    return `chat-${user?.userId || 'anon'}-${ts}`;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user?.userId]);

  const [sessionId, setSessionId] = useState<string | null>(null);

  // Initialize sessionId once user is available
  useEffect(() => {
    if (user?.userId && !sessionId) {
      setSessionId(currentSessionId);
    }
  }, [user?.userId, currentSessionId, sessionId]);

  const wsRef = useRef<RealtimeWebSocket | null>(null);
  const activeEntryIdRef = useRef<string | null>(null);
  const streamingEntryIdRef = useRef<string | null>(null);
  const answerTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const aliveRef = useRef(true);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const sessionsRef = useRef<HTMLDivElement>(null);
  const finishRequest = useCallback((entryId: string): boolean => {
    if (activeEntryIdRef.current !== entryId) return false;
    activeEntryIdRef.current = null;
    streamingEntryIdRef.current = null;
    if (answerTimerRef.current) clearTimeout(answerTimerRef.current);
    answerTimerRef.current = null;
    const ws = wsRef.current;
    wsRef.current = null;
    ws?.disconnect();
    return true;
  }, []);
  const failRequest = useCallback((entryId: string, message: string, preserveSession = false) => {
    if (!aliveRef.current || !finishRequest(entryId)) return;
    setChatHistory(prev => prev.map(entry => entry.id === entryId ? {
      ...entry,
      answer: entry.answer ? `${entry.answer}\n\n${message}` : message,
      isStreaming: false,
    } : entry));
    setError(message);
    setIsAsking(false);
    // Keep acknowledged terminal model history; unknown work still gets a new
    // session so a delayed server write cannot overwrite the next request.
    if (!preserveSession) setSessionId(`chat-${crypto.randomUUID()}`);
    inputRef.current?.focus();
  }, [finishRequest]);
  const startAnswerDeadline = useCallback((entryId: string, timeout = ANSWER_TIMEOUT_MS) => {
    if (answerTimerRef.current) clearTimeout(answerTimerRef.current);
    answerTimerRef.current = setTimeout(() => {
      failRequest(entryId, '답변이 제한 시간 안에 완료되지 않았습니다. 다시 질문해 주세요.');
    }, timeout);
  }, [failRequest]);

  useEffect(() => {
    let active = true;
    getRuntimeConfig().then(cfg => { if (active) setWsUrl(runtimeWebSocketUrl(cfg.wsUrl)); });
    return () => { active = false; };
  }, []);

  // Auth guard
  useEffect(() => {
    if (!isLoading && !isAuthenticated) {
      router.push('/');
    }
  }, [isLoading, isAuthenticated, router]);

  // Auto-scroll on new messages
  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [chatHistory]);

  // Load sessions on mount
  useEffect(() => {
    if (!isAuthenticated) return;
    chatApi.listSessions().then(res => {
      setSessions(res.sessions || []);
    }).catch(() => {
      // Silently fail — sessions list is non-critical
    });
  }, [isAuthenticated]);

  // Close sessions dropdown on outside click
  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (sessionsRef.current && !sessionsRef.current.contains(e.target as Node)) {
        setShowSessions(false);
      }
    }
    if (showSessions) {
      document.addEventListener('mousedown', handleClickOutside);
      return () => document.removeEventListener('mousedown', handleClickOutside);
    }
  }, [showSessions]);

  const handleStreamMessage = useCallback((msg: WebSocketMessage) => {
    const entryId = activeEntryIdRef.current;
    if (!aliveRef.current || !entryId || streamingEntryIdRef.current !== entryId) return;

    switch (msg.type) {
      case 'answer_delta':
        setChatHistory(prev =>
          prev.map(e =>
            e.id === entryId ? { ...e, answer: e.answer + (msg.text || '') } : e
          )
        );
        break;
      case 'answer_complete':
        if (!finishRequest(entryId)) return;
        setChatHistory(prev =>
          prev.map(e =>
            e.id === entryId
              ? {
                  ...e,
                  answer: msg.answer || e.answer,
                  sources: msg.sources,
                  sourceDetails: msg.sourceDetails,
                  usedKB: msg.usedKB,
                  usedDocs: msg.usedDocs,
                  toolsUsed: msg.toolsUsed,
                  isStreaming: false,
                }
              : e
          )
        );
        setIsAsking(false);
        inputRef.current?.focus();
        break;
      case 'answer_error':
        failRequest(entryId, msg.error || '답변 생성 중 오류가 발생했습니다.',
          isTerminalModelError(msg) && msg.sessionContinuable === true);
        break;
      case 'error':
        failRequest(entryId, msg.error || '답변 생성 중 오류가 발생했습니다. 다시 질문해 주세요.');
        break;
    }
  }, [failRequest, finishRequest]);

  const ensureWebSocket = useCallback(async (): Promise<RealtimeWebSocket | null> => {
    if (!wsUrl) return null;
    if (wsRef.current?.isConnected) return wsRef.current;

    const ws = new RealtimeWebSocket(wsUrl, msg => {
      if (wsRef.current === ws) handleStreamMessage(msg);
    }, () => {
      if (wsRef.current !== ws) return;
      const entryId = streamingEntryIdRef.current;
      if (entryId) {
        failRequest(entryId, '연결이 끊어져 답변이 완료되지 않았습니다. 다시 질문해 주세요.');
      } else {
        wsRef.current = null;
      }
    }, undefined, { autoReconnect: false });
    wsRef.current = ws;
    try {
      await ws.connect();
      if (wsRef.current !== ws) { ws.disconnect(); return null; }
      return ws;
    } catch {
      if (wsRef.current === ws) wsRef.current = null;
      ws.disconnect();
      return null;
    }
  }, [wsUrl, handleStreamMessage, failRequest]);

  // Cleanup WebSocket on unmount
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      const entryId = activeEntryIdRef.current;
      if (entryId) finishRequest(entryId);
    };
  }, [finishRequest]);

  const handleAsk = async (q: string) => {
    if (!q.trim() || activeEntryIdRef.current || !sessionId || !aliveRef.current) return;

    setQuestion('');
    setError(null);
    setIsAsking(true);

    const entryId = crypto.randomUUID();
    const newEntry: ChatEntry = {
      id: entryId,
      question: q.trim(),
      answer: '',
      isStreaming: true,
    };
    setChatHistory(prev => [...prev, newEntry]);
    activeEntryIdRef.current = entryId;

    // Try WebSocket streaming first
    const ws = await ensureWebSocket();
    if (!aliveRef.current || activeEntryIdRef.current !== entryId) return;
    if (ws) {
      streamingEntryIdRef.current = entryId;
      startAnswerDeadline(entryId);
      if (!ws.askLive(q.trim(), undefined, undefined, sessionId)) {
        failRequest(entryId, '질문을 전송하지 못했습니다. 다시 질문해 주세요.');
      }
      return;
    }

    // REST uses the API wrapper's bounded sync/job selection.
    startAnswerDeadline(entryId, QA_POLL_DEADLINE_MS + 30_000);
    try {
      setChatHistory(prev => prev.map(e => e.id === entryId ? { ...e, isStreaming: false } : e));
      const response = await qaApi.ask(q.trim(), undefined, sessionId);
      if (!aliveRef.current || activeEntryIdRef.current !== entryId) return;
      setChatHistory(prev =>
        prev.map(entry =>
          entry.id === entryId
            ? {
                ...entry,
                answer: response.answer,
                sources: response.sources,
                sourceDetails: response.sourceDetails,
                usedKB: response.usedKB,
                usedDocs: response.usedDocs,
                toolsUsed: response.toolsUsed,
              }
            : entry
        )
      );
    } catch (err) {
      if (!aliveRef.current || activeEntryIdRef.current !== entryId) return;
      failRequest(entryId, err instanceof Error ? err.message : '답변을 가져오지 못했습니다. 다시 시도해주세요.');
    } finally {
      if (aliveRef.current && finishRequest(entryId)) {
        setIsAsking(false);
        inputRef.current?.focus();
      }
    }
  };

  const handleNewChat = () => {
    const entryId = activeEntryIdRef.current;
    if (entryId) finishRequest(entryId);
    setChatHistory([]);
    setError(null);
    setIsAsking(false);
    setSessionId(`chat-${crypto.randomUUID()}`);
    inputRef.current?.focus();
  };

  const handleDeleteSession = async (sid: string) => {
    try {
      await chatApi.deleteSession(sid);
      setSessions(prev => prev.filter(s => s.sessionId !== sid));
    } catch {
      // Silently fail
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    handleAsk(question);
  };

  // Show loading while auth is resolving
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) return null;

  return (
    <AppLayout activePath="/chat">
      <div className="flex flex-1 flex-col min-w-0 min-h-0">
        {/* Header */}
        <div className="flex items-center justify-between px-4 sm:px-6 py-3 border-b border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest">
          <div className="flex items-center gap-2">
            <span className="material-symbols-outlined text-primary text-2xl">smart_toy</span>
            <h1 className="text-lg font-semibold text-slate-900 dark:text-white">TTOBAK Assistant</h1>
          </div>
          <div className="flex items-center gap-2">
            {/* Session dropdown */}
            <div className="relative" ref={sessionsRef}>
              <button
                aria-label="이전 대화"
                onClick={() => setShowSessions(!showSessions)}
                className="flex items-center gap-1.5 px-3 py-2 text-sm text-slate-600 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-white/5 rounded-lg transition-colors"
              >
                <span className="material-symbols-outlined text-lg">forum</span>
                <span className="hidden sm:inline">이전 대화</span>
              </button>
              {showSessions && (
                <div className="absolute right-0 top-full mt-1 w-72 bg-white dark:bg-[#1a1a24] border border-slate-200 dark:border-white/10 rounded-xl shadow-lg z-50 max-h-80 overflow-y-auto">
                  {sessions.length === 0 ? (
                    <div className="px-4 py-6 text-center text-sm text-slate-400 dark:text-slate-500">
                      이전 대화가 없습니다
                    </div>
                  ) : (
                    sessions.map(s => (
                      <div
                        key={s.sessionId}
                        className="flex items-center justify-between px-4 py-3 hover:bg-slate-50 dark:hover:bg-white/5 border-b border-slate-100 dark:border-white/5 last:border-b-0"
                      >
                        <div className="flex-1 min-w-0 mr-2">
                          <div className="text-sm font-medium text-slate-900 dark:text-white truncate">
                            {s.title || '제목 없음'}
                          </div>
                          <div className="text-xs text-slate-400 dark:text-slate-500 mt-0.5">
                            {new Date(s.createdAt).toLocaleDateString('ko-KR')}
                            {s.messageCount > 0 && ` · ${s.messageCount}개 메시지`}
                          </div>
                        </div>
                        <button
                          onClick={(e) => {
                            e.stopPropagation();
                            handleDeleteSession(s.sessionId);
                          }}
                          className="flex-shrink-0 p-1 text-slate-400 hover:text-red-500 dark:hover:text-red-400 rounded transition-colors"
                          title="삭제"
                        >
                          <span className="material-symbols-outlined text-lg">delete</span>
                        </button>
                      </div>
                    ))
                  )}
                </div>
              )}
            </div>
            {/* New chat button */}
            <button
              aria-label="새 대화"
              onClick={handleNewChat}
              className="flex items-center gap-1.5 px-3 py-2 text-sm bg-primary text-white rounded-lg hover:bg-primary/90 transition-colors"
            >
              <span className="material-symbols-outlined text-lg">add</span>
              <span className="hidden sm:inline">새 대화</span>
            </button>
          </div>
        </div>

        {/* Chat area */}
        <div ref={containerRef} className="flex-1 overflow-y-auto min-h-0">
          {chatHistory.length === 0 ? (
            <div className="flex flex-col items-center justify-center min-h-full px-4 py-8 sm:py-12">
              <div className="flex items-center justify-center w-16 h-16 rounded-2xl bg-primary/10 dark:bg-primary/20 mb-6">
                <span className="material-symbols-outlined text-primary text-4xl">smart_toy</span>
              </div>
              <h2 className="text-xl font-semibold text-slate-900 dark:text-white mb-2">
                {user?.name ? `${user.name.slice(0, 80)}님, 오늘은 무엇을 도와드릴까요?` : '오늘은 무엇을 도와드릴까요?'}
              </h2>
              <p className="text-sm text-slate-500 dark:text-slate-400 text-center max-w-md mb-8">
                최근 미팅과 자주 다룬 주제를 바탕으로 시작해 보세요. 접근 가능한 미팅 기록과 지식베이스에서 답변을 찾습니다.
              </p>
              <div className="grid w-full max-w-2xl grid-cols-1 sm:grid-cols-2 gap-3">
                {cards.map(card => (
                  <button
                    key={card.label}
                    onClick={() => handleAsk(card.question)}
                    disabled={isAsking}
                    className="group flex flex-col gap-2 p-4 text-left bg-white dark:bg-white/5 border border-slate-200 dark:border-white/10 rounded-xl hover:bg-primary/5 hover:border-primary/30 transition-colors disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  >
                    <span className="flex items-center gap-2 text-xs font-semibold text-primary">
                      <span className="material-symbols-outlined text-lg" aria-hidden="true">{card.icon}</span>
                      {card.label}
                      <span className="material-symbols-outlined ml-auto text-sm opacity-40 group-hover:opacity-100" aria-hidden="true">north_east</span>
                    </span>
                    <span className="text-sm font-medium text-slate-800 dark:text-slate-200 break-words">{card.question}</span>
                    <span className="text-xs text-slate-500 dark:text-slate-400 truncate max-w-full">{card.detail}</span>
                  </button>
                ))}
              </div>
            </div>
          ) : (
            <div className="workspace-page">
              <div className="w-full max-w-[90ch] space-y-4">
                {chatHistory.map(entry => (
                  <QAChatMessage
                    key={entry.id}
                    question={entry.question}
                    answer={entry.answer}
                    sources={entry.sources}
                    sourceDetails={entry.sourceDetails}
                    usedKB={entry.usedKB}
                    usedDocs={entry.usedDocs}
                    toolsUsed={entry.toolsUsed}
                    isStreaming={entry.isStreaming}
                  />
                ))}
              </div>
            </div>
          )}
        </div>

        {/* Error */}
        {error && (
          <div className="px-4 py-2 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm text-center">
            {error}
          </div>
        )}

        {/* Input bar */}
        <div className="border-t border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest workspace-inline-gutter shrink-0 py-4">
          <form onSubmit={handleSubmit} className="w-full min-w-0">
            <div className="flex items-center gap-2">
              <input
                ref={inputRef}
                type="text"
                value={question}
                onChange={(e) => setQuestion(e.target.value)}
                placeholder="질문을 입력하세요..."
                className="min-w-0 flex-1 px-4 py-2.5 text-sm bg-slate-100 dark:bg-white/5 border border-slate-200 dark:border-white/10 rounded-lg focus:ring-2 focus:ring-primary/20 focus:border-primary/30 placeholder:text-slate-400 dark:placeholder:text-slate-500 dark:text-white outline-none transition-colors"
                disabled={isAsking}
              />
              <button
                type="submit"
                aria-label="질문 보내기"
                disabled={!question.trim() || isAsking}
                className="flex items-center justify-center shrink-0 w-10 h-10 rounded-lg bg-primary text-white hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                <span className="material-symbols-outlined text-xl">send</span>
              </button>
            </div>
          </form>
        </div>
      </div>
    </AppLayout>
  );
}
