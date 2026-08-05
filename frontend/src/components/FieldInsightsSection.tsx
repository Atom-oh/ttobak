'use client';

import { useMemo, useState } from 'react';
import type { FieldInsight } from '@/types/meeting';
import { INSIGHT_TYPES } from '@/types/meeting';

interface FieldInsightsSectionProps {
  insights: FieldInsight[];
  error?: string;
}

const insightMeta: Record<string, {
  label: string;
  icon: string;
  accent: string;
  iconStyle: string;
  badgeStyle: string;
}> = {
  trend: {
    label: '트렌드',
    icon: 'trending_up',
    accent: 'border-l-cyan-500',
    iconStyle: 'bg-cyan-50 text-cyan-700 dark:bg-cyan-500/10 dark:text-cyan-300',
    badgeStyle: 'bg-cyan-50 text-cyan-700 dark:bg-cyan-500/10 dark:text-cyan-300',
  },
  need: {
    label: '고객 니즈',
    icon: 'ads_click',
    accent: 'border-l-blue-500',
    iconStyle: 'bg-blue-50 text-blue-700 dark:bg-blue-500/10 dark:text-blue-300',
    badgeStyle: 'bg-blue-50 text-blue-700 dark:bg-blue-500/10 dark:text-blue-300',
  },
  competitive: {
    label: '경쟁 동향',
    icon: 'compare_arrows',
    accent: 'border-l-amber-500',
    iconStyle: 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300',
    badgeStyle: 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300',
  },
  risk: {
    label: '리스크',
    icon: 'warning',
    accent: 'border-l-rose-500',
    iconStyle: 'bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300',
    badgeStyle: 'bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300',
  },
  opportunity: {
    label: '기회',
    icon: 'lightbulb',
    accent: 'border-l-emerald-500',
    iconStyle: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300',
    badgeStyle: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300',
  },
  tech: {
    label: '기술·워크로드',
    icon: 'memory',
    accent: 'border-l-violet-500',
    iconStyle: 'bg-violet-50 text-violet-700 dark:bg-violet-500/10 dark:text-violet-300',
    badgeStyle: 'bg-violet-50 text-violet-700 dark:bg-violet-500/10 dark:text-violet-300',
  },
  stakeholder: {
    label: '이해관계자',
    icon: 'groups',
    accent: 'border-l-slate-500',
    iconStyle: 'bg-slate-100 text-slate-700 dark:bg-slate-500/15 dark:text-slate-300',
    badgeStyle: 'bg-slate-100 text-slate-700 dark:bg-slate-500/15 dark:text-slate-300',
  },
  action: {
    label: '다음 액션',
    icon: 'task_alt',
    accent: 'border-l-orange-500',
    iconStyle: 'bg-orange-50 text-orange-700 dark:bg-orange-500/10 dark:text-orange-300',
    badgeStyle: 'bg-orange-50 text-orange-700 dark:bg-orange-500/10 dark:text-orange-300',
  },
};

const fallbackMeta = {
  label: '기타',
  icon: 'insights',
  accent: 'border-l-slate-400',
  iconStyle: 'bg-slate-100 text-slate-700 dark:bg-white/10 dark:text-slate-300',
  badgeStyle: 'bg-slate-100 text-slate-700 dark:bg-white/10 dark:text-slate-300',
};

function formatMarker(marker?: string): string | null {
  const seconds = Number(marker?.match(/^\[TS:(\d+)\]$/)?.[1]);
  if (!Number.isFinite(seconds)) return marker || null;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, '0')}`;
}

export function FieldInsightsSection({ insights, error }: FieldInsightsSectionProps) {
  const [activeType, setActiveType] = useState('');
  const counts = useMemo(
    () => insights.reduce<Record<string, number>>((result, insight) => {
      result[insight.type] = (result[insight.type] || 0) + 1;
      return result;
    }, {}),
    [insights]
  );
  const selectedType = activeType && counts[activeType] ? activeType : '';
  const shownInsights = selectedType
    ? insights.filter((insight) => insight.type === selectedType)
    : insights;

  return (
    <section className="mb-10" aria-labelledby="field-insights-heading">
      <div className="mb-4 flex flex-wrap items-end justify-between gap-2">
        <div>
          <h3
            id="field-insights-heading"
            className="text-base font-bold text-slate-900 dark:text-text-main"
          >
            필드 인사이트
          </h3>
          <p className="mt-1 text-sm text-slate-500 dark:text-text-muted">
            회의에서 확인된 신호와 후속 조치를 유형별로 정리했습니다.
          </p>
        </div>
        {!error && insights.length > 0 && (
          <span className="text-xs tabular-nums text-slate-400 dark:text-text-muted">
            총 {insights.length}건
          </span>
        )}
      </div>

      {!error && insights.length > 0 && (
        <div className="-mx-1 mb-4 overflow-x-auto px-1 pb-1">
          <div className="flex min-w-max gap-2 lg:min-w-0 lg:flex-wrap" role="group" aria-label="인사이트 유형 필터">
            <button
              type="button"
              onClick={() => setActiveType('')}
              aria-pressed={selectedType === ''}
              className={`rounded-md border px-3 py-1.5 text-xs font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 ${
                selectedType === ''
                  ? 'border-primary bg-primary text-white'
                  : 'border-slate-200 bg-white text-slate-600 hover:border-slate-300 dark:border-white/10 dark:bg-transparent dark:text-text-muted dark:hover:border-white/20'
              }`}
            >
              전체 <span className="ml-1 tabular-nums opacity-75">{insights.length}</span>
            </button>
            {INSIGHT_TYPES.filter((type) => counts[type]).map((type) => {
              const meta = insightMeta[type] || fallbackMeta;
              return (
                <button
                  type="button"
                  key={type}
                  onClick={() => setActiveType(type)}
                  aria-pressed={selectedType === type}
                  className={`rounded-md border px-3 py-1.5 text-xs font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 ${
                    selectedType === type
                      ? 'border-primary bg-primary text-white'
                      : 'border-slate-200 bg-white text-slate-600 hover:border-slate-300 dark:border-white/10 dark:bg-transparent dark:text-text-muted dark:hover:border-white/20'
                  }`}
                >
                  {meta.label} <span className="ml-1 tabular-nums opacity-75">{counts[type]}</span>
                </button>
              );
            })}
          </div>
        </div>
      )}

      {error ? (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-600 dark:border-red-500/20 dark:bg-red-500/10 dark:text-red-300">
          {error}
        </div>
      ) : shownInsights.length === 0 ? (
        <div className="rounded-lg border border-dashed border-slate-200 py-10 text-center dark:border-white/10">
          <span className="material-symbols-outlined text-3xl text-slate-300 dark:text-text-muted" aria-hidden="true">
            lightbulb
          </span>
          <p className="mt-2 text-sm text-slate-400 dark:text-text-muted">아직 추출된 인사이트가 없습니다.</p>
        </div>
      ) : (
        <div className="grid gap-3">
          {shownInsights.map((insight, index) => {
            const meta = insightMeta[insight.type] || fallbackMeta;
            const marker = formatMarker(insight.tsMarker);
            const isMeetingSource = !insight.sourceType || insight.sourceType === 'meeting';
            return (
              <article
                key={`${insight.sourceId}-${insight.occurredAt}-${insight.type}-${index}`}
                className={`rounded-lg border border-l-4 border-slate-200 bg-white p-4 shadow-sm dark:border-white/10 dark:bg-white/[0.03] lg:p-5 ${meta.accent}`}
              >
                <div className="flex items-start gap-3">
                  <div className={`flex size-9 shrink-0 items-center justify-center rounded-md ${meta.iconStyle}`}>
                    <span className="material-symbols-outlined text-xl" aria-hidden="true">{meta.icon}</span>
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <span className={`rounded px-2 py-0.5 text-xs font-semibold ${meta.badgeStyle}`}>
                        {meta.label}
                      </span>
                      <time className="text-xs text-slate-400 dark:text-text-muted" dateTime={insight.occurredAt}>
                        {new Date(insight.occurredAt).toLocaleDateString('ko-KR')}
                      </time>
                    </div>
                    <h4 className="mt-2 max-w-[76ch] text-[15px] font-semibold leading-6 text-slate-900 dark:text-text-main">
                      {insight.text}
                    </h4>
                  </div>
                </div>

                {(insight.evidence || insight.implication) && (
                  <dl className="mt-4 grid gap-3 border-t border-slate-100 pt-4 dark:border-white/5 md:grid-cols-2">
                    {insight.evidence && (
                      <div>
                        <dt className="flex items-center gap-1.5 text-xs font-semibold text-slate-500 dark:text-text-muted">
                          <span className="material-symbols-outlined text-base" aria-hidden="true">format_quote</span>
                          근거
                        </dt>
                        <dd className="mt-1 text-sm leading-6 text-slate-700 dark:text-text-secondary">{insight.evidence}</dd>
                      </div>
                    )}
                    {insight.implication && (
                      <div>
                        <dt className="flex items-center gap-1.5 text-xs font-semibold text-slate-500 dark:text-text-muted">
                          <span className="material-symbols-outlined text-base" aria-hidden="true">conversion_path</span>
                          의미
                        </dt>
                        <dd className="mt-1 text-sm leading-6 text-slate-700 dark:text-text-secondary">{insight.implication}</dd>
                      </div>
                    )}
                  </dl>
                )}

                {insight.nextAction && (
                  <div className="mt-3 rounded-md bg-slate-50 px-3 py-2.5 dark:bg-black/15">
                    <p className="flex items-start gap-2 text-sm leading-6 text-slate-700 dark:text-text-secondary">
                      <span className="material-symbols-outlined mt-0.5 text-base text-emerald-600 dark:text-emerald-400" aria-hidden="true">
                        arrow_forward
                      </span>
                      <span>
                        <span className="mr-2 text-xs font-bold text-slate-500 dark:text-text-muted">다음 액션</span>
                        {insight.nextAction}
                      </span>
                    </p>
                  </div>
                )}

                <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
                  <div className="flex flex-wrap gap-1.5">
                    {[...new Set(insight.entities || [])].map((entity) => (
                      <span
                        key={entity}
                        className="rounded border border-slate-200 px-2 py-0.5 text-xs text-slate-500 dark:border-white/10 dark:text-text-muted"
                      >
                        {entity}
                      </span>
                    ))}
                  </div>
                  {insight.sourceId && isMeetingSource ? (
                    <a
                      href={`/meeting/${encodeURIComponent(insight.sourceId)}`}
                      className="ml-auto inline-flex min-w-0 max-w-full flex-wrap items-center justify-end gap-1 break-all text-right text-xs font-semibold text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50"
                      aria-label={`출처 회의 ${insight.sourceId} 보기${marker ? `, ${marker}` : ''}`}
                    >
                      <span className="material-symbols-outlined text-base" aria-hidden="true">video_file</span>
                      출처: 회의 · {insight.sourceId}{marker ? ` · ${marker}` : ''}
                    </a>
                  ) : insight.sourceId ? (
                    <span className="ml-auto inline-flex min-w-0 max-w-full flex-wrap items-center justify-end gap-1 break-all text-right text-xs text-slate-500 dark:text-text-muted">
                      <span className="material-symbols-outlined text-base" aria-hidden="true">source</span>
                      출처: {insight.sourceType} · {insight.sourceId}{marker ? ` · ${marker}` : ''}
                    </span>
                  ) : null}
                </div>
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}
