'use client';

import {
  LiveTranslation,
  type TranslateRequestOptions,
  type TranslateResponse,
  type TranslationEntry,
} from './liveTranslation';

// Bridge to the Mac app's floating caption windows. Nothing here runs unless
// called, and caption text is never logged.

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
  | 'aws-error';

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

export function pushCaption(
  lang: CaptionLang, kind: CaptionPushKind, id: string, order: number, text: string, isFinal: boolean,
): Promise<void> {
  return invoke<void>('caption_overlay_push', { lang, kind, id, order, text, isFinal });
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
    default:
      return '자막 번역에 실패했어요. 잠시 후 다시 시도해 주세요.';
  }
}

function rejectionCode(err: unknown): string | undefined {
  const code = (err as { code?: unknown } | null)?.code;
  return typeof code === 'string' ? code : undefined;
}

interface ControllerDeps {
  translate: (
    text: string, sourceLang: string, targetLang: string, isFinal: boolean, context: string[],
  ) => Promise<TranslateResponse>;
  push: typeof pushCaption;
  onError: (code: string) => void;
}

/** Runs one LiveTranslation engine per open caption window. */
export class CaptionOverlayController {
  private engines = new Map<CaptionLang, LiveTranslation>();
  private reported = new Set<string>();
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
    if (entry.status === 'error' || entry.status === 'skipped' || !entry.translated) return;
    void this.send(lang, 'entry', entry.id, entry.order, entry.translated, entry.isFinal);
  }

  private send(
    lang: CaptionLang, kind: CaptionPushKind, id: string, order: number, text: string, isFinal: boolean,
  ): Promise<void> {
    // A closed window or missing command must not break the live session.
    return this.deps.push(lang, kind, id, order, text, isFinal).catch(() => undefined);
  }

  update(id: string, text: string, isFinal: boolean, detectedLanguage?: string): void {
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
  }
}
