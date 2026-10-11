import { BrowserSpeechRecognition } from './speechRecognition';
import type { TranslationEntry } from './liveTranslation';

export interface TranscribeCallbacks {
  onTranscript: (text: string, isFinal: boolean) => void;
  onTranslation: (entry: TranslationEntry) => void;
  onTranslationReset?: () => void;
  onError: (error: string) => void;
}

export class TranscribeFallbackClient {
  private speech: BrowserSpeechRecognition | null = null;

  constructor(private callbacks: Pick<TranscribeCallbacks, 'onTranscript' | 'onError'>) {}

  start(sourceLang = 'ko-KR'): boolean {
    if (!BrowserSpeechRecognition.isSupported()) {
      this.callbacks.onError('Speech recognition not supported in this browser');
      return false;
    }

    this.speech = new BrowserSpeechRecognition(sourceLang);
    return this.speech.start(
      (result) => this.callbacks.onTranscript(result.text, result.isFinal),
      (error) => this.callbacks.onError(error),
    );
  }

  getPendingInterim(): string | null {
    return this.speech?.getPendingInterim() ?? null;
  }

  pause(): void {
    this.speech?.pause();
  }

  resume(): void {
    this.speech?.resume();
  }

  stop(): void {
    this.speech?.stop();
    this.speech = null;
  }
}
