'use client';

import { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { qaApi, chatApi } from '@/lib/api';
import { RealtimeWebSocket, type WebSocketMessage } from '@/lib/websocket';
import { QAChatMessage } from '@/components/qa';
import type { ChatSession } from '@/types/meeting';

interface ChatEntry {
  id: string;
  question: string;
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  isStreaming?: boolean;
}

const suggestedQuestions = [
  '이번 주 미팅 요약해줘',
  '미완료 액션아이템 모아줘',
  '최근 공유받은 미팅 정리해줘',
  'EKS 관련 논의 요약해줘',
];

const WS_URL = process.env.NEXT_PUBLIC_WEBSOCKET_URL || '';

export function ChatClient() {
  const router = useRouter();
  const { user, isLoading, isAuthenticated } = useAuth();

  const [question, setQuestion] = useState('');
  const [chatHistory, setChatHistory] = useState<ChatEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sessions, setSessions] = useState<ChatSession[]>([]);
  const [showSessions, setShowSessions] = useState(false);

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
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const sessionsRef = useRef<HTMLDivElement>(null);

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
    if (!entryId) return;

    switch (msg.type) {
      case 'answer_delta':
        setChatHistory(prev =>
          prev.map(e =>
            e.id === entryId ? { ...e, answer: e.answer + (msg.text || '') } : e
          )
        );
        break;
      case 'answer_complete':
        setChatHistory(prev =>
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
        setChatHistory(prev =>
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
        setError(msg.error || 'WebSocket error');
        break;
    }
  }, []);

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

  // Cleanup WebSocket on unmount
  useEffect(() => {
    return () => {
      wsRef.current?.disconnect();
    };
  }, []);

  const handleAsk = async (q: string) => {
    if (!q.trim() || isAsking || !sessionId) return;

    setQuestion('');
    setError(null);
    setIsAsking(true);

    const entryId = Date.now().toString();
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
    if (ws) {
      ws.askLive(q.trim(), undefined, undefined, sessionId);
      return;
    }

    // Fallback to HTTP sync
    try {
      setChatHistory(prev => prev.map(e => e.id === entryId ? { ...e, isStreaming: false } : e));
      const response = await qaApi.ask(q.trim(), undefined, sessionId);
      setChatHistory(prev =>
        prev.map(entry =>
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
      setError(err instanceof Error ? err.message : 'Failed to get answer');
      setChatHistory(prev =>
        prev.map(entry =>
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
  };

  const handleNewChat = () => {
    setChatHistory([]);
    setError(null);
    setSessionId(`chat-${user?.userId || 'anon'}-${Date.now()}`);
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
      <div className="flex flex-col h-full">
        {/* Header */}
        <div className="flex items-center justify-between px-4 sm:px-6 py-3 border-b border-slate-200 dark:border-white/10 bg-white dark:bg-[#0e0e13]">
          <div className="flex items-center gap-2">
            <span className="material-symbols-outlined text-primary text-2xl">smart_toy</span>
            <h1 className="text-lg font-semibold text-slate-900 dark:text-white">Ttobak Assistant</h1>
          </div>
          <div className="flex items-center gap-2">
            {/* Session dropdown */}
            <div className="relative" ref={sessionsRef}>
              <button
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
            <div className="flex flex-col items-center justify-center h-full px-4 py-12">
              <div className="flex items-center justify-center w-16 h-16 rounded-2xl bg-primary/10 dark:bg-primary/20 mb-6">
                <span className="material-symbols-outlined text-primary text-4xl">smart_toy</span>
              </div>
              <h2 className="text-xl font-semibold text-slate-900 dark:text-white mb-2">
                무엇이든 물어보세요
              </h2>
              <p className="text-sm text-slate-500 dark:text-slate-400 text-center max-w-md mb-8">
                모든 미팅 기록과 지식베이스를 기반으로 답변합니다. 미팅 요약, 액션아이템, 특정 주제 검색 등을 도와드려요.
              </p>
              <div className="flex flex-wrap justify-center gap-2 max-w-lg">
                {suggestedQuestions.map((sq, i) => (
                  <button
                    key={i}
                    onClick={() => handleAsk(sq)}
                    disabled={isAsking}
                    className="px-4 py-2 text-sm text-slate-700 dark:text-slate-300 bg-slate-100 dark:bg-white/5 border border-slate-200 dark:border-white/10 rounded-lg hover:bg-slate-200 dark:hover:bg-white/10 hover:border-primary/30 dark:hover:border-primary/30 transition-colors disabled:opacity-50"
                  >
                    {sq}
                  </button>
                ))}
              </div>
            </div>
          ) : (
            <div className="max-w-3xl mx-auto px-4 py-6 space-y-4">
              {chatHistory.map(entry => (
                <QAChatMessage
                  key={entry.id}
                  question={entry.question}
                  answer={entry.answer}
                  sources={entry.sources}
                  usedKB={entry.usedKB}
                  usedDocs={entry.usedDocs}
                  toolsUsed={entry.toolsUsed}
                  isStreaming={entry.isStreaming}
                />
              ))}
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
        <div className="border-t border-slate-200 dark:border-white/10 bg-white dark:bg-[#0e0e13] px-4 py-4">
          <form onSubmit={handleSubmit} className="max-w-3xl mx-auto">
            <div className="flex items-center gap-2">
              <input
                ref={inputRef}
                type="text"
                value={question}
                onChange={(e) => setQuestion(e.target.value)}
                placeholder="질문을 입력하세요..."
                className="flex-1 px-4 py-2.5 text-sm bg-slate-100 dark:bg-white/5 border border-slate-200 dark:border-white/10 rounded-lg focus:ring-2 focus:ring-primary/20 focus:border-primary/30 placeholder:text-slate-400 dark:placeholder:text-slate-500 dark:text-white outline-none transition-colors"
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
      </div>
    </AppLayout>
  );
}
