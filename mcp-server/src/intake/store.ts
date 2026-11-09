// Local intake state under ~/.ttobak/intake (0700; files 0600, atomic rename).
// It holds the selection (references to host entries, never their values),
// per-source calendar ledgers and pending batches, run status and a lock.

import { chmodSync, closeSync, fsyncSync, lstatSync, mkdirSync, openSync, readFileSync, renameSync,
  unlinkSync, writeSync } from 'node:fs';
import { join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { IntakeError } from './errors.js';

const MAX_STATE_BYTES = 16 * 1024 * 1024;

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
    const path = this.path(relative);
    this.ensureDirectory(this.directory);
    for (let index = 1; index < relative.length; index++) this.ensureDirectory(this.path(relative.slice(0, index)));
    const data = JSON.stringify(value, null, 2) + '\n';
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

  /** Exclusive run lock. A lock left by a dead process is replaced once. */
  lock(name: string): () => void {
    this.ensureDirectory(this.directory);
    const path = this.path([`${name}.lock`]);
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        const descriptor = openSync(path, 'wx', 0o600);
        writeSync(descriptor, String(process.pid));
        closeSync(descriptor);
        let released = false;
        return () => {
          if (released) return;
          released = true;
          try { unlinkSync(path); } catch { /* already gone */ }
        };
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw new IntakeError('STORE_INVALID', 'lock unavailable');
        let owner = 0;
        try { owner = Number.parseInt(readFileSync(path, 'utf8'), 10); } catch { /* unreadable */ }
        if (attempt === 0 && Number.isSafeInteger(owner) && owner > 0 && !alive(owner)) {
          try { unlinkSync(path); } catch { /* raced */ }
          continue;
        }
        throw new IntakeError('LOCKED', 'another intake run is in progress');
      }
    }
    throw new IntakeError('LOCKED', 'another intake run is in progress');
  }
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === 'EPERM';
  }
}
