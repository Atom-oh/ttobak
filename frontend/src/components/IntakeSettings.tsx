'use client';

import { useCallback, useEffect, useState } from 'react';
import { useIntake } from '@/hooks/useIntake';
import {
  errorCode, finishedMessage, loadErrorText, INTAKE_CONNECTORS, INTAKE_COPY, intakeCodeLabel, needsTtobakLogin, SCHEDULE_OPTIONS,
  scheduleFromForm, scheduleLabel, setupConnectorRows, showLaunchAgentWarning, sourceRows, type IntakeConnector, type IntakeFinished,
  type IntakeScheduleKind,
} from '@/lib/intake';
import {
  cancelIntake, checkIntakeAdapter, isTauri, runIntakeLogin, runIntakeNow, runIntakeSetup, setIntakeNodePath,
  setIntakeSchedule, turnOffIntakeLaunchAgent, updateIntakeAdapter,
} from '@/lib/tauri';

const buttonClass = 'rounded-lg border border-slate-200 px-3 py-1.5 text-sm font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50 dark:border-white/10 dark:text-text-main dark:hover:bg-white/5';
const primaryClass = 'rounded-lg bg-primary px-3 py-1.5 text-sm font-semibold text-white hover:opacity-90 disabled:opacity-50';
const inputClass = 'rounded-lg border border-slate-200 bg-white px-2 py-1.5 text-sm text-slate-900 dark:border-white/10 dark:bg-surface-lowest dark:text-text-main';
const mutedClass = 'text-xs text-slate-500 dark:text-text-muted';

function formatTime(iso: string | null): string {
  if (!iso) return '-';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString('ko-KR', { dateStyle: 'short', timeStyle: 'short' });
}

/** Mac app only: local intake setup, schedule and runs (ADR-065). Hidden in a
 * browser or an app build without intake. */
export function IntakeSettings() {
  const [message, setMessage] = useState<string | null>(null);
  const onFinished = useCallback((finished: IntakeFinished) => setMessage(finishedMessage(finished) || null), []);
  const { state, setState, progress, loadError, refresh, markStarted } = useIntake(onFinished);
  const [busy, setBusy] = useState(false);
  const [nodeInput, setNodeInput] = useState('');
  const [form, setForm] = useState<{ kind: IntakeScheduleKind; first: string; second: string } | null>(null);
  const [setupOpen, setSetupOpen] = useState(false);
  const [acceptPrivate, setAcceptPrivate] = useState(false);
  const [skip, setSkip] = useState<IntakeConnector[]>([]);

  // Best effort, once the state is loaded: offline leaves updateAvailable null.
  const loaded = state !== null;
  useEffect(() => {
    if (!loaded) return;
    let active = true;
    checkIntakeAdapter()
      .then(adapter => { if (active) setState(current => current ? { ...current, adapter } : current); })
      .catch(() => {});
    return () => { active = false; };
  }, [loaded, setState]);

  if (!isTauri()) return null;
  if (!state) {
    return loadError ? (
      <section className="mb-6"><p role="alert" className="text-sm text-red-600 dark:text-red-400">{loadErrorText(loadError)}</p></section>
    ) : null;
  }

  const running = state.running !== null;
  const schedule = form ?? {
    kind: state.schedule.kind,
    first: state.schedule.times[0] ?? '09:00',
    second: state.schedule.times[1] ?? '18:00',
  };
  const scheduleValue = scheduleFromForm(schedule.kind, schedule.first, schedule.second);
  const rows = sourceRows(state.status, state.selection);
  const setupRows = running ? [] : setupConnectorRows(state.lastResult);

  const act = async (work: () => Promise<unknown>, after?: (value: unknown) => void) => {
    setBusy(true);
    setMessage(null);
    try {
      const value = await work();
      after?.(value);
    } catch (err) {
      const code = errorCode(err);
      setMessage(code === 'invalid-node-path' ? 'node 경로가 올바르지 않아요.' : intakeCodeLabel(code));
    } finally {
      setBusy(false);
      await refresh();
    }
  };

  const start = (action: 'run' | 'setup' | 'login', work: () => Promise<void>) => {
    markStarted(action);
    void act(work);
  };

  return (
    <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
      <h3 className="section-header mb-4">{INTAKE_COPY.sectionTitle}</h3>
      <div className="flex flex-col gap-5 text-sm text-slate-700 dark:text-text-secondary">
        <div className="flex flex-wrap items-center gap-3">
          <span>
            {state.adapter.present ? '수집 도구 설치됨' : intakeCodeLabel('ADAPTER_MISSING')}
            {state.adapter.updateAvailable && state.adapter.present && ' · 새 버전이 있어요'}
          </span>
          <button type="button" className={buttonClass} disabled={busy || running}
            onClick={() => void act(updateIntakeAdapter, value => setState(current => current ? { ...current, adapter: value as typeof current.adapter } : current))}>
            {INTAKE_COPY.update}
          </button>
        </div>

        {state.nodeMissing ? (
          <div className="flex flex-col gap-2">
            <p>{intakeCodeLabel('NODE_MISSING')}</p>
            <div className="flex flex-wrap gap-2">
              <input aria-label="node 경로" className={`${inputClass} min-w-64 flex-1 font-mono`} placeholder="/opt/homebrew/bin/node"
                value={nodeInput} onChange={event => setNodeInput(event.target.value)} />
              <button type="button" className={buttonClass} disabled={busy || !nodeInput.trim()}
                onClick={() => void act(() => setIntakeNodePath(nodeInput.trim()), () => setNodeInput(''))}>저장</button>
            </div>
          </div>
        ) : state.nodePath && <p className={mutedClass}>Node.js: <code className="font-mono">{state.nodePath}</code></p>}

        {state.selection.length > 0 && (
          <table className="w-full text-left text-sm">
            <thead className={mutedClass}>
              <tr><th className="py-1 font-medium">소스</th><th className="py-1 font-medium">서버</th><th className="py-1 font-medium">호스트</th><th className="py-1 font-medium">상태</th></tr>
            </thead>
            <tbody>
              {state.selection.map(entry => (
                <tr key={entry.connector} className="border-t border-slate-100 dark:border-white/10">
                  <td className="py-1.5">{INTAKE_CONNECTORS.find(item => item.connector === entry.connector)?.label ?? entry.connector}</td>
                  <td className="py-1.5 font-mono text-xs">{entry.server}</td>
                  <td className="py-1.5 font-mono text-xs">{entry.host}</td>
                  <td className="py-1.5">{entry.disabled ? INTAKE_COPY.connectorSkipped : INTAKE_COPY.connectorSelected}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        <div className="flex flex-col gap-2">
          <p className={mutedClass}>자동 수집: {scheduleLabel(state.schedule)}</p>
          <div className="flex flex-wrap items-center gap-2">
            <select aria-label="자동 수집 주기" className={inputClass} value={schedule.kind}
              onChange={event => setForm({ ...schedule, kind: event.target.value as IntakeScheduleKind })}>
              {SCHEDULE_OPTIONS.map(option => <option key={option.kind} value={option.kind}>{option.label}</option>)}
            </select>
            {(schedule.kind === 'daily' || schedule.kind === 'twiceDaily') && (
              <input type="time" aria-label="첫 수집 시각" className={inputClass} value={schedule.first}
                onChange={event => setForm({ ...schedule, first: event.target.value })} />
            )}
            {schedule.kind === 'twiceDaily' && (
              <input type="time" aria-label="두 번째 수집 시각" className={inputClass} value={schedule.second}
                onChange={event => setForm({ ...schedule, second: event.target.value })} />
            )}
            <button type="button" className={buttonClass} disabled={busy || !form || !scheduleValue}
              onClick={() => scheduleValue && void act(() => setIntakeSchedule(scheduleValue), () => setForm(null))}>저장</button>
          </div>
        </div>

        {showLaunchAgentWarning(state) && (
          <div role="alert" className="flex flex-wrap items-center gap-3 rounded-lg border border-amber-300 bg-amber-50 p-3 text-amber-900 dark:border-amber-400/30 dark:bg-amber-400/10 dark:text-amber-200">
            <span>터미널에서 설치한 자동 수집도 켜져 있어요. 같은 수집이 두 번 실행되지 않게 하나를 끄세요.</span>
            <button type="button" className={buttonClass} disabled={busy || running}
              onClick={() => void act(turnOffIntakeLaunchAgent)}>터미널 자동 수집 끄기</button>
          </div>
        )}

        {rows.length > 0 && (
          <table className="w-full text-left text-sm">
            <thead className={mutedClass}>
              <tr>
                <th className="py-1 font-medium">소스</th><th className="py-1 font-medium">마지막 실행</th>
                <th className="py-1 font-medium">마지막 성공</th><th className="py-1 font-medium">결과</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(row => (
                <tr key={row.source} className="border-t border-slate-100 dark:border-white/10">
                  <td className="py-1.5">{row.label}</td>
                  <td className="py-1.5">{formatTime(row.lastRunAt)}</td>
                  <td className="py-1.5">{formatTime(row.lastSuccessAt)}</td>
                  <td className="py-1.5">
                    {row.result === null ? '-' : row.result === 'ok' ? '성공' : intakeCodeLabel(row.result)}
                    {row.counts && <span className={`block ${mutedClass}`}>{row.counts}</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        <div className="flex flex-wrap gap-2">
          <button type="button" className={buttonClass} disabled={busy || running} aria-expanded={setupOpen}
            onClick={() => setSetupOpen(open => !open)}>{INTAKE_COPY.connectSetup}</button>
          <button type="button" className={primaryClass} disabled={busy || running}
            onClick={() => start('run', runIntakeNow)}>{INTAKE_COPY.collectNow}</button>
          {needsTtobakLogin(state.lastResult, state.status) && (
            <button type="button" className={buttonClass} disabled={busy || running}
              onClick={() => start('login', runIntakeLogin)}>{INTAKE_COPY.ttobakLogin}</button>
          )}
          {running && state.running !== 'scheduleOff' && (
            <button type="button" className={buttonClass} onClick={() => void act(cancelIntake)}>{INTAKE_COPY.cancel}</button>
          )}
        </div>

        {setupOpen && !running && (
          <div className="flex flex-col gap-3 rounded-lg border border-slate-200 p-3 dark:border-white/10">
            <label className="flex items-start gap-2">
              <input type="checkbox" className="mt-0.5" checked={acceptPrivate} onChange={event => setAcceptPrivate(event.target.checked)} />
              <span>비공개 일정도 포함해 캘린더를 연결합니다.</span>
            </label>
            <div className="flex flex-wrap gap-2">
              {INTAKE_CONNECTORS.map(({ connector, label }) => {
                const skipped = skip.includes(connector);
                return (
                  <button key={connector} type="button" aria-pressed={!skipped} className={buttonClass}
                    onClick={() => setSkip(list => skipped ? list.filter(item => item !== connector) : [...list, connector])}>
                    {label} · {skipped ? INTAKE_COPY.connectorSkipped : INTAKE_COPY.connectorSelected}
                  </button>
                );
              })}
            </div>
            <div className="flex gap-2">
              <button type="button" className={primaryClass} disabled={busy}
                onClick={() => { setSetupOpen(false); start('setup', () => runIntakeSetup(acceptPrivate, skip)); }}>
                {INTAKE_COPY.connectSetup}
              </button>
              <button type="button" className={buttonClass} onClick={() => setSetupOpen(false)}>{INTAKE_COPY.cancel}</button>
            </div>
          </div>
        )}

        {setupRows.length > 0 && (
          <div className="flex flex-col gap-1">
            <p className={mutedClass}>연결 설정 결과</p>
            <ul className="flex flex-col gap-1">
              {setupRows.map(row => (
                <li key={row.connector}>
                  <span className="font-semibold">{row.label}</span>
                  {' · '}{row.status === 'selected' ? INTAKE_COPY.connectorSelected : INTAKE_COPY.connectorSkipped}
                  {row.status === 'selected' && row.host && <span className={mutedClass}> ({row.host})</span>}
                  {row.code && <span className={`block ${mutedClass}`}>{intakeCodeLabel(row.code)}</span>}
                </li>
              ))}
            </ul>
          </div>
        )}

        {message && <p role="status" className="text-sm text-red-600 dark:text-red-400">{message}</p>}

        {running && state.running !== 'scheduleOff' && (
          <details open className="rounded-lg border border-slate-200 p-3 dark:border-white/10">
            <summary className="cursor-pointer text-sm font-semibold">수집 중… 진행 기록</summary>
            <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap text-xs text-slate-600 dark:text-text-muted">
              {progress.join('\n') || '…'}
            </pre>
          </details>
        )}
      </div>
    </section>
  );
}
