export const BILINGUAL_TARGET = 'ko-en';
export const MAX_TRANSLATION_ENTRIES = 200;

export const TRANSLATION_TARGETS = [
  { code: BILINGUAL_TARGET, label: '한국어 ↔ English' },
  { code: 'ko', label: '한국어' },
  { code: 'en', label: 'English' },
  { code: 'ja', label: '日本語' },
  { code: 'zh', label: '中文' },
  { code: 'es', label: 'Español' },
  { code: 'fr', label: 'Français' },
  { code: 'de', label: 'Deutsch' },
];

/** Browser deadline for one translation request (server interpreter: 6s + fallback). */
export const TRANSLATION_TIMEOUT_MS = 10000;
/** Matches the API's context bounds: two items, 2,000 UTF-8 bytes in total. */
export const MAX_CONTEXT_ITEMS = 2;
export const MAX_CONTEXT_BYTES = 2000;

export type TranslationQuality = 'fast' | 'high';
/** ai = interpreter model; machine = Amazon Translate; machine-fallback = high was requested but Translate answered. */
export type TranslationEngineBadge = 'ai' | 'machine' | 'machine-fallback';

export interface TranslateRequestOptions {
  quality: TranslationQuality;
  /** Earlier final source texts, oldest first; reference data for quality high only. */
  context?: string[];
}

export interface TranslateResponse {
  translatedText: string;
  engine?: string;
  quality?: string;
  limited?: boolean;
}

export interface TranslationEntry {
  id: string;
  order: number;
  original: string;
  originalTruncated?: boolean;
  translated: string;
  sourceLang: string;
  targetLang: string;
  timestamp: string;
  isFinal: boolean;
  /** skipped = a hesitation-only final that was intentionally not translated. */
  status: 'pending' | 'complete' | 'error' | 'skipped';
  engine?: TranslationEngineBadge;
}

interface TranslationSegment {
  entry: TranslationEntry;
  revision: number;
  /** Frozen when the final is enqueued, so completion order cannot change it. */
  context?: string[];
}

interface TranslationOptions {
  enabled: boolean;
  targetLang: string;
  /**
   * Translate partial (interim) captions too. Off by default: every partial
   * revision is a billed request, while finals alone keep the cost bounded.
   */
  interim?: boolean;
  onUpdate: (entry: TranslationEntry) => void;
  onReset: () => void;
  translate: (
    text: string, sourceLang: string, targetLang: string, signal: AbortSignal, options: TranslateRequestOptions,
  ) => Promise<TranslateResponse>;
}

// Pure hesitation sounds only, including repeats ("어어", "umm"). Short words
// that carry meaning (네, 예, 그) and numbers are deliberately absent.
const FILLER_TOKEN = /^(?:어+|음+|아+|으+음+|u+h+|u+m+|e+r+|h+m+)$/;

/** True when every word of text is a hesitation sound; such finals are not translated. */
export function isFillerOnly(text: string): boolean {
  const tokens = text.normalize('NFC').toLowerCase().split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  return tokens.length > 0 && tokens.every((token) => FILLER_TOKEN.test(token));
}

export function countWords(text: string): number {
  return text.trim().split(/\s+/).filter(Boolean).length;
}

/** G4 measurement: finals of at most two words among the retained finals. No merging. */
export function fragmentStats(entries: TranslationEntry[]): { finals: number; fragments: number } {
  const finals = entries.filter((entry) => entry.isFinal);
  return { finals: finals.length, fragments: finals.filter((entry) => countWords(entry.original) <= 2).length };
}

export function translationEngineBadge(
  requested: TranslationQuality, response: TranslateResponse,
): TranslationEngineBadge | undefined {
  if (response.engine === 'llm') return 'ai';
  if (response.engine === 'translate') return requested === 'high' ? 'machine-fallback' : 'machine';
  return undefined;
}

const utf8Length = (text: string) => new TextEncoder().encode(text).length;

export function translationSourceLanguage(text: string, detectedLanguage?: string): string {
  const language = detectedLanguage?.split('-')[0];
  if (language === 'ko' || language === 'en') return language;
  return /[가-힣ㄱ-ㅎㅏ-ㅣ]/.test(text) ? 'ko' : 'en';
}

export class LiveTranslation {
  private segments = new Map<string, TranslationSegment>();
  private finalQueue: TranslationSegment[] = [];
  private finalRequests = 0;
  private interim: TranslationSegment | null = null;
  private interimTimer: ReturnType<typeof setTimeout> | undefined;
  private interimRequest: AbortController | null = null;
  private requests = new Set<AbortController>();
  private generation = 0;
  private nextOrder = 0;
  private stopped = false;
  /** Recent final source texts in enqueue order, for request context. */
  private recentFinals: string[] = [];

  constructor(private options: TranslationOptions) {}

  update(id: string, text: string, isFinal: boolean, detectedLanguage?: string): void {
    if (this.stopped || !this.options.enabled || !text.trim()) return;
    const previous = this.segments.get(id);
    if (previous?.entry.isFinal) return;
    const oversized = text.length > 9000 || new TextEncoder().encode(text).length > 9000;
    const sourceLang = translationSourceLanguage(text, detectedLanguage);
    const targetLang = this.options.targetLang === BILINGUAL_TARGET
      ? sourceLang === 'ko' ? 'en' : 'ko'
      : this.options.targetLang;
    const filler = isFillerOnly(text);
    const segment: TranslationSegment = previous ?? {
      entry: {
        id, order: this.nextOrder++, original: '', translated: '',
        sourceLang, targetLang, timestamp: new Date().toISOString(), isFinal, status: 'pending',
      },
      revision: 0,
    };
    const sameDirection = segment.entry.sourceLang === sourceLang && segment.entry.targetLang === targetLang;
    segment.revision++;
    segment.entry = {
      ...segment.entry, original: text.slice(0, 9000), originalTruncated: text.length > 9000,
      sourceLang, targetLang, isFinal, status: 'pending',
      translated: !isFinal && sameDirection && text.startsWith(segment.entry.original)
        ? segment.entry.translated
        : '',
    };
    this.segments.set(id, segment);
    if (this.segments.size > MAX_TRANSLATION_ENTRIES) {
      this.segments.delete(this.segments.keys().next().value!);
    }
    this.publish(segment);

    if (oversized) {
      if (this.interim === segment) this.clearInterim();
      segment.entry = { ...segment.entry, translated: '', status: 'error' };
      this.publish(segment);
      return;
    }

    if (isFinal) {
      if (this.interim === segment) this.clearInterim();
      if (filler) {
        segment.entry = { ...segment.entry, translated: '', status: 'skipped' };
        this.publish(segment);
        return;
      }
      segment.context = this.finalContext();
      this.rememberFinal(segment.entry.original);
      if (this.finalQueue.length >= 40) {
        segment.entry = { ...segment.entry, translated: '', status: 'error' };
        this.publish(segment);
        return;
      }
      this.finalQueue.push(segment);
      this.drainFinals();
    } else if (filler) {
      if (this.interim === segment) this.clearInterim();
    } else if (this.options.interim) {
      this.interim = segment;
      this.scheduleInterim();
    }
  }

  /** Up to two most recent earlier finals within the byte budget, oldest first. */
  private finalContext(): string[] {
    const context: string[] = [];
    let bytes = 0;
    for (let index = this.recentFinals.length - 1; index >= 0 && context.length < MAX_CONTEXT_ITEMS; index--) {
      const size = utf8Length(this.recentFinals[index]);
      if (bytes + size > MAX_CONTEXT_BYTES) break;
      bytes += size;
      context.unshift(this.recentFinals[index]);
    }
    return context;
  }

  private rememberFinal(text: string): void {
    this.recentFinals.push(text);
    if (this.recentFinals.length > MAX_CONTEXT_ITEMS) this.recentFinals.shift();
  }

  private publish(segment: TranslationSegment): void {
    this.options.onUpdate({ ...segment.entry });
  }

  private scheduleInterim(): void {
    if (this.interimTimer || this.interimRequest || !this.interim) return;
    this.interimTimer = setTimeout(() => {
      this.interimTimer = undefined;
      const segment = this.interim;
      if (!segment || segment.entry.isFinal) return;
      void this.translate(segment, false);
    }, 500);
  }

  private drainFinals(): void {
    while (this.finalRequests < 2 && this.finalQueue.length > 0) {
      const segment = this.finalQueue.shift()!;
      if (this.segments.get(segment.entry.id) !== segment) continue;
      this.finalRequests++;
      void this.translate(segment, true).finally(() => {
        this.finalRequests--;
        this.drainFinals();
      });
    }
  }

  private async translate(segment: TranslationSegment, isFinal: boolean): Promise<void> {
    const snapshot = { ...segment.entry };
    const revision = segment.revision;
    const generation = this.generation;
    const controller = new AbortController();
    this.requests.add(controller);
    if (!isFinal) this.interimRequest = controller;
    const timer = setTimeout(() => controller.abort(), TRANSLATION_TIMEOUT_MS);
    // Finals ask for the interpreter; drafts stay on the fast path.
    const quality: TranslationQuality = isFinal ? 'high' : 'fast';
    const requestOptions: TranslateRequestOptions = isFinal && segment.context?.length
      ? { quality, context: [...segment.context] }
      : { quality };
    let rejectCancellation: (reason: Error) => void = () => {};
    const cancellation = new Promise<never>((resolve, reject) => {
      rejectCancellation = reject;
    });
    const onAbort = () => rejectCancellation(new Error('Translation cancelled'));
    controller.signal.addEventListener('abort', onAbort, { once: true });
    const isCurrent = () => !this.stopped && this.options.enabled &&
      generation === this.generation && this.segments.get(snapshot.id) === segment &&
      segment.entry.sourceLang === snapshot.sourceLang && segment.entry.targetLang === snapshot.targetLang &&
      (isFinal ? segment.revision === revision : !!this.options.interim && !segment.entry.isFinal);
    try {
      if (utf8Length(snapshot.original) > 9000) throw new Error('Translation input too long');
      const request: Promise<TranslateResponse> = snapshot.sourceLang === snapshot.targetLang
        ? Promise.resolve({ translatedText: snapshot.original })
        : this.options.translate(snapshot.original, snapshot.sourceLang, snapshot.targetLang, controller.signal, requestOptions);
      const result = await Promise.race([request, cancellation]);
      if (!isCurrent() || controller.signal.aborted) return;
      if (typeof result.translatedText !== 'string' || !result.translatedText.trim() || utf8Length(result.translatedText) > 30000) {
        throw new Error('Invalid translation result');
      }
      if (!isFinal && !segment.entry.original.startsWith(snapshot.original)) return;
      segment.entry = {
        ...segment.entry, translated: result.translatedText,
        status: isFinal ? 'complete' : 'pending',
        engine: isFinal ? translationEngineBadge(quality, result) : undefined,
      };
      this.publish(segment);
    } catch {
      if (isCurrent()) {
        segment.entry = { ...segment.entry, translated: '', status: 'error' };
        this.publish(segment);
      }
    } finally {
      clearTimeout(timer);
      controller.signal.removeEventListener('abort', onAbort);
      this.requests.delete(controller);
      if (this.interimRequest === controller) {
        this.interimRequest = null;
        if (this.interim && (this.interim !== segment || this.interim.revision !== revision)) {
          this.scheduleInterim();
        }
      }
    }
  }

  private clearInterim(): void {
    if (this.interimTimer) clearTimeout(this.interimTimer);
    this.interimTimer = undefined;
    this.interim = null;
    this.interimRequest?.abort();
    this.interimRequest = null;
  }

  private reset(): void {
    this.generation++;
    this.clearInterim();
    this.requests.forEach((controller) => controller.abort());
    this.requests.clear();
    this.finalQueue = [];
    this.recentFinals = [];
    this.segments.clear();
    this.options.onReset();
  }

  updateTargetLang(targetLang: string): void {
    if (this.options.targetLang === targetLang) return;
    this.options.targetLang = targetLang;
    this.reset();
  }

  /** Turning drafts off cancels the pending draft; finals are unaffected. */
  updateInterim(interim: boolean): void {
    if (!!this.options.interim === interim) return;
    this.options.interim = interim;
    if (!interim) this.clearInterim();
  }

  updateEnabled(enabled: boolean): void {
    if (this.options.enabled === enabled) return;
    this.options.enabled = enabled;
    this.reset();
  }

  stop(): void {
    this.stopped = true;
    this.generation++;
    this.clearInterim();
    this.requests.forEach((controller) => controller.abort());
    this.requests.clear();
    this.finalQueue = [];
    this.recentFinals = [];
    this.segments.clear();
  }
}
