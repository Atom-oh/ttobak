/**
 * AudioWorklet processor for PCM resampling.
 * Takes microphone input (typically 44.1kHz or 48kHz) and resamples to 16kHz 16-bit PCM
 * for both Transcribe Streaming and faster-whisper.
 */
class AudioProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    // Target: 16kHz 16-bit mono PCM
    this._targetSampleRate = 16000;
  }

  process(inputs, outputs, parameters) {
    const input = inputs[0];
    if (!input || !input[0]) return true;

    const channelData = input[0]; // mono channel
    const sourceSampleRate = sampleRate; // global in AudioWorklet scope

    // Simple linear resampling to 16kHz
    const ratio = sourceSampleRate / this._targetSampleRate;
    const targetLength = Math.floor(channelData.length / ratio);
    const resampled = new Float32Array(targetLength);

    for (let i = 0; i < targetLength; i++) {
      const srcIndex = i * ratio;
      const srcIndexFloor = Math.floor(srcIndex);
      const srcIndexCeil = Math.min(srcIndexFloor + 1, channelData.length - 1);
      const frac = srcIndex - srcIndexFloor;
      resampled[i] = channelData[srcIndexFloor] * (1 - frac) + channelData[srcIndexCeil] * frac;
    }

    // Convert float32 [-1, 1] to int16 PCM bytes
    const pcm = new Int16Array(resampled.length);
    for (let i = 0; i < resampled.length; i++) {
      const s = Math.max(-1, Math.min(1, resampled[i]));
      pcm[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
    }

    // Send PCM bytes to main thread
    this.port.postMessage(pcm.buffer, [pcm.buffer]);

    return true;
  }
}

registerProcessor('audio-processor', AudioProcessor);
