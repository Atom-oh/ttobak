'use client';

const TOOL_LABELS: Record<string, { label: string; color: string }> = {
  search_knowledge_base: { label: 'KB 검색', color: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400' },
  search_aws_docs: { label: 'AWS Docs', color: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400' },
  search_transcript: { label: '회의록 검색', color: 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-400' },
  get_aws_recommendation: { label: 'AWS 추천', color: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400' },
};

interface QAChatMessageProps {
  question: string;
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  isStreaming?: boolean;
}

export function QAChatMessage({ question, answer, sources, usedKB, usedDocs, toolsUsed, isStreaming }: QAChatMessageProps) {
  const isLoading = !answer && !isStreaming;

  return (
    <div className="space-y-3 animate-fade-in">
      {/* Question bubble - right aligned */}
      <div className="flex justify-end">
        <div className="bg-primary/10 rounded-2xl rounded-tr-sm px-4 py-2.5 max-w-[85%]">
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
              <p className="text-sm text-slate-700 dark:text-slate-300 whitespace-pre-wrap">
                {answer}
                {isStreaming && <span className="inline-block w-0.5 h-4 ml-0.5 bg-primary animate-pulse align-middle" />}
              </p>

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
              {sources && sources.length > 0 && (
                <div className="mt-2.5 pt-2 border-t border-slate-100 dark:border-slate-700">
                  <p className="text-[10px] font-semibold text-slate-400 uppercase mb-1.5">
                    Sources
                  </p>
                  <div className="flex flex-wrap gap-1">
                    {sources.map((source, idx) => (
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
          )}
        </div>
      </div>
    </div>
  );
}
