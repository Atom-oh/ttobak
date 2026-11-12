'use client';

import { useEffect, useRef, useState } from 'react';
import { performanceApi } from '@/lib/api';
import { errorCode, intakeCodeLabel } from '@/lib/intake';
import {
  customRangeError, formatElapsed, HIGHLIGHT_FIELDS, highlightsText, newestReportOfKind, parseHighlightLines,
  REPORT_COPY, REPORT_PERIOD_OPTIONS, bedrockNeedsProfile, bedrockToggleProfile, channelRows, reportCodeLabel, toggleExcluded,
  type PerformanceReportHighlights, type ReportPeriodKind, type ReportSettingsView, type ReportStateView, type ReportSummaryRow,
} from '@/lib/performanceReport';
import { cancelReport, generateReport, getReportSettings, refreshSlackChannels, setReportSettings } from '@/lib/tauri';

type HighlightTexts = Partial<Record<keyof PerformanceReportHighlights, string>>;

/** Report codes first, then the shared runner codes (ADAPTER_MISSING, NODE_MISSING). */
export function reportMessage(code: string): string {
  const label = reportCodeLabel(code);
  return label !== code ? label : intakeCodeLabel(code);
}

const inputClass = 'w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-900 dark:border-white/10 dark:bg-surface-lowest dark:text-text-main';

/**
 * [리포트 만들기] dialog of the Mac app (never a browser dialog). The period
 * and four highlight lists start from the newest stored report of the same
 * period kind; while a run is going it shows elapsed time, progress and
 * [취소]. Closing it leaves the run going.
 */
export function ReportGenerateDialog({ rows, userId, state, progress, startedAt, message, onStarted, onRefresh, onClose }: {
  rows: ReportSummaryRow[];
  userId: string;
  state: ReportStateView;
  progress: string[];
  startedAt: number | null;
  /** Message of the last finished run, shown until the next start. */
  message: string;
  onStarted: () => void;
  onRefresh: () => Promise<unknown>;
  onClose: () => void;
}) {
  const [kind, setKind] = useState<ReportPeriodKind>('week');
  const [start, setStart] = useState('');
  const [end, setEnd] = useState('');
  // User edits per period kind win over the prefill from the stored report.
  const [drafts, setDrafts] = useState<Partial<Record<ReportPeriodKind, HighlightTexts>>>({});
  const [prefill, setPrefill] = useState<Partial<Record<ReportPeriodKind, HighlightTexts>>>({});
  const [settings, setSettings] = useState<ReportSettingsView | null>(null);
  const [settingsError, setSettingsError] = useState<string | null>(null);
  const [channelsBusy, setChannelsBusy] = useState(false);
  // Bedrock switched on but not saved until a profile is chosen.
  const [bedrockPending, setBedrockPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [cancelError, setCancelError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const dialogRef = useRef<HTMLDivElement>(null);
  const progressRef = useRef<HTMLOListElement>(null);
  const running = state.running;

  const sourceKey = newestReportOfKind(rows, kind);
  useEffect(() => {
    if (!sourceKey || prefill[kind]) return;
    const controller = new AbortController();
    performanceApi.report(sourceKey, { signal: controller.signal, expectedUserId: userId })
      .then(report => {
        const texts: HighlightTexts = {};
        for (const { field } of HIGHLIGHT_FIELDS) texts[field] = highlightsText(report?.highlights?.[field]);
        setPrefill(current => ({ ...current, [kind]: texts }));
      })
      // A missing prefill only leaves the lists empty.
      .catch(() => {});
    return () => controller.abort();
  }, [kind, sourceKey, userId, prefill]);

  useEffect(() => {
    let active = true;
    getReportSettings()
      .then(view => { if (active) setSettings(view); })
      .catch(err => { if (active) setSettingsError(reportMessage(errorCode(err))); });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    if (!running) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [running]);

  useEffect(() => {
    dialogRef.current?.focus();
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  useEffect(() => {
    const list = progressRef.current;
    if (list) list.scrollTop = list.scrollHeight;
  }, [progress]);

  const textOf = (field: keyof PerformanceReportHighlights) => drafts[kind]?.[field] ?? prefill[kind]?.[field] ?? '';
  const setText = (field: keyof PerformanceReportHighlights, value: string) =>
    setDrafts(current => ({ ...current, [kind]: { ...prefill[kind], ...current[kind], [field]: value } }));

  const updateSettings = async (useBedrock: boolean, awsProfile: string | null) => {
    setSettingsError(null);
    setBedrockPending(useBedrock && !awsProfile);
    // Bedrock without a profile cannot be saved: wait for the profile choice.
    if (useBedrock && !awsProfile) return;
    try {
      setSettings(await setReportSettings(useBedrock, awsProfile));
    } catch (err) {
      setSettingsError(reportMessage(errorCode(err)));
    }
  };

  const toggleChannel = async (name: string) => {
    if (!settings || channelsBusy) return;
    setSettingsError(null);
    // One save at a time: each save replaces the whole stored list.
    setChannelsBusy(true);
    try {
      setSettings(await setReportSettings(settings.useBedrock, bedrockToggleProfile(settings),
        toggleExcluded(settings.excludedSlackChannels ?? [], name)));
    } catch (err) {
      setSettingsError(reportMessage(errorCode(err)));
    } finally {
      setChannelsBusy(false);
    }
  };

  const reloadChannels = async () => {
    setChannelsBusy(true);
    setSettingsError(null);
    try {
      const slackChannels = await refreshSlackChannels();
      setSettings(current => current ? { ...current, slackChannels } : current);
    } catch (err) {
      setSettingsError(reportMessage(errorCode(err)));
    } finally {
      setChannelsBusy(false);
    }
  };

  const submit = async () => {
    setFormError(null);
    // The shown Bedrock choice is not saved yet; the run would use the stored one.
    if (bedrockPending || bedrockNeedsProfile(settings)) { setFormError(reportMessage('profile-required')); return; }
    if (kind === 'custom') {
      const rangeError = customRangeError(start, end);
      if (rangeError) { setFormError(rangeError); return; }
    }
    const highlights: Partial<PerformanceReportHighlights> = {};
    for (const { field, label } of HIGHLIGHT_FIELDS) {
      const parsed = parseHighlightLines(textOf(field));
      if (parsed.error) { setFormError(`${label}: ${parsed.error}`); return; }
      highlights[field] = parsed.items;
    }
    onStarted();
    try {
      await generateReport(kind === 'custom' ? { period: kind, start, end, highlights } : { period: kind, highlights });
    } catch (err) {
      setFormError(reportMessage(errorCode(err)));
      await onRefresh();
    }
  };

  const elapsed = running && startedAt !== null ? formatElapsed(now - startedAt) : null;
  const profiles = settings?.profiles ?? [];

  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/40 p-4 sm:items-center" onMouseDown={event => {
      if (event.target === event.currentTarget) onClose();
    }}>
      <div ref={dialogRef} tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby="report-dialog-title"
        className="max-h-[90vh] w-full max-w-lg overflow-y-auto rounded-xl bg-white p-5 shadow-xl outline-none dark:bg-surface-container">
        <div className="flex items-center justify-between gap-3">
          <h3 id="report-dialog-title" className="text-lg font-bold text-slate-900 dark:text-text-main">{REPORT_COPY.makeReport}</h3>
          <button type="button" onClick={onClose} aria-label="닫기" className="text-slate-500 hover:text-slate-900 dark:text-text-muted dark:hover:text-text-main">
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        {running ? (
          <div className="mt-4 space-y-3">
            <p className="text-sm text-slate-700 dark:text-text-main">
              리포트를 만들고 있어요{elapsed && <> · <span className="tabular-nums">{elapsed}</span></>}
            </p>
            <p className="text-xs text-slate-500 dark:text-text-muted">창을 닫아도 계속 만들어요. 끝나면 새 리포트를 열어요.</p>
            <ol ref={progressRef} aria-live="polite"
              className="max-h-60 overflow-y-auto rounded-lg bg-slate-50 p-3 font-mono text-xs text-slate-600 dark:bg-white/5 dark:text-text-muted">
              {progress.length === 0 ? <li>시작하는 중…</li> : progress.map((line, index) => <li key={index} className="break-words">{line}</li>)}
            </ol>
            <button type="button" onClick={() => {
              setCancelError(null);
              void cancelReport().catch(err => setCancelError(reportMessage(errorCode(err)))).finally(() => void onRefresh());
            }}
              className="rounded-lg border border-slate-200 px-3 py-1.5 text-sm font-semibold text-slate-700 hover:border-red-300 hover:text-red-600 dark:border-white/10 dark:text-text-main">
              {REPORT_COPY.cancel}
            </button>
            {cancelError && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{cancelError}</p>}
          </div>
        ) : (
          <form className="mt-4 space-y-4" onSubmit={event => { event.preventDefault(); void submit(); }}>
            <div role="group" aria-label="기간" className="flex flex-wrap gap-1 rounded-lg border border-slate-200 bg-slate-50 p-1 dark:border-white/10 dark:bg-surface-lowest">
              {REPORT_PERIOD_OPTIONS.map(option => (
                <button key={option.value} type="button" aria-pressed={kind === option.value} onClick={() => setKind(option.value)}
                  className={`flex-1 rounded-md px-3 py-1.5 text-sm font-semibold ${kind === option.value
                    ? 'bg-white text-primary shadow-sm dark:bg-surface-high'
                    : 'text-slate-600 hover:text-slate-900 dark:text-text-muted dark:hover:text-text-main'}`}>
                  {option.label}
                </button>
              ))}
            </div>
            {kind === 'custom' && (
              <div className="grid grid-cols-2 gap-3">
                <label className="text-xs font-semibold text-slate-600 dark:text-text-muted">시작일
                  <input type="date" value={start} onChange={event => setStart(event.target.value)} className={`${inputClass} mt-1`} />
                </label>
                <label className="text-xs font-semibold text-slate-600 dark:text-text-muted">종료일
                  <input type="date" value={end} onChange={event => setEnd(event.target.value)} className={`${inputClass} mt-1`} />
                </label>
              </div>
            )}
            <div className="space-y-3">
              <p className="text-xs text-slate-500 dark:text-text-muted">강조할 항목을 한 줄에 하나씩 적어요. 같은 기간 종류의 최근 리포트에서 가져왔어요.</p>
              {HIGHLIGHT_FIELDS.map(({ field, label, hint }) => (
                <label key={field} className="block text-xs font-semibold text-slate-600 dark:text-text-muted">{label}
                  <textarea rows={2} value={textOf(field)} placeholder={hint} onChange={event => setText(field, event.target.value)}
                    className={`${inputClass} mt-1 resize-y`} />
                </label>
              ))}
            </div>
            <details open={bedrockPending || bedrockNeedsProfile(settings) || undefined}
              className="rounded-lg border border-slate-200 p-3 dark:border-white/10">
              <summary className="cursor-pointer text-sm font-semibold text-slate-700 dark:text-text-main">고급 설정</summary>
              <div className="mt-3 space-y-3 text-sm">
                <label className="flex items-center gap-2">
                  <input type="checkbox" disabled={!settings} checked={(settings?.useBedrock ?? false) || bedrockPending}
                    onChange={event => void updateSettings(event.target.checked, bedrockToggleProfile(settings))} />
                  <span>Amazon Bedrock으로 Claude 실행</span>
                </label>
                {settings && (settings.useBedrock || bedrockPending) && (
                  <label className="block text-xs font-semibold text-slate-600 dark:text-text-muted">AWS 프로필
                    <select value={settings.awsProfile ?? ''} onChange={event => void updateSettings(true, event.target.value || null)}
                      className={`${inputClass} mt-1`}>
                      <option value="" disabled>프로필 선택</option>
                      {profiles.map(profile => <option key={profile} value={profile}>{profile}</option>)}
                    </select>
                  </label>
                )}
                <p className="text-xs text-slate-500 dark:text-text-muted">끄면 Claude Code의 설정을 그대로 써요. TTOBAK은 모델 키를 저장하지 않아요.</p>
                {(bedrockPending || bedrockNeedsProfile(settings)) && (
                  <p role="alert" className="text-xs text-amber-700 dark:text-amber-300">{reportMessage('profile-required')}</p>
                )}
                {settings && settings.excludedSlackChannels !== undefined && (
                  <div className="space-y-2">
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-semibold text-slate-600 dark:text-text-muted">Slack 채널</span>
                      <button type="button" disabled={channelsBusy} onClick={() => void reloadChannels()}
                        className="rounded border border-slate-200 px-2 py-0.5 text-xs disabled:opacity-50 dark:border-white/10">
                        {channelsBusy ? '불러오는 중…' : '새로고침'}
                      </button>
                    </div>
                    <p className="text-xs text-slate-500 dark:text-text-muted">체크를 끈 채널은 리포트에서 빠져요. 목록에 없는 채널은 그대로 포함돼요.</p>
                    <ul className="max-h-48 space-y-1 overflow-auto">
                      {channelRows(settings.slackChannels, settings.excludedSlackChannels).map(row => (
                        <li key={row.name}>
                          <label className="flex items-center gap-2 text-xs">
                            <input type="checkbox" checked={row.included} disabled={channelsBusy} onChange={() => void toggleChannel(row.name)} />
                            <span className="font-mono">#{row.name}</span>
                            {row.count !== null && <span className="text-slate-500 dark:text-text-muted">{row.count}</span>}
                          </label>
                        </li>
                      ))}
                    </ul>
                    {settings.slackChannels?.partial && <p className="text-xs text-slate-500 dark:text-text-muted">메시지가 많아 일부 채널만 불러왔어요.</p>}
                  </div>
                )}
                {settingsError && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{settingsError}</p>}
              </div>
            </details>
            {(formError || message) && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{formError || message}</p>}
            <div className="flex justify-end gap-2">
              <button type="button" onClick={onClose}
                className="rounded-lg px-3 py-2 text-sm font-semibold text-slate-600 hover:text-slate-900 dark:text-text-muted dark:hover:text-text-main">
                {REPORT_COPY.cancel}
              </button>
              <button type="submit" disabled={bedrockPending || bedrockNeedsProfile(settings)}
                className="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white hover:opacity-90 disabled:opacity-50">
                {REPORT_COPY.makeReport}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
