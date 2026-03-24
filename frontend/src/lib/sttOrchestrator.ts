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
  onBackendAudioSaved?: (key: string) => void;
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

  private translationEnabled: boolean;
  private userId?: string;
  private meetingId?: string;
  private backendAudioKey?: string;

  constructor(
    callbacks: SttCallbacks,
    sourceLang = 'ko',
    targetLang = 'en',
    translationEnabled = false,
    userId?: string,
    meetingId?: string,
  ) {
    this.callbacks = callbacks;
    this.sourceLang = sourceLang;
    this.targetLang = targetLang;
    this.translationEnabled = translationEnabled;
    this.userId = userId;
    this.meetingId = meetingId;
  }

  updateTranslationEnabled(enabled: boolean): void {
    this.translationEnabled = enabled;
    this.fallbackClient?.updateTranslationEnabled(enabled);
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
      this.targetLang,
      this.translationEnabled
    );

    this.fallbackClient.start(this.sourceLang === 'ko' ? 'ko-KR' : this.sourceLang);
    this.activeSource = 'fallback';
    this.callbacks.onSourceChange('fallback');

    // 2. Start ECS polling for faster-whisper upgrade
    this.startEcsPolling();
  }

  private async startEcsPolling(): Promise<void> {
    try {
      // Trigger ECS scale-up (returns immediately with status: "starting" or "ready")
      const startResult = await realtimeApi.start();
      if (this.stopped) return;

      // If already ready, switch immediately
      if (startResult.status === 'ready' && startResult.websocketUrl) {
        await this.switchToWhisper(this.buildWsUrl(startResult.websocketUrl));
        return;
      }

      // Poll for readiness (max 60 polls * 5s = 300s = 5 minutes)
      // GPU cold start can take 3-5 minutes (instance launch + Whisper model load)
      for (let i = 0; i < 60; i++) {
        if (this.stopped) return;
        await new Promise(resolve => setTimeout(resolve, 5000));
        if (this.stopped) return;

        try {
          const result = await realtimeApi.status();
          if (result.status === 'ready' && result.websocketUrl) {
            await this.switchToWhisper(this.buildWsUrl(result.websocketUrl));
            return;
          }
        } catch {
          // Continue polling on error
        }
      }
      console.warn('ECS did not become ready within 300s, staying on fallback');
    } catch (err) {
      // ECS start failed (e.g. capacity provider empty, TLS error) — stay on fallback silently.
      // Do NOT call callbacks.onError here: fallback STT is still working fine.
      console.warn('ECS start failed, staying on fallback:', err);
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
      this.targetLang,
      this.translationEnabled
    );
    this.fallbackClient.start(this.sourceLang === 'ko' ? 'ko-KR' : this.sourceLang);
  }

  /** Build full WebSocket URL. Relative paths (e.g. "/ws") use the current host via CloudFront. */
  private buildWsUrl(url: string): string {
    if (url.startsWith('/')) {
      const protocol = typeof window !== 'undefined' && window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = typeof window !== 'undefined' ? window.location.host : 'localhost';
      return `${protocol}//${host}${url}`;
    }
    // Backward compat: full URL — upgrade ws:// to wss:// if needed
    if (typeof window !== 'undefined' && window.location.protocol === 'https:') {
      return url.replace(/^ws:\/\//, 'wss://');
    }
    return url;
  }

  private async switchToWhisper(websocketUrl: string): Promise<void> {
    if (this.stopped || !this.stream) return;

    // Promote pending interim text from fallback before switching sources
    const pendingInterim = this.fallbackClient?.getPendingInterim();
    if (pendingInterim) {
      this.callbacks.onTranscript(pendingInterim, true);
    }

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
      onAudioSaved: (key: string) => {
        this.backendAudioKey = key;
        this.callbacks.onBackendAudioSaved?.(key);
      },
    });

    try {
      await this.realtimeClient.connect(
        websocketUrl,
        this.stream,
        this.sourceLang,
        this.targetLang,
        this.userId,
        this.meetingId,
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

    // Tell backend to scale down ECS (5s timeout — don't block post-recording flow)
    try {
      await Promise.race([
        realtimeApi.stop(),
        new Promise<void>(resolve => setTimeout(resolve, 5000)),
      ]);
    } catch (err) {
      console.error('Failed to stop ECS:', err);
    }

    this.callbacks.onSourceChange('idle');
  }

  /** Get backend-saved audio S3 key (if ECS whisper saved audio). */
  getBackendAudioKey(): string | undefined {
    return this.backendAudioKey;
  }

  get currentSource(): SttSource {
    return this.activeSource;
  }
}
