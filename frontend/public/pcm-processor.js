/**
 * AudioWorklet processor that converts browser audio (48kHz Float32)
 * to 16kHz 16-bit PCM for Amazon Transcribe Streaming.
 *
 * Runs on the audio rendering thread — no main-thread blocking.
 * Loaded via: audioContext.audioWorklet.addModule('/pcm-processor.js')
 */
class PCMProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this._buffer = [];
    this._targetSampleRate = 16000;
  }

  process(inputs) {
    const input = inputs[0];
    if (!input || input.length === 0) return true;

    const channelData = input[0]; // mono (first channel)
    if (!channelData || channelData.length === 0) return true;

    // Downsample from AudioContext sampleRate to 16kHz using linear interpolation
    const ratio = sampleRate / this._targetSampleRate;
    const outputLength = Math.floor(channelData.length / ratio);

    for (let i = 0; i < outputLength; i++) {
      const srcIndex = i * ratio;
      const srcFloor = Math.floor(srcIndex);
      const srcCeil = Math.min(srcFloor + 1, channelData.length - 1);
      const fraction = srcIndex - srcFloor;

      // Linear interpolation between adjacent samples
      const sample = channelData[srcFloor] * (1 - fraction) + channelData[srcCeil] * fraction;
      this._buffer.push(sample);
    }

    // Send in chunks of 1024 samples (~64ms at 16kHz) for efficient streaming
    while (this._buffer.length >= 1024) {
      const chunk = this._buffer.splice(0, 1024);
      const pcm = new Int16Array(chunk.length);
      for (let i = 0; i < chunk.length; i++) {
        const clamped = Math.max(-1, Math.min(1, chunk[i]));
        pcm[i] = clamped < 0 ? clamped * 0x8000 : clamped * 0x7FFF;
      }
      this.port.postMessage(pcm.buffer, [pcm.buffer]);
    }

    return true;
  }
}

registerProcessor('pcm-processor', PCMProcessor);
