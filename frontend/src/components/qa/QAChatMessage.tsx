'use client';

import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import Link from 'next/link';
import { qaSources } from '@/lib/qaSources';
import type { QASourceDetail } from '@/types/meeting';

const TOOL_LABELS: Record<string, { label: string; color: string }> = {
  search_knowledge_base: { label: 'KB 검색', color: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400' },
  search_aws_docs: { label: 'AWS Docs', color: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400' },
  search_transcript: { label: '회의록 검색', color: 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-400' },
  get_aws_recommendation: { label: 'AWS 추천', color: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400' },
  search_web: { label: '웹 검색', color: 'bg-sky-100 text-sky-700 dark:bg-sky-900/30 dark:text-sky-400' },
};

interface QAChatMessageProps {
  question: string;
  answer: string;
  sources?: string[];
  sourceDetails?: QASourceDetail[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  isStreaming?: boolean;
  /** Adds a completed answer to the parent's notes draft; does not attest persistence. */
  onSaveToNotes?: () => void;
  isSavedToNotes?: boolean;
  /** AI-suggested follow-up questions for this answer */
  followUps?: string[];
  onAskFollowUp?: (question: string) => void;
  followUpsDisabled?: boolean;
  /** Auto-fired proactive search (detected from the conversation, not typed by the user) */
  isProactive?: boolean;
}

export function QAChatMessage({ question, answer, sources, sourceDetails, usedKB, usedDocs, toolsUsed, isStreaming, onSaveToNotes, isSavedToNotes, followUps, onAskFollowUp, followUpsDisabled, isProactive }: QAChatMessageProps) {
  const isLoading = !answer && !isStreaming;
  const displayedSources = qaSources(sources, sourceDetails);

  return (
    <div className="space-y-3 animate-fade-in">
      {/* Question bubble - right aligned */}
      <div className="flex justify-end">
        <div className={`rounded-2xl rounded-tr-sm px-4 py-2.5 max-w-[85%] ${isProactive ? 'bg-sky-50 dark:bg-sky-900/20 border border-sky-200/60 dark:border-sky-800/60' : 'bg-primary/10'}`}>
          {isProactive && (
            <span className="flex items-center gap-1 text-[10px] font-semibold text-sky-600 dark:text-sky-400 uppercase mb-0.5">
              <span className="material-symbols-outlined text-xs">travel_explore</span>
              선제 검색 · 대화에서 감지된 질문
            </span>
          )}
          <p className="text-sm text-slate-900 dark:text-gray-100">{question}</p>
        </div>
      </div>

      {/* Answer bubble - left aligned with AI avatar */}
      <div className="flex justify-start gap-2">
        {/* AI Avatar */}
        <div className="flex-shrink-0 w-7 h-7 rounded-full bg-primary flex items-center justify-center">
          <span className="material-symbols-outlined text-white text-sm">auto_awesome</span>
        </div>

        <div className="bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-2xl rounded-tl-sm px-4 py-2.5 max-w-[calc(85%-36px)]">
          {isLoading ? (
            <div className="flex items-center gap-1.5 py-1">
              <span className="text-sm text-slate-500 dark:text-slate-400">답변을 생성하고 있어요</span>
              <div className="flex gap-1">
                <span
                  className="w-1.5 h-1.5 rounded-full bg-primary animate-bounce"
                  style={{ animationDelay: '0ms' }}
                />
                <span
                  className="w-1.5 h-1.5 rounded-full bg-primary animate-bounce"
                  style={{ animationDelay: '150ms' }}
                />
                <span
                  className="w-1.5 h-1.5 rounded-full bg-primary animate-bounce"
                  style={{ animationDelay: '300ms' }}
                />
              </div>
            </div>
          ) : isStreaming && !answer ? (
            <div className="flex items-center gap-1.5 py-1">
              <span className="text-sm text-slate-500 dark:text-slate-400">AI가 답변을 작성 중...</span>
              <span className="inline-block w-0.5 h-4 bg-primary animate-pulse" />
            </div>
          ) : (
            <>
              <div className="text-sm text-slate-700 dark:text-slate-300 prose prose-sm dark:prose-invert max-w-none prose-p:my-1 prose-headings:my-2 prose-ul:my-1 prose-ol:my-1 prose-li:my-0.5 prose-table:my-2 prose-pre:my-2 prose-hr:my-2 prose-th:border prose-th:border-slate-300 dark:prose-th:border-slate-600 prose-th:px-2 prose-th:py-1 prose-td:border prose-td:border-slate-200 dark:prose-td:border-slate-700 prose-td:px-2 prose-td:py-1">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{answer}</ReactMarkdown>
                {isStreaming && <span className="inline-block w-0.5 h-4 ml-0.5 bg-primary animate-pulse align-middle" />}
              </div>

              {/* Tool badges */}
              {toolsUsed && toolsUsed.length > 0 && (
                <div className="flex flex-wrap gap-1 mt-2">
                  {toolsUsed.map((tool) => {
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

              {/* Legacy fallback badges when toolsUsed is empty */}
              {(!toolsUsed || toolsUsed.length === 0) && (usedKB !== undefined || usedDocs) && (
                <div className="flex gap-1 mt-2">
                  {usedKB !== undefined && (
                    <span className={`inline-block text-[10px] px-1.5 py-0.5 rounded ${usedKB ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400' : 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-400'}`}>
                      {usedKB ? 'KB 참조' : '모델 지식'}
                    </span>
                  )}
                  {usedDocs && (
                    <span className="inline-block text-[10px] px-1.5 py-0.5 rounded bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400">
                      AWS Docs
                    </span>
                  )}
                </div>
              )}

              {/* Sources */}
              {displayedSources.length > 0 && (
                <div className="mt-2.5 pt-2 border-t border-slate-100 dark:border-slate-700">
                  <p className="text-[10px] font-semibold text-slate-400 uppercase mb-1.5">
                    Sources
                  </p>
                  <div className="flex flex-wrap gap-1">
                    {displayedSources.map((source, idx) => (
                      <span key={idx} className="max-w-full break-words rounded bg-slate-100 px-2 py-1 text-[11px] text-slate-700 dark:bg-slate-700 dark:text-slate-200">
                        {source.href ? source.external ? (
                          <a href={source.href} target="_blank" rel="noopener noreferrer" className="text-blue-600 hover:underline dark:text-blue-300">{source.label}</a>
                        ) : <Link href={source.href} className="text-blue-600 hover:underline dark:text-blue-300">{source.label}</Link>
                          : source.label}
                        {source.kind && <span className="ml-1 text-slate-500 dark:text-slate-400">· {source.kind}</span>}
                        {source.caveats.map((caveat) => <span key={caveat} className="ml-1 text-amber-800 dark:text-amber-200">· {caveat}</span>)}
                      </span>
                    ))}
                  </div>
                </div>
              )}

              {/* Save to meeting notes */}
              {onSaveToNotes && !isStreaming && answer && (
                <div className="mt-2 pt-2 border-t border-slate-100 dark:border-slate-700 flex justify-end">
                  <button
                    type="button"
                    aria-label={isSavedToNotes ? '메모에 추가됨' : '메모에 추가'}
                    onClick={onSaveToNotes}
                    disabled={isSavedToNotes}
                    className={`flex items-center gap-1 text-[11px] font-medium px-2 py-1 rounded-md transition-colors ${
                      isSavedToNotes
                        ? 'text-emerald-500 cursor-default'
                        : 'text-slate-500 dark:text-text-muted hover:text-primary hover:bg-primary/5'
                    }`}
                  >
                    <span aria-hidden="true" className="material-symbols-outlined text-sm">
                      {isSavedToNotes ? 'check_circle' : 'note_add'}
                    </span>
                    {isSavedToNotes ? '메모에 추가됨' : '메모에 추가'}
                  </button>
                </div>
              )}

              {/* Follow-up question cards */}
              {followUps && followUps.length > 0 && onAskFollowUp && !isStreaming && (
                <div className="mt-2.5 pt-2 border-t border-slate-100 dark:border-slate-700">
                  <p className="flex items-center gap-1 text-[10px] font-semibold text-slate-400 dark:text-text-muted uppercase mb-1.5">
                    <span className="material-symbols-outlined text-xs">forum</span>
                    추가 질문
                  </p>
                  <div className="flex flex-col gap-1.5">
                    {followUps.map((q) => (
                      <button
                        key={q}
                        onClick={() => onAskFollowUp(q)}
                        disabled={followUpsDisabled}
                        className="text-left text-xs px-2.5 py-1.5 rounded-lg border border-primary/20 bg-primary/5 text-primary hover:bg-primary/10 hover:border-primary/40 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                      >
                        {q}
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
