'use client';

import { useState, useRef, useEffect, useMemo } from 'react';
import { qaApi } from '@/lib/api';
import type { QAEntry } from '@/types/meeting';
import { QAChatMessage, QASuggestedQuestions, QAEmptyState } from '@/components/qa';

interface QAPanelProps {
  meetingId: string;
}

const defaultSuggestions = [
  '주요 논의 사항은?',
  '결정된 액션 아이템은?',
  '참석자별 발언 요약',
];

export function QAPanel({ meetingId }: QAPanelProps) {
  const [question, setQuestion] = useState('');
  const [qaHistory, setQaHistory] = useState<QAEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const sessionId = useMemo(
    () => `qa-${meetingId}-${Date.now()}`,
    [meetingId]
  );

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [qaHistory]);

  const handleAsk = async (q: string) => {
    if (!q.trim() || isAsking) return;

    setQuestion('');
    setError(null);
    setIsAsking(true);

    // Add question to history immediately
    const entryId = Date.now().toString();
    const newEntry: QAEntry = {
      id: entryId,
      question: q.trim(),
      answer: '',
      timestamp: new Date().toISOString(),
    };
    setQaHistory((prev) => [...prev, newEntry]);

    try {
      const response = await qaApi.askMeeting(meetingId, q.trim(), sessionId);
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
            ? { ...entry, answer: '죄송합니다. 답변을 생성하지 못했습니다. 다시 시도해주세요.' }
            : entry
        )
      );
    } finally {
      setIsAsking(false);
      inputRef.current?.focus();
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    handleAsk(question);
  };

  return (
    <div className="flex flex-col h-full bg-white dark:bg-[#0e0e13] rounded-xl lg:rounded-none border border-slate-200 dark:border-white/10 lg:border-0">
      {/* Header */}
      <div className="flex items-center gap-2 px-4 py-3 border-b border-slate-100 dark:border-slate-800">
        <span className="material-symbols-outlined text-primary">question_answer</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">Ask about this meeting</h3>
      </div>

      {/* Chat History */}
      <div ref={containerRef} className="flex-1 overflow-y-auto p-4 space-y-4 min-h-0">
        {qaHistory.length === 0 ? (
          <div className="space-y-4">
            <QAEmptyState isLive={false} />
            <QASuggestedQuestions
              questions={defaultSuggestions}
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

      {/* Input */}
      <form onSubmit={handleSubmit} className="p-4 border-t border-slate-100 dark:border-slate-800">
        <div className="flex items-center gap-2">
          <input
            ref={inputRef}
            type="text"
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            placeholder="Ask a question..."
            className="flex-1 px-4 py-2.5 text-sm bg-transparent border border-slate-200 dark:border-white/10 rounded-lg focus:ring-2 focus:ring-primary/20 placeholder:text-slate-400"
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
