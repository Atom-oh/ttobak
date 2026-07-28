/**
 * Browser-native Speech Recognition wrapper for real-time transcription.
 * Uses Web Speech API (SpeechRecognition / webkitSpeechRecognition).
 *
 * Architecture: Let Chrome handle all segmentation naturally.
 * NO forced restarts, NO sentence-ending detection, NO flush timers.
 * Chrome's continuous mode segments Korean speech into interim/final results on its own.
 * We only restart on: watchdog timeout (30s silence) or tab visibility change.
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
  private isRestarting = false;
  private networkRetryCount = 0;
  private readonly MAX_NETWORK_RETRIES = 5;
  private readonly WATCHDOG_TIMEOUT_MS = 15_000;
  private readonly WATCHDOG_CHECK_INTERVAL_MS = 5_000;

  // Overlap buffer: tracks recently promoted text to prevent duplicates after restart
  private overlapBuffer: string[] = [];
  private overlapWindowEnd = 0;
  private readonly OVERLAP_WINDOW_MS = 1500;

  constructor(lang = 'ko-KR') {
    this.lang = lang;
  }

  /** Exponential backoff: 500ms, 1s, 2s, 4s, 5s (capped) */
  private getRetryDelay(): number {
    return Math.min(500 * Math.pow(2, this.networkRetryCount - 1), 5000);
  }

  /**
   * Check if two strings are duplicates (for onend interim promotion).
   * Prevents double-text when Chrome naturally restarts recognition.
   */
  private isDuplicate(a: string, b: string): boolean {
    if (!a || !b) return false;
    const na = a.trim();
    const nb = b.trim();
    if (na === nb) return true;
    // Short strings: only exact match (Korean syllables are too similar for fuzzy matching)
    if (Math.max(na.length, nb.length) < 8) return false;
    const longer = na.length >= nb.length ? na : nb;
    const shorter = na.length < nb.length ? na : nb;
    const lengthRatio = shorter.length / longer.length;
    // Substring check: require minimum length to avoid false positives with common Korean particles
    if (shorter.length >= 6 && lengthRatio >= 0.7 && longer.includes(shorter)) return true;
    if (lengthRatio < 0.7) return false;
    let matches = 0;
    const bChars = [...nb];
    for (const ch of na) {
      const idx = bChars.indexOf(ch);
      if (idx !== -1) {
        matches++;
        bChars.splice(idx, 1);
      }
    }
    return matches / Math.max(na.length, nb.length) >= 0.95;
  }

  /**
   * Restart recognition. Only used for:
   * - Watchdog (30s no results — Chrome's 5-min limit)
   * - Tab visibility change
   * - Natural onend when Chrome stops unexpectedly
   */
  private restartRecognition(): void {
    if (this.isRestarting) return;
    this.isRestarting = true;

    // Promote any pending interim text before restart
    const pendingInterim = this.lastInterimText;
    const pendingTimestamp = this.lastInterimTimestamp;
    if (pendingInterim && !this.isDuplicate(pendingInterim, this.lastFinalText)) {
      this.lastFinalText = pendingInterim;
      // Track in overlap buffer to prevent duplicates from fresh instance
      this.overlapBuffer.push(pendingInterim.trim());
      if (this.overlapBuffer.length > 3) this.overlapBuffer.shift();
      this.onResult?.({
        text: pendingInterim,
        isFinal: true,
        timestamp: pendingTimestamp || new Date().toISOString(),
      });
    }
    this.lastInterimText = '';
    this.lastInterimTimestamp = '';

    // Start overlap window
    this.overlapWindowEnd = Date.now() + this.OVERLAP_WINDOW_MS;

    // Create fresh instance BEFORE aborting old one (closure guard)
    const old = this.recognition;
    const fresh = this.setupRecognition();
    if (fresh) {
      this.recognition = fresh;
      try { old?.abort(); } catch { /* ignore */ }
      try { fresh.start(); } catch {
        this.isRestarting = false;
        this.onError?.('recognition-stalled');
        return;
      }
    } else {
      try { old?.abort(); } catch { /* ignore */ }
      this.isRestarting = false;
      this.onError?.('recognition-stalled');
      return;
    }

    // Clear isRestarting immediately — the flag only prevents re-entrant calls
    this.isRestarting = false;
  }

  private handleVisibilityChange = () => {
    if (document.visibilityState === 'visible' && this.isListening && this.shouldRestart) {
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
      // Ignore stale events from a previous recognition instance
      if (this.recognition !== recognition) return;
      this.lastResultTime = Date.now();
      // Reset network retry counter on successful result
      this.networkRetryCount = 0;

      // Clear expired overlap window
      if (this.overlapWindowEnd > 0 && Date.now() >= this.overlapWindowEnd) {
        this.overlapBuffer = [];
        this.overlapWindowEnd = 0;
      }

      for (let i = event.resultIndex; i < event.results.length; i++) {
        const result = event.results[i];
        const transcript = result[0].transcript;

        if (result.isFinal) {
          // Check overlap window: skip duplicates from restart
          if (Date.now() < this.overlapWindowEnd) {
            const trimmed = transcript.trim();
            const isOverlapDupe = this.overlapBuffer.some(buf =>
              this.isDuplicate(trimmed, buf) ||
              buf.includes(trimmed) ||
              trimmed.includes(buf)
            );
            if (isOverlapDupe) continue;
          }

          // Chrome truncation guard: Chrome sometimes finalizes shorter text
          // than what was shown as interim. Preserve the truncated tail
          // as pending interim for promotion on onend/restart.
          const trimmedFinal = transcript.trim();
          const trimmedInterim = this.lastInterimText.trim();
          if (trimmedInterim.length > trimmedFinal.length + 2 &&
              trimmedInterim.startsWith(trimmedFinal)) {
            this.lastInterimText = trimmedInterim.slice(trimmedFinal.length).trim();
            this.lastInterimTimestamp = new Date().toISOString();
          } else {
            this.lastInterimText = '';
            this.lastInterimTimestamp = '';
          }
          this.lastFinalText = transcript;
        } else {
          this.lastInterimText = transcript;
          this.lastInterimTimestamp = new Date().toISOString();
        }

        // Pass through to consumer — let Chrome decide interim vs final
        this.onResult?.({
          text: transcript,
          isFinal: result.isFinal,
          timestamp: new Date().toISOString(),
        });
      }
    };

    recognition.onerror = (event: SpeechRecognitionErrorEvent) => {
      if (event.error === 'no-speech') return;
      if (event.error === 'aborted') return;

      const fatalErrors = ['not-allowed', 'service-not-allowed', 'language-not-supported'];
      const transientErrors = ['network', 'audio-capture'];

      if (fatalErrors.includes(event.error)) {
        this.shouldRestart = false;
        this.isListening = false;
        this.onError?.(event.error);
      } else if (transientErrors.includes(event.error)) {
        // Transient error: auto-retry with backoff, up to MAX_NETWORK_RETRIES
        this.networkRetryCount++;
        console.warn(`Speech recognition transient error: ${event.error} (retry ${this.networkRetryCount}/${this.MAX_NETWORK_RETRIES})`);
        if (this.networkRetryCount <= this.MAX_NETWORK_RETRIES) {
          setTimeout(() => {
            if (this.isListening && this.shouldRestart) {
              this.restartRecognition();
            }
          }, this.getRetryDelay());
        } else {
          console.error('Speech recognition: max retries exceeded, stopping');
          this.shouldRestart = false;
          this.isListening = false;
          this.onError?.('recognition-failed');
        }
      }
      console.warn('Speech recognition error:', event.error);
    };

    recognition.onend = () => {
      // Ignore stale events from a previous recognition instance
      if (this.recognition !== recognition) return;

      // Promote remaining interim text to final (Chrome stopped mid-utterance)
      if (this.lastInterimText && !this.isDuplicate(this.lastInterimText, this.lastFinalText)) {
        this.lastFinalText = this.lastInterimText;
        // Track in overlap buffer so the fresh instance doesn't duplicate this text
        this.overlapBuffer.push(this.lastInterimText.trim());
        if (this.overlapBuffer.length > 3) this.overlapBuffer.shift();
        this.overlapWindowEnd = Date.now() + this.OVERLAP_WINDOW_MS;
        this.onResult?.({
          text: this.lastInterimText,
          isFinal: true,
          timestamp: this.lastInterimTimestamp || new Date().toISOString(),
        });
      }
      this.lastInterimText = '';
      this.lastInterimTimestamp = '';

      // Auto-restart if Chrome stopped unexpectedly (e.g. silence timeout)
      // Skip restart when tab is hidden — Chrome kills new instances immediately in background.
      // handleVisibilityChange will restart when the tab becomes visible again.
      if (this.shouldRestart && this.isListening && !this.isRestarting && document.visibilityState !== 'hidden') {
        setTimeout(() => {
          if (this.shouldRestart && this.isListening && !this.isRestarting && document.visibilityState !== 'hidden') {
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

    // Watchdog: if no onresult for 15s while listening, force restart
    this.clearWatchdog();
    this.watchdogTimer = setInterval(() => {
      if (this.isListening && this.shouldRestart && Date.now() - this.lastResultTime > this.WATCHDOG_TIMEOUT_MS) {
        console.warn('Speech recognition watchdog: no results, restarting...');
        this.restartRecognition();
        this.lastResultTime = Date.now();
      }
    }, this.WATCHDOG_CHECK_INTERVAL_MS);

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
    try { this.recognition?.stop(); } catch { /* ignore */ }
  }

  resume(): void {
    if (!this.onResult) return;
    this.isListening = true;
    this.shouldRestart = true;
    this.lastResultTime = Date.now();

    this.clearWatchdog();
    this.watchdogTimer = setInterval(() => {
      if (this.isListening && this.shouldRestart && Date.now() - this.lastResultTime > this.WATCHDOG_TIMEOUT_MS) {
        console.warn('Speech recognition watchdog: no results, restarting...');
        this.restartRecognition();
        this.lastResultTime = Date.now();
      }
    }, this.WATCHDOG_CHECK_INTERVAL_MS);

    const fresh = this.setupRecognition();
    if (fresh) {
      this.recognition = fresh;
      try { fresh.start(); } catch { this.onError?.('recognition-stalled'); }
    }
  }

  stop(): void {
    this.isListening = false;
    this.shouldRestart = false;
    this.clearWatchdog();
    document.removeEventListener('visibilitychange', this.handleVisibilityChange);
    try { this.recognition?.stop(); } catch { /* ignore */ }
    this.recognition = null;
    this.onResult = null;
  }

  /** Get pending interim text (for source-switch promotion). */
  getPendingInterim(): string | null {
    return this.lastInterimText || null;
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

  wordCount += Math.ceil(koreanCharCount / 2);
  return wordCount;
}
