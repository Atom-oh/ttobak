'use client';

import { useEffect, useState } from 'react';
import {
  errorCode, setupCodeText, setupStatusLabel, setupStepLabel, type SetupFinished, type SetupStep,
} from '@/lib/intake';
import { getSetupState, onSetupFinished, onSetupRunning, onSetupStep, startSetup } from '@/lib/tauri';

const buttonClass = 'rounded-lg border border-slate-200 px-3 py-1.5 text-sm font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50 dark:border-white/10 dark:text-text-main dark:hover:bg-white/5';
const primaryClass = 'rounded-lg bg-primary px-3 py-1.5 text-sm font-semibold text-white hover:opacity-90 disabled:opacity-50';
const mutedClass = 'text-xs text-slate-500 dark:text-text-muted';

const statusClass: Record<SetupStep['status'], string> = {
  ok: 'text-emerald-700 dark:text-emerald-300',
  current: 'text-slate-600 dark:text-text-muted',
  skipped: 'text-slate-500 dark:text-text-muted',
  failed: 'text-red-600 dark:text-red-400',
  'needs-approval': 'text-amber-700 dark:text-amber-300',
};

/** [한 번에 설정] of the intake section (onDone refreshes the intake state): PATH, adapter, skill packages, MCP
 * servers and `intake setup`, with a live result per step. Hidden in an app
 * build without the setup commands. */
export function OneClickSetup({ disabled, onDone }: { disabled: boolean; onDone: () => void }) {
  const [supported, setSupported] = useState(false);
  const [running, setRunning] = useState(false);
  const [steps, setSteps] = useState<SetupStep[]>([]);
  const [finished, setFinished] = useState<SetupFinished | null>(null);
  const [acceptPrivate, setAcceptPrivate] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    getSetupState()
      .then(state => {
        if (!active || !state) return;
        setSupported(true);
        setRunning(state.running);
        if (state.last) { setFinished(state.last); setSteps(state.last.steps); }
      })
      .catch(() => {});
    const offStep = onSetupStep(step => {
      setSteps(current => [...current.filter(item => item.id !== step.id), step]);
      // The intake section shows the run (cancel, progress log) once it holds the run slot.
      onDone();
    });
    const offFinished = onSetupFinished(result => {
      setRunning(false);
      setFinished(result);
      setSteps(result.steps);
      onDone();
    });
    const offRunning = onSetupRunning(onDone);
    return () => { active = false; offStep(); offFinished(); offRunning(); };
  }, [onDone]);

  if (!supported) return null;

  const start = async (approve: string[]) => {
    setError(null);
    setSteps([]);
    setFinished(null);
    setRunning(true);
    try {
      await startSetup(approve, acceptPrivate);
    } catch (err) {
      setRunning(false);
      setError(setupCodeText(errorCode(err)));
    }
  };

  const approvals = finished?.needsApproval ?? [];
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-slate-200 p-3 dark:border-white/10">
      <div className="flex flex-wrap items-center gap-3">
        <button type="button" className={primaryClass} disabled={disabled || running} onClick={() => void start([])}>
          한 번에 설정
        </button>
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={acceptPrivate} disabled={running} onChange={event => setAcceptPrivate(event.target.checked)} />
          <span>비공개 일정도 포함해 캘린더를 연결합니다 (이미 동의했다면 그대로 써요).</span>
        </label>
      </div>
      <p className={mutedClass}>PATH, 수집 도구, 스킬 패키지, MCP 서버, 연결 설정을 차례로 확인해요. 처음 보는 MCP 서버는 설치 전에 물어봐요.</p>
      {steps.length > 0 && (
        <ol className="flex flex-col gap-1">
          {steps.map(step => (
            <li key={step.id}>
              <span className="font-semibold">{setupStepLabel(step.id)}</span>
              {' · '}<span className={statusClass[step.status]}>{setupStatusLabel(step.status)}</span>
              {step.code && <span className={`block ${mutedClass}`}>{setupCodeText(step.code)}</span>}
              {step.id === 'skills' && step.status === 'ok' && step.items && step.items.length > 0 && (
                <span className={`block ${mutedClass}`}>{step.items.join(', ')}</span>
              )}
            </li>
          ))}
          {running && <li className={mutedClass}>진행 중…</li>}
        </ol>
      )}
      {approvals.length > 0 && !running && (
        <div role="alert" className="flex flex-col gap-2 rounded-lg border border-amber-300 bg-amber-50 p-3 text-amber-900 dark:border-amber-400/30 dark:bg-amber-400/10 dark:text-amber-200">
          <p>이 Mac에 처음 설치하는 항목이에요. 설치할까요?</p>
          <ul className="list-disc pl-5">
            {approvals.map(item => <li key={item.key} className="font-mono text-xs">{item.label}</li>)}
          </ul>
          <div className="flex gap-2">
            <button type="button" className={primaryClass} onClick={() => void start(approvals.map(item => item.key))}>설치</button>
            <button type="button" className={buttonClass} onClick={() => setFinished(current => current ? { ...current, needsApproval: [] } : current)}>취소</button>
          </div>
        </div>
      )}
      {finished && finished.warnings.length > 0 && (
        <ul className={`flex flex-col gap-0.5 ${mutedClass}`}>{finished.warnings.map(line => <li key={line}>{line}</li>)}</ul>
      )}
      {error && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{error}</p>}
    </div>
  );
}
