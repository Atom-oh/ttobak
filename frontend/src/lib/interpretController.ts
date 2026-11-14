// Session logic for the Mac interpret-only screen (/interpret), kept out of
// React so node:test can drive it: start, stop, reconnect, idle stop and the
// stops the Mac app makes itself. `useInterpretSession` supplies the real
// capture and STT; tests supply fakes.
//
// Nothing here stores or logs caption text beyond the on-screen list.

import { IdleStop, INTERPRET_IDLE_STOP_MS, INTERPRET_MESSAGES, ReconnectBudget, type IdleStopTimers } from './interpretSession';

export type InterpretPhase = 'idle' | 'starting' | 'running' | 'reconnecting' | 'stopping';

/** A new STT session with no failure for this long counts as connected. */
export const CONNECT_GRACE_MS = 5000;
/** Most captions kept on screen. */
export const MAX_INTERPRET_ENTRIES = 200;
/** Keeps entries of successive STT sessions apart: each session restarts its ids and order. */
const SESSION_ORDER_SPAN = 1_000_000;

/** The caption fields this controller reads; the rest passes through untouched. */
export interface InterpretEntry {
  id: string;
  order: number;
  status: string;
}

export interface InterpretState<Entry extends InterpretEntry, Input> {
  phase: InterpretPhase;
  translations: Entry[];
  error: string | null;
  notice: string | null;
  warnings: string[];
  /** The microphone being heard. */
  input: Input | null;
  /** `now()` when the capture started; `null` while idle. */
  startedAt: number | null;
}

export interface InterpretSttOptions {
  targetLang: string;
  interimTranslation: boolean;
}

export interface InterpretSttHandlers<Entry> {
  /** Speech was recognized (interim or final). */
  onTranscript(): void;
  onTranslation(entry: Entry): void;
  /** Queued translations were abandoned (e.g. the target language changed). */
  onReset(): void;
  /** The caption stream failed. */
  onUnavailable(): void;
}

export interface InterpretStt {
  /** Settles when the caption stream ends, not when it connects. */
  start(): Promise<void>;
  stop(): void;
  push(chunk: Uint8Array): void;
  setTargetLang(lang: string): void;
  setInterimTranslation(enabled: boolean): void;
}

export interface InterpretDeps<Config, Entry, Input> {
  /** Transcribe configuration, or `null` when live captions are not configured. */
  loadConfig(): Promise<Config | null>;
  startCapture(inputDeviceUid?: string): Promise<{ warnings?: string[]; input?: Input }>;
  /** Idempotent. */
  stopCapture(): Promise<unknown>;
  createStt(config: Config, options: InterpretSttOptions, handlers: InterpretSttHandlers<Entry>): InterpretStt;
  /** Capture events; returns the unsubscribe function. */
  subscribe(handlers: { onChunk(chunk: Uint8Array): void; onStopped(reason: 'system-sleep' | 'recording'): void }): () => void;
  /**
   * Calls `handler` when the page is going away (a reload or a navigation
   * that unloads it); returns the unsubscribe function.
   */
  onPageHide?(handler: () => void): () => void;
  isCommandNotFound(err: unknown): boolean;
  versionSkewMessage: string;
  timers?: IdleStopTimers;
  now?: () => number;
}

const realTimers: IdleStopTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
};

export class InterpretController<Config, Entry extends InterpretEntry, Input> {
  private state: InterpretState<Entry, Input> = {
    phase: 'idle', translations: [], error: null, notice: null, warnings: [], input: null, startedAt: null,
  };
  /** Bumped by every start and every end; a stale value marks a superseded run. */
  private run = 0;
  private sessionNo = 0;
  private stt: InterpretStt | null = null;
  /** The Mac app is capturing for this screen. */
  private capturing = false;
  /** Settles once a start has either installed its capture or cleaned it up. Never waits for the caption stream. */
  private captureStart: Promise<void> | null = null;
  private ready: Promise<void> = Promise.resolve();
  private readonly budget = new ReconnectBudget();
  private idle: IdleStop | null = null;
  private reconnectTimer: unknown = null;
  private graceTimer: unknown = null;
  private unsubscribe: (() => void) | null = null;
  private unwatchPage: (() => void) | null = null;
  private disposed = false;
  private readonly timers: IdleStopTimers;
  private readonly options: InterpretSttOptions;

  constructor(
    private readonly deps: InterpretDeps<Config, Entry, Input>,
    private readonly onChange: (state: InterpretState<Entry, Input>) => void,
    options: InterpretSttOptions,
  ) {
    this.timers = deps.timers ?? realTimers;
    this.options = { ...options };
  }

  /**
   * Stop a capture left running for a page that no longer exists (a reload
   * whose page-hide stop did not reach the Mac app).
   * A later `start` waits for this, so it cannot end the new capture. An old
   * app without the command rejects; that is not an error.
   */
  init(): Promise<void> {
    // A page that goes away without unmounting the screen would leave the
    // Mac app capturing with nothing listening; stop it while the page can.
    this.unwatchPage ??= this.deps.onPageHide?.(() => void this.stop()) ?? null;
    this.ready = this.deps.stopCapture().then(() => undefined, () => undefined);
    return this.ready;
  }

  setTargetLang(lang: string): void {
    this.options.targetLang = lang;
    this.stt?.setTargetLang(lang);
  }

  setInterimTranslation(enabled: boolean): void {
    this.options.interimTranslation = enabled;
    this.stt?.setInterimTranslation(enabled);
  }

  async start(inputDeviceUid?: string): Promise<void> {
    if (this.state.phase !== 'idle') return;
    const run = ++this.run;
    this.sessionNo = 0;
    this.budget.reset();
    this.update({
      phase: 'starting', translations: [], error: null, notice: null, warnings: [], input: null, startedAt: null,
    });

    this.captureStart = (async () => {
      await this.ready;
      const config = await this.deps.loadConfig().catch(() => null);
      if (this.run !== run) return;
      if (!config) {
        await this.end({ error: INTERPRET_MESSAGES.unavailable }, false);
        return;
      }
      let started;
      try {
        started = await this.deps.startCapture(inputDeviceUid);
      } catch (err) {
        if (this.run !== run) return;
        const detail = err instanceof Error ? err.message : String(err);
        await this.end({
          error: this.deps.isCommandNotFound(err)
            ? this.deps.versionSkewMessage
            : `${INTERPRET_MESSAGES.startFailed}: ${detail}`,
        }, false);
        return;
      }
      if (this.run !== run) {
        // Stopped or left while the Mac app was still starting the capture.
        await this.deps.stopCapture().catch(() => undefined);
        return;
      }
      this.capturing = true;
      this.unsubscribe = this.deps.subscribe({
        onChunk: (chunk) => this.stt?.push(chunk),
        onStopped: (reason) => {
          if (this.run !== run) return;
          // The Mac app already stopped the capture.
          this.capturing = false;
          void this.end({
            notice: reason === 'system-sleep' ? INTERPRET_MESSAGES.sleepStopped : INTERPRET_MESSAGES.recordingStopped,
          }, false);
        },
      });
      this.idle = new IdleStop(INTERPRET_IDLE_STOP_MS, () => {
        if (this.run === run) void this.end({ notice: INTERPRET_MESSAGES.idleStopped }, true);
      }, this.timers);
      this.idle.start();
      this.update({
        phase: 'running',
        warnings: started.warnings ?? [],
        input: started.input ?? null,
        startedAt: (this.deps.now ?? Date.now)(),
      });
      this.connect(run, config);
    })();
    await this.captureStart;
  }

  async stop(): Promise<void> {
    if (this.state.phase === 'idle' || this.state.phase === 'stopping') return;
    this.update({ phase: 'stopping' });
    // Supersede the run first: a capture start still in flight then stops
    // the capture it was creating instead of leaving it running.
    this.run += 1;
    await this.captureStart?.catch(() => undefined);
    await this.end({}, true);
  }

  /** The screen is going away: stop everything and report nothing further. */
  async dispose(): Promise<void> {
    this.disposed = true;
    this.unwatchPage?.();
    this.unwatchPage = null;
    this.run += 1;
    await this.captureStart?.catch(() => undefined);
    await this.end({}, true);
  }

  /** Open one caption session on the running capture. Does not wait for the stream. */
  private connect(run: number, config: Config): void {
    const sessionNo = ++this.sessionNo;
    let connected = false;
    let heard = false;
    // Every callback checks that its run and its session are still current:
    // a superseded session must not add entries, reset the idle timer or
    // start another reconnect.
    const current = () => this.run === run && this.stt === stt;

    const markConnected = () => {
      if (connected) return;
      connected = true;
      this.clearGrace();
      if (this.state.phase === 'reconnecting') this.update({ phase: 'running', notice: null });
    };

    const fail = () => {
      if (!current()) return;
      this.stt = null;
      stt.stop();
      this.clearGrace();
      this.dropPending();
      if (!connected && sessionNo === 1) {
        void this.end({ error: INTERPRET_MESSAGES.unavailable }, true);
        return;
      }
      const delay = this.budget.next();
      if (delay === null) {
        void this.end({ error: INTERPRET_MESSAGES.reconnectFailed }, true);
        return;
      }
      this.update({ phase: 'reconnecting', notice: INTERPRET_MESSAGES.reconnecting });
      this.reconnectTimer = this.timers.set(() => {
        this.reconnectTimer = null;
        if (this.run === run) this.connect(run, config);
      }, delay);
    };

    const stt: InterpretStt = this.deps.createStt(config, { ...this.options }, {
      onTranscript: () => {
        if (!current()) return;
        this.idle?.touch();
        markConnected();
        if (!heard) {
          // A transcript proves the stream is alive again.
          heard = true;
          this.budget.reset();
        }
      },
      onTranslation: (entry) => {
        if (!current()) return;
        const next: Entry = {
          ...entry,
          id: `${sessionNo}/${entry.id}`,
          order: sessionNo * SESSION_ORDER_SPAN + entry.order,
        };
        this.update({
          translations: [...this.state.translations.filter((item) => item.id !== next.id), next]
            .sort((left, right) => left.order - right.order)
            .slice(-MAX_INTERPRET_ENTRIES),
        });
      },
      onReset: () => {
        if (current()) this.dropPending();
      },
      onUnavailable: fail,
    });
    this.stt = stt;
    this.graceTimer = this.timers.set(() => {
      this.graceTimer = null;
      if (current()) markConnected();
    }, CONNECT_GRACE_MS);
    // The promise settles when the stream ends. While this session is still
    // current that is a drop, however it ended.
    void stt.start().then(fail, fail);
  }

  /** Stop everything this run owns. Safe in any phase and safe to repeat. */
  private async end(outcome: { error?: string; notice?: string }, stopCapture: boolean): Promise<void> {
    const ending = ++this.run;
    if (this.reconnectTimer !== null) this.timers.clear(this.reconnectTimer);
    this.reconnectTimer = null;
    this.clearGrace();
    this.idle?.stop();
    this.idle = null;
    const stt = this.stt;
    this.stt = null;
    stt?.stop();
    this.unsubscribe?.();
    this.unsubscribe = null;
    this.dropPending();
    const wasCapturing = this.capturing;
    this.capturing = false;
    if (stopCapture && wasCapturing) {
      // Until the Mac app has stopped, the screen is neither running nor
      // idle: `stopping` keeps Stop and Start from acting on a capture that
      // is on its way out.
      this.update({ phase: 'stopping' });
      await this.deps.stopCapture().catch(() => undefined);
    }
    // A newer run must never be reported idle by an older end.
    if (this.run !== ending) return;
    this.update({
      phase: 'idle',
      startedAt: null,
      // A progress notice such as "reconnecting" ends with the session.
      notice: outcome.notice ?? null,
      ...(outcome.error ? { error: outcome.error } : {}),
    });
  }

  private clearGrace(): void {
    if (this.graceTimer !== null) this.timers.clear(this.graceTimer);
    this.graceTimer = null;
  }

  /** Remove captions whose translation was abandoned; they would read "translating" forever. */
  private dropPending(): void {
    const kept = this.state.translations.filter((item) => item.status !== 'pending');
    if (kept.length !== this.state.translations.length) this.update({ translations: kept });
  }

  private update(patch: Partial<InterpretState<Entry, Input>>): void {
    this.state = { ...this.state, ...patch };
    if (!this.disposed) this.onChange(this.state);
  }
}
