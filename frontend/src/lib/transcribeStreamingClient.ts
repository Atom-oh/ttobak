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
  languageCode: string;
  onTranscript: (text: string, isFinal: boolean) => void;
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

  // Queue for bridging AudioWorklet messages → async iterable
  private audioQueue: Array<Uint8Array> = [];
  private audioResolve: ((value: IteratorResult<AudioChunkMessage>) => void) | null = null;
  private audioDone = false;

  constructor(private config: TranscribeStreamingConfig) {}

  async start(stream: MediaStream): Promise<void> {
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

    // Set up AudioWorklet for PCM conversion
    this.audioContext = new AudioContext({ sampleRate: 48000 });
    await this.audioContext.audioWorklet.addModule('/pcm-processor.js');
    const source = this.audioContext.createMediaStreamSource(stream);
    this.audioWorkletNode = new AudioWorkletNode(this.audioContext, 'pcm-processor');
    source.connect(this.audioWorkletNode);

    // Bridge AudioWorklet messages → audio queue
    this.audioQueue = [];
    this.audioResolve = null;
    this.audioDone = false;

    this.audioWorkletNode.port.onmessage = (event: MessageEvent<ArrayBuffer>) => {
      const chunk = new Uint8Array(event.data);
      if (this.audioResolve) {
        const resolve = this.audioResolve;
        this.audioResolve = null;
        resolve({ value: { AudioEvent: { AudioChunk: chunk } }, done: false });
      } else {
        this.audioQueue.push(chunk);
      }
    };

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
      const command = new StartStreamTranscriptionCommand({
        LanguageCode: this.config.languageCode as StartStreamTranscriptionCommandInput['LanguageCode'],
        MediaEncoding: 'pcm',
        MediaSampleRateHertz: 16000,
        AudioStream: audioStream as AsyncIterable<AudioStream>,
      });

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
              this.config.onTranscript(text, !result.IsPartial);
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

    // Disconnect AudioWorklet
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
