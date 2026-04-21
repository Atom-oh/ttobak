'use client';

import { useState, useRef, useEffect, useMemo, useCallback } from 'react';
import { qaApi } from '@/lib/api';
import { RealtimeWebSocket, type WebSocketMessage } from '@/lib/websocket';
import { QAChatMessage, QASuggestedQuestions, QAEmptyState } from '@/components/qa';

interface LiveQAPanelProps {
  transcriptContext?: string;
  meetingId?: string;
  onDetectedQuestionsChange?: (count: number) => void;
  serverDetectedQuestions?: string[];
  onAskedQuestion?: (question: string) => void;
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
}

const suggestedQuestions = [
  '주요 논의 사항은?',
  '결정된 액션 아이템은?',
  '핵심 키워드 정리해줘',
];

const WS_URL = process.env.NEXT_PUBLIC_WEBSOCKET_URL || '';

export function LiveQAPanel({ transcriptContext, meetingId, onDetectedQuestionsChange, serverDetectedQuestions, onAskedQuestion }: LiveQAPanelProps) {
  const [question, setQuestion] = useState('');
  const [qaHistory, setQaHistory] = useState<QAEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [detectedQuestions, setDetectedQuestions] = useState<string[]>([]);
  const [askedQuestions, setAskedQuestions] = useState<string[]>([]);
  const wsRef = useRef<RealtimeWebSocket | null>(null);
  const activeEntryIdRef = useRef<string | null>(null);

  const sessionId = useMemo(() => {
    const ts = Date.now();
    return `qa-${meetingId || 'live'}-${ts}`;
  }, [meetingId]);

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

  const handleStreamMessage = useCallback((msg: WebSocketMessage) => {
    const entryId = activeEntryIdRef.current;
    if (!entryId) return;

    switch (msg.type) {
      case 'answer_delta':
        setQaHistory(prev =>
          prev.map(e =>
            e.id === entryId ? { ...e, answer: e.answer + (msg.text || '') } : e
          )
        );
        break;
      case 'answer_complete':
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
    if (!q.trim() || isAsking) return;

    setQuestion('');
    setAskedQuestions(prev => [...prev, q.trim()]);
    onAskedQuestion?.(q.trim());
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
    };
    setQaHistory((prev) => [...prev, newEntry]);
    activeEntryIdRef.current = entryId;

    // Try WebSocket streaming first
    const ws = await ensureWebSocket();
    if (ws) {
      ws.askLive(q.trim(), transcriptContext, meetingId, sessionId);
      return;
    }

    // Fallback to HTTP sync
    try {
      setQaHistory(prev => prev.map(e => e.id === entryId ? { ...e, isStreaming: false } : e));
      const response = await qaApi.ask(q.trim(), transcriptContext, sessionId);
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
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    handleAsk(question);
  };

  return (
    <div className="flex flex-col h-full bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800">
      {/* Header */}
      <div className="flex items-center gap-2 px-4 py-3 border-b border-slate-100 dark:border-slate-800">
        <span className="material-symbols-outlined text-primary">assistant</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">AI 어시스턴트</h3>
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
