// `ttobak-mcp team-sync run`: an administrator's team sync. Preview lists the
// pool users through the admin API, looks each verified @amazon.com alias up in
// the internal directory and stores the proposed changes; apply posts the stored
// changes. Only user IDs and team values reach the API. stdout gets exactly one
// JSON line; stderr gets `team-sync: <n>/<total>` and never an alias.

import { randomBytes } from 'node:crypto';
import { unlinkSync } from 'node:fs';
import { join } from 'node:path';
import { ApiResponseError, type TeamSyncResult } from '../api.js';
import { deliveryFailure } from '../intake/batch.js';
import { IntakeError, intakeCode } from '../intake/errors.js';
import { signIn, type IntakeEnvironment, type Selection } from '../intake/run.js';
import { IntakeStore } from '../intake/store.js';
import { openDirectory, type Directory } from './directory.js';
import { TEAM_PATTERN, directoryAlias, planTeams, type DirectoryPerson } from './rule.js';

export const PREVIEW_FILE = 'team-sync-preview.json';
export const MAX_USERS = 500;
export const PREVIEW_TTL_MS = 15 * 60 * 1000;
const CHUNK = 200;
const PREVIEW_ID = /^[a-f0-9]{32}$/;

export interface TeamSyncEnvironment extends IntakeEnvironment {
  /** Test seam: replaces the `builder-mcp` directory connector. */
  openDirectory?: () => Promise<Directory>;
}

const USAGE = `Usage:
  ttobak-mcp team-sync run
  ttobak-mcp team-sync run --apply --preview-id <32 hex>`;

interface StoredChange { userId: string; alias: string; from: string; to: string }
interface StoredPreview { previewId: string; createdAt: string; changes: StoredChange[] }

function parseFlags(args: string[]): { apply: boolean; previewId?: string } {
  let apply = false;
  let previewId: string | undefined;
  for (let index = 0; index < args.length; index++) {
    const name = args[index];
    if (name === '--apply' && !apply) apply = true;
    else if (name === '--preview-id' && previewId === undefined) previewId = args[++index];
    else throw new IntakeError('INVALID_ARGUMENT', 'unexpected argument');
  }
  if (apply !== (previewId !== undefined) || (previewId !== undefined && !PREVIEW_ID.test(previewId))) {
    throw new IntakeError('INVALID_ARGUMENT', '--apply and --preview-id <32 hex> go together');
  }
  return { apply, ...(previewId !== undefined ? { previewId } : {}) };
}

async function adminCall<T>(call: () => Promise<T>): Promise<T> {
  try { return await call(); } catch (error) {
    if (error instanceof IntakeError) throw error;
    if (error instanceof ApiResponseError && error.status === 403) throw new IntakeError('NOT_ADMIN');
    const failure = deliveryFailure(error);
    if (failure.kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    if (failure.kind === 'rejected') throw new IntakeError('TTOBAK_REJECTED');
    throw new IntakeError('TTOBAK_UNAVAILABLE');
  }
}

async function preview(environment: TeamSyncEnvironment, store: IntakeStore) {
  const { api } = await signIn(store.read<Selection>('selection.json'), environment, false, true);
  if (!api) throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
  const listed = await adminCall(() => api.adminListUsers());
  const users = Array.isArray(listed.users) ? listed.users : [];
  const aliases = [...new Set(users.map(directoryAlias).filter((alias): alias is string => alias !== null))];
  if (aliases.length > MAX_USERS) throw new IntakeError('TOO_MANY_USERS', `more than ${MAX_USERS} directory users`);

  const people = new Map<string, DirectoryPerson | null>();
  if (aliases.length) {
    const directory = await (environment.openDirectory ?? (() => openDirectory(environment)))();
    try {
      for (const [index, alias] of aliases.entries()) {
        people.set(alias, await directory.lookup(alias));
        environment.stderr?.(`team-sync: ${index + 1}/${aliases.length}`);
      }
    } finally {
      await directory.close();
    }
  }
  const plan = planTeams(users, people);
  const previewId = randomBytes(16).toString('hex');
  const stored: StoredPreview = { previewId, createdAt: environment.now().toISOString(), changes: plan.changes };
  store.write(stored, PREVIEW_FILE);
  return {
    ok: true, previewId,
    changes: plan.changes.map(({ alias, from, to }) => ({ alias, from, to })),
    unchanged: plan.unchanged, notFound: plan.notFound, skippedNonDirectory: plan.skippedNonDirectory,
    truncated: listed.truncated === true,
  };
}

function loadPreview(store: IntakeStore, previewId: string, now: Date): StoredChange[] {
  const stored = store.read<StoredPreview>(PREVIEW_FILE);
  const created = stored ? Date.parse(stored.createdAt) : NaN;
  if (!stored || stored.previewId !== previewId || !Number.isFinite(created) || now.getTime() - created > PREVIEW_TTL_MS ||
      created - now.getTime() > 60_000 || !Array.isArray(stored.changes) || stored.changes.length > MAX_USERS) {
    throw new IntakeError('PREVIEW_EXPIRED');
  }
  for (const change of stored.changes) {
    if (typeof change?.userId !== 'string' || typeof change.from !== 'string' || typeof change.to !== 'string' ||
        !TEAM_PATTERN.test(change.to)) throw new IntakeError('PREVIEW_EXPIRED');
  }
  return stored.changes;
}

async function apply(previewId: string, environment: TeamSyncEnvironment, store: IntakeStore) {
  const changes = loadPreview(store, previewId, environment.now());
  const total: TeamSyncResult = { updated: [], unchanged: 0, conflicts: [], rejected: [] };
  if (changes.length) {
    const { api } = await signIn(store.read<Selection>('selection.json'), environment, false, true);
    if (!api) throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    for (let start = 0; start < changes.length; start += CHUNK) {
      const items = changes.slice(start, start + CHUNK).map(change => ({ userId: change.userId, team: change.to, expected: change.from }));
      const result = await adminCall(() => api.syncTeams({ items }));
      total.updated.push(...result.updated);
      total.unchanged += result.unchanged;
      total.conflicts.push(...result.conflicts);
      total.rejected.push(...result.rejected);
      environment.stderr?.(`team-sync: ${Math.min(start + CHUNK, changes.length)}/${changes.length}`);
    }
  }
  // One preview, one apply.
  try { unlinkSync(join(store.directory, PREVIEW_FILE)); } catch { /* already gone */ }
  return { ok: true, ...total };
}

export async function runTeamSync(args: string[], environment: TeamSyncEnvironment): Promise<number> {
  const [command, ...rest] = args;
  if (command !== 'run') {
    environment.stdout(USAGE);
    return command === '--help' || command === 'help' ? 0 : 2;
  }
  const store = new IntakeStore(environment.home);
  let release: (() => void) | undefined;
  try {
    const options = parseFlags(rest);
    release = store.lock('team-sync');
    const result = options.apply ? await apply(options.previewId as string, environment, store)
      : await preview(environment, store);
    environment.stdout(JSON.stringify(result));
    return 0;
  } catch (error) {
    const code = error instanceof IntakeError && error.code === 'LOCKED' ? 'RUN_IN_PROGRESS' : intakeCode(error);
    environment.stdout(JSON.stringify({ ok: false, code }));
    return 2;
  } finally {
    release?.();
  }
}
