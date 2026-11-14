'use client';

import {
  LiveTranslation,
  type TranslateRequestOptions,
  type TranslateResponse,
  type TranslationEntry,
} from './liveTranslation';
import { MAX_SPEAKER_ENTRIES, speakerNumber } from './interpretSpeakers';

// Bridge to the Mac app's floating caption windows. Nothing here runs unless
// called, and caption text is never logged. Decision record: ADR-018 (Mac
// caption overlay windows). A window can sit over another app's full-screen
// Space only if the native side created it for that; one opened by an older
// app build has to be closed and opened again.

export type CaptionLang = 'ko' | 'en' | 'ja' | 'zh';
export type CaptionPushKind = 'entry' | 'clear' | 'preview';

export interface CaptionStyle {
  fontFamily: string;
  fontSize: number;
  fontColor: string;
  outlineColor: string;
  outlineWidth: number;
  backgroundOpacity: number;
}

export interface WindowLayout {
  open: boolean;
  style: CaptionStyle;
  x: number | null;
  y: number | null;
  width: number;
  height: number;
}

export interface CaptionSettings {
  profile: string | null;
  region: string;
  extraLanguage: 'ja' | 'zh' | null;
  windows: Partial<Record<CaptionLang, WindowLayout>>;
}

export interface CaptionSettingsView {
  settings: CaptionSettings;
  profiles: string[];
  fonts: string[];
}

export type CaptionErrorCode =
  | 'profile-missing'
  | 'credentials-expired'
  | 'model-access'
  | 'rate-limited'
  | 'invalid-input'
  | 'invalid-output'
  | 'timeout'
  | 'aws-error'
  | 'save-failed'
  | 'window-error'
  | 'confirmation-declined'
  | 'confirmation-cooldown'
  | 'unavailable';

/** Largest caption push, in UTF-8 bytes; matches `MAX_TEXT_BYTES` in caption_overlay.rs. */
export const MAX_PUSH_TEXT_BYTES = 4000;

/**
 * Regions offered for caption translation (the Mac app validates the region
 * format). The model is a global inference profile, so the region only picks
 * the request endpoint and the Amazon Translate region; ap-northeast-2 is the
 * Mac app's default.
 */
export const CAPTION_REGIONS = ['ap-northeast-2', 'us-east-1'] as const;

/** Region choices, keeping a saved region that is not listed so it is never lost. */
export function captionRegionOptions(saved: string): string[] {
  const listed: readonly string[] = CAPTION_REGIONS;
  return !saved || listed.includes(saved) ? [...listed] : [...listed, saved];
}

function utf8Bytes(char: string): number {
  const code = char.codePointAt(0) ?? 0;
  return code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
}

/** Longest tail of `text` within `maxBytes` UTF-8 bytes, cut on a character boundary. */
export function utf8Tail(text: string, maxBytes: number): string {
  const chars = Array.from(text);
  let bytes = 0;
  let start = chars.length;
  while (start > 0) {
    const size = utf8Bytes(chars[start - 1]);
    if (bytes + size > maxBytes) break;
    bytes += size;
    start--;
  }
  return start === 0 ? text : chars.slice(start).join('');
}

export interface CaptionTranslateResult {
  translatedText: string;
  engine: 'llm' | 'translate';
}

function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const core = typeof window === 'undefined' ? undefined : window.__TAURI__?.core;
  if (!core) return Promise.reject(new Error('Tauri API not available'));
  return core.invoke<T>(cmd, args);
}

export function getCaptionSettings(): Promise<CaptionSettingsView> {
  return invoke<CaptionSettingsView>('caption_settings_get');
}

export function setCaptionSettings(
  profile: string | null, region: string, extraLanguage: 'ja' | 'zh' | null,
): Promise<CaptionSettings> {
  return invoke<CaptionSettings>('caption_settings_set', { profile, region, extraLanguage });
}

export function openCaptionWindow(lang: CaptionLang): Promise<WindowLayout> {
  return invoke<WindowLayout>('caption_overlay_open', { lang });
}

export function closeCaptionWindow(lang: CaptionLang): Promise<void> {
  return invoke<void>('caption_overlay_close', { lang });
}

export function setCaptionStyle(lang: CaptionLang, style: CaptionStyle): Promise<void> {
  return invoke<void>('caption_overlay_set_style', { lang, style });
}

/** `speaker` (zero-based) colours the line in the window; a Mac app without it ignores the field. */
export function pushCaption(
  lang: CaptionLang, kind: CaptionPushKind, id: string, order: number, text: string, isFinal: boolean,
  speaker?: number,
): Promise<void> {
  const args = { lang, kind, id, order, text, isFinal };
  return invoke<void>('caption_overlay_push', speaker === undefined ? args : { ...args, speaker });
}

export function translateCaption(
  text: string, sourceLang: string, targetLang: string, isFinal: boolean, context: string[],
): Promise<CaptionTranslateResult> {
  return invoke<CaptionTranslateResult>('caption_translate', { text, sourceLang, targetLang, isFinal, context });
}

/** Caption windows allowed by the settings: ko and en, plus at most one of ja/zh. */
export function captionLanguages(extraLanguage: CaptionSettings['extraLanguage']): CaptionLang[] {
  return extraLanguage ? ['ko', 'en', extraLanguage] : ['ko', 'en'];
}

/** How long an explicitly opened window shows its sample while settings are collapsed. */
export const TIMED_PREVIEW_MS = 4000;

export type CaptionPreviewEvent =
  | 'mount-reopen'
  | 'toggle-open'
  | 'settings-expand'
  | 'settings-collapse'
  | 'recording-start'
  | 'recording-stop';
export type CaptionPreviewAction = 'preview' | 'clear' | 'timed-preview' | 'none';

/**
 * Decides what the open windows should show. Samples appear only while the
 * user arranges (settings expanded) or briefly after an explicit open, and
 * never while recording, so live captions are not disturbed.
 */
export function captionPreviewAction(
  event: CaptionPreviewEvent, isRecording: boolean, expanded: boolean,
): CaptionPreviewAction {
  if (event === 'recording-start' || event === 'recording-stop') return 'clear';
  if (isRecording) return 'none';
  switch (event) {
    case 'mount-reopen':
      // Reopened at launch: stay hidden unless the user left the settings
      // section expanded, which means they are still arranging.
      return expanded ? 'preview' : 'clear';
    case 'settings-collapse':
      return 'clear';
    case 'settings-expand':
      return 'preview';
    case 'toggle-open':
      return expanded ? 'preview' : 'timed-preview';
  }
}

/** Saved-open windows to reopen at launch, limited to the allowed languages. */
export function savedOpenWindows(settings: CaptionSettings | null | undefined): CaptionLang[] {
  if (!settings) return [];
  return captionLanguages(settings.extraLanguage).filter((lang) => settings.windows[lang]?.open === true);
}

export function captionErrorMessage(code: string): string {
  switch (code) {
    case 'credentials-expired':
      return '자격 증명이 만료됐어요. 터미널에서 mwinit을 실행한 뒤 다시 시도해 주세요.';
    case 'profile-missing':
      return 'AWS 프로필을 먼저 선택해 주세요.';
    case 'model-access':
      return '선택한 AWS 계정에서 GPT-6 Luna 모델을 사용할 수 없어요. Bedrock 모델 접근 권한을 확인해 주세요.';
    case 'rate-limited':
      return '번역 요청이 너무 많아 잠시 멈췄어요.';
    case 'confirmation-declined':
      return 'AWS 프로필 변경을 취소했어요.';
    case 'confirmation-cooldown':
      return '잠시 후 다시 시도해 주세요.';
    case 'unavailable':
      return '이 Mac에서는 자막 창을 사용할 수 없어요.';
    default:
      return '자막 번역에 실패했어요. 잠시 후 다시 시도해 주세요.';
  }
}

function rejectionCode(err: unknown): string | undefined {
  const code = (err as { code?: unknown } | null)?.code;
  return typeof code === 'string' ? code : undefined;
}

/** Highest speaker number the Mac app accepts (Transcribe labels up to 30 voices). */
export const MAX_PUSH_SPEAKER = 29;

interface ControllerDeps {
  translate: (
    text: string, sourceLang: string, targetLang: string, isFinal: boolean, context: string[],
  ) => Promise<TranslateResponse>;
  push: typeof pushCaption;
  onError: (code: string) => void;
  /** Every caption update of an open window, before it is sent there (for an in-app copy). */
  onEntry?: (lang: CaptionLang, entry: TranslationEntry) => void;
}

/** Runs one LiveTranslation engine per open caption window. */
export class CaptionOverlayController {
  private engines = new Map<CaptionLang, LiveTranslation>();
  private reported = new Set<string>();
  /** Speaker number by segment ID, in memory only; the oldest are dropped. */
  private speakers = new Map<string, number>();
  private deps: ControllerDeps;

  constructor(deps: Partial<ControllerDeps> & Pick<ControllerDeps, 'onError'>) {
    this.deps = { translate: translateCaption, push: pushCaption, ...deps };
  }

  /** Creates engines for newly open languages and stops those no longer open. */
  setLanguages(langs: CaptionLang[]): void {
    const wanted = new Set(langs);
    for (const [lang, engine] of this.engines) {
      if (!wanted.has(lang)) {
        engine.stop();
        this.engines.delete(lang);
      }
    }
    for (const lang of wanted) {
      if (!this.engines.has(lang)) this.engines.set(lang, this.createEngine(lang));
    }
  }

  private createEngine(lang: CaptionLang): LiveTranslation {
    return new LiveTranslation({
      enabled: true,
      interim: true,
      targetLang: lang,
      onUpdate: (entry) => this.publish(lang, entry),
      onReset: () => {},
      translate: async (text, sourceLang, targetLang, _signal, options: TranslateRequestOptions) => {
        try {
          return await this.deps.translate(text, sourceLang, targetLang, options.quality === 'high', options.context ?? []);
        } catch (err) {
          const code = rejectionCode(err);
          if (code && !this.reported.has(code)) {
            this.reported.add(code);
            this.deps.onError(code);
          }
          throw err;
        }
      },
    });
  }

  private publish(lang: CaptionLang, entry: TranslationEntry): void {
    this.deps.onEntry?.(lang, entry);
    if (entry.status === 'skipped') return;
    // A failed translation clears its line rather than leaving a stale draft.
    if (entry.status === 'error') {
      void this.send(lang, 'entry', entry.id, entry.order, '', entry.isFinal);
      return;
    }
    if (!entry.translated) return;
    // The window shows the end of a long sentence; the Mac app rejects longer text.
    const text = utf8Tail(entry.translated, MAX_PUSH_TEXT_BYTES);
    void this.send(lang, 'entry', entry.id, entry.order, text, entry.isFinal, this.speakers.get(entry.id));
  }

  private send(
    lang: CaptionLang, kind: CaptionPushKind, id: string, order: number, text: string, isFinal: boolean,
    speaker?: number,
  ): Promise<void> {
    // A closed window or missing command must not break the live session.
    const sent = speaker === undefined
      ? this.deps.push(lang, kind, id, order, text, isFinal)
      : this.deps.push(lang, kind, id, order, text, isFinal, speaker);
    return sent.catch(() => undefined);
  }

  /** `speaker` is the recognizer's label for a final segment, when it told voices apart. */
  update(id: string, text: string, isFinal: boolean, detectedLanguage?: string, speaker?: string): void {
    const number = isFinal ? speakerNumber(speaker) : null;
    if (number !== null && number <= MAX_PUSH_SPEAKER) {
      this.speakers.delete(id);
      this.speakers.set(id, number);
      if (this.speakers.size > MAX_SPEAKER_ENTRIES) this.speakers.delete(this.speakers.keys().next().value!);
    }
    for (const engine of this.engines.values()) engine.update(id, text, isFinal, detectedLanguage);
  }

  clear(): void {
    for (const lang of this.engines.keys()) void this.send(lang, 'clear', '', 0, '', false);
  }

  preview(): void {
    for (const lang of this.engines.keys()) void this.send(lang, 'preview', '', 0, '', false);
  }

  /** Lets an already reported error code surface again. */
  resetErrors(): void {
    this.reported.clear();
  }

  stop(): void {
    for (const engine of this.engines.values()) engine.stop();
    this.engines.clear();
    this.speakers.clear();
  }
}
