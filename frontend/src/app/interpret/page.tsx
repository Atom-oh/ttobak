'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { CaptionOverlayPanel } from '@/components/record/CaptionOverlayPanel';
import { NativeMicSelector, readNativeInputPreference } from '@/components/record/NativeMicSelector';
import { useCaptionOverlay, type CaptionOverlayState, type CaptionSegmentHandler } from '@/hooks/useCaptionOverlay';
import { useInterpretSession } from '@/hooks/useInterpretSession';
import { captionLanguages, type CaptionLang } from '@/lib/captionOverlay';
import { upsertCaptionLine, visibleCaptionLines } from '@/lib/interpretCaptions';
import { INTERPRET_MESSAGES, formatElapsed } from '@/lib/interpretSession';
import { BILINGUAL_TARGET, type TranslationEntry } from '@/lib/liveTranslation';
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

const CAPTION_LANGUAGE_LABELS: Record<CaptionLang, string> = { ko: '한국어', en: 'English', ja: '日本語', zh: '中文' };

type CaptionLines = Partial<Record<CaptionLang, TranslationEntry[]>>;

/** One caption language's lines in large text; follows the newest line. */
function CaptionColumn({ lang, lines, open }: { lang: CaptionLang; lines: TranslationEntry[]; open: boolean }) {
  const endRef = useRef<HTMLDivElement>(null);
  const last = lines[lines.length - 1];
  useEffect(() => {
    endRef.current?.scrollIntoView({ block: 'end' });
  }, [lines.length, last?.translated]);
  return (
    <section aria-label={`${CAPTION_LANGUAGE_LABELS[lang]} 자막`} className="flex min-h-0 min-w-0 flex-col rounded-2xl border border-slate-200 bg-white dark:border-white/10 dark:bg-white/5">
      <h3 className="shrink-0 border-b border-slate-200 px-4 py-2 text-sm font-semibold text-slate-600 dark:border-white/10 dark:text-text-secondary">
        {CAPTION_LANGUAGE_LABELS[lang]}
      </h3>
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto px-4 py-3">
        {lines.map((line) => (
          <p key={line.id} className={`text-2xl font-semibold leading-snug break-words ${line.isFinal ? 'text-slate-900 dark:text-text-main' : 'text-slate-500 dark:text-text-muted'}`}>
            {line.translated}
          </p>
        ))}
        {open && lines.length === 0 && (
          <p className="text-sm text-slate-500 dark:text-text-muted">통역을 시작하면 여기에 자막이 쌓여요.</p>
        )}
        {!open && (
          <p className="text-sm text-slate-500 dark:text-text-muted">
            위 &quot;자막 창&quot;에서 이 언어의 창을 열면 여기에도 같은 자막이 보여요.
          </p>
        )}
        <div ref={endRef} />
      </div>
    </section>
  );
}

function Interpreter() {
  const [inputUid, setInputUid] = useState(readNativeInputPreference);
  // In-app copy of the caption windows, in memory only.
  const [captionLines, setCaptionLines] = useState<CaptionLines>({});
  const onCaptionEntry = useCallback((lang: CaptionLang, entry: TranslationEntry) => {
    setCaptionLines((previous) => {
      const lines = previous[lang] ?? [];
      const next = upsertCaptionLine(lines, entry);
      return next === lines ? previous : { ...previous, [lang]: next };
    });
  }, []);
  // The caption windows need the session's phase and the session needs their
  // segment handler; the ref breaks that cycle (as on the record page).
  const captionLinkRef = useRef<Pick<CaptionOverlayState, 'extraLanguage' | 'onSegment'> | null>(null);
  const getExtraLanguage = useCallback(() => captionLinkRef.current?.extraLanguage, []);
  const onSegment = useCallback<CaptionSegmentHandler>((id, text, isFinal, languageCode) => {
    captionLinkRef.current?.onSegment(id, text, isFinal, languageCode);
  }, []);
  const session = useInterpretSession({ targetLang: BILINGUAL_TARGET, interimTranslation: false, onSegment, getExtraLanguage });
  const idle = session.phase === 'idle';
  const captions = useCaptionOverlay({ isRecording: !idle, onEntry: onCaptionEntry });
  const start = () => {
    // A new run restarts the caption engines and their line order.
    setCaptionLines({});
    void session.start(inputUid || undefined);
  };
  const languages = captionLanguages(captions.view?.settings.extraLanguage ?? null);
  const { extraLanguage: captionExtraLanguage, onSegment: captionOnSegment } = captions;
  useEffect(() => {
    captionLinkRef.current = { extraLanguage: captionExtraLanguage, onSegment: captionOnSegment };
  }, [captionExtraLanguage, captionOnSegment]);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 p-4 lg:p-6">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <h2 className="text-2xl font-bold tracking-tight text-slate-900 dark:font-headline dark:text-text-main">실시간 통역</h2>
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
              onClick={start}
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

      {/* The same languages as the caption windows, side by side in large text. */}
      <div className="grid min-h-0 flex-1 auto-rows-fr gap-4 md:grid-flow-col md:auto-cols-fr">
        {languages.map((lang) => (
          <CaptionColumn key={lang} lang={lang} lines={visibleCaptionLines(captionLines[lang] ?? [])} open={captions.openLangs.includes(lang)} />
        ))}
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
