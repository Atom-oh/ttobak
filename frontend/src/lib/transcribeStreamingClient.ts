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

  constructor(private config: TranscribeStreamingConfig) {}

  /** Start from a browser MediaStream (mic/tab modes). */
  async start(stream: MediaStream): Promise<void> {
    this.audioContext = new AudioContext({ sampleRate: 48000 });
    await this.audioContext.audioWorklet.addModule('/pcm-processor.js');
    const source = this.audioContext.createMediaStreamSource(stream);
    this.audioWorkletNode = new AudioWorkletNode(this.audioContext, 'pcm-processor');
    source.connect(this.audioWorkletNode);
    this.audioWorkletNode.port.onmessage = (event: MessageEvent<ArrayBuffer>) => {
      this.pushChunk(new Uint8Array(event.data));
    };

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
