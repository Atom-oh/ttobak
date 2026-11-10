'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { isCommandNotFound, isTauri } from '@/lib/tauri';
import {
  CaptionOverlayController,
  captionLanguages,
  closeCaptionWindow,
  getCaptionSettings,
  openCaptionWindow,
  setCaptionSettings,
  setCaptionStyle,
  type CaptionLang,
  type CaptionSettings,
  type CaptionSettingsView,
  type CaptionStyle,
  type WindowLayout,
} from '@/lib/captionOverlay';
import type { ExtraLiveLanguage } from '@/lib/liveTranslation';

// Mac app only: drives the floating caption windows. Outside the Mac app the
// hook makes no calls and reports `supported: false`. Caption text is never logged.

export type CaptionSegmentHandler = (id: string, text: string, isFinal: boolean, languageCode?: string) => void;

export interface CaptionOverlayState {
  supported: boolean;
  view: CaptionSettingsView | null;
  /** Error code from the Mac app; shown via `captionErrorMessage`. */
  error: string | null;
  openLangs: CaptionLang[];
  /** Third recognition language for live STT, if any. */
  extraLanguage: ExtraLiveLanguage | undefined;
  onSegment: CaptionSegmentHandler;
  setProfile: (profile: string | null) => void;
  setRegion: (region: string) => void;
  setExtraLanguage: (lang: CaptionSettings['extraLanguage']) => void;
  toggleWindow: (lang: CaptionLang) => void;
  updateStyle: (lang: CaptionLang, style: CaptionStyle) => void;
  retry: () => void;
}

const RECOGNITION_LANGUAGE: Record<'ja' | 'zh', ExtraLiveLanguage> = { ja: 'ja-JP', zh: 'zh-CN' };

function errorCode(err: unknown): string {
  const code = (err as { code?: unknown } | null)?.code;
  return typeof code === 'string' ? code : 'aws-error';
}

export function useCaptionOverlay({ isRecording }: { isRecording: boolean }): CaptionOverlayState {
  const [supported, setSupported] = useState(false);
  const [view, setView] = useState<CaptionSettingsView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [openLangs, setOpenLangs] = useState<CaptionLang[]>([]);
  const controllerRef = useRef<CaptionOverlayController | null>(null);
  const openRef = useRef<CaptionLang[]>([]);
  const isRecordingRef = useRef(isRecording);

  const applyLangs = useCallback((langs: CaptionLang[], extra: CaptionSettings['extraLanguage']) => {
    const allowed = captionLanguages(extra);
    const next = allowed.filter((lang) => langs.includes(lang));
    openRef.current = next;
    setOpenLangs(next);
    controllerRef.current?.setLanguages(next);
  }, []);

  const applyView = useCallback((next: CaptionSettingsView) => {
    setSupported(true);
    setView(next);
    const { windows, extraLanguage } = next.settings;
    applyLangs(captionLanguages(extraLanguage).filter((lang) => windows[lang]?.open), extraLanguage);
  }, [applyLangs]);

  const handleLoadError = useCallback((err: unknown) => {
    // An older app build without the caption commands hides the panel.
    if (isCommandNotFound(err)) {
      setSupported(false);
      return;
    }
    setSupported(true);
    setError(errorCode(err));
  }, []);

  useEffect(() => {
    if (!isTauri()) return;
    const controller = new CaptionOverlayController({ onError: setError });
    controllerRef.current = controller;
    getCaptionSettings().then(applyView, handleLoadError);
    return () => {
      controller.stop();
      if (controllerRef.current === controller) controllerRef.current = null;
    };
  }, [applyView, handleLoadError]);

  useEffect(() => {
    const wasRecording = isRecordingRef.current;
    isRecordingRef.current = isRecording;
    const controller = controllerRef.current;
    if (!wasRecording || isRecording || !controller) return;
    // Clear the windows, then start fresh engines: the next recording's
    // segment IDs restart and must not collide with this one's.
    controller.clear();
    controller.stop();
    controller.setLanguages(openRef.current);
  }, [isRecording]);

  const onSegment = useCallback<CaptionSegmentHandler>((id, text, isFinal, languageCode) => {
    controllerRef.current?.update(id, text, isFinal, languageCode);
  }, []);

  const replaceSettings = (settings: CaptionSettings) => {
    setView((previous) => (previous ? { ...previous, settings } : previous));
  };

  const save = async (profile: string | null, region: string, extra: CaptionSettings['extraLanguage']) => {
    const settings = await setCaptionSettings(profile, region, extra);
    replaceSettings(settings);
    setError((previous) => (previous === 'profile-missing' && settings.profile ? null : previous));
    return settings;
  };

  const settings = view?.settings;

  const setProfile = (profile: string | null) => {
    if (!settings || profile === settings.profile) return;
    save(profile, settings.region, settings.extraLanguage).catch((err) => setError(errorCode(err)));
  };

  const setRegion = (region: string) => {
    const trimmed = region.trim();
    if (!settings || trimmed === settings.region) return;
    save(settings.profile, trimmed, settings.extraLanguage).catch((err) => setError(errorCode(err)));
  };

  const setExtraLanguage = (next: CaptionSettings['extraLanguage']) => {
    if (!settings || isRecordingRef.current || next === settings.extraLanguage) return;
    const current = settings.extraLanguage;
    void (async () => {
      try {
        // The Mac app rejects dropping a language whose window is still open.
        if (current && openRef.current.includes(current)) {
          await closeCaptionWindow(current);
          applyLangs(openRef.current.filter((lang) => lang !== current), current);
        }
        await save(settings.profile, settings.region, next);
        applyLangs(openRef.current, next);
      } catch (err) {
        setError(errorCode(err));
      }
    })();
  };

  const toggleWindow = (lang: CaptionLang) => {
    if (!settings) return;
    if (!settings.profile) {
      setError('profile-missing');
      return;
    }
    const extra = settings.extraLanguage;
    if (!captionLanguages(extra).includes(lang)) return;
    void (async () => {
      try {
        if (openRef.current.includes(lang)) {
          await closeCaptionWindow(lang);
          applyLangs(openRef.current.filter((item) => item !== lang), extra);
          setView((previous) => previous && patchWindow(previous, lang, { open: false }));
          return;
        }
        const layout = await openCaptionWindow(lang);
        setView((previous) => previous && setWindow(previous, lang, layout));
        applyLangs([...openRef.current, lang], extra);
        if (!isRecordingRef.current) controllerRef.current?.preview();
      } catch (err) {
        setError(errorCode(err));
      }
    })();
  };

  const updateStyle = (lang: CaptionLang, style: CaptionStyle) => {
    setView((previous) => previous && patchWindow(previous, lang, { style }));
    setCaptionStyle(lang, style).catch((err) => setError(errorCode(err)));
  };

  const retry = () => {
    // Lets a fixed error (for example after `mwinit`) surface again if it recurs.
    controllerRef.current?.resetErrors();
    setError(null);
    getCaptionSettings().then(applyView, handleLoadError);
  };

  const extra = settings?.extraLanguage;
  return {
    supported,
    view,
    error,
    openLangs,
    extraLanguage: supported && extra ? RECOGNITION_LANGUAGE[extra] : undefined,
    onSegment,
    setProfile,
    setRegion,
    setExtraLanguage,
    toggleWindow,
    updateStyle,
    retry,
  };
}

function patchWindow(view: CaptionSettingsView, lang: CaptionLang, patch: Partial<WindowLayout>): CaptionSettingsView {
  const current = view.settings.windows[lang];
  if (!current) return view;
  return setWindow(view, lang, { ...current, ...patch });
}

function setWindow(view: CaptionSettingsView, lang: CaptionLang, layout: WindowLayout): CaptionSettingsView {
  return { ...view, settings: { ...view.settings, windows: { ...view.settings.windows, [lang]: layout } } };
}
