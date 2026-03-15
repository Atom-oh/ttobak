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
  onTranslation: (original: string, translated: string, targetLang: string, isFinal: boolean) => void;
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
        onTranslation: (original, translated, targetLang, isFinal) => {
          if (this.activeSource === 'fallback') {
            this.callbacks.onTranslation(original, translated, targetLang, isFinal);
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
      // Trigger ECS scale-up (returns immediately with status: "starting" or "ready")
      const startResult = await realtimeApi.start();
      if (this.stopped) return;

      // If already ready, switch immediately
      if (startResult.status === 'ready' && startResult.websocketUrl) {
        await this.switchToWhisper(startResult.websocketUrl);
        return;
      }

      // Poll for readiness (max 24 polls = 120s)
      for (let i = 0; i < 24; i++) {
        if (this.stopped) return;
        await new Promise(resolve => setTimeout(resolve, 5000));
        if (this.stopped) return;

        try {
          const result = await realtimeApi.status();
          if (result.status === 'ready' && result.websocketUrl) {
            await this.switchToWhisper(result.websocketUrl);
            return;
          }
        } catch {
          // Continue polling on error
        }
      }
      console.warn('ECS did not become ready within 120s, staying on fallback');
    } catch (err) {
      console.error('ECS start failed:', err);
      // Stay on fallback - it's still working
    }
  }

  /** Restart browser Speech API fallback (e.g. after Spot reclaim disconnects Whisper) */
  private restartFallback(): void {
    this.activeSource = 'fallback';
    this.callbacks.onSourceChange('fallback');

    // Re-create fallback client since stop() was called when whisper took over
    this.fallbackClient = new TranscribeFallbackClient(
      {
        onTranscript: (text, isFinal) => {
          if (this.activeSource === 'fallback') {
            this.callbacks.onTranscript(text, isFinal);
          }
        },
        onTranslation: (original, translated, targetLang, isFinal) => {
          if (this.activeSource === 'fallback') {
            this.callbacks.onTranslation(original, translated, targetLang, isFinal);
          }
        },
        onError: (error) => {
          this.callbacks.onError(error);
        },
      },
      this.targetLang
    );
    this.fallbackClient.start(this.sourceLang === 'ko' ? 'ko-KR' : this.sourceLang);
  }

  private async switchToWhisper(websocketUrl: string): Promise<void> {
    if (this.stopped || !this.stream) return;

    this.realtimeClient = new RealtimeClient({
      onTranscript: (text, isFinal) => {
        if (this.activeSource === 'whisper') {
          this.callbacks.onTranscript(text, isFinal);
        }
      },
      onTranslation: (original, translated, targetLang, isFinal) => {
        if (this.activeSource === 'whisper') {
          this.callbacks.onTranslation(original, translated, targetLang, isFinal);
        }
      },
      onQuestion: (questions) => {
        if (this.activeSource === 'whisper') {
          this.callbacks.onQuestion(questions);
        }
      },
      onError: (error) => {
        this.callbacks.onError(error);
        // Fall back to speech recognition if whisper fails (e.g. Spot reclaim)
        if (this.activeSource === 'whisper' && !this.stopped) {
          this.restartFallback();
        }
      },
      onDisconnect: () => {
        if (this.activeSource === 'whisper' && !this.stopped) {
          // Spot reclaimed or connection lost — restart browser STT fallback
          this.restartFallback();
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
