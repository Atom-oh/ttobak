// Local intake state under ~/.ttobak/intake (0700; files 0600, atomic rename).
// It holds the selection (references to host entries, never their values),
// per-source calendar ledgers and pending batches, run status and a lock.

import { chmodSync, closeSync, constants, fstatSync, fsyncSync, lstatSync, mkdirSync, openSync, readFileSync,
  renameSync, unlinkSync, writeSync } from 'node:fs';
import { join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { IntakeError } from './errors.js';

const MAX_STATE_BYTES = 16 * 1024 * 1024;
export const MAX_APPEND_LINE_BYTES = 4 * 1024;

export class IntakeStore {
  readonly directory: string;

  constructor(home: string) {
    this.directory = join(home, '.ttobak', 'intake');
  }

  private ensureDirectory(path: string): void {
    try { mkdirSync(path, { recursive: true, mode: 0o700 }); } catch {
      throw new IntakeError('STORE_INVALID', 'state directory could not be created');
    }
    const stat = lstatSync(path);
    const uid = process.getuid?.();
    if (!stat.isDirectory() || stat.isSymbolicLink() || (uid !== undefined && stat.uid !== uid)) {
      throw new IntakeError('STORE_INVALID', 'state directory has the wrong type or owner');
    }
    if ((stat.mode & 0o077) !== 0) chmodSync(path, 0o700);
  }

  private path(relative: string[]): string {
    for (const segment of relative) {
      if (!/^[A-Za-z0-9_.-]{1,128}$/.test(segment) || segment === '.' || segment === '..') {
        throw new IntakeError('STORE_INVALID', 'invalid state path');
      }
    }
    return join(this.directory, ...relative);
  }

  read<T>(...relative: string[]): T | null {
    const path = this.path(relative);
    let stat;
    try { stat = lstatSync(path); } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
      throw new IntakeError('STORE_INVALID', 'state file unreadable');
    }
    const uid = process.getuid?.();
    if (!stat.isFile() || stat.isSymbolicLink() || (uid !== undefined && stat.uid !== uid) ||
        stat.size > MAX_STATE_BYTES) throw new IntakeError('STORE_INVALID', 'state file rejected');
    try { return JSON.parse(readFileSync(path, 'utf8')) as T; } catch {
      throw new IntakeError('STORE_INVALID', 'state file is not valid JSON');
    }
  }

  write(value: unknown, ...relative: string[]): void {
    this.writeText(JSON.stringify(value, null, 2) + '\n', ...relative);
  }

  /** Atomically replaces a file with `data` (0600, fsync, rename). */
  writeText(data: string, ...relative: string[]): void {
    const path = this.path(relative);
    this.ensureDirectory(this.directory);
    for (let index = 1; index < relative.length; index++) this.ensureDirectory(this.path(relative.slice(0, index)));
    if (Buffer.byteLength(data) > MAX_STATE_BYTES) throw new IntakeError('STORE_INVALID', 'state too large');
    const temporary = `${path}.${randomBytes(6).toString('hex')}.tmp`;
    const descriptor = openSync(temporary, 'wx', 0o600);
    try {
      writeSync(descriptor, data);
      fsyncSync(descriptor);
    } finally {
      closeSync(descriptor);
    }
    try { renameSync(temporary, path); } catch (error) {
      try { unlinkSync(temporary); } catch { /* best effort */ }
      throw error;
    }
  }

  /** The raw text of a state file, or null when absent. Same owner/type checks as `read`. */
  readText(...relative: string[]): string | null {
    const path = this.path(relative);
    let stat;
    try { stat = lstatSync(path); } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
      throw new IntakeError('STORE_INVALID', 'state file unreadable');
    }
    const uid = process.getuid?.();
    if (!stat.isFile() || stat.isSymbolicLink() || (uid !== undefined && stat.uid !== uid) ||
        stat.size > MAX_STATE_BYTES) throw new IntakeError('STORE_INVALID', 'state file rejected');
    try { return readFileSync(path, 'utf8'); } catch { throw new IntakeError('STORE_INVALID', 'state file unreadable'); }
  }

  /**
   * Appends one line (plus a newline) to a state file: O_APPEND|O_CREAT, 0600,
   * never following a symlink. The line must be at most 4 KiB and hold no CR/LF.
   */
  append(line: string, ...relative: string[]): void {
    if (typeof line !== 'string' || /[\r\n]/.test(line) || Buffer.byteLength(line) > MAX_APPEND_LINE_BYTES) {
      throw new IntakeError('STORE_INVALID', 'append line rejected');
    }
    const path = this.path(relative);
    this.ensureDirectory(this.directory);
    for (let index = 1; index < relative.length; index++) this.ensureDirectory(this.path(relative.slice(0, index)));
    const uid = process.getuid?.();
    const check = (stat: { isFile(): boolean; isSymbolicLink?(): boolean; uid: number; size: number }): void => {
      if (!stat.isFile() || stat.isSymbolicLink?.() || (uid !== undefined && stat.uid !== uid) ||
          stat.size > MAX_STATE_BYTES) throw new IntakeError('STORE_INVALID', 'state file rejected');
    };
    try { check(lstatSync(path)); } catch (error) {
      if (error instanceof IntakeError) throw error;
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw new IntakeError('STORE_INVALID', 'state file unreadable');
    }
    let descriptor: number;
    try {
      descriptor = openSync(path, constants.O_APPEND | constants.O_CREAT | constants.O_WRONLY | constants.O_NOFOLLOW, 0o600);
    } catch { throw new IntakeError('STORE_INVALID', 'state file rejected'); }
    try {
      check(fstatSync(descriptor));
      writeSync(descriptor, line + '\n');
      fsyncSync(descriptor);
    } finally {
      closeSync(descriptor);
    }
  }

  /** Size in bytes of a state file, or 0 when absent. */
  size(...relative: string[]): number {
    try { return lstatSync(this.path(relative)).size; } catch { return 0; }
  }

  /**
   * Exclusive run lock. The lock file holds this run's PID and a random token,
   * and only that token's owner removes it. A lock whose owner is dead (or an
   * empty lock older than a few seconds, from a crash before its PID was
   * written) is replaced under a second exclusive `.break` lock, which
   * serializes recovery: a run that read the stale owner earlier re-reads it
   * there and backs off once another run holds a live lock.
   */
  lock(name: string): () => void {
    this.ensureDirectory(this.directory);
    const path = this.path([`${name}.lock`]);
    const token = `${process.pid} ${randomBytes(16).toString('hex')}`;
    if (!createExclusive(path, token)) {
      const breaker = this.path([`${name}.lock.break`]);
      clearAbandoned(breaker);
      if (!createExclusive(breaker, String(process.pid))) {
        throw new IntakeError('LOCKED', 'another intake run is in progress');
      }
      try {
        if (!stale(path) || !removeIfUnchanged(path) || !createExclusive(path, token)) {
          throw new IntakeError('LOCKED', 'another intake run is in progress');
        }
      } finally {
        try { unlinkSync(breaker); } catch { /* already gone */ }
      }
    }
    let released = false;
    return () => {
      if (released) return;
      released = true;
      removeIfOwned(path, token);
    };
  }
}

/** Creates `path` with `content` only if it does not exist. */
function createExclusive(path: string, content: string): boolean {
  let descriptor: number;
  try { descriptor = openSync(path, 'wx', 0o600); } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'EEXIST') return false;
    throw new IntakeError('STORE_INVALID', 'lock unavailable');
  }
  try {
    writeSync(descriptor, content);
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
  return true;
}

const EMPTY_LOCK_GRACE_MS = 5_000;
const ABANDONED_BREAK_MS = 30_000;

function lockContent(path: string): string | null {
  try { return readFileSync(path, 'utf8'); } catch { return null; }
}

/** The lock's owner is dead, or it is an empty lock past its grace period. */
function stale(path: string): boolean {
  const content = lockContent(path);
  if (content === null) return true;
  const owner = Number.parseInt(content.split(' ')[0] ?? '', 10);
  if (Number.isSafeInteger(owner) && owner > 0) return !alive(owner);
  try { return Date.now() - lstatSync(path).mtimeMs > EMPTY_LOCK_GRACE_MS; } catch { return true; }
}

/** Removes the lock only if it still holds what `stale` just read. */
function removeIfUnchanged(path: string): boolean {
  const before = lockContent(path);
  if (before === null) return true;
  if (!stale(path) || lockContent(path) !== before) return false;
  try { unlinkSync(path); } catch { /* gone */ }
  return true;
}

function removeIfOwned(path: string, token: string): void {
  if (lockContent(path) !== token) return;
  try { unlinkSync(path); } catch { /* already gone */ }
}

/** A `.break` lock left by a run that crashed mid-recovery. */
function clearAbandoned(breaker: string): void {
  try {
    const stat = lstatSync(breaker);
    const owner = Number.parseInt(lockContent(breaker) ?? '', 10);
    const dead = Number.isSafeInteger(owner) && owner > 0 ? !alive(owner) : true;
    if (dead && Date.now() - stat.mtimeMs > ABANDONED_BREAK_MS) unlinkSync(breaker);
  } catch { /* absent or raced */ }
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === 'EPERM';
  }
}
