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

export interface TranscribeStreamingConfig {
  region: string;
  identityPoolId: string;
  userPoolId: string;
  vocabularyName?: string;
}

export interface SttManagerConfig {
  callbacks: TranscribeCallbacks;
  targetLang: string;
  translationEnabled: boolean;
  transcribeStreamingConfig?: TranscribeStreamingConfig;
  onProviderChange?: (provider: LiveSttProvider) => void;
}

export class SttManager {
  private activeProvider: LiveSttProvider = 'web-speech';
  private transcribeSession: TranscribeStreamingSession | null = null;
  private webSpeechClient: TranscribeFallbackClient | null = null;
  private stream: MediaStream | null = null;
  private config: SttManagerConfig;
  // Recording is paused (MediaRecorder.pause()): the mic/tab track is still
  // attached but producing nothing MediaRecorder will keep. retryWithConfig
  // checks this to avoid starting a Transcribe session against a stream
  // that's momentarily not actually being recorded, and to avoid resume()
  // starting a SECOND session on top of one retryWithConfig already started
  // while paused.
  private paused = false;
  // Set once by stop() and never unset -- a fresh SttManager instance is
  // constructed per recording (useRecordingSession's createManager), so
  // this only needs to catch async work (a late Transcribe failure/success)
  // that resolves after the user already ended THIS recording. Without it,
  // fallbackToWebSpeech would still start Web Speech (grabbing the mic
  // again) after the user thinks recording has already stopped.
  private stopped = false;
  // The provider the user actually asked for at start() -- distinct from
  // `activeProvider` (what's running right now, which can differ after a
  // fallback). The provider selector is disabled for the whole recording
  // once it's started, so this can't go stale mid-session. Both
  // `resume()`'s promotion branch and `retryWithConfig` gate on this: an
  // explicit 'web-speech' choice must never get silently upgraded to
  // Transcribe Streaming just because a config happens to be available by
  // the time the user pauses/resumes.
  private preferredProvider: LiveSttProvider = 'web-speech';

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
    this.preferredProvider = preferredProvider;

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
   * mode has no fallback to recover from in the first place, and stop()
   * always clears `stream` so this can't fire after the recording ended).
   */
  retryWithConfig(config: TranscribeStreamingConfig | undefined): void {
    // stop() already clears `stream` (guarded by the check below), making
    // this redundant in practice -- kept as cost-0 defense-in-depth in
    // case that invariant ever changes.
    if (this.stopped) return;
    if (!config || this.preferredProvider !== 'transcribe-streaming') return;
    if (this.activeProvider === 'transcribe-streaming' || !this.stream) return;
    this.config.transcribeStreamingConfig = config;
    // Paused: no audio is being recorded right now, and resume()'s own
    // transcribe-streaming branch has no idea a session was already
    // started here -- it would start a SECOND one on top of this one,
    // duplicating captions and leaking the first session's WebSocket.
    // Just persist the config and let resume() (below) pick up the
    // promotion as its one and only session start.
    if (this.paused) return;
    this.transcribeSession?.stop();
    this.transcribeSession = null;
    this.webSpeechClient?.stop();
    this.webSpeechClient = null;
    // startTranscribeStreaming's own body never rejects (its internal
    // TranscribeStreamingSession.start().catch() already routes failures
    // to fallbackToWebSpeech) -- this .catch only guards the rarer case
    // of the synchronous session construction itself throwing.
    this.startTranscribeStreaming(this.stream).catch(() => this.fallbackToWebSpeech(true));
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
   *
   * Also guards against two timing issues around async Transcribe
   * Streaming failures, which can resolve well after they were triggered:
   * a `stop()`-after-this-call is a no-op (a late failure must not start
   * Web Speech, and re-grab the mic, after the user already ended the
   * recording), and the mobile-guard branch resets `activeProvider` away
   * from 'transcribe-streaming' so a later `retryWithConfig`/`resume()`
   * isn't permanently blocked by its own optimistic state from the
   * attempt that just failed.
   */
  private fallbackToWebSpeech(notifyChange: boolean): void {
    if (this.stopped) return;
    // A Transcribe Streaming failure can resolve after pause() already
    // ran (pause() stops the session but the in-flight start/connect
    // attempt it was racing isn't cancelled). Starting Web Speech now
    // would open the mic and produce captions while the user believes
    // recording is paused. No-op instead -- resume() already restarts
    // Transcribe Streaming fresh when active, and its own failure path
    // reaches this same function unpaused if that retry also fails.
    if (this.paused) return;
    if (this.stream && hasMobileMicConflictRisk()) {
      this.activeProvider = 'web-speech';
      if (notifyChange) this.config.onProviderChange?.('web-speech');
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

    // Captured by this attempt's own callbacks below so they can tell
    // whether they're still the CURRENT session before touching shared
    // state. `start()` doesn't await this method (fire-and-forget, so the
    // caller isn't blocked for the session's whole lifetime), so a second
    // attempt -- from pause()/resume() or retryWithConfig() -- can
    // already be running as `this.transcribeSession` by the time this
    // attempt's connect/stream error resolves. Without this check, that
    // late failure would stop and null out the NEW, healthy session and
    // fall back to Web Speech out from under it.
    const session = new TranscribeStreamingSession({
      region: tsConfig.region,
      identityPoolId: tsConfig.identityPoolId,
      userPoolId: tsConfig.userPoolId,
      multiLanguage: true,
      languageOptions: 'ko-KR,en-US',
      preferredLanguage: 'ko-KR',
      vocabularyName: tsConfig.vocabularyName,
      onTranscript: (text, isFinal, detectedLang) => {
        if (this.transcribeSession !== session) return;
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
        if (this.transcribeSession !== session) return;
        console.error('Transcribe Streaming error, switching to Web Speech:', error);
        session.stop();
        this.transcribeSession = null;
        this.fallbackToWebSpeech(true);
      },
    });
    this.transcribeSession = session;

    session.start(stream).catch((err) => {
      if (this.transcribeSession !== session) return;
      console.error('Transcribe Streaming start failed:', err);
      session.stop();
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
    this.paused = true;
    if (this.activeProvider === 'transcribe-streaming') {
      // Transcribe Streaming doesn't support pause — stop and restart on resume
      this.transcribeSession?.stop();
    } else {
      this.webSpeechClient?.pause();
    }
  }

  resume(): void {
    if (this.stopped) return;
    this.paused = false;
    // A config arrived while paused (see retryWithConfig, which persists
    // it but defers starting anything until now to avoid a duplicate
    // session) -- promote in the SAME single restart resume() already
    // does for an active Transcribe Streaming session, rather than
    // resuming whatever fallback was active before the pause. Gated on
    // `preferredProvider`, NOT just "is a config available" -- a user who
    // explicitly chose 'web-speech' (the provider selector is disabled for
    // the rest of the recording once it's started, so this can't have
    // changed since) must stay on it even though `transcribeStreamingConfig`
    // is unconditionally present in `config` the moment it loads. Without
    // this check, ANY pause/resume on an explicit Web Speech session would
    // get silently upgraded to Transcribe Streaming as soon as the config
    // fetch resolved -- sending audio to AWS against the user's choice.
    if (
      this.preferredProvider === 'transcribe-streaming' &&
      this.activeProvider !== 'transcribe-streaming' &&
      this.config.transcribeStreamingConfig &&
      this.stream
    ) {
      this.webSpeechClient?.stop();
      this.webSpeechClient = null;
      this.startTranscribeStreaming(this.stream).catch(() => this.fallbackToWebSpeech(true));
      this.activeProvider = 'transcribe-streaming';
      this.config.onProviderChange?.('transcribe-streaming');
      return;
    }
    if (this.activeProvider === 'transcribe-streaming' && this.stream) {
      this.startTranscribeStreaming(this.stream).catch(() => {
        this.fallbackToWebSpeech(true);
      });
    } else {
      this.webSpeechClient?.resume();
    }
  }

  stop(): void {
    this.stopped = true;
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
