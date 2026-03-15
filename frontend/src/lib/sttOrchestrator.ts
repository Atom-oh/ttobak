/**
 * STT Orchestrator - Auto-switches between fallback (Web Speech API) and Whisper (ECS).
 * Starts with Web Speech API immediately, then switches to faster-whisper when ECS is ready.
 */

import { TranscribeFallbackClient } from './transcribeClient';
import { RealtimeClient } from './realtimeClient';
import { realtimeApi } from './api';

export type SttSource = 'fallback' | 'whisper' | 'idle';

export interface SttCallbacks {
  onTranscript: (text: string, isFinal: boolean) => void;
  onTranslation: (original: string, translated: string, targetLang: string) => void;
  onQuestion: (questions: string[]) => void;
  onSourceChange: (source: SttSource) => void;
  onError: (error: string) => void;
}

export class SttOrchestrator {
  private fallbackClient: TranscribeFallbackClient | null = null;
  private realtimeClient: RealtimeClient | null = null;
  private callbacks: SttCallbacks;
  private activeSource: SttSource = 'idle';
  private stream: MediaStream | null = null;
  private sourceLang: string;
  private targetLang: string;
  private stopped = false;

  constructor(callbacks: SttCallbacks, sourceLang = 'ko', targetLang = 'en') {
    this.callbacks = callbacks;
    this.sourceLang = sourceLang;
    this.targetLang = targetLang;
  }

  async start(stream: MediaStream): Promise<void> {
    this.stream = stream;
    this.stopped = false;

    // 1. Start fallback (Web Speech API) immediately
    this.fallbackClient = new TranscribeFallbackClient(
      {
        onTranscript: (text, isFinal) => {
          if (this.activeSource === 'fallback') {
            this.callbacks.onTranscript(text, isFinal);
          }
        },
        onTranslation: (original, translated, targetLang) => {
          if (this.activeSource === 'fallback') {
            this.callbacks.onTranslation(original, translated, targetLang);
          }
        },
        onError: (error) => {
          this.callbacks.onError(error);
        },
      },
      this.targetLang
    );

    this.fallbackClient.start(this.sourceLang === 'ko' ? 'ko-KR' : this.sourceLang);
    this.activeSource = 'fallback';
    this.callbacks.onSourceChange('fallback');

    // 2. Request ECS start (async, non-blocking)
    this.startEcsPolling();
  }

  private async startEcsPolling(): Promise<void> {
    try {
      const result = await realtimeApi.start();
      if (this.stopped) return;

      // ECS is ready - switch to whisper
      await this.switchToWhisper(result.websocketUrl);
    } catch (err) {
      console.error('ECS start failed:', err);
      // Stay on fallback - it's still working
    }
  }

  private async switchToWhisper(websocketUrl: string): Promise<void> {
    if (this.stopped || !this.stream) return;

    this.realtimeClient = new RealtimeClient({
      onTranscript: (text, isFinal) => {
        if (this.activeSource === 'whisper') {
          this.callbacks.onTranscript(text, isFinal);
        }
      },
      onTranslation: (original, translated, targetLang) => {
        if (this.activeSource === 'whisper') {
          this.callbacks.onTranslation(original, translated, targetLang);
        }
      },
      onQuestion: (questions) => {
        if (this.activeSource === 'whisper') {
          this.callbacks.onQuestion(questions);
        }
      },
      onError: (error) => {
        this.callbacks.onError(error);
        // Fall back to speech recognition if whisper fails
        if (this.activeSource === 'whisper' && this.fallbackClient) {
          this.activeSource = 'fallback';
          this.callbacks.onSourceChange('fallback');
        }
      },
      onDisconnect: () => {
        if (this.activeSource === 'whisper' && !this.stopped) {
          // Reconnect fallback
          this.activeSource = 'fallback';
          this.callbacks.onSourceChange('fallback');
        }
      },
    });

    try {
      await this.realtimeClient.connect(
        websocketUrl,
        this.stream,
        this.sourceLang,
        this.targetLang
      );

      // Successfully connected - switch source
      this.fallbackClient?.stop();
      this.activeSource = 'whisper';
      this.callbacks.onSourceChange('whisper');
    } catch (err) {
      console.error('Failed to connect to whisper:', err);
      // Stay on fallback
    }
  }

  updateTargetLang(lang: string): void {
    this.targetLang = lang;
    this.fallbackClient?.updateTargetLang(lang);
    this.realtimeClient?.updateConfig(this.sourceLang, lang);
  }

  async stop(): Promise<void> {
    this.stopped = true;
    this.activeSource = 'idle';

    this.fallbackClient?.stop();
    this.fallbackClient = null;

    this.realtimeClient?.disconnect();
    this.realtimeClient = null;

    // Tell backend to scale down ECS
    try {
      await realtimeApi.stop();
    } catch (err) {
      console.error('Failed to stop ECS:', err);
    }

    this.callbacks.onSourceChange('idle');
  }

  get currentSource(): SttSource {
    return this.activeSource;
  }
}
