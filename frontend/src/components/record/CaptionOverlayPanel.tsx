'use client';

import { useEffect, useState } from 'react';
import {
  captionErrorMessage,
  captionLanguages,
  type CaptionLang,
  type CaptionStyle,
} from '@/lib/captionOverlay';
import type { CaptionOverlayState } from '@/hooks/useCaptionOverlay';
import { readCaptionPanelExpanded, writeCaptionPanelExpanded } from '@/lib/recordLayoutPrefs';

// Mac app caption windows: AWS profile, third recognition language, one
// toggle per window and a style editor for each open window. Collapsed by
// default: only the per-window toggles (used during a talk), the error line
// and the profile note stay visible; the rest sits behind a settings button.

const LANGUAGE_LABELS: Record<CaptionLang, string> = {
  ko: '한국어',
  en: 'English',
  ja: '日本語',
  zh: '中文',
};

const CONTROL_CLASS =
  'text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-slate-600 dark:text-gray-400 focus:outline-none focus:ring-2 focus:ring-primary/30 disabled:opacity-50 disabled:cursor-not-allowed';
const LABEL_CLASS = 'flex items-center gap-1.5 text-sm text-slate-600 dark:text-gray-400';
const NOTE_CLASS = 'text-xs text-slate-500 dark:text-text-muted';

const DETAILS_ID = 'caption-panel-details';

export type CaptionOverlayPanelProps = CaptionOverlayState & {
  isRecording: boolean;
  // Initial expanded state. When omitted the saved per-browser choice is read
  // after mount (collapsed until then, so static rendering stays stable).
  defaultExpanded?: boolean;
};

export function CaptionOverlayPanel({
  supported,
  view,
  error,
  openLangs,
  setProfile,
  setRegion,
  setExtraLanguage,
  toggleWindow,
  updateStyle,
  retry,
  isRecording,
  defaultExpanded,
}: CaptionOverlayPanelProps) {
  const [expanded, setExpanded] = useState(defaultExpanded ?? false);
  useEffect(() => {
    if (defaultExpanded !== undefined) return;
    const saved = readCaptionPanelExpanded();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (saved !== null) setExpanded(saved);
  }, [defaultExpanded]);
  const toggleExpanded = () => {
    const next = !expanded;
    setExpanded(next);
    writeCaptionPanelExpanded(next);
  };

  if (!supported) return null;

  const errorLine = error && (
    <div role="alert" className="flex flex-wrap items-center gap-2 text-xs text-amber-700 dark:text-amber-400">
      <span>{captionErrorMessage(error)}</span>
      <button
        type="button"
        onClick={retry}
        className="px-3 py-1 text-xs font-medium rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700"
      >
        다시 시도
      </button>
    </div>
  );

  if (!view) {
    return <section aria-label="자막 창" className="w-full flex flex-col items-center gap-2">{errorLine}</section>;
  }

  const { settings, profiles, fonts } = view;
  const hasProfile = !!settings.profile;

  return (
    <section aria-label="자막 창" className="w-full flex flex-col items-center gap-3">
      <div className="flex flex-wrap items-center justify-center gap-3">
        <span className="text-sm font-medium text-slate-600 dark:text-gray-400">자막 창</span>
        {captionLanguages(settings.extraLanguage).map((lang) => {
          const open = openLangs.includes(lang);
          return (
            <div key={lang} className="flex items-center gap-1.5 text-sm text-slate-600 dark:text-gray-400">
              <span>{LANGUAGE_LABELS[lang]}</span>
              <button
                id={`caption-${lang}`}
                type="button"
                disabled={!hasProfile}
                aria-pressed={open}
                aria-label={`${LANGUAGE_LABELS[lang]} 자막 창 ${open ? '닫기' : '열기'}`}
                onClick={() => toggleWindow(lang)}
                className={`px-3 py-1 text-xs font-medium rounded-lg transition-colors ${
                  open
                    ? 'bg-primary text-white'
                    : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 border border-slate-200 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-700'
                } disabled:opacity-50 disabled:cursor-not-allowed`}
              >
                {open ? '닫기' : '열기'}
              </button>
            </div>
          );
        })}
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={DETAILS_ID}
          onClick={toggleExpanded}
          className={`flex items-center gap-0.5 pl-3 pr-2 py-1 text-xs font-medium rounded-lg border transition-colors ${
            expanded
              ? 'bg-primary/10 text-primary border-primary/30'
              : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 border-slate-200 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-700'
          }`}
        >
          설정
          <span aria-hidden="true" className={`material-symbols-outlined text-base transition-transform ${expanded ? 'rotate-180' : ''}`}>expand_more</span>
        </button>
      </div>
      {!hasProfile && <p className={NOTE_CLASS}>{captionErrorMessage('profile-missing')}</p>}
      {errorLine}

      {expanded && (
        <div id={DETAILS_ID} className="w-full flex flex-col items-center gap-3">
          <div className="flex flex-wrap items-center justify-center gap-3">
            <label htmlFor="caption-profile" className={LABEL_CLASS}>AWS 프로필</label>
            <select
              id="caption-profile"
              value={settings.profile ?? ''}
              onChange={(e) => setProfile(e.target.value || null)}
              className={CONTROL_CLASS}
            >
              <option value="">선택 안 함</option>
              {profiles.map((profile) => (
                <option key={profile} value={profile}>{profile}</option>
              ))}
            </select>
            <label htmlFor="caption-region" className={LABEL_CLASS}>리전</label>
            <input
              key={settings.region}
              id="caption-region"
              type="text"
              defaultValue={settings.region}
              onBlur={(e) => setRegion(e.target.value)}
              spellCheck={false}
              className={`${CONTROL_CLASS} w-32`}
            />
            <label htmlFor="caption-extra-language" className={LABEL_CLASS}>세 번째 언어</label>
            <select
              id="caption-extra-language"
              value={settings.extraLanguage ?? ''}
              disabled={isRecording}
              onChange={(e) => setExtraLanguage(e.target.value === 'ja' || e.target.value === 'zh' ? e.target.value : null)}
              className={CONTROL_CLASS}
            >
              <option value="">없음</option>
              <option value="ja">{LANGUAGE_LABELS.ja}</option>
              <option value="zh">{LANGUAGE_LABELS.zh}</option>
            </select>
          </div>
          <p className={NOTE_CLASS}>인식 언어는 녹음을 시작하기 전에만 바꿀 수 있어요.</p>

          {openLangs.map((lang) => {
            const style = settings.windows[lang]?.style;
            return style ? (
              <StyleEditor
                key={lang}
                lang={lang}
                style={style}
                fonts={fonts}
                onChange={(next) => updateStyle(lang, next)}
              />
            ) : null;
          })}

          <p className={NOTE_CLASS}>Zoom에서는 창이 아니라 전체 화면을 공유해야 자막이 함께 보여요.</p>
        </div>
      )}
    </section>
  );
}

function StyleEditor({
  lang, style, fonts, onChange,
}: {
  lang: CaptionLang;
  style: CaptionStyle;
  fonts: string[];
  onChange: (style: CaptionStyle) => void;
}) {
  const id = (field: string) => `caption-${lang}-${field}`;
  const set = (patch: Partial<CaptionStyle>) => onChange({ ...style, ...patch });
  return (
    <fieldset className="flex flex-wrap items-center justify-center gap-3 border-t border-slate-100 dark:border-white/10 pt-3">
      <legend className="sr-only">{LANGUAGE_LABELS[lang]} 자막 스타일</legend>
      <span className="text-xs font-medium text-slate-600 dark:text-gray-400">{LANGUAGE_LABELS[lang]}</span>
      <label htmlFor={id('font')} className={LABEL_CLASS}>글꼴</label>
      <select
        id={id('font')}
        value={style.fontFamily}
        onChange={(e) => set({ fontFamily: e.target.value })}
        className={CONTROL_CLASS}
      >
        {fonts.map((font) => (
          <option key={font} value={font}>{font}</option>
        ))}
      </select>
      <label htmlFor={id('size')} className={LABEL_CLASS}>크기 {style.fontSize}</label>
      <input
        id={id('size')}
        type="range"
        min={16}
        max={160}
        step={1}
        value={style.fontSize}
        onChange={(e) => set({ fontSize: Number(e.target.value) })}
        className="accent-primary"
      />
      <label htmlFor={id('font-color')} className={LABEL_CLASS}>글자색</label>
      <input
        id={id('font-color')}
        type="color"
        value={style.fontColor}
        onChange={(e) => set({ fontColor: e.target.value.toUpperCase() })}
      />
      <label htmlFor={id('outline-color')} className={LABEL_CLASS}>외곽선 색</label>
      <input
        id={id('outline-color')}
        type="color"
        value={style.outlineColor}
        onChange={(e) => set({ outlineColor: e.target.value.toUpperCase() })}
      />
      <label htmlFor={id('outline-width')} className={LABEL_CLASS}>외곽선 두께 {style.outlineWidth}</label>
      <input
        id={id('outline-width')}
        type="range"
        min={0}
        max={12}
        step={1}
        value={style.outlineWidth}
        onChange={(e) => set({ outlineWidth: Number(e.target.value) })}
        className="accent-primary"
      />
      <label htmlFor={id('background')} className={LABEL_CLASS}>
        배경 불투명도 {Math.round(style.backgroundOpacity * 100)}%
      </label>
      <input
        id={id('background')}
        type="range"
        min={0}
        max={100}
        step={5}
        value={Math.round(style.backgroundOpacity * 100)}
        onChange={(e) => set({ backgroundOpacity: Number(e.target.value) / 100 })}
        className="accent-primary"
      />
    </fieldset>
  );
}
