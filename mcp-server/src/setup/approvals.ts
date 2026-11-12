// Items the user approved with [Install] on this Mac, so a later [Setup]
// installs them without asking again. ~/.ttobak/setup/approvals.json, 0600.

import { mkdirSync, readFileSync, renameSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const MAX_BYTES = 256 * 1024;
const MAX_KEYS = 500;

const file = (home: string) => join(home, '.ttobak', 'setup', 'approvals.json');

export function readApprovals(home: string): Set<string> {
  try {
    if (statSync(file(home)).size > MAX_BYTES) return new Set();
    const value = JSON.parse(readFileSync(file(home), 'utf8')) as { approved?: unknown };
    return new Set(Array.isArray(value.approved) ? value.approved.filter((key): key is string => typeof key === 'string') : []);
  } catch { return new Set(); }
}

export function addApprovals(home: string, keys: string[]): void {
  const approved = readApprovals(home);
  for (const key of keys) approved.add(key);
  const directory = join(home, '.ttobak', 'setup');
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const temporary = `${file(home)}.tmp-${process.pid}`;
  writeFileSync(temporary, JSON.stringify({ approved: [...approved].slice(-MAX_KEYS) }) + '\n', { mode: 0o600 });
  renameSync(temporary, file(home));
}
