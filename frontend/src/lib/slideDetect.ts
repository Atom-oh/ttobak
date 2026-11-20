// Pure slide-change detection for automatic screen capture. No DOM access, so
// scripts/slide-detect.test.mjs can exercise it directly.
//
// Each sample is a GRID_W x GRID_H grayscale signature (one luminance byte per
// cell). Cells that keep changing (participant video tiles, clocks, playing
// video) are learned as volatile and ignored, so a Zoom window with a speaker
// strip still settles once the shared slide is static.

export const GRID_W = 64;
export const GRID_H = 36;
export const GRID_CELLS = GRID_W * GRID_H;

export interface SlideDetectorOptions {
  /** Luminance difference (0-255) for a cell to count as changed. */
  cellDelta: number;
  /** Max changed fraction between consecutive samples to count as still. */
  stableFraction: number;
  /** Consecutive still samples required before a frame is considered. */
  stableSamples: number;
  /** Min changed fraction against the last capture to count as a new slide. */
  changeFraction: number;
  /** Max changed fraction against any earlier capture to treat it as a repeat. */
  duplicateFraction: number;
  /** Volatility above which a cell is excluded from comparisons. */
  volatileThreshold: number;
  /** Fraction of cells that must be non-volatile to judge the frame at all. */
  minStaticFraction: number;
  /** Minimum luminance standard deviation; below it the frame is blank. */
  minContrast: number;
  /** Minimum time between two captures. */
  minGapMs: number;
  /** Hard cap on captures per session. */
  maxCaptures: number;
}

export const DEFAULT_SLIDE_DETECTOR_OPTIONS: SlideDetectorOptions = {
  cellDelta: 10,
  stableFraction: 0.01,
  stableSamples: 2,
  changeFraction: 0.03,
  duplicateFraction: 0.01,
  volatileThreshold: 0.4,
  minStaticFraction: 0.3,
  minContrast: 6,
  minGapMs: 3000,
  maxCaptures: 120,
};

/** Slide uploads allowed per recording, across every auto-capture session. */
export const MAX_SLIDES_PER_RECORDING = 120;

// Hyphenated so the icon-font scanner never reads a decision as an icon name.
export type SlideDecision = 'new-slide' | 'skip' | 'limit';

/** Averages an RGBA buffer of (GRID_W*scale) x (GRID_H*scale) into a signature. */
export function signatureFromRgba(data: Uint8ClampedArray | Uint8Array, width: number, height: number): Uint8Array {
  const sig = new Uint8Array(GRID_CELLS);
  const cellW = width / GRID_W;
  const cellH = height / GRID_H;
  for (let gy = 0; gy < GRID_H; gy++) {
    const y0 = Math.floor(gy * cellH);
    const y1 = Math.max(y0 + 1, Math.floor((gy + 1) * cellH));
    for (let gx = 0; gx < GRID_W; gx++) {
      const x0 = Math.floor(gx * cellW);
      const x1 = Math.max(x0 + 1, Math.floor((gx + 1) * cellW));
      let sum = 0;
      let n = 0;
      for (let y = y0; y < y1 && y < height; y++) {
        for (let x = x0; x < x1 && x < width; x++) {
          const i = (y * width + x) * 4;
          sum += 0.299 * data[i] + 0.587 * data[i + 1] + 0.114 * data[i + 2];
          n++;
        }
      }
      sig[gy * GRID_W + gx] = n ? Math.round(sum / n) : 0;
    }
  }
  return sig;
}

function contrast(sig: Uint8Array): number {
  let sum = 0;
  for (const v of sig) sum += v;
  const mean = sum / sig.length;
  let sq = 0;
  for (const v of sig) sq += (v - mean) * (v - mean);
  return Math.sqrt(sq / sig.length);
}

export class SlideChangeDetector {
  private readonly opts: SlideDetectorOptions;
  private readonly volatility = new Float32Array(GRID_CELLS);
  private prev: Uint8Array | null = null;
  private stable = 0;
  private lastCaptureAt = Number.NEGATIVE_INFINITY;
  private current: Uint8Array | null = null;
  private readonly captured: Uint8Array[] = [];

  constructor(options: Partial<SlideDetectorOptions> = {}) {
    this.opts = { ...DEFAULT_SLIDE_DETECTOR_OPTIONS, ...options };
  }

  get captureCount(): number {
    return this.captured.length;
  }

  /** Fraction of non-volatile cells that differ between a and b, or null. */
  private changedFraction(a: Uint8Array, b: Uint8Array): number | null {
    let considered = 0;
    let changed = 0;
    for (let i = 0; i < GRID_CELLS; i++) {
      if (this.volatility[i] > this.opts.volatileThreshold) continue;
      considered++;
      if (Math.abs(a[i] - b[i]) > this.opts.cellDelta) changed++;
    }
    if (considered < GRID_CELLS * this.opts.minStaticFraction) return null;
    return changed / considered;
  }

  /** Feeds one sample; returns 'new-slide' when it shows a new stable slide. */
  push(sig: Uint8Array, now: number): SlideDecision {
    if (sig.length !== GRID_CELLS) throw new Error('bad signature size');
    // Report the cap on the next sample so the session ends right away.
    if (this.captured.length >= this.opts.maxCaptures) return 'limit';
    const prev = this.prev;
    this.prev = sig;
    if (!prev) return 'skip';

    for (let i = 0; i < GRID_CELLS; i++) {
      const moved = Math.abs(sig[i] - prev[i]) > this.opts.cellDelta ? 1 : 0;
      this.volatility[i] = this.volatility[i] * 0.8 + moved * 0.2;
    }

    const step = this.changedFraction(sig, prev);
    if (step === null || step > this.opts.stableFraction) {
      this.stable = 0;
      return 'skip';
    }
    this.stable++;
    if (this.stable < this.opts.stableSamples) return 'skip';
    if (contrast(sig) < this.opts.minContrast) return 'skip';

    if (this.current) {
      const fromCurrent = this.changedFraction(sig, this.current);
      if (fromCurrent === null || fromCurrent < this.opts.changeFraction) return 'skip';
    }
    for (const earlier of this.captured) {
      const d = this.changedFraction(sig, earlier);
      if (d !== null && d <= this.opts.duplicateFraction) {
        // Flipping back to an earlier slide: remember it as current, no upload.
        this.current = sig;
        return 'skip';
      }
    }
    if (now - this.lastCaptureAt < this.opts.minGapMs) return 'skip';

    this.current = sig;
    this.captured.push(sig);
    this.lastCaptureAt = now;
    return 'new-slide';
  }
}
