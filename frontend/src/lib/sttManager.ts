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
import { hasMobileMicConflictRisk } from './device';

export type LiveSttProvider = 'transcribe-streaming' | 'web-speech';

export interface SttManagerConfig {
  callbacks: TranscribeCallbacks;
  targetLang: string;
  translationEnabled: boolean;
  transcribeStreamingConfig?: {
    region: string;
    identityPoolId: string;
    userPoolId: string;
    vocabularyName?: string;
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
  ): Promise<void> {
    this.stream = stream;

    if (preferredProvider === 'transcribe-streaming' && this.config.transcribeStreamingConfig) {
      try {
        await this.startTranscribeStreaming(stream);
        this.activeProvider = 'transcribe-streaming';
        return;
      } catch (err) {
        console.warn('Transcribe Streaming failed, falling back to Web Speech:', err);
        this.fallbackToWebSpeech(true);
        return;
      }
    }

    this.fallbackToWebSpeech(false);
  }

  /**
   * Promote a running session onto Transcribe Streaming once a config
   * arrives after `start()` already had to decide without one (the
   * runtime-config + dictionary-lookup fetch in useRecordingSession can
   * still be in flight when a user starts recording right away). Without
   * this, that race permanently stuck the session on whatever `start()`
   * fell back to — on mobile, "no captions at all" for the rest of the
   * recording, with no way to ever recover. No-op if Transcribe Streaming
   * is already active, or if there's no browser stream (native/system
   * mode has no fallback to recover from in the first place).
   */
  retryWithConfig(config: SttManagerConfig['transcribeStreamingConfig']): void {
    if (!config || this.activeProvider === 'transcribe-streaming' || !this.stream) return;
    this.config.transcribeStreamingConfig = config;
    this.webSpeechClient?.stop();
    this.webSpeechClient = null;
    this.startTranscribeStreaming(this.stream);
    this.activeProvider = 'transcribe-streaming';
    this.config.onProviderChange?.('transcribe-streaming');
  }

  /**
   * Fall back to (or start) Web Speech — guarded against a mobile mic
   * conflict. Web Speech's own SpeechRecognition capture runs independent
   * of the MediaStream MediaRecorder already holds for the recording
   * itself; on iOS/Android it can end that mic track out from under a
   * running recording with no other signal (see RecordButton's `onended`
   * handler, which now stops the whole recording when that happens).
   * Recording must never be sacrificed for live captions, so on mobile
   * with a live mic/tab stream this surfaces an error instead of silently
   * starting Web Speech — captions become unavailable, but the recording
   * keeps going untouched.
   */
  private fallbackToWebSpeech(notifyChange: boolean): void {
    if (this.stream && hasMobileMicConflictRisk()) {
      this.config.callbacks.onError('web-speech-mobile-unavailable');
      return;
    }
    this.startWebSpeech('ko-KR');
    this.activeProvider = 'web-speech';
    if (notifyChange) this.config.onProviderChange?.('web-speech');
  }

  /**
   * Start live captions with no MediaStream — Tauri System Audio mode.
   * There is no microphone in this mode, so Web Speech (which requires
   * one) can never be a fallback here: if Transcribe Streaming isn't
   * configured or fails, this surfaces `transcribe-native-unavailable`
   * instead of silently producing no captions.
   */
  async startNative(preferredProvider: LiveSttProvider): Promise<void> {
    if (preferredProvider === 'transcribe-streaming' && this.config.transcribeStreamingConfig) {
      try {
        await this.startTranscribeStreamingNative();
        this.activeProvider = 'transcribe-streaming';
        return;
      } catch (err) {
        console.warn('Transcribe Streaming (native) failed:', err);
        // The session object was assigned BEFORE the await above — if the
        // connect failed (SDK import/credential exchange), it must not
        // survive: pushNativeChunk would keep queueing PCM into a session
        // with no consumer, ~32KB/s, ~115MB over an hour-long recording --
        // exactly the WebView memory blowup this feature exists to avoid.
        this.transcribeSession?.stop();
        this.transcribeSession = null;
      }
    }
    this.config.callbacks.onError('transcribe-native-unavailable');
  }

  /**
   * Feed one 16kHz mono 16-bit PCM chunk (from `lib/tauri.ts`'s
   * `onNativePcmChunk`) into the active Transcribe Streaming session. No-op
   * if `startNative` wasn't called or already failed.
   */
  pushNativeChunk(chunk: Uint8Array): void {
    this.transcribeSession?.pushChunk(chunk);
  }

  private lastDetectedLang = 'ko';

  private async startTranscribeStreaming(stream: MediaStream): Promise<void> {
    const tsConfig = this.config.transcribeStreamingConfig!;

    this.transcribeSession = new TranscribeStreamingSession({
      region: tsConfig.region,
      identityPoolId: tsConfig.identityPoolId,
      userPoolId: tsConfig.userPoolId,
      multiLanguage: true,
      languageOptions: 'ko-KR,en-US',
      preferredLanguage: 'ko-KR',
      vocabularyName: tsConfig.vocabularyName,
      onTranscript: (text, isFinal, detectedLang) => {
        if (detectedLang) {
          this.lastDetectedLang = detectedLang.substring(0, 2);
        }
        this.config.callbacks.onTranscript(text, isFinal);
        if (isFinal) {
          this.handleFinalTranslation(text);
        } else {
          this.handleInterimTranslation(text);
        }
      },
      onError: (error) => {
        console.error('Transcribe Streaming error, switching to Web Speech:', error);
        this.transcribeSession?.stop();
        this.transcribeSession = null;
        this.fallbackToWebSpeech(true);
      },
    });

    this.transcribeSession.start(stream).catch((err) => {
      console.error('Transcribe Streaming start failed:', err);
      this.transcribeSession?.stop();
      this.transcribeSession = null;
      this.fallbackToWebSpeech(true);
    });
  }

  private async startTranscribeStreamingNative(): Promise<void> {
    const tsConfig = this.config.transcribeStreamingConfig!;

    this.transcribeSession = new TranscribeStreamingSession({
      region: tsConfig.region,
      identityPoolId: tsConfig.identityPoolId,
      userPoolId: tsConfig.userPoolId,
      multiLanguage: true,
      languageOptions: 'ko-KR,en-US',
      preferredLanguage: 'ko-KR',
      vocabularyName: tsConfig.vocabularyName,
      onTranscript: (text, isFinal, detectedLang) => {
        if (detectedLang) {
          this.lastDetectedLang = detectedLang.substring(0, 2);
        }
        this.config.callbacks.onTranscript(text, isFinal);
        if (isFinal) {
          this.handleFinalTranslation(text);
        } else {
          this.handleInterimTranslation(text);
        }
      },
      onError: (error) => {
        console.error('Transcribe Streaming (native) error:', error);
        this.transcribeSession?.stop();
        this.transcribeSession = null;
        this.config.callbacks.onError('transcribe-native-unavailable');
      },
    });

    await this.transcribeSession.startNative();
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
      const srcLang = this.lastDetectedLang || 'ko';
      if (srcLang === this.config.targetLang) return;
      translateApi
        .translate(combined, srcLang, this.config.targetLang)
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
      const srcLang = this.lastDetectedLang || 'ko';
      if (srcLang === this.config.targetLang) return;
      translateApi
        .translate(text, srcLang, this.config.targetLang)
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
      this.startTranscribeStreaming(this.stream).catch(() => {
        this.fallbackToWebSpeech(true);
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
