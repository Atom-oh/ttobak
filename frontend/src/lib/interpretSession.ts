// Policy for the Mac interpret-only screen (/interpret): when a session gives
// up reconnecting and when a forgotten session stops itself. Pure: no React,
// no DOM, timers injected, so node:test covers it.

/** A session with no recognized speech for this long stops itself. */
export const INTERPRET_IDLE_STOP_MS = 600_000;

/** Waits before each live-caption reconnect attempt; three attempts in all. */
export const INTERPRET_RECONNECT_DELAYS_MS: readonly number[] = [1000, 2000, 4000];

export const INTERPRET_MESSAGES = {
  notSaved: '통역 내용과 소리는 저장되지 않습니다.',
  idleStopped: '10분 동안 인식된 말이 없어 통역을 멈췄습니다.',
  sleepStopped: 'Mac이 잠자기에 들어가 통역을 멈췄습니다.',
  /** Shown when a recording start ended the session, whether or not that recording then started. */
  recordingStopped: '녹음 시작 요청으로 통역을 멈췄습니다.',
  reconnecting: '다시 연결 중…',
  reconnectFailed: '실시간 자막 연결이 끊어져 통역을 멈췄습니다. 네트워크를 확인한 뒤 다시 시작해 주세요.',
  unavailable: '실시간 자막을 사용할 수 없어 통역을 시작할 수 없습니다 (AWS 인증/연결 필요).',
  macOnly: '통역 전용 모드는 Mac 앱에서만 사용할 수 있습니다.',
  /** Followed by `: <cause>`. */
  startFailed: '통역을 시작하지 못했습니다',
} as const;

/** Hands out the reconnect delays in order, then `null` once they are used up. */
export class ReconnectBudget {
  private used = 0;

  next(): number | null {
    if (this.used >= INTERPRET_RECONNECT_DELAYS_MS.length) return null;
    return INTERPRET_RECONNECT_DELAYS_MS[this.used++];
  }

  reset(): void {
    this.used = 0;
  }
}

export interface IdleStopTimers {
  set(fn: () => void, ms: number): unknown;
  clear(id: unknown): void;
}

const realTimers: IdleStopTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
};

/**
 * Calls `onIdle` once, `ms` after the last `start()` or `touch()`. Only a
 * started timer can be touched, so speech arriving before the session runs,
 * after it stopped, or after it already fired cannot arm it.
 */
export class IdleStop {
  private timer: unknown = null;
  private armed = false;

  constructor(
    private readonly ms: number,
    private readonly onIdle: () => void,
    private readonly timers: IdleStopTimers = realTimers,
  ) {}

  start(): void {
    this.armed = true;
    this.schedule();
  }

  touch(): void {
    if (this.armed) this.schedule();
  }

  stop(): void {
    this.armed = false;
    if (this.timer !== null) this.timers.clear(this.timer);
    this.timer = null;
  }

  private schedule(): void {
    if (this.timer !== null) this.timers.clear(this.timer);
    this.timer = this.timers.set(() => {
      this.timer = null;
      this.armed = false;
      this.onIdle();
    }, this.ms);
  }
}

/**
 * Wraps `load` so a value is fetched once and kept, while a failure (`null`
 * or a rejection, both returned as `null`) is tried again on the next call.
 * Calls made during one load share it.
 */
export function retryUntilLoaded<T>(load: () => Promise<T | null>): () => Promise<T | null> {
  let current: Promise<T | null> | null = null;
  return () => {
    if (current) return current;
    const attempt: Promise<T | null> = load().catch(() => null).then((value) => {
      if (value === null && current === attempt) current = null;
      return value;
    });
    current = attempt;
    return attempt;
  };
}

/** `HH:MM:SS` for the session clock. */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const part = (value: number) => String(value).padStart(2, '0');
  return `${part(Math.floor(total / 3600))}:${part(Math.floor((total % 3600) / 60))}:${part(total % 60)}`;
}
