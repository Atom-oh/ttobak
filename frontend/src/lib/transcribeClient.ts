/**
 * Transcribe fallback client using Browser Speech Recognition.
 * Wraps BrowserSpeechRecognition to provide the same callback interface as RealtimeClient.
 * Used as a fallback while ECS faster-whisper is booting up.
 */

import { BrowserSpeechRecognition } from './speechRecognition';
import { translateApi } from './api';

export interface TranscribeCallbacks {
  onTranscript: (text: string, isFinal: boolean) => void;
  onTranslation: (original: string, translated: string, targetLang: string) => void;
  onError: (error: string) => void;
}

export class TranscribeFallbackClient {
  private speech: BrowserSpeechRecognition | null = null;
  private callbacks: TranscribeCallbacks;
  private targetLang: string;
  private translateTimer: ReturnType<typeof setTimeout> | undefined;
  private pendingTexts: string[] = [];

  constructor(callbacks: TranscribeCallbacks, targetLang = 'en') {
    this.callbacks = callbacks;
    this.targetLang = targetLang;
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
          // Debounced translation
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
                  this.callbacks.onTranslation(original, parts[i] || '', this.targetLang);
                });
              })
              .catch((err) => console.error('Translation failed:', err));
          }, 300);
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

  stop(): void {
    this.speech?.stop();
    this.speech = null;
    if (this.translateTimer) clearTimeout(this.translateTimer);
  }
}
