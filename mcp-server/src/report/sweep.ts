// Removal of run leftovers of earlier report runs (an app quit or a crash
// mid-run): temp run directories (they hold a copy of the host MCP config) and
// the CLI session directories of their work folders (raw tool results). Called
// only while `report.lock` is held, so no live run can own any of them.

import { lstatSync, readdirSync, realpathSync, rmSync } from 'node:fs';
import { basename, join } from 'node:path';
import { claudeProjectDir } from './claude.js';

export const RUN_DIR_PREFIX = 'ttobak-report-';
/** Exactly a `mkdtemp(<root>/ttobak-report-)` name: six random characters. */
const RUN_DIR = /^ttobak-report-[A-Za-z0-9]{6}$/;
const SESSION_SUFFIX = /^[A-Za-z0-9]{6}-work$/;

/** A real directory (not a link) owned by this user. */
function ownedDirectory(path: string, uid: number | undefined): boolean {
  try {
    const stat = lstatSync(path);
    return stat.isDirectory() && (uid === undefined || stat.uid === uid);
  } catch { return false; }
}

function entries(directory: string): string[] {
  try { return readdirSync(directory); } catch { return []; }
}

/** Removes stale run and session directories; returns how many were removed. Errors are ignored. */
export function sweepStaleRuns(home: string, roots: readonly string[]): number {
  const uid = process.getuid?.();
  const projects = join(home, '.claude', 'projects');
  const realRoots = [...new Set(roots.flatMap(root => { try { return [realpathSync(root)]; } catch { return []; } }))];
  let removed = 0;
  const remove = (path: string) => {
    if (!ownedDirectory(path, uid)) return;
    try { rmSync(path, { recursive: true, force: true }); removed++; } catch { /* best effort */ }
  };
  for (const root of realRoots) {
    for (const name of entries(root)) if (RUN_DIR.test(name)) remove(join(root, name));
    // The session folder name of `<root>/ttobak-report-XXXXXX/work`, by the same formula as the run.
    const prefix = basename(claudeProjectDir(home, join(root, RUN_DIR_PREFIX)));
    for (const name of entries(projects)) {
      if (name.startsWith(prefix) && SESSION_SUFFIX.test(name.slice(prefix.length))) remove(join(projects, name));
    }
  }
  return removed;
}
