'use client';

import { useState, useRef, useEffect } from 'react';
import { qaApi } from '@/lib/api';
import type { QAEntry } from '@/types/meeting';

interface QAPanelProps {
  meetingId: string;
}

export function QAPanel({ meetingId }: QAPanelProps) {
  const [question, setQuestion] = useState('');
  const [qaHistory, setQaHistory] = useState<QAEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [qaHistory]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!question.trim() || isAsking) return;

    const currentQuestion = question.trim();
    setQuestion('');
    setError(null);
    setIsAsking(true);

    // Add question to history immediately
    const entryId = Date.now().toString();
    const newEntry: QAEntry = {
      id: entryId,
      question: currentQuestion,
      answer: '',
      timestamp: new Date().toISOString(),
    };
    setQaHistory((prev) => [...prev, newEntry]);

    try {
      const response = await qaApi.ask(meetingId, currentQuestion);
      setQaHistory((prev) =>
        prev.map((entry) =>
          entry.id === entryId
            ? { ...entry, answer: response.answer, sources: response.sources }
            : entry
        )
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to get answer');
      setQaHistory((prev) =>
        prev.map((entry) =>
          entry.id === entryId
            ? { ...entry, answer: 'Sorry, I could not process your question. Please try again.' }
            : entry
        )
      );
    } finally {
      setIsAsking(false);
      inputRef.current?.focus();
    }
  };

  return (
    <div className="flex flex-col h-full bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800">
      {/* Header */}
      <div className="flex items-center gap-2 px-4 py-3 border-b border-slate-100 dark:border-slate-800">
        <span className="material-symbols-outlined text-primary">question_answer</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">Ask about this meeting</h3>
      </div>

      {/* Chat History */}
      <div ref={containerRef} className="flex-1 overflow-y-auto p-4 space-y-4 min-h-0">
        {qaHistory.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-8 text-slate-400">
            <span className="material-symbols-outlined text-4xl mb-2">chat</span>
            <p className="text-sm text-center">
              Ask questions about this meeting.
              <br />
              <span className="text-xs">e.g., &quot;What were the action items?&quot;</span>
            </p>
          </div>
        ) : (
          qaHistory.map((entry) => (
            <div key={entry.id} className="space-y-3">
              {/* Question */}
              <div className="flex justify-end">
                <div className="bg-primary/10 rounded-xl px-4 py-2.5 max-w-[85%]">
                  <p className="text-sm text-slate-900 dark:text-slate-100">{entry.question}</p>
                </div>
              </div>

              {/* Answer */}
              <div className="flex justify-start">
                <div className="bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-xl px-4 py-2.5 max-w-[85%]">
                  {entry.answer ? (
                    <>
                      <p className="text-sm text-slate-700 dark:text-slate-300 whitespace-pre-wrap">
                        {entry.answer}
                      </p>
                      {entry.sources && entry.sources.length > 0 && (
                        <div className="mt-3 pt-2 border-t border-slate-100 dark:border-slate-700">
                          <p className="text-[10px] font-semibold text-slate-400 uppercase mb-1.5">
                            Sources
                          </p>
                          <div className="flex flex-wrap gap-1.5">
                            {entry.sources.map((source, index) => (
                              <span
                                key={index}
                                className="text-[10px] px-2 py-1 bg-slate-100 dark:bg-slate-700 rounded text-slate-600 dark:text-slate-300"
                                title={source.snippet}
                              >
                                {source.title}
                              </span>
                            ))}
                          </div>
                        </div>
                      )}
                    </>
                  ) : (
                    <div className="flex items-center gap-2">
                      <div className="animate-spin rounded-full h-4 w-4 border-2 border-primary border-t-transparent" />
                      <span className="text-sm text-slate-400">Thinking...</span>
                    </div>
                  )}
                </div>
              </div>
            </div>
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
