// Single-run lock for intake commands that touch connectors or state. A second
// concurrent run fails fast with RUN_IN_PROGRESS; a lock whose process is gone
// is reclaimed. The file holds only a PID and a timestamp.

import { mkdirSync, openSync, closeSync, readFileSync, writeSync, unlinkSync, statSync, constants } from 'node:fs';
import { join } from 'node:path';
import { IntakeError } from './errors.js';

export interface RunLock { release(): Promise<void> }

const LOCK_FILE = 'run.lock';
/** An unreadable lock younger than this may still be mid-write by its owner. */
const UNREADABLE_GRACE_MS = 5000;

function alive(pid: number): boolean {
  try { process.kill(pid, 0); return true; } catch (error) {
    return (error as NodeJS.ErrnoException).code !== 'ESRCH';
  }
}

function holderIsLive(path: string, now: () => number): boolean {
  let pid: unknown;
  try { pid = (JSON.parse(readFileSync(path, 'utf8')) as { pid?: unknown }).pid; } catch {
    try { return now() - statSync(path).mtimeMs < UNREADABLE_GRACE_MS; } catch { return false; }
  }
  return typeof pid === 'number' && Number.isInteger(pid) && pid > 0 && alive(pid);
}

export async function acquireRunLock(dir: string, now: () => number = Date.now): Promise<RunLock> {
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const path = join(dir, LOCK_FILE);
  for (let attempt = 0; attempt < 3; attempt++) {
    let fd: number;
    try {
      fd = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL, 0o600);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw new IntakeError('STORE_INVALID', 'run lock could not be created');
      if (holderIsLive(path, now)) throw new IntakeError('RUN_IN_PROGRESS');
      try { unlinkSync(path); } catch { /* another run reclaimed it first */ }
      continue;
    }
    try { writeSync(fd, JSON.stringify({ pid: process.pid, startedAt: new Date(now()).toISOString() })); } finally { closeSync(fd); }
    let released = false;
    return {
      async release() {
        if (released) return;
        released = true;
        try {
          const held = JSON.parse(readFileSync(path, 'utf8')) as { pid?: unknown };
          if (held.pid === process.pid) unlinkSync(path);
        } catch { /* already gone */ }
      },
    };
  }
  throw new IntakeError('RUN_IN_PROGRESS');
}
