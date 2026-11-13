'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { CaptionOverlayPanel } from '@/components/record/CaptionOverlayPanel';
import { NativeMicSelector, readNativeInputPreference } from '@/components/record/NativeMicSelector';
import { TranslationView } from '@/components/TranslationView';
import { useCaptionOverlay, type CaptionOverlayState, type CaptionSegmentHandler } from '@/hooks/useCaptionOverlay';
import { useInterpretSession } from '@/hooks/useInterpretSession';
import { INTERPRET_MESSAGES, formatElapsed } from '@/lib/interpretSession';
import { BILINGUAL_TARGET } from '@/lib/liveTranslation';
import { onNativeAudioLevel, useIsMacApp } from '@/lib/tauri';

/** Input level while listening. Its own component so the ~30 Hz level events do not re-render the captions. */
function LevelMeter({ active }: { active: boolean }) {
  const [level, setLevel] = useState(0);
  useEffect(() => {
    if (!active) return;
    const unsubscribe = onNativeAudioLevel(setLevel);
    return () => {
      unsubscribe();
      setLevel(0);
    };
  }, [active]);
  return (
    <div
      role="meter"
      aria-label="입력 음량"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(level * 100)}
      className="h-2 w-24 overflow-hidden rounded-full bg-slate-200 dark:bg-white/10"
    >
      <div className="h-full rounded-full bg-primary transition-[width] duration-75" style={{ width: `${Math.round(level * 100)}%` }} />
    </div>
  );
}

function Interpreter() {
  const [targetLang, setTargetLang] = useState(BILINGUAL_TARGET);
  const [interimTranslation, setInterimTranslation] = useState(false);
  const [inputUid, setInputUid] = useState(readNativeInputPreference);
  // The caption windows need the session's phase and the session needs their
  // segment handler; the ref breaks that cycle (as on the record page).
  const captionLinkRef = useRef<Pick<CaptionOverlayState, 'extraLanguage' | 'onSegment'> | null>(null);
  const getExtraLanguage = useCallback(() => captionLinkRef.current?.extraLanguage, []);
  const onSegment = useCallback<CaptionSegmentHandler>((id, text, isFinal, languageCode) => {
    captionLinkRef.current?.onSegment(id, text, isFinal, languageCode);
  }, []);
  const session = useInterpretSession({ targetLang, interimTranslation, onSegment, getExtraLanguage });
  const idle = session.phase === 'idle';
  const captions = useCaptionOverlay({ isRecording: !idle });
  const { extraLanguage: captionExtraLanguage, onSegment: captionOnSegment } = captions;
  useEffect(() => {
    captionLinkRef.current = { extraLanguage: captionExtraLanguage, onSegment: captionOnSegment };
  }, [captionExtraLanguage, captionOnSegment]);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 p-4 lg:p-6">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <h2 className="text-2xl font-bold tracking-tight text-slate-900 dark:font-headline dark:text-text-main">실시간 통역</h2>
        <label
          className="ml-auto flex items-center gap-1.5 text-sm text-slate-600 dark:text-text-secondary"
          title="말하는 도중의 문장도 번역합니다. 번역 요청이 크게 늘어납니다."
        >
          <input
            type="checkbox"
            checked={interimTranslation}
            onChange={(event) => setInterimTranslation(event.target.checked)}
            className="rounded border-slate-300 text-primary focus:ring-primary"
          />
          초안 번역 (비용↑)
        </label>
      </div>

      <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
        <NativeMicSelector value={inputUid} onChange={setInputUid} disabled={!idle} mode="listen" />
        <div className="ml-auto flex items-center gap-4">
          {!idle && <LevelMeter active />}
          {!idle && (
            <time role="timer" aria-label="통역 시간" className="font-mono text-sm tabular-nums text-slate-600 dark:text-text-secondary">
              {formatElapsed(session.elapsedMs)}
            </time>
          )}
          {idle ? (
            <button
              type="button"
              onClick={() => void session.start(inputUid || undefined)}
              className="flex h-10 items-center gap-2 rounded-lg bg-primary px-4 text-sm font-semibold text-white transition-colors hover:bg-primary-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
            >
              <span className="material-symbols-outlined text-lg" aria-hidden="true">translate</span>
              통역 시작
            </button>
          ) : (
            <button
              type="button"
              onClick={() => void session.stop()}
              disabled={session.phase === 'stopping'}
              className="flex h-10 items-center gap-2 rounded-lg bg-red-600 px-4 text-sm font-semibold text-white transition-colors hover:bg-red-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-red-600 disabled:opacity-60"
            >
              <span className="material-symbols-outlined text-lg" aria-hidden="true">stop</span>
              정지
            </button>
          )}
        </div>
      </div>

      {session.error && (
        <p role="alert" className="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300">
          {session.error}
        </p>
      )}
      {session.notice && (
        <p role="status" className="rounded-lg bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
          {session.notice}
        </p>
      )}
      {session.warnings.length > 0 && (
        <ul role="status" className="rounded-lg bg-slate-100 px-4 py-3 text-sm text-slate-700 dark:bg-white/5 dark:text-text-secondary">
          {session.warnings.map((warning) => <li key={warning}>{warning}</li>)}
        </ul>
      )}
      {!idle && session.input && (
        <p className="text-xs text-slate-500 dark:text-text-muted">마이크: {session.input.name} · Mac에서 나는 소리도 함께 듣습니다.</p>
      )}
      {idle && (
        <p className="text-sm text-slate-500 dark:text-text-muted">
          마이크와 Mac에서 나는 소리를 함께 듣고 통역합니다. {INTERPRET_MESSAGES.notSaved}
        </p>
      )}

      {/* Floating caption windows for a projector or a shared screen. */}
      <CaptionOverlayPanel {...captions} isRecording={!idle} onExpandedChange={captions.setArranging} />

      <div className="min-h-0 flex-1">
        <TranslationView
          translations={session.translations}
          targetLang={targetLang}
          onTargetLangChange={setTargetLang}
          isActive={!idle}
          fill
          size="large"
        />
      </div>
    </div>
  );
}

// Mac app only: live interpretation with no meeting and nothing saved.
export default function InterpretPage() {
  const { isLoading, isAuthenticated } = useAuth();
  const router = useRouter();
  const macApp = useIsMacApp();
  useEffect(() => {
    if (!isLoading && !isAuthenticated) router.replace('/');
  }, [isLoading, isAuthenticated, router]);

  if (isLoading || macApp === null) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated) return null;

  return (
    <AppLayout activePath="/interpret">
      {macApp ? <Interpreter /> : (
        <div className="workspace-page">
          <h2 className="text-2xl font-bold tracking-tight text-slate-900 dark:font-headline dark:text-text-main">실시간 통역</h2>
          <p className="mt-3 text-slate-600 dark:text-text-muted">{INTERPRET_MESSAGES.macOnly}</p>
          <Link href="/record" className="mt-4 inline-flex items-center gap-1.5 text-sm font-medium text-primary focus-visible:outline-2 focus-visible:outline-primary">
            녹음 화면에서 실시간 통역 사용하기
          </Link>
        </div>
      )}
    </AppLayout>
  );
}
