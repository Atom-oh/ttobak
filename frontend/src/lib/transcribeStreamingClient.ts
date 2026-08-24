'use client';

/**
 * Browser-direct Amazon Transcribe Streaming client.
 *
 * Uses Cognito Identity Pool for temporary AWS credentials,
 * AudioWorklet for real-time PCM conversion, and the AWS SDK
 * TranscribeStreamingClient for WebSocket-based streaming STT.
 *
 * This bypasses browser Web Speech API limitations (Chrome 5-min limit,
 * tab visibility kills, network flakiness) by connecting directly to
 * Amazon Transcribe's WebSocket endpoint.
 *
 * Two ways to feed it audio:
 * - `start(stream)`: browser mic/tab modes — sets up an AudioWorklet that
 *   downsamples the MediaStream to 16kHz mono PCM.
 * - `startNative()`: Tauri System Audio mode, where there is no
 *   MediaStream at all (capture happens in Rust via ScreenCaptureKit).
 *   Chunks are pushed in externally via `pushChunk` — see
 *   `useRecordingSession`'s native path, fed by `lib/tauri.ts`'s
 *   `onNativePcmChunk`. Rust downsamples to the same 16kHz mono format the
 *   AudioWorklet produces, so both paths feed this class identically from
 *   here on.
 */

import type {
  TranscribeStreamingClient as TSClient,
  StartStreamTranscriptionCommandInput,
  AudioStream,
} from '@aws-sdk/client-transcribe-streaming';
import { getIdToken, refreshSession } from '@/lib/auth';

export interface TranscribeStreamingConfig {
  region: string;
  identityPoolId: string;
  userPoolId: string;
  languageCode?: string;
  multiLanguage?: boolean;
  languageOptions?: string;
  preferredLanguage?: string;
  vocabularyName?: string;
  onTranscript: (text: string, isFinal: boolean, languageCode?: string) => void;
  onError: (error: string) => void;
}

interface AudioChunkMessage {
  AudioEvent: { AudioChunk: Uint8Array };
}

export class TranscribeStreamingSession {
  private client: TSClient | null = null;
  private audioWorkletNode: AudioWorkletNode | null = null;
  private audioContext: AudioContext | null = null;
  private isActive = false;
  private abortController: AbortController | null = null;

  // Queue for bridging PCM chunks (from either the AudioWorklet or an
  // external pushChunk() caller) → async iterable.
  private audioQueue: Array<Uint8Array> = [];
  private audioResolve: ((value: IteratorResult<AudioChunkMessage>) => void) | null = null;
  private audioDone = false;

  // Stall watchdog for the browser MediaStream path only (startNative() has
  // no AudioContext to suspend — Tauri feeds pushChunk directly from Rust).
  // Mobile OSes suspend a page's AudioContext on screen lock/background to
  // save power; when that happens the AudioWorklet stops calling process(),
  // pushChunk() stops firing, and this class's async iterator blocks
  // forever with no error — the mic indicator/track stays "live" the whole
  // time, so nothing else in the recording UI notices. This timer is the
  // only thing that detects that silence and surfaces it.
  private lastChunkAt = 0;
  private stallReported = false;
  private watchdogTimer: ReturnType<typeof setInterval> | null = null;
  private notRunningTicks = 0;
  private readonly STALL_TIMEOUT_MS = 15_000;
  private readonly STALL_CHECK_INTERVAL_MS = 5_000;
  // Caps the "AudioContext not running yet" resume grace (see the tick
  // logic below) to 2 checks (~10s) -- resume() can keep failing
  // indefinitely (e.g. iOS Safari refusing resume() without a fresh user
  // gesture after an unlock), and an unbounded grace would silence the
  // stall watchdog for as long as that persists, permanently blocking the
  // Web Speech fallback this class exists to trigger.
  private readonly MAX_NOT_RUNNING_GRACE_TICKS = 2;

  constructor(private config: TranscribeStreamingConfig) {}

  /** Start from a browser MediaStream (mic/tab modes). */
  async start(stream: MediaStream): Promise<void> {
    // No sampleRate override: pcm-processor.js downsamples from whatever
    // the worklet's global `sampleRate` actually is to 16kHz, so forcing
    // 48000 here is unnecessary -- and on iOS Safari it can make context
    // creation fail outright when the current audio route's hardware rate
    // differs (e.g. many Bluetooth headsets negotiate 16/24kHz), especially
    // with a second AudioContext already open for the RecordButton waveform.
    this.audioContext = new AudioContext();
    // Mirrors RecordButton's tryResumeAudioContext: iOS/mobile browsers can
    // suspend this context (screen lock, background) independently of the
    // waveform's own AudioContext, since they're two separate instances.
    const tryResumeAudioContext = () => {
      if (this.audioContext && this.audioContext.state === 'suspended' && this.isActive) {
        this.audioContext.resume().catch((err) => {
          console.warn('Transcribe Streaming: AudioContext resume failed:', err);
        });
      }
    };
    this.audioContext.onstatechange = tryResumeAudioContext;
    await this.audioContext.audioWorklet.addModule('/pcm-processor.js');
    const source = this.audioContext.createMediaStreamSource(stream);
    this.audioWorkletNode = new AudioWorkletNode(this.audioContext, 'pcm-processor');
    source.connect(this.audioWorkletNode);
    this.audioWorkletNode.port.onmessage = (event: MessageEvent<ArrayBuffer>) => {
      this.pushChunk(new Uint8Array(event.data));
    };

    this.lastChunkAt = Date.now();
    this.stallReported = false;
    this.notRunningTicks = 0;
    this.watchdogTimer = setInterval(() => {
      if (!this.isActive) return;
      tryResumeAudioContext();
      if (this.audioContext && this.audioContext.state !== 'running') {
        // resume() above is async and hasn't necessarily landed yet (e.g.
        // right after an unlock, where lastChunkAt is still stale from
        // before the suspend) -- without this, the very next tick can see
        // silentMs already past STALL_TIMEOUT_MS and report a stall before
        // resume() ever gets a chance to actually restart the chunk flow,
        // which defeats the resume path entirely. Treat "not running yet"
        // as one more grace interval rather than accumulated silence, but
        // only for a bounded number of ticks -- see
        // MAX_NOT_RUNNING_GRACE_TICKS's doc comment for why this can't be
        // unbounded.
        if (this.notRunningTicks < this.MAX_NOT_RUNNING_GRACE_TICKS) {
          this.notRunningTicks++;
          this.lastChunkAt = Date.now();
          return;
        }
      } else {
        this.notRunningTicks = 0;
      }
      const silentMs = Date.now() - this.lastChunkAt;
      if (silentMs > this.STALL_TIMEOUT_MS) {
        if (!this.stallReported) {
          this.stallReported = true;
          console.warn(`Transcribe Streaming: no audio chunks for ${silentMs}ms — audio pipeline likely suspended`);
          this.config.onError('transcribe-stream-stalled');
        }
      } else {
        this.stallReported = false;
      }
    }, this.STALL_CHECK_INTERVAL_MS);

    await this.connectAndTranscribe();
  }

  /**
   * Start with no MediaStream — Tauri System Audio mode. Audio arrives
   * exclusively via `pushChunk`, called by the caller as
   * `native-pcm-chunk` events come in from Rust.
   */
  async startNative(): Promise<void> {
    await this.connectAndTranscribe();
  }

  /**
   * Feed one 16kHz mono 16-bit PCM chunk into the transcription stream.
   * The `start(stream)` AudioWorklet bridge calls this internally; for
   * `startNative()` this is the ONLY source of audio, so callers must call
   * it directly for every chunk they receive.
   */
  pushChunk(chunk: Uint8Array): void {
    this.lastChunkAt = Date.now();
    if (this.audioResolve) {
      const resolve = this.audioResolve;
      this.audioResolve = null;
      resolve({ value: { AudioEvent: { AudioChunk: chunk } }, done: false });
    } else {
      this.audioQueue.push(chunk);
    }
  }

  private async connectAndTranscribe(): Promise<void> {
    // Reset the queue BEFORE any await: chunks produced while the SDK
    // import/credential exchange below is in flight (native PCM starts
    // flowing as soon as capture does) must be queued and sent, not wiped
    // by a post-await reset — that used to drop the first utterance.
    this.audioQueue = [];
    this.audioResolve = null;
    this.audioDone = false;

    // Dynamically import SDK to avoid bundling when not used
    const [{ TranscribeStreamingClient, StartStreamTranscriptionCommand }, { fromCognitoIdentityPool }] =
      await Promise.all([
        import('@aws-sdk/client-transcribe-streaming'),
        import('@aws-sdk/credential-providers'),
      ]);

    // Get fresh ID token for credential exchange
    let idToken = getIdToken();
    if (!idToken) {
      idToken = await refreshSession();
    }
    if (!idToken) {
      this.config.onError('transcribe-auth-failed');
      return;
    }

    const providerName = `cognito-idp.${this.config.region}.amazonaws.com/${this.config.userPoolId}`;

    this.client = new TranscribeStreamingClient({
      region: this.config.region,
      credentials: fromCognitoIdentityPool({
        identityPoolId: this.config.identityPoolId,
        logins: { [providerName]: idToken },
        clientConfig: { region: this.config.region },
      }),
    });

    // Create async iterable for the SDK
    const audioStream: AsyncIterable<AudioChunkMessage> = {
      [Symbol.asyncIterator]: () => ({
        next: (): Promise<IteratorResult<AudioChunkMessage>> => {
          if (this.audioDone) {
            return Promise.resolve({ value: undefined as unknown as AudioChunkMessage, done: true });
          }
          if (this.audioQueue.length > 0) {
            const chunk = this.audioQueue.shift()!;
            return Promise.resolve({
              value: { AudioEvent: { AudioChunk: chunk } },
              done: false,
            });
          }
          return new Promise((resolve) => {
            this.audioResolve = resolve;
          });
        },
      }),
    };

    this.isActive = true;
    this.abortController = new AbortController();

    try {
      const commandInput: StartStreamTranscriptionCommandInput = {
        MediaEncoding: 'pcm',
        MediaSampleRateHertz: 16000,
        AudioStream: audioStream as AsyncIterable<AudioStream>,
      };

      if (this.config.multiLanguage) {
        commandInput.IdentifyMultipleLanguages = true;
        commandInput.LanguageOptions = this.config.languageOptions || 'ko-KR,en-US';
        commandInput.PreferredLanguage = (this.config.preferredLanguage || 'ko-KR') as StartStreamTranscriptionCommandInput['PreferredLanguage'];
      } else {
        commandInput.LanguageCode = (this.config.languageCode || 'ko-KR') as StartStreamTranscriptionCommandInput['LanguageCode'];
      }

      if (this.config.vocabularyName) {
        commandInput.VocabularyName = this.config.vocabularyName;
      }

      const command = new StartStreamTranscriptionCommand(commandInput);

      const response = await this.client.send(command, {
        abortSignal: this.abortController.signal,
      });

      if (!response.TranscriptResultStream) {
        this.config.onError('transcribe-no-stream');
        return;
      }

      for await (const event of response.TranscriptResultStream) {
        if (!this.isActive) break;
        if (event.TranscriptEvent?.Transcript?.Results) {
          for (const result of event.TranscriptEvent.Transcript.Results) {
            const text = result.Alternatives?.[0]?.Transcript || '';
            if (text) {
              const lang = (result as Record<string, unknown>).LanguageCode as string | undefined;
              this.config.onTranscript(text, !result.IsPartial, lang);
            }
          }
        }
      }
    } catch (err) {
      if (this.isActive) {
        console.error('Transcribe Streaming error:', err);
        this.config.onError('transcribe-stream-error');
      }
    }
  }

  stop(): void {
    this.isActive = false;
    this.audioDone = true;

    if (this.watchdogTimer) {
      clearInterval(this.watchdogTimer);
      this.watchdogTimer = null;
    }

    // Resolve any pending audio queue read
    if (this.audioResolve) {
      this.audioResolve({ value: undefined as unknown as AudioChunkMessage, done: true });
      this.audioResolve = null;
    }

    // Abort the streaming request
    this.abortController?.abort();
    this.abortController = null;

    // Disconnect AudioWorklet (no-op if this session was started via
    // startNative(), which never sets these)
    if (this.audioWorkletNode) {
      this.audioWorkletNode.port.onmessage = null;
      this.audioWorkletNode.disconnect();
      this.audioWorkletNode = null;
    }

    // Close AudioContext
    if (this.audioContext) {
      this.audioContext.close().catch(() => {});
      this.audioContext = null;
    }

    this.client = null;
    this.audioQueue = [];
  }
}
