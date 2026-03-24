/**
 * WebSocket client for ECS faster-whisper real-time transcription.
 * Captures audio via AudioWorklet, resamples to 16kHz PCM, and streams to the server.
 * Includes client-side VAD (Voice Activity Detection) to filter silence and reduce bandwidth.
 */

export interface RealtimeCallbacks {
  onTranscript: (text: string, isFinal: boolean) => void;
  onTranslation: (original: string, translated: string, targetLang: string, isFinal: boolean) => void;
  onQuestion: (questions: string[]) => void;
  onError: (error: string) => void;
  onDisconnect: () => void;
  onAudioSaved?: (key: string) => void;
  onVadStatus?: (isSpeaking: boolean) => void;
}

export interface VadConfig {
  enabled: boolean;
  threshold: number;      // RMS threshold for speech detection (default: 0.01)
  hangoverFrames: number; // Frames to keep sending after speech ends (default: 6 = ~300ms)
  preBufferFrames: number; // Frames to buffer before speech (default: 2 = ~100ms)
}

export interface VadStats {
  totalChunks: number;
  sentChunks: number;
  savedPercent: number;
  isSpeaking: boolean;
}

/**
 * Simple energy-based VAD using RMS (Root Mean Square) of audio samples.
 * Returns true if the audio chunk contains voice activity.
 */
function isVoiceActive(pcmData: Int16Array, threshold: number): boolean {
  if (pcmData.length === 0) return false;

  // Calculate RMS energy (normalized to [-1, 1] range)
  let sum = 0;
  for (let i = 0; i < pcmData.length; i++) {
    const normalized = pcmData[i] / 32768; // Convert int16 to float
    sum += normalized * normalized;
  }
  const rms = Math.sqrt(sum / pcmData.length);
  return rms > threshold;
}

export class RealtimeClient {
  private ws: WebSocket | null = null;
  private audioContext: AudioContext | null = null;
  private workletNode: AudioWorkletNode | null = null;
  private sourceNode: MediaStreamAudioSourceNode | null = null;
  private silentGain: GainNode | null = null;
  private callbacks: RealtimeCallbacks;

  // VAD state
  private vadConfig: VadConfig = {
    enabled: true,
    threshold: 0.01,
    hangoverFrames: 6,   // ~300ms at 50ms per frame
    preBufferFrames: 2,  // ~100ms
  };
  private vadState = {
    isSpeaking: false,
    silenceFrames: 0,
    preBuffer: [] as ArrayBuffer[],
  };
  private vadStats = {
    totalChunks: 0,
    sentChunks: 0,
  };

  constructor(callbacks: RealtimeCallbacks) {
    this.callbacks = callbacks;
  }

  /**
   * Configure VAD settings. Call before connect() to take effect.
   */
  setVadConfig(config: Partial<VadConfig>): void {
    this.vadConfig = { ...this.vadConfig, ...config };
  }

  /**
   * Get current VAD statistics.
   */
  getVadStats(): VadStats {
    const savedPercent = this.vadStats.totalChunks > 0
      ? Math.round((1 - this.vadStats.sentChunks / this.vadStats.totalChunks) * 100)
      : 0;
    return {
      ...this.vadStats,
      savedPercent,
      isSpeaking: this.vadState.isSpeaking,
    };
  }

  /**
   * Process audio chunk through VAD filter.
   * Returns true if chunk was sent, false if filtered (silence).
   */
  private processAudioWithVad(chunk: ArrayBuffer): boolean {
    this.vadStats.totalChunks++;

    // VAD disabled - send everything
    if (!this.vadConfig.enabled) {
      this.ws?.send(chunk);
      this.vadStats.sentChunks++;
      return true;
    }

    const pcm = new Int16Array(chunk);
    const isActive = isVoiceActive(pcm, this.vadConfig.threshold);

    if (isActive) {
      // Speech detected
      if (!this.vadState.isSpeaking) {
        // Speech just started - send pre-buffer first (captures speech onset)
        for (const buf of this.vadState.preBuffer) {
          if (this.ws?.readyState === WebSocket.OPEN) {
            this.ws.send(buf);
            this.vadStats.sentChunks++;
          }
        }
        this.vadState.preBuffer = [];
        this.vadState.isSpeaking = true;
        this.callbacks.onVadStatus?.(true);
      }
      this.vadState.silenceFrames = 0;
      if (this.ws?.readyState === WebSocket.OPEN) {
        this.ws.send(chunk);
        this.vadStats.sentChunks++;
      }
      return true;
    } else {
      // Silence detected
      this.vadState.silenceFrames++;

      if (this.vadState.isSpeaking && this.vadState.silenceFrames <= this.vadConfig.hangoverFrames) {
        // Still in hangover period - keep sending
        if (this.ws?.readyState === WebSocket.OPEN) {
          this.ws.send(chunk);
          this.vadStats.sentChunks++;
        }
        return true;
      }

      // End of speech or continued silence
      if (this.vadState.isSpeaking) {
        this.vadState.isSpeaking = false;
        this.callbacks.onVadStatus?.(false);
      }

      // Keep in pre-buffer (circular, limited to preBufferFrames)
      this.vadState.preBuffer.push(chunk);
      if (this.vadState.preBuffer.length > this.vadConfig.preBufferFrames) {
        this.vadState.preBuffer.shift();
      }
      return false;
    }
  }

  async connect(
    websocketUrl: string,
    stream: MediaStream,
    sourceLang = 'ko',
    targetLang = 'en',
    userId?: string,
    meetingId?: string,
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
            this.callbacks.onTranslation(msg.original, msg.translated, msg.targetLang, msg.isFinal ?? true);
            break;
          case 'question':
            this.callbacks.onQuestion(msg.questions);
            break;
          case 'error':
            this.callbacks.onError(msg.error || 'Unknown server error');
            break;
          case 'audio_saved':
            this.callbacks.onAudioSaved?.(msg.key);
            break;
        }
      } catch (err) {
        console.error('Failed to parse WebSocket message:', err);
      }
    };

    this.ws.onclose = () => {
      this.callbacks.onDisconnect();
    };

    // 3. Send config (include IDs for backend audio aggregation)
    this.ws.send(
      JSON.stringify({
        action: 'config',
        language: sourceLang,
        targetLang: targetLang,
        ...(userId && { userId }),
        ...(meetingId && { meetingId }),
      })
    );

    // 4. Set up AudioWorklet to capture PCM from the stream
    this.audioContext = new AudioContext({ sampleRate: 48000 });
    await this.audioContext.audioWorklet.addModule('/audio-processor.js');
    this.sourceNode = this.audioContext.createMediaStreamSource(stream);
    this.workletNode = new AudioWorkletNode(this.audioContext, 'audio-processor');

    this.workletNode.port.onmessage = (event) => {
      // Process through VAD filter before sending
      this.processAudioWithVad(event.data);
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

    // Reset VAD state
    this.vadState = {
      isSpeaking: false,
      silenceFrames: 0,
      preBuffer: [],
    };
    this.vadStats = {
      totalChunks: 0,
      sentChunks: 0,
    };
  }

  get isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }
}
