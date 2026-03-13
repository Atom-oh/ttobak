'use client';

import { useState, useRef, useEffect, useMemo } from 'react';
import { qaApi } from '@/lib/api';

interface LiveQAPanelProps {
  transcriptContext?: string;
  meetingId?: string;
  onDetectedQuestionsChange?: (count: number) => void;
}

interface QAEntry {
  id: string;
  question: string;
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
}

const TOOL_LABELS: Record<string, { label: string; color: string }> = {
  search_knowledge_base: { label: 'KB 검색', color: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400' },
  search_aws_docs: { label: 'AWS Docs', color: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400' },
  search_transcript: { label: '회의록 검색', color: 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-400' },
  get_aws_recommendation: { label: 'AWS 추천', color: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400' },
};

const suggestedQuestions = [
  '주요 논의 사항은?',
  '결정된 액션 아이템은?',
  '핵심 키워드 정리해줘',
];

export function LiveQAPanel({ transcriptContext, meetingId, onDetectedQuestionsChange }: LiveQAPanelProps) {
  const [question, setQuestion] = useState('');
  const [qaHistory, setQaHistory] = useState<QAEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [detectedQuestions, setDetectedQuestions] = useState<string[]>([]);
  const [askedQuestions, setAskedQuestions] = useState<string[]>([]);
  const lastDetectRef = useRef<string>('');
  const detectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastDetectTimeRef = useRef<number>(0);

  const sessionId = useMemo(() => {
    const ts = Date.now();
    return `qa-${meetingId || 'live'}-${ts}`;
  }, [meetingId]);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [qaHistory]);

  useEffect(() => {
    if (!transcriptContext || transcriptContext.length < 100) return;

    const newContentLength = transcriptContext.length - lastDetectRef.current.length;
    const timeSinceLastDetect = Date.now() - lastDetectTimeRef.current;

    // Detect on 100 chars of new content, or time-based fallback (15s since last detect)
    const shouldDetect =
      newContentLength >= 100 ||
      (lastDetectRef.current === '' && transcriptContext.length >= 100) ||
      (lastDetectTimeRef.current > 0 && timeSinceLastDetect >= 15000 && newContentLength > 0);

    if (!shouldDetect) return;

    if (detectTimerRef.current) clearTimeout(detectTimerRef.current);
    detectTimerRef.current = setTimeout(async () => {
      try {
        const result = await qaApi.detectQuestions(transcriptContext, askedQuestions);
        if (result.questions.length > 0) {
          setDetectedQuestions(result.questions);
          onDetectedQuestionsChange?.(result.questions.length);
        }
        lastDetectRef.current = transcriptContext;
        lastDetectTimeRef.current = Date.now();
      } catch {
        // Silent fail — don't block QA flow
      }
    }, 500);

    return () => {
      if (detectTimerRef.current) clearTimeout(detectTimerRef.current);
    };
  }, [transcriptContext, askedQuestions]);

  const handleAsk = async (q: string) => {
    if (!q.trim() || isAsking) return;

    setQuestion('');
    setAskedQuestions(prev => [...prev, q.trim()]);
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
    };
    setQaHistory((prev) => [...prev, newEntry]);

    try {
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
        <span className="material-symbols-outlined text-primary">question_answer</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">Live Q&A</h3>
      </div>

      {/* Chat History */}
      <div ref={containerRef} className="flex-1 overflow-y-auto p-4 space-y-4 min-h-0">
        {qaHistory.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-8 text-slate-400">
            <span className="material-symbols-outlined text-4xl mb-3">chat</span>
            <p className="text-sm text-center mb-4">
              미팅 중 궁금한 점을 물어보세요
            </p>
            <div className="flex flex-col gap-2 w-full">
              {suggestedQuestions.map((sq) => (
                <button
                  key={sq}
                  onClick={() => handleAsk(sq)}
                  disabled={isAsking}
                  className="text-left text-xs px-3 py-2 bg-primary/5 hover:bg-primary/10 text-primary/80 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  &quot;{sq}&quot;
                </button>
              ))}
            </div>
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
                      {/* Tool badges */}
                      {entry.toolsUsed && entry.toolsUsed.length > 0 && (
                        <div className="flex flex-wrap gap-1 mt-1.5">
                          {entry.toolsUsed.map((tool) => {
                            const info = TOOL_LABELS[tool];
                            if (!info) return null;
                            return (
                              <span
                                key={tool}
                                className={`inline-block text-[10px] px-1.5 py-0.5 rounded ${info.color}`}
                              >
                                {info.label}
                              </span>
                            );
                          })}
                        </div>
                      )}
                      {/* Fallback: legacy usedKB/usedDocs badges when toolsUsed is empty */}
                      {(!entry.toolsUsed || entry.toolsUsed.length === 0) && (
                        <div className="flex gap-1 mt-1.5">
                          {entry.usedKB !== undefined && (
                            <span className={`inline-block text-[10px] px-1.5 py-0.5 rounded ${entry.usedKB ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400' : 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-400'}`}>
                              {entry.usedKB ? 'KB 참조' : '모델 지식'}
                            </span>
                          )}
                          {entry.usedDocs && (
                            <span className="inline-block text-[10px] px-1.5 py-0.5 rounded bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400">
                              AWS Docs
                            </span>
                          )}
                        </div>
                      )}
                      {entry.sources && entry.sources.length > 0 && (
                        <div className="mt-2 pt-1.5 border-t border-slate-100 dark:border-slate-700">
                          <div className="flex flex-wrap gap-1">
                            {entry.sources.map((source, idx) => (
                              source.startsWith('http') ? (
                                <a
                                  key={idx}
                                  href={source}
                                  target="_blank"
                                  rel="noopener noreferrer"
                                  className="text-[10px] px-1.5 py-0.5 bg-blue-50 dark:bg-blue-900/20 text-blue-600 dark:text-blue-400 rounded hover:underline"
                                >
                                  {new URL(source).hostname}
                                </a>
                              ) : (
                                <span
                                  key={idx}
                                  className="text-[10px] px-1.5 py-0.5 bg-slate-100 dark:bg-slate-700 rounded text-slate-600 dark:text-slate-300"
                                >
                                  {source}
                                </span>
                              )
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

      {/* Detected Questions */}
      {detectedQuestions.length > 0 && (
        <div className="px-4 py-2 border-t border-slate-100 dark:border-slate-800">
          <p className="text-[10px] font-semibold text-slate-400 uppercase mb-1.5">감지된 질문</p>
          <div className="flex flex-wrap gap-1.5">
            {detectedQuestions.map((dq) => (
              <button
                key={dq}
                onClick={() => handleAsk(dq)}
                disabled={isAsking}
                className="text-xs px-2.5 py-1.5 bg-amber-50 dark:bg-amber-900/20 text-amber-700 dark:text-amber-400 border border-amber-200 dark:border-amber-800 rounded-full hover:bg-amber-100 dark:hover:bg-amber-900/40 transition-colors disabled:opacity-50"
              >
                {dq}
              </button>
            ))}
          </div>
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
