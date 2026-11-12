export interface LiveDetectionInput {
  transcript: string;
  meetingTitle?: string;
  meetingContext?: string;
}

export interface LiveDetectionResult {
  questions: string[];
  proactive?: string[];
  automatic?: string[];
}

/** Coalesce transcript updates independently of the summary word threshold. */
export class LiveQuestionDetector {
  private latest?: LiveDetectionInput;
  private submitted?: LiveDetectionInput;
  private timer?: ReturnType<typeof setTimeout>;
  private epoch = 0;
  private inFlight = false;
  private disposed = false;
  private lastStarted = -Infinity;
  private controller?: AbortController;

  constructor(
    private detect: (input: LiveDetectionInput, signal: AbortSignal) => Promise<LiveDetectionResult>,
    private apply: (result: LiveDetectionResult) => void,
  ) {}

  update(input: LiveDetectionInput) {
    if (this.disposed || input.transcript.trim().length < 4) return;
    const next = {
      transcript: input.transcript.slice(-6000),
      meetingTitle: input.meetingTitle?.slice(0, 120),
      meetingContext: input.meetingContext?.slice(0, 3000),
    };
    if (JSON.stringify(next) === JSON.stringify(this.latest)) return;
    this.latest = next;
    this.schedule();
  }

  reset() {
    this.epoch++;
    this.controller?.abort();
    this.controller = undefined;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
    this.latest = this.submitted = undefined;
    this.inFlight = this.disposed = false;
    this.lastStarted = -Infinity;
  }

  dispose() {
    this.reset();
    this.disposed = true;
  }

  private schedule() {
    if (this.disposed || this.inFlight || this.timer !== undefined || !this.latest
      || this.latest === this.submitted) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.run();
    }, Math.max(1500, 10_000 - (Date.now() - this.lastStarted)));
  }

  private async run() {
    const input = this.latest;
    if (!input || this.disposed) return;
    const epoch = this.epoch;
    this.submitted = input;
    this.inFlight = true;
    this.lastStarted = Date.now();
    const controller = new AbortController();
    this.controller = controller;
    const timeout = setTimeout(() => controller.abort(), 30_000);
    try {
      const result = await this.detect(input, controller.signal);
      if (epoch === this.epoch && !this.disposed) this.apply(result);
    } catch {
      // A failed round must not leave an old automatic batch armed.
      if (epoch === this.epoch && !this.disposed) this.apply({ questions: [], automatic: [] });
    } finally {
      clearTimeout(timeout);
      if (epoch === this.epoch) {
        this.controller = undefined;
        this.inFlight = false;
        this.schedule();
      }
    }
  }
}
