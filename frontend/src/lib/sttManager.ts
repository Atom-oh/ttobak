'use client';

/**
 * STT Provider Manager — orchestrates selection and fallback between
 * Amazon Transcribe Streaming (browser-direct) and Web Speech API.
 *
 * Transcribe Streaming is the primary provider (reliable, server-grade).
 * Web Speech API is the fallback when Transcribe is unavailable or fails.
 */

import { TranscribeFallbackClient, type TranscribeCallbacks } from './transcribeClient';
import { TranscribeStreamingSession } from './transcribeStreamingClient';
import { translateApi } from './api';

export type LiveSttProvider = 'transcribe-streaming' | 'web-speech';

export interface SttManagerConfig {
  callbacks: TranscribeCallbacks;
  targetLang: string;
  translationEnabled: boolean;
  transcribeStreamingConfig?: {
    region: string;
    identityPoolId: string;
    userPoolId: string;
  };
  onProviderChange?: (provider: LiveSttProvider) => void;
}

export class SttManager {
  private activeProvider: LiveSttProvider = 'web-speech';
  private transcribeSession: TranscribeStreamingSession | null = null;
  private webSpeechClient: TranscribeFallbackClient | null = null;
  private stream: MediaStream | null = null;
  private config: SttManagerConfig;

  // Translation state (shared across providers)
  private translateTimer: ReturnType<typeof setTimeout> | undefined;
  private interimTranslateTimer: ReturnType<typeof setTimeout> | undefined;
  private pendingTexts: string[] = [];

  constructor(config: SttManagerConfig) {
    this.config = config;
  }

  getActiveProvider(): LiveSttProvider {
    return this.activeProvider;
  }

  async start(
    stream: MediaStream,
    preferredProvider: LiveSttProvider,
    sourceLang = 'ko-KR',
  ): Promise<void> {
    this.stream = stream;

    if (preferredProvider === 'transcribe-streaming' && this.config.transcribeStreamingConfig) {
      try {
        await this.startTranscribeStreaming(stream, sourceLang);
        this.activeProvider = 'transcribe-streaming';
        return;
      } catch (err) {
        console.warn('Transcribe Streaming failed, falling back to Web Speech:', err);
        this.config.onProviderChange?.('web-speech');
      }
    }

    // Fallback: Web Speech API
    this.startWebSpeech(sourceLang);
    this.activeProvider = 'web-speech';
  }

  private async startTranscribeStreaming(stream: MediaStream, sourceLang: string): Promise<void> {
    const tsConfig = this.config.transcribeStreamingConfig!;

    this.transcribeSession = new TranscribeStreamingSession({
      region: tsConfig.region,
      identityPoolId: tsConfig.identityPoolId,
      userPoolId: tsConfig.userPoolId,
      languageCode: sourceLang,
      onTranscript: (text, isFinal) => {
        this.config.callbacks.onTranscript(text, isFinal);
        if (isFinal) {
          this.handleFinalTranslation(text);
        } else {
          this.handleInterimTranslation(text);
        }
      },
      onError: (error) => {
        console.error('Transcribe Streaming error, switching to Web Speech:', error);
        // Auto-fallback to Web Speech
        this.transcribeSession?.stop();
        this.transcribeSession = null;
        this.startWebSpeech(sourceLang);
        this.activeProvider = 'web-speech';
        this.config.onProviderChange?.('web-speech');
      },
    });

    // start() is async — it opens the WebSocket and begins streaming.
    // Run in background so we don't block the caller.
    this.transcribeSession.start(stream).catch((err) => {
      console.error('Transcribe Streaming start failed:', err);
      this.transcribeSession?.stop();
      this.transcribeSession = null;
      this.startWebSpeech(sourceLang);
      this.activeProvider = 'web-speech';
      this.config.onProviderChange?.('web-speech');
    });
  }

  private startWebSpeech(sourceLang: string): void {
    this.webSpeechClient = new TranscribeFallbackClient(
      this.config.callbacks,
      this.config.targetLang,
      this.config.translationEnabled,
    );
    this.webSpeechClient.start(sourceLang);
  }

  private handleFinalTranslation(text: string): void {
    if (!this.config.translationEnabled) return;
    this.pendingTexts.push(text);
    if (this.translateTimer) clearTimeout(this.translateTimer);
    this.translateTimer = setTimeout(() => {
      const batch = this.pendingTexts.splice(0);
      if (batch.length === 0) return;
      const combined = batch.join('\n');
      translateApi
        .translate(combined, 'ko', this.config.targetLang)
        .then((res) => {
          const parts = res.translatedText.split('\n');
          batch.forEach((original, i) => {
            this.config.callbacks.onTranslation(original, parts[i] || '', this.config.targetLang, true);
          });
        })
        .catch((err) => console.error('Translation failed:', err));
    }, 300);
  }

  private handleInterimTranslation(text: string): void {
    if (!this.config.translationEnabled) return;
    if (this.interimTranslateTimer) clearTimeout(this.interimTranslateTimer);
    this.interimTranslateTimer = setTimeout(() => {
      translateApi
        .translate(text, 'ko', this.config.targetLang)
        .then((res) => {
          this.config.callbacks.onTranslation(text, res.translatedText, this.config.targetLang, false);
        })
        .catch((err) => console.error('Interim translation failed:', err));
    }, 500);
  }

  updateTargetLang(lang: string): void {
    this.config.targetLang = lang;
    this.webSpeechClient?.updateTargetLang(lang);
  }

  updateTranslationEnabled(enabled: boolean): void {
    this.config.translationEnabled = enabled;
    this.webSpeechClient?.updateTranslationEnabled(enabled);
  }

  pause(): void {
    if (this.activeProvider === 'transcribe-streaming') {
      // Transcribe Streaming doesn't support pause — stop and restart on resume
      this.transcribeSession?.stop();
    } else {
      this.webSpeechClient?.pause();
    }
  }

  resume(): void {
    if (this.activeProvider === 'transcribe-streaming' && this.stream) {
      // Restart Transcribe Streaming session
      this.startTranscribeStreaming(this.stream, 'ko-KR').catch(() => {
        this.startWebSpeech('ko-KR');
        this.activeProvider = 'web-speech';
        this.config.onProviderChange?.('web-speech');
      });
    } else {
      this.webSpeechClient?.resume();
    }
  }

  stop(): void {
    this.transcribeSession?.stop();
    this.transcribeSession = null;
    this.webSpeechClient?.stop();
    this.webSpeechClient = null;
    this.stream = null;
    if (this.translateTimer) clearTimeout(this.translateTimer);
    if (this.interimTranslateTimer) clearTimeout(this.interimTranslateTimer);
    this.pendingTexts = [];
  }
}
