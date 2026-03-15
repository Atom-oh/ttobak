/**
 * WebSocket client for ECS faster-whisper real-time transcription.
 * Captures audio via AudioWorklet, resamples to 16kHz PCM, and streams to the server.
 */

export interface RealtimeCallbacks {
  onTranscript: (text: string, isFinal: boolean) => void;
  onTranslation: (original: string, translated: string, targetLang: string) => void;
  onQuestion: (questions: string[]) => void;
  onError: (error: string) => void;
  onDisconnect: () => void;
}

export class RealtimeClient {
  private ws: WebSocket | null = null;
  private audioContext: AudioContext | null = null;
  private workletNode: AudioWorkletNode | null = null;
  private sourceNode: MediaStreamAudioSourceNode | null = null;
  private silentGain: GainNode | null = null;
  private callbacks: RealtimeCallbacks;

  constructor(callbacks: RealtimeCallbacks) {
    this.callbacks = callbacks;
  }

  async connect(
    websocketUrl: string,
    stream: MediaStream,
    sourceLang = 'ko',
    targetLang = 'en'
  ): Promise<void> {
    // 1. Connect WebSocket
    this.ws = new WebSocket(websocketUrl);

    await new Promise<void>((resolve, reject) => {
      this.ws!.onopen = () => resolve();
      this.ws!.onerror = () => reject(new Error('WebSocket connection failed'));
    });

    // 2. Set up message handler
    this.ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        switch (msg.type) {
          case 'transcript':
            this.callbacks.onTranscript(msg.text, msg.isFinal ?? true);
            break;
          case 'translation':
            this.callbacks.onTranslation(msg.original, msg.translated, msg.targetLang);
            break;
          case 'question':
            this.callbacks.onQuestion(msg.questions);
            break;
          case 'error':
            this.callbacks.onError(msg.error || 'Unknown server error');
            break;
        }
      } catch (err) {
        console.error('Failed to parse WebSocket message:', err);
      }
    };

    this.ws.onclose = () => {
      this.callbacks.onDisconnect();
    };

    // 3. Send config
    this.ws.send(
      JSON.stringify({
        action: 'config',
        language: sourceLang,
        targetLang: targetLang,
      })
    );

    // 4. Set up AudioWorklet to capture PCM from the stream
    this.audioContext = new AudioContext({ sampleRate: 48000 });
    await this.audioContext.audioWorklet.addModule('/audio-processor.js');
    this.sourceNode = this.audioContext.createMediaStreamSource(stream);
    this.workletNode = new AudioWorkletNode(this.audioContext, 'audio-processor');

    this.workletNode.port.onmessage = (event) => {
      // Send PCM binary data via WebSocket
      if (this.ws?.readyState === WebSocket.OPEN) {
        this.ws.send(event.data);
      }
    };

    // Connect source -> worklet -> silent gain -> destination
    // AudioWorkletNode processes audio even without connecting to destination,
    // but some browsers require it - connect to a silent gain node
    this.sourceNode.connect(this.workletNode);
    this.silentGain = this.audioContext.createGain();
    this.silentGain.gain.value = 0;
    this.workletNode.connect(this.silentGain);
    this.silentGain.connect(this.audioContext.destination);
  }

  updateConfig(sourceLang: string, targetLang: string): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(
        JSON.stringify({
          action: 'config',
          language: sourceLang,
          targetLang: targetLang,
        })
      );
    }
  }

  disconnect(): void {
    // Send stop command
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ action: 'stop' }));
      this.ws.close();
    }
    this.ws = null;

    // Cleanup audio
    this.workletNode?.disconnect();
    this.sourceNode?.disconnect();
    this.silentGain?.disconnect();
    this.audioContext?.close().catch(() => {});
    this.workletNode = null;
    this.sourceNode = null;
    this.silentGain = null;
    this.audioContext = null;
  }

  get isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }
}
