'use client';

import { useState, useRef, useEffect } from 'react';
import { qaApi } from '@/lib/api';
import type { QAEntry } from '@/types/meeting';
import type { QuestionDraft, QAReferenceEvidence } from '@/lib/meetingReferences';
import { QAChatMessage, QASuggestedQuestions, QAEmptyState } from '@/components/qa';

interface QAPanelProps {
  meetingId: string;
  questionDraft?: QuestionDraft;
  onSaveToNotes?: (question: string, answer: string, evidence?: QAReferenceEvidence) => void;
}

type AnswerEntry = QAEntry & { status: 'pending' | 'complete' | 'error' };

const defaultSuggestions = [
  '주요 논의 사항은?',
  '결정된 액션 아이템은?',
  '참석자별 발언 요약',
];

export function QAPanel(props: QAPanelProps) {
  return <MeetingQAPanel key={props.meetingId} {...props} />;
}

function MeetingQAPanel({ meetingId, questionDraft, onSaveToNotes }: QAPanelProps) {
  const [question, setQuestion] = useState('');
  const [qaHistory, setQaHistory] = useState<AnswerEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedEntryIds, setSavedEntryIds] = useState<Set<string>>(new Set());
  const [draftId, setDraftId] = useState<number>();
  const [pendingDraft, setPendingDraft] = useState<QuestionDraft | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const activeRef = useRef(true);
  const askingRef = useRef(false);
  const sessionIdRef = useRef<string | null>(null);

  // Consume each explicit reference action once. Never replace a typed draft.
  if (questionDraft && questionDraft.id !== draftId) {
    setDraftId(questionDraft.id);
    if (!question.trim()) setQuestion(questionDraft.text);
    else setPendingDraft(questionDraft);
  }

  useEffect(() => {
    activeRef.current = true;
    return () => { activeRef.current = false; };
  }, []);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [qaHistory]);

  const handleAsk = async (q: string) => {
    if (!q.trim() || askingRef.current) return;
    askingRef.current = true;

    setError(null);
    setIsAsking(true);

    // Add question to history immediately
    const entryId = crypto.randomUUID();
    const newEntry: AnswerEntry = {
      id: entryId,
      question: q.trim(),
      answer: '',
      timestamp: new Date().toISOString(),
      status: 'pending',
    };
    setQaHistory((prev) => [...prev, newEntry]);

    try {
      const sessionId = sessionIdRef.current ??= `qa-${meetingId}-${crypto.randomUUID()}`;
      const response = await qaApi.askMeeting(meetingId, q.trim(), sessionId);
      if (!activeRef.current) return;
      if (!response.answer?.trim()) throw new Error('답변이 비어 있습니다. 다시 시도해주세요.');
      setQuestion(current => current.trim() === q.trim() ? '' : current);
      setQaHistory((prev) =>
        prev.map((entry) =>
          entry.id === entryId
            ? {
                ...entry,
                answer: response.answer,
                sources: response.sources,
                sourceDetails: response.sourceDetails,
                usedKB: response.usedKB,
                usedDocs: response.usedDocs,
                toolsUsed: response.toolsUsed,
                status: 'complete',
              }
            : entry
        )
      );
    } catch (err) {
      if (!activeRef.current) return;
      // HTTP failure cannot prove the worker stopped. Isolate the next explicit
      // turn from late writes without clearing the visible history or retrying.
      sessionIdRef.current = null;
      setError(err instanceof Error ? err.message : 'Failed to get answer');
      setQuestion(current => current || q);
      setQaHistory((prev) =>
        prev.map((entry) =>
          entry.id === entryId
            ? { ...entry, status: 'error', answer: '죄송합니다. 답변을 생성하지 못했습니다. 다시 시도해주세요.' }
            : entry
        )
      );
    } finally {
      askingRef.current = false;
      if (activeRef.current) {
        setIsAsking(false);
        inputRef.current?.focus();
      }
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    handleAsk(question);
  };

  return (
    <div className="flex flex-col h-full bg-white dark:bg-surface-lowest rounded-xl lg:rounded-none border border-slate-200 dark:border-white/10 lg:border-0">
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
              sourceDetails={entry.sourceDetails}
              usedKB={entry.usedKB}
              usedDocs={entry.usedDocs}
              toolsUsed={entry.toolsUsed}
              isStreaming={entry.status === 'pending'}
              onSaveToNotes={onSaveToNotes && entry.status === 'complete' ? () => {
                try {
                  onSaveToNotes(entry.question, entry.answer, {
                    sources: entry.sources, sourceDetails: entry.sourceDetails,
                  });
                  setSavedEntryIds(prev => new Set(prev).add(entry.id));
                } catch (error) {
                  setError(error instanceof Error ? error.message : '메모에 추가하지 못했습니다.');
                }
              } : undefined}
              isSavedToNotes={savedEntryIds.has(entry.id)}
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
        {pendingDraft && (
          <div className="mb-2 text-xs text-slate-500">
            작성 중인 질문을 유지했습니다.
            <button type="button" className="ml-2 text-primary underline" disabled={isAsking} onClick={() => {
              setQuestion(current => `${current.trimEnd()}\n${pendingDraft.text}`.trim());
              setPendingDraft(null);
              inputRef.current?.focus();
            }}>참조 질문 덧붙이기</button>
            <button type="button" className="ml-2 underline" onClick={() => setPendingDraft(null)}>닫기</button>
          </div>
        )}
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
