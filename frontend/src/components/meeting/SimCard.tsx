'use client';

import { useState } from 'react';
import { MarkdownRenderer } from '@/components/markdown/MarkdownRenderer';
import { useSimulation } from '@/hooks/useSimulation';
import type { SimRun, SimRequirement, SimOption } from '@/types/meeting';

interface SimCardProps {
  meetingId: string;
  simRun?: SimRun;
  onUpdate: (simRun: SimRun | undefined) => void;
}

/** `sim://chart_N` (1-indexed, matching the generated code's contract) ->
 * that chart's presigned CloudFront URL. Mirrors resolveAttachmentUrls in
 * MeetingDetailClient.tsx -- same rewrite-before-render shape, different
 * scheme. */
function resolveSimChartUrls(markdown: string, charts?: { url?: string }[]): string {
  if (!markdown || !charts?.length) return markdown;
  return markdown.replace(/sim:\/\/chart_(\d+)/g, (match, n) => {
    const chart = charts[Number(n) - 1];
    return chart?.url || match;
  });
}

function RequirementRow({
  req,
  onChange,
}: {
  req: SimRequirement;
  onChange: (value: string) => void;
}) {
  return (
    <div className="flex items-center gap-3 py-2 border-b border-slate-100 dark:border-white/5 last:border-0">
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium text-slate-700 dark:text-text-secondary">
          {req.label}
          {req.required && <span className="text-red-500 ml-1">*</span>}
        </div>
        {req.evidence && (
          <a href={req.evidence.replace('transcript://', '#ts-')} className="text-xs text-primary hover:underline">
            녹취록에서 확인
          </a>
        )}
      </div>
      <input
        type="text"
        value={req.value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={req.required ? '필수' : '선택'}
        className="w-40 px-3 py-1.5 text-sm rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest dark:text-text-main focus:outline-none focus:ring-2 focus:ring-primary/40"
      />
    </div>
  );
}

/**
 * Exported wrapper only remounts SimCardInner (keyed on simRunId) when a
 * fresh extraction produces a new run -- the queued/running/done polling
 * updates that follow keep the same simRunId, so the confirm form's local
 * draft state is preserved across cheap status changes.
 */
export function SimCard({ meetingId, simRun, onUpdate }: SimCardProps) {
  return <SimCardInner key={simRun?.simRunId ?? 'none'} meetingId={meetingId} simRun={simRun} onUpdate={onUpdate} />;
}

function SimCardInner({ meetingId, simRun, onUpdate }: SimCardProps) {
  const { extract, run, isExtracting, isSubmitting, error, setError } = useSimulation({ meetingId, simRun, onUpdate });
  // Seeded once from the extraction draft -- no effect needed since a new
  // extraction remounts this component (see the key above) instead of
  // updating props in place.
  const [draftReqs, setDraftReqs] = useState<SimRequirement[]>(
    simRun?.status === 'extracted' ? simRun.requirements ?? [] : []
  );
  const [draftOpts, setDraftOpts] = useState<SimOption[]>([{ name: '' }, { name: '' }]);

  const updateReq = (key: string, value: string) => {
    setDraftReqs((prev) => prev.map((r) => (r.key === key ? { ...r, value, source: 'user' } : r)));
  };

  const updateOpt = (idx: number, field: 'name' | 'description', value: string) => {
    setDraftOpts((prev) => prev.map((o, i) => (i === idx ? { ...o, [field]: value } : o)));
  };

  const canSubmit =
    draftOpts.filter((o) => o.name.trim()).length >= 2 &&
    draftReqs.every((r) => !r.required || r.value.trim());

  if (!simRun) {
    return (
      <section className="mb-12">
        <h3 className="text-base font-bold flex items-center gap-2 mb-4 dark:text-text-main">
          <span className="material-symbols-outlined text-primary">query_stats</span>
          비용·사이징 시뮬레이션 (베타)
        </h3>
        <div className="bg-white dark:bg-surface-lowest glass-panel rounded-xl p-5 dark:border dark:border-white/10 flex items-center justify-between gap-4">
          <p className="text-sm text-slate-600 dark:text-text-secondary">
            회의에서 언급된 정량적 요구사항으로 아키텍처 대안의 비용을 비교합니다. AWS Code Interpreter에서 실제로 실행된 코드로 계산하며, 실행 코드는 언제나 다운로드해 검증할 수 있습니다.
          </p>
          <button
            onClick={extract}
            disabled={isExtracting}
            className="shrink-0 px-4 py-2 rounded-lg bg-[#3211d4] text-white text-sm font-medium disabled:opacity-50"
          >
            {isExtracting ? '분석 중...' : '시뮬레이션 실행'}
          </button>
        </div>
        {error && <p className="text-sm text-red-500 mt-2">{error}</p>}
      </section>
    );
  }

  return (
    <section className="mb-12">
      <h3 className="text-base font-bold flex items-center gap-2 mb-4 dark:text-text-main">
        <span className="material-symbols-outlined text-primary">query_stats</span>
        비용·사이징 시뮬레이션 (베타)
      </h3>

      <div className="bg-white dark:bg-surface-lowest glass-panel rounded-xl p-5 dark:border dark:border-white/10">
        {simRun.status === 'extracted' && (
          <div>
            <p className="text-sm text-slate-500 dark:text-text-muted mb-4">
              회의에서 추출된 값을 확인·보정한 뒤 비교할 아키텍처 대안 2~3개를 입력하세요.
            </p>
            {draftReqs.length === 0 ? (
              <p className="text-sm text-amber-600 dark:text-amber-400 mb-4">
                이 회의에서 정량적 요구사항을 찾지 못했습니다. 직접 입력하려면 아래 값을 채워주세요.
              </p>
            ) : (
              <div className="mb-4">
                {draftReqs.map((r) => (
                  <RequirementRow key={r.key} req={r} onChange={(v) => updateReq(r.key, v)} />
                ))}
              </div>
            )}

            <div className="mb-4">
              <div className="text-sm font-medium text-slate-700 dark:text-text-secondary mb-2">비교할 아키텍처 대안</div>
              {draftOpts.map((opt, i) => (
                <div key={i} className="flex gap-2 mb-2">
                  <input
                    type="text"
                    value={opt.name}
                    onChange={(e) => updateOpt(i, 'name', e.target.value)}
                    placeholder={`대안 ${i + 1} 이름`}
                    className="w-40 px-3 py-1.5 text-sm rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest dark:text-text-main"
                  />
                  <input
                    type="text"
                    value={opt.description || ''}
                    onChange={(e) => updateOpt(i, 'description', e.target.value)}
                    placeholder="간단한 설명 (선택)"
                    className="flex-1 px-3 py-1.5 text-sm rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest dark:text-text-main"
                  />
                </div>
              ))}
              {draftOpts.length < 3 && (
                <button
                  onClick={() => setDraftOpts((prev) => [...prev, { name: '' }])}
                  className="text-xs text-primary hover:underline"
                >
                  + 대안 추가
                </button>
              )}
            </div>

            <button
              onClick={() => run(draftReqs, draftOpts.filter((o) => o.name.trim()))}
              disabled={!canSubmit || isSubmitting}
              className="px-4 py-2 rounded-lg bg-[#3211d4] text-white text-sm font-medium disabled:opacity-50"
            >
              {isSubmitting ? '시작 중...' : '실행'}
            </button>
            {error && <p className="text-sm text-red-500 mt-2">{error}</p>}
          </div>
        )}

        {(simRun.status === 'queued' || simRun.status === 'running') && (
          <div className="flex items-center gap-3 py-4">
            <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent" />
            <span className="text-sm text-slate-600 dark:text-text-secondary">
              시뮬레이션을 실행하고 있습니다 (1~3분 소요) — 다른 작업을 계속하셔도 됩니다.
            </span>
          </div>
        )}

        {simRun.status === 'error' && (
          <div>
            <p className="text-sm text-red-500 mb-3">{simRun.errorMessage || '시뮬레이션 실행에 실패했습니다.'}</p>
            <button
              onClick={() => { setError(null); extract(); }}
              className="px-4 py-2 rounded-lg border border-slate-200 dark:border-white/10 text-sm font-medium"
            >
              다시 시도
            </button>
          </div>
        )}

        {simRun.status === 'done' && (
          <div>
            <div className="flex items-center gap-2 mb-3 px-3 py-2 rounded-lg bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800">
              <span className="material-symbols-outlined text-amber-500 text-lg">info</span>
              <span className="text-xs text-amber-700 dark:text-amber-300">
                추정치입니다 — 고객에게 제시하기 전 반드시 검증하세요. 단가 기준 시각: {simRun.priceSnapshotAt || '알 수 없음'}
              </span>
            </div>
            <MarkdownRenderer content={resolveSimChartUrls(simRun.reportMarkdown || '', simRun.charts)} />
            <div className="mt-3 flex items-center gap-3">
              <button onClick={extract} className="text-xs text-primary hover:underline">
                다시 실행
              </button>
              {simRun.codeUrl && (
                <a
                  href={simRun.codeUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-xs text-primary hover:underline flex items-center gap-1"
                >
                  <span className="material-symbols-outlined text-sm">code</span>
                  실행된 코드 보기
                </a>
              )}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
