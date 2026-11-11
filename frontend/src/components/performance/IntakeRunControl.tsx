'use client';

import { useCallback, useState } from 'react';
import Link from 'next/link';
import { useIntake } from '@/hooks/useIntake';
import { errorCode, finishedMessage, INTAKE_COPY, loadErrorText, intakeCodeLabel, type IntakeFinished } from '@/lib/intake';
import { cancelIntake, isTauri, runIntakeNow } from '@/lib/tauri';

/** [지금 수집] next to the freshness lines. A browser shows only a hint; an
 * app build without intake shows nothing. `onCollected` runs after a run
 * finishes successfully or with PARTIAL_FAILURE. */
export function IntakeRunControl({ onCollected }: { onCollected: () => void }) {
  const [message, setMessage] = useState<string | null>(null);
  const onFinished = useCallback((finished: IntakeFinished) => {
    if (finished.action !== 'run') return;
    setMessage(finishedMessage(finished) || null);
    if (!finished.code || finished.code === 'PARTIAL_FAILURE') onCollected();
  }, [onCollected]);
  const { state, loadError, refresh, markStarted } = useIntake(onFinished);

  if (!isTauri()) return <p className="text-xs text-slate-500 dark:text-text-muted">{INTAKE_COPY.browserHint}</p>;
  if (!state) {
    return loadError ? <p role="alert" className="text-xs text-red-600 dark:text-red-400">{loadErrorText(loadError)}</p> : null;
  }

  const running = state.running !== null;
  const collect = async () => {
    setMessage(null);
    markStarted('run');
    try {
      await runIntakeNow();
    } catch (err) {
      setMessage(intakeCodeLabel(errorCode(err)));
      await refresh();
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <button type="button" disabled={running} onClick={() => void collect()}
        className="rounded-lg bg-primary px-3 py-1.5 text-sm font-semibold text-white hover:opacity-90 disabled:opacity-50">
        {running ? '수집 중…' : INTAKE_COPY.collectNow}
      </button>
      {state.running === 'run' && (
        <button type="button" onClick={() => void cancelIntake().finally(() => void refresh())}
          className="font-semibold text-primary hover:underline">{INTAKE_COPY.cancel}</button>
      )}
      {message && <span role="status" className="text-red-600 dark:text-red-400">{message}</span>}
      {(message || !state.adapter.present || state.nodeMissing) && (
        <Link href="/settings" className="font-semibold text-primary hover:underline">{INTAKE_COPY.sectionTitle} 설정</Link>
      )}
    </div>
  );
}
