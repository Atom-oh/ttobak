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
  private onError: ((error: string) => void) | null = null;
  private lang: string;
  private lastInterimText = '';
  private lastInterimTimestamp = '';
  private lastFinalText = '';
  private lastResultTime = 0;
  private watchdogTimer: ReturnType<typeof setInterval> | null = null;

  constructor(lang = 'ko-KR') {
    this.lang = lang;
  }

  /**
   * Check if two strings are duplicates (>= 80% character overlap).
   * Used to prevent interim→final promotion from duplicating the last final result.
   */
  private isDuplicate(a: string, b: string): boolean {
    if (!a || !b) return false;
    const na = a.trim();
    const nb = b.trim();
    if (na === nb) return true;
    const longer = na.length >= nb.length ? na : nb;
    const shorter = na.length < nb.length ? na : nb;
    if (longer.includes(shorter)) return true;
    // Character-level overlap ratio
    let matches = 0;
    const bChars = [...nb];
    for (const ch of na) {
      const idx = bChars.indexOf(ch);
      if (idx !== -1) {
        matches++;
        bChars.splice(idx, 1);
      }
    }
    const ratio = matches / Math.max(na.length, nb.length);
    return ratio >= 0.8;
  }

  private restartRecognition(): void {
    try {
      this.recognition?.abort();
    } catch {
      // ignore
    }
    const fresh = this.setupRecognition();
    if (fresh) {
      this.recognition = fresh;
      try {
        fresh.start();
      } catch {
        this.onError?.('recognition-stalled');
      }
    } else {
      this.onError?.('recognition-stalled');
    }
  }

  private handleVisibilityChange = () => {
    if (document.visibilityState === 'visible' && this.isListening && this.shouldRestart) {
      // Tab regained focus — force restart to recover from Chrome throttling
      setTimeout(() => {
        if (this.isListening && this.shouldRestart) {
          this.restartRecognition();
        }
      }, 300);
    }
  };

  static isSupported(): boolean {
    return !!(
      typeof window !== 'undefined' &&
      (window.SpeechRecognition || window.webkitSpeechRecognition)
    );
  }

  /** Create a fresh SpeechRecognition instance with all handlers wired up. */
  private setupRecognition(): SpeechRecognitionInstance | null {
    const Ctor = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (!Ctor) return null;

    const recognition = new Ctor();
    recognition.continuous = true;
    recognition.interimResults = true;
    recognition.lang = this.lang;

    recognition.onresult = (event: SpeechRecognitionEvent) => {
      this.lastResultTime = Date.now();
      for (let i = event.resultIndex; i < event.results.length; i++) {
        const result = event.results[i];
        const transcript = result[0].transcript;
        if (result.isFinal) {
          this.lastFinalText = transcript;
          this.lastInterimText = '';
          this.lastInterimTimestamp = '';
        } else {
          this.lastInterimText = transcript;
          this.lastInterimTimestamp = new Date().toISOString();
        }
        this.onResult?.({
          text: transcript,
          isFinal: result.isFinal,
          timestamp: new Date().toISOString(),
        });
      }
    };

    recognition.onerror = (event: SpeechRecognitionErrorEvent) => {
      // no-speech is normal during pauses — ignore it
      if (event.error === 'no-speech') return;
      // aborted happens when we call stop() — ignore it
      if (event.error === 'aborted') return;

      // Fatal errors: stop auto-restart and notify the caller
      const fatalErrors = ['not-allowed', 'network', 'service-not-allowed', 'language-not-supported'];
      if (fatalErrors.includes(event.error)) {
        this.shouldRestart = false;
        this.isListening = false;
        this.onError?.(event.error);
      }
      console.warn('Speech recognition error:', event.error);
    };

    recognition.onend = () => {
      // Promote any remaining interim text to final before restarting,
      // but skip if it duplicates the last final result (prevents
      // double-text when Chrome restarts recognition).
      if (this.lastInterimText && !this.isDuplicate(this.lastInterimText, this.lastFinalText)) {
        this.lastFinalText = this.lastInterimText;
        this.onResult?.({
          text: this.lastInterimText,
          isFinal: true,
          timestamp: this.lastInterimTimestamp || new Date().toISOString(),
        });
      }
      this.lastInterimText = '';
      this.lastInterimTimestamp = '';

      // Auto-restart with a fresh instance if still supposed to be listening.
      if (this.shouldRestart && this.isListening) {
        setTimeout(() => {
          if (this.shouldRestart && this.isListening) {
            this.restartRecognition();
          }
        }, 100);
      }
    };

    return recognition;
  }

  start(onResult: SpeechCallback, onError?: (error: string) => void): boolean {
    if (!BrowserSpeechRecognition.isSupported()) return false;

    this.onResult = onResult;
    this.onError = onError ?? null;
    this.shouldRestart = true;
    this.isListening = true;
    this.lastInterimText = '';
    this.lastInterimTimestamp = '';
    this.lastFinalText = '';
    this.lastResultTime = Date.now();

    const recognition = this.setupRecognition();
    if (!recognition) return false;
    this.recognition = recognition;

    // Watchdog: if no onresult for 30s while listening, force restart
    this.clearWatchdog();
    this.watchdogTimer = setInterval(() => {
      if (this.isListening && this.shouldRestart && Date.now() - this.lastResultTime > 30_000) {
        console.warn('Speech recognition watchdog: no results for 30s, restarting...');
        this.restartRecognition();
        this.lastResultTime = Date.now(); // reset to avoid rapid retries
      }
    }, 10_000);

    // Visibility change: restart when tab regains focus
    document.addEventListener('visibilitychange', this.handleVisibilityChange);

    try {
      this.recognition.start();
      return true;
    } catch {
      return false;
    }
  }

  private clearWatchdog(): void {
    if (this.watchdogTimer) {
      clearInterval(this.watchdogTimer);
      this.watchdogTimer = null;
    }
  }

  pause(): void {
    this.isListening = false;
    this.shouldRestart = false;
    this.clearWatchdog();
    try {
      this.recognition?.stop();
    } catch {
      // Ignore
    }
  }

  resume(): void {
    if (!this.onResult) return;
    this.isListening = true;
    this.shouldRestart = true;
    this.lastResultTime = Date.now();

    // Restart watchdog
    this.clearWatchdog();
    this.watchdogTimer = setInterval(() => {
      if (this.isListening && this.shouldRestart && Date.now() - this.lastResultTime > 30_000) {
        console.warn('Speech recognition watchdog: no results for 30s, restarting...');
        this.restartRecognition();
        this.lastResultTime = Date.now();
      }
    }, 10_000);

    const fresh = this.setupRecognition();
    if (fresh) {
      this.recognition = fresh;
      try {
        fresh.start();
      } catch {
        // Already running — ignore
      }
    }
  }

  stop(): void {
    this.isListening = false;
    this.shouldRestart = false;
    this.clearWatchdog();
    document.removeEventListener('visibilitychange', this.handleVisibilityChange);
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
