/**
 * Transcribe fallback client using Browser Speech Recognition.
 * Wraps BrowserSpeechRecognition to provide the same callback interface as RealtimeClient.
 * Used as a fallback while ECS faster-whisper is booting up.
 */

import { BrowserSpeechRecognition } from './speechRecognition';
import { translateApi } from './api';

export interface TranscribeCallbacks {
  onTranscript: (text: string, isFinal: boolean) => void;
  onTranslation: (original: string, translated: string, targetLang: string, isFinal: boolean) => void;
  onError: (error: string) => void;
}

export class TranscribeFallbackClient {
  private speech: BrowserSpeechRecognition | null = null;
  private callbacks: TranscribeCallbacks;
  private targetLang: string;
  private translationEnabled: boolean;
  private translateTimer: ReturnType<typeof setTimeout> | undefined;
  private interimTranslateTimer: ReturnType<typeof setTimeout> | undefined;
  private pendingTexts: string[] = [];

  constructor(callbacks: TranscribeCallbacks, targetLang = 'en', translationEnabled = false) {
    this.callbacks = callbacks;
    this.targetLang = targetLang;
    this.translationEnabled = translationEnabled;
  }

  updateTranslationEnabled(enabled: boolean): void {
    this.translationEnabled = enabled;
  }

  start(sourceLang = 'ko-KR'): boolean {
    if (!BrowserSpeechRecognition.isSupported()) {
      this.callbacks.onError('Speech recognition not supported in this browser');
      return false;
    }

    this.speech = new BrowserSpeechRecognition(sourceLang);
    return this.speech.start(
      (result) => {
        this.callbacks.onTranscript(result.text, result.isFinal);

        if (result.isFinal) {
          // Debounced translation (only when enabled)
          if (this.translationEnabled) {
            this.pendingTexts.push(result.text);
            if (this.translateTimer) clearTimeout(this.translateTimer);
            this.translateTimer = setTimeout(() => {
              const batch = this.pendingTexts.splice(0);
              if (batch.length === 0) return;
              const combined = batch.join('\n');
              translateApi
                .translate(combined, 'ko', this.targetLang)
                .then((res) => {
                  const parts = res.translatedText.split('\n');
                  batch.forEach((original, i) => {
                    this.callbacks.onTranslation(original, parts[i] || '', this.targetLang, true);
                  });
                })
                .catch((err) => console.error('Translation failed:', err));
            }, 300);
          }
        } else {
          // Interim: debounce translate at 500ms (only when enabled)
          if (this.translationEnabled) {
            if (this.interimTranslateTimer) clearTimeout(this.interimTranslateTimer);
            this.interimTranslateTimer = setTimeout(() => {
              translateApi
                .translate(result.text, 'ko', this.targetLang)
                .then((res) => {
                  this.callbacks.onTranslation(result.text, res.translatedText, this.targetLang, false);
                })
                .catch((err) => console.error('Interim translation failed:', err));
            }, 500);
          }
        }
      },
      (error) => {
        this.callbacks.onError(error);
      }
    );
  }

  updateTargetLang(lang: string): void {
    this.targetLang = lang;
  }

  /** Get pending interim text from underlying speech recognition. */
  getPendingInterim(): string | null {
    return this.speech?.getPendingInterim() ?? null;
  }

  pause(): void {
    this.speech?.pause();
  }

  resume(): void {
    this.speech?.resume();
  }

  stop(): void {
    this.speech?.stop();
    this.speech = null;
    if (this.translateTimer) clearTimeout(this.translateTimer);
    if (this.interimTranslateTimer) clearTimeout(this.interimTranslateTimer);
  }
}
