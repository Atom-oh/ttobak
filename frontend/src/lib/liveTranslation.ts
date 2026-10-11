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
  status: 'pending' | 'complete' | 'error';
}

interface TranslationSegment {
  entry: TranslationEntry;
  revision: number;
}

interface TranslationOptions {
  enabled: boolean;
  targetLang: string;
  onUpdate: (entry: TranslationEntry) => void;
  onReset: () => void;
  translate: (text: string, sourceLang: string, targetLang: string, signal: AbortSignal) => Promise<{ translatedText: string }>;
}

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
      translated: sameDirection ? segment.entry.translated : '',
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
      if (this.finalQueue.length >= 40) {
        segment.entry = { ...segment.entry, translated: '', status: 'error' };
        this.publish(segment);
        return;
      }
      this.finalQueue.push(segment);
      this.drainFinals();
    } else {
      this.interim = segment;
      this.scheduleInterim();
    }
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
    const timer = setTimeout(() => controller.abort(), 8000);
    const isCurrent = () => !this.stopped && this.options.enabled &&
      generation === this.generation && this.segments.get(snapshot.id) === segment &&
      segment.entry.sourceLang === snapshot.sourceLang && segment.entry.targetLang === snapshot.targetLang &&
      (isFinal ? segment.revision === revision : !segment.entry.isFinal);
    try {
      if (new TextEncoder().encode(snapshot.original).length > 9000) throw new Error('Translation input too long');
      const result = snapshot.sourceLang === snapshot.targetLang
        ? { translatedText: snapshot.original }
        : await this.options.translate(snapshot.original, snapshot.sourceLang, snapshot.targetLang, controller.signal);
      if (!isCurrent() || controller.signal.aborted) return;
      if (!result.translatedText.trim() || new TextEncoder().encode(result.translatedText).length > 30000) {
        throw new Error('Invalid translation result');
      }
      if (!isFinal && !segment.entry.original.startsWith(snapshot.original)) return;
      segment.entry = {
        ...segment.entry, translated: result.translatedText,
        status: isFinal ? 'complete' : 'pending',
      };
      this.publish(segment);
    } catch {
      if (isCurrent()) {
        segment.entry = { ...segment.entry, status: 'error' };
        this.publish(segment);
      }
    } finally {
      clearTimeout(timer);
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
    this.segments.clear();
    this.options.onReset();
  }

  updateTargetLang(targetLang: string): void {
    if (this.options.targetLang === targetLang) return;
    this.options.targetLang = targetLang;
    this.reset();
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
    this.segments.clear();
  }
}
