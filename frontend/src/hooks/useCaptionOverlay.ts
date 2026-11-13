'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { isCommandNotFound, isTauri, onCaptionWindowClosed } from '@/lib/tauri';
import {
  CaptionOverlayController,
  TIMED_PREVIEW_MS,
  captionLanguages,
  captionPreviewAction,
  closeCaptionWindow,
  getCaptionSettings,
  openCaptionWindow,
  setCaptionSettings,
  setCaptionStyle,
  savedOpenWindows,
  type CaptionLang,
  type CaptionPreviewEvent,
  type CaptionSettings,
  type CaptionSettingsView,
  type CaptionStyle,
  type WindowLayout,
} from '@/lib/captionOverlay';
import type { ExtraLiveLanguage, TranslationEntry } from '@/lib/liveTranslation';

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
  /** Reports whether the panel's settings section is expanded (the user is arranging windows). */
  setArranging: (expanded: boolean) => void;
}

const RECOGNITION_LANGUAGE: Record<'ja' | 'zh', ExtraLiveLanguage> = { ja: 'ja-JP', zh: 'zh-CN' };

function errorCode(err: unknown): string {
  const code = (err as { code?: unknown } | null)?.code;
  return typeof code === 'string' ? code : 'aws-error';
}

export interface CaptionOverlayOptions {
  isRecording: boolean;
  /** Receives every caption update of an open window, for an in-app copy. Nothing is stored. */
  onEntry?: (lang: CaptionLang, entry: TranslationEntry) => void;
}

export function useCaptionOverlay({ isRecording, onEntry }: CaptionOverlayOptions): CaptionOverlayState {
  const [supported, setSupported] = useState(false);
  const [view, setView] = useState<CaptionSettingsView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [openLangs, setOpenLangs] = useState<CaptionLang[]>([]);
  const controllerRef = useRef<CaptionOverlayController | null>(null);
  const openRef = useRef<CaptionLang[]>([]);
  const isRecordingRef = useRef(isRecording);
  const extraRef = useRef<CaptionSettings['extraLanguage']>(null);
  const reopenedRef = useRef(false);
  const arrangingRef = useRef(false);
  const previewTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const onEntryRef = useRef(onEntry);
  useEffect(() => {
    onEntryRef.current = onEntry;
  }, [onEntry]);

  const cancelPreviewTimer = useCallback(() => {
    if (previewTimerRef.current === null) return;
    clearTimeout(previewTimerRef.current);
    previewTimerRef.current = null;
  }, []);

  // Applies the pure decision in `captionPreviewAction` to the open windows.
  const runPreview = useCallback((event: CaptionPreviewEvent) => {
    const controller = controllerRef.current;
    const action = captionPreviewAction(event, isRecordingRef.current, arrangingRef.current);
    if (action === 'none') return;
    cancelPreviewTimer();
    if (!controller) return;
    if (action === 'clear') {
      controller.clear();
      return;
    }
    controller.preview();
    if (action === 'timed-preview') {
      previewTimerRef.current = setTimeout(() => {
        previewTimerRef.current = null;
        controllerRef.current?.clear();
      }, TIMED_PREVIEW_MS);
    }
  }, [cancelPreviewTimer]);

  const applyLangs = useCallback((langs: CaptionLang[], extra: CaptionSettings['extraLanguage']) => {
    extraRef.current = extra;
    const allowed = captionLanguages(extra);
    const next = allowed.filter((lang) => langs.includes(lang));
    openRef.current = next;
    setOpenLangs(next);
    controllerRef.current?.setLanguages(next);
  }, []);

  // Stops the engine before the window closes: the Mac app refuses to
  // translate for a closed window. A failed close restarts the engine.
  const closeWindow = useCallback(async (lang: CaptionLang, extra: CaptionSettings['extraLanguage']) => {
    applyLangs(openRef.current.filter((item) => item !== lang), extra);
    try {
      await closeCaptionWindow(lang);
    } catch (err) {
      applyLangs([...openRef.current, lang], extra);
      throw err;
    }
  }, [applyLangs]);

  const applyView = useCallback((next: CaptionSettingsView) => {
    setSupported(true);
    setView(next);
    // Saved `open` flags survive an app quit, but the windows do not; only
    // windows this mount actually opened count as open.
    applyLangs(openRef.current, next.settings.extraLanguage);
    if (reopenedRef.current) return;
    reopenedRef.current = true;
    // Reopen last session's layout once per mount. A failure leaves that
    // window closed and never touches the recording.
    for (const lang of savedOpenWindows(next.settings)) {
      openCaptionWindow(lang).then((layout) => {
        setView((previous) => previous && setWindow(previous, lang, layout));
        applyLangs([...openRef.current, lang], extraRef.current);
        runPreview('mount-reopen');
      }, (err) => setError(errorCode(err)));
    }
  }, [applyLangs, runPreview]);

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
    const controller = new CaptionOverlayController({
      onError: setError,
      onEntry: (lang, entry) => onEntryRef.current?.(lang, entry),
    });
    controllerRef.current = controller;
    getCaptionSettings().then(applyView, handleLoadError);
    return () => {
      cancelPreviewTimer();
      controller.clear();
      controller.stop();
      if (controllerRef.current === controller) controllerRef.current = null;
    };
  }, [applyView, handleLoadError, cancelPreviewTimer]);

  useEffect(() => {
    const wasRecording = isRecordingRef.current;
    isRecordingRef.current = isRecording;
    const controller = controllerRef.current;
    if (!wasRecording && isRecording) {
      // Never show a leftover sample sentence before the first real caption.
      runPreview('recording-start');
      return;
    }
    if (!wasRecording || isRecording || !controller) return;
    cancelPreviewTimer();
    // Clear the windows, then start fresh engines: the next recording's
    // segment IDs restart and must not collide with this one's.
    controller.clear();
    controller.stop();
    controller.setLanguages(openRef.current);
  }, [isRecording, runPreview, cancelPreviewTimer]);

  // A window closed from its own close button: the Mac app already closed and
  // saved it, so only this panel's state and the translation engine follow.
  useEffect(() => {
    if (!isTauri()) return;
    return onCaptionWindowClosed((lang) => {
      const closed = openRef.current.find((item) => item === lang);
      if (!closed) return;
      applyLangs(openRef.current.filter((item) => item !== closed), extraRef.current);
      setView((previous) => previous && patchWindow(previous, closed, { open: false }));
    });
  }, [applyLangs]);

  const onSegment = useCallback<CaptionSegmentHandler>((id, text, isFinal, languageCode) => {
    controllerRef.current?.update(id, text, isFinal, languageCode);
  }, []);

  const replaceSettings = (settings: CaptionSettings) => {
    extraRef.current = settings.extraLanguage;
    setView((previous) => (previous ? { ...previous, settings } : previous));
  };

  const save = async (profile: string | null, region: string, extra: CaptionSettings['extraLanguage']) => {
    const settings = await setCaptionSettings(profile, region, extra);
    replaceSettings(settings);
    setError((previous) => (previous === 'profile-missing' && settings.profile ? null : previous));
    return settings;
  };

  const settings = view?.settings;

  // A declined native confirmation changed nothing: re-read the saved values
  // so the selects show them again.
  const saveFailed = (err: unknown) => {
    const code = errorCode(err);
    setError(code);
    if (code !== 'confirmation-declined') return;
    getCaptionSettings().then((next) => {
      extraRef.current = next.settings.extraLanguage;
      setView(next);
    }, () => {});
  };

  const setProfile = (profile: string | null) => {
    if (!settings || profile === settings.profile) return;
    save(profile, settings.region, settings.extraLanguage).catch(saveFailed);
  };

  const setRegion = (region: string) => {
    const trimmed = region.trim();
    if (!settings || trimmed === settings.region) return;
    save(settings.profile, trimmed, settings.extraLanguage).catch(saveFailed);
  };

  const setExtraLanguage = (next: CaptionSettings['extraLanguage']) => {
    if (!settings || isRecordingRef.current || next === settings.extraLanguage) return;
    const current = settings.extraLanguage;
    void (async () => {
      try {
        // The Mac app rejects dropping a language whose window is still open.
        if (current && openRef.current.includes(current)) await closeWindow(current, current);
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
          await closeWindow(lang, extra);
          setView((previous) => previous && patchWindow(previous, lang, { open: false }));
          return;
        }
        const layout = await openCaptionWindow(lang);
        setView((previous) => previous && setWindow(previous, lang, layout));
        applyLangs([...openRef.current, lang], extra);
        runPreview('toggle-open');
      } catch (err) {
        setError(errorCode(err));
      }
    })();
  };

  const setArranging = useCallback((expanded: boolean) => {
    if (arrangingRef.current === expanded) return;
    arrangingRef.current = expanded;
    runPreview(expanded ? 'settings-expand' : 'settings-collapse');
  }, [runPreview]);

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
    setArranging,
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
