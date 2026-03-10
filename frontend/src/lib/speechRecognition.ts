/**
 * Browser-native Speech Recognition wrapper for real-time transcription.
 * Uses Web Speech API (SpeechRecognition / webkitSpeechRecognition).
 * Good Korean support in Chrome/Edge. Falls back gracefully if unsupported.
 */

// Web Speech API type declarations (not in default TS lib)
interface SpeechRecognitionResult {
  readonly length: number;
  item(index: number): SpeechRecognitionAlternative;
  [index: number]: SpeechRecognitionAlternative;
  readonly isFinal: boolean;
}

interface SpeechRecognitionAlternative {
  readonly transcript: string;
  readonly confidence: number;
}

interface SpeechRecognitionResultList {
  readonly length: number;
  item(index: number): SpeechRecognitionResult;
  [index: number]: SpeechRecognitionResult;
}

interface SpeechRecognitionEvent extends Event {
  readonly resultIndex: number;
  readonly results: SpeechRecognitionResultList;
}

interface SpeechRecognitionErrorEvent extends Event {
  readonly error: string;
}

interface SpeechRecognitionInstance extends EventTarget {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((event: SpeechRecognitionEvent) => void) | null;
  onerror: ((event: SpeechRecognitionErrorEvent) => void) | null;
  onend: (() => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
}

interface SpeechRecognitionConstructor {
  new (): SpeechRecognitionInstance;
}

declare global {
  interface Window {
    SpeechRecognition?: SpeechRecognitionConstructor;
    webkitSpeechRecognition?: SpeechRecognitionConstructor;
  }
}

export interface SpeechResult {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

type SpeechCallback = (result: SpeechResult) => void;

export class BrowserSpeechRecognition {
  private recognition: SpeechRecognitionInstance | null = null;
  private isListening = false;
  private shouldRestart = false;
  private onResult: SpeechCallback | null = null;
  private lang: string;

  constructor(lang = 'ko-KR') {
    this.lang = lang;
  }

  static isSupported(): boolean {
    return !!(
      typeof window !== 'undefined' &&
      (window.SpeechRecognition || window.webkitSpeechRecognition)
    );
  }

  start(onResult: SpeechCallback): boolean {
    if (!BrowserSpeechRecognition.isSupported()) return false;

    const Ctor = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (!Ctor) return false;

    this.recognition = new Ctor();
    this.recognition.continuous = true;
    this.recognition.interimResults = true;
    this.recognition.lang = this.lang;

    this.onResult = onResult;
    this.shouldRestart = true;
    this.isListening = true;

    this.recognition.onresult = (event: SpeechRecognitionEvent) => {
      for (let i = event.resultIndex; i < event.results.length; i++) {
        const result = event.results[i];
        this.onResult?.({
          text: result[0].transcript,
          isFinal: result.isFinal,
          timestamp: new Date().toISOString(),
        });
      }
    };

    this.recognition.onerror = (event: SpeechRecognitionErrorEvent) => {
      // no-speech is normal during pauses — ignore it
      if (event.error === 'no-speech') return;
      // aborted happens when we call stop() — ignore it
      if (event.error === 'aborted') return;
      console.warn('Speech recognition error:', event.error);
    };

    this.recognition.onend = () => {
      // Auto-restart if still supposed to be listening
      if (this.shouldRestart && this.isListening) {
        try {
          this.recognition?.start();
        } catch {
          // Already started — ignore
        }
      }
    };

    try {
      this.recognition.start();
      return true;
    } catch {
      return false;
    }
  }

  pause(): void {
    this.isListening = false;
    this.shouldRestart = false;
    try {
      this.recognition?.stop();
    } catch {
      // Ignore
    }
  }

  resume(): void {
    if (!this.recognition || !this.onResult) return;
    this.isListening = true;
    this.shouldRestart = true;
    try {
      this.recognition.start();
    } catch {
      // Already running — ignore
    }
  }

  stop(): void {
    this.isListening = false;
    this.shouldRestart = false;
    try {
      this.recognition?.stop();
    } catch {
      // Ignore
    }
    this.recognition = null;
    this.onResult = null;
  }
}

/**
 * Count words in mixed Korean/English text.
 * Korean: ~2 characters per "word" (syllable blocks).
 * English/other: standard whitespace splitting.
 */
export function countWords(text: string): number {
  if (!text.trim()) return 0;

  let wordCount = 0;
  let koreanCharCount = 0;

  const tokens = text.trim().split(/\s+/);

  for (const token of tokens) {
    const koreanChars = token.match(/[\uAC00-\uD7AF\u3130-\u318F\u1100-\u11FF]/g);
    if (koreanChars) {
      koreanCharCount += koreanChars.length;
      const nonKorean = token.replace(/[\uAC00-\uD7AF\u3130-\u318F\u1100-\u11FF]/g, '').trim();
      if (nonKorean.length > 0) wordCount++;
    } else {
      wordCount++;
    }
  }

  // ~2 Korean chars = 1 word
  wordCount += Math.ceil(koreanCharCount / 2);

  return wordCount;
}
