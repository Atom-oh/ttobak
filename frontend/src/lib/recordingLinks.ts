// Customer/project links chosen while a recording is running. The server
// meeting usually exists from recording start (draft), but draft creation can
// fail and fall back to creation at save time; selections made before then
// are kept here and applied once a meeting ID exists.

export type RecordingAccountChange =
  /** No server meeting yet: the creation path validates and applies it. */
  | { kind: 'defer' }
  /** Already the meeting's account; nothing to send. */
  | { kind: 'none' }
  /** Link the existing meeting now (the save step retries it on failure). */
  | { kind: 'link'; meetingId: string; accountId: string }
  /** No API removes a meeting's account; it can only be replaced. */
  | { kind: 'locked' };

export function planRecordingAccountChange(
  meetingId: string | null, boundAccountId: string | undefined, nextAccountId: string | undefined,
): RecordingAccountChange {
  if (!meetingId) return { kind: 'defer' };
  if (!nextAccountId) return boundAccountId ? { kind: 'locked' } : { kind: 'none' };
  if (nextAccountId === boundAccountId) return { kind: 'none' };
  return { kind: 'link', meetingId, accountId: nextAccountId };
}

export function normalizeProjectIds(ids: readonly string[]): string[] {
  return [...new Set(ids.filter(Boolean))];
}

export function planProjectLinks(linked: readonly string[], desired: readonly string[], attempted: readonly string[] = []) {
  const have = new Set(linked), want = new Set(desired);
  return {
    link: normalizeProjectIds(desired).filter((id) => !have.has(id)),
    unlink: normalizeProjectIds([...linked, ...attempted]).filter((id) => !want.has(id)),
  };
}

export function hasPendingProjectLinks(linked: readonly string[], desired: readonly string[], attempted: readonly string[] = []) {
  const plan = planProjectLinks(linked, desired, attempted);
  return plan.link.length > 0 || plan.unlink.length > 0;
}

export interface ProjectLinkTransport {
  link: (projectId: string, meetingId: string) => Promise<unknown>;
  unlink: (projectId: string, meetingId: string) => Promise<unknown>;
  isNotFound?: (error: unknown) => boolean;
}

export interface ProjectLinkFailure { projectId: string; action: 'link' | 'unlink'; message: string }

/**
 * Applies every pending project change, continuing past individual failures
 * so one rejected project does not hide the others. Returns the project IDs
 * now known to be linked plus each failure; callers surface failures.
 */
export async function syncProjectLinks(
  meetingId: string, linked: readonly string[], desired: readonly string[], transport: ProjectLinkTransport,
  attempted: readonly string[] = [],
): Promise<{ linked: string[]; attempted: string[]; failures: ProjectLinkFailure[] }> {
  const plan = planProjectLinks(linked, desired, attempted);
  const current = new Set(linked);
  const possiblyLinked = new Set(attempted);
  const failures: ProjectLinkFailure[] = [];
  for (const projectId of plan.link) {
    possiblyLinked.add(projectId);
    try {
      await transport.link(projectId, meetingId);
      current.add(projectId);
    } catch (error) {
      failures.push({ projectId, action: 'link', message: error instanceof Error ? error.message : String(error) });
    }
  }
  for (const projectId of plan.unlink) {
    current.delete(projectId);
    possiblyLinked.add(projectId);
    try {
      await transport.unlink(projectId, meetingId);
      possiblyLinked.delete(projectId);
    } catch (error) {
      if (transport.isNotFound?.(error)) {
        possiblyLinked.delete(projectId);
      } else {
        failures.push({ projectId, action: 'unlink', message: error instanceof Error ? error.message : String(error) });
      }
    }
  }
  return { linked: [...current], attempted: [...possiblyLinked], failures };
}

export function projectLinkFailureMessage(failures: readonly ProjectLinkFailure[]): string | null {
  if (!failures.length) return null;
  const linking = failures.some((failure) => failure.action === 'link');
  const unlinking = failures.some((failure) => failure.action === 'unlink');
  const what = linking && unlinking ? '프로젝트 연결·해제' : linking ? '프로젝트 연결' : '프로젝트 연결 해제';
  return `${what}에 실패했습니다 (${failures.length}건).`;
}

/** Pending project changes as deltas: additions not yet confirmed linked, and
 * removals of projects that are or may be linked. */
export function projectLinkDeltas(linked: readonly string[], desired: readonly string[], attempted: readonly string[] = []) {
  const want = new Set(normalizeProjectIds(desired));
  const have = new Set(normalizeProjectIds(linked));
  return {
    projectAdditions: [...want].filter(id => !have.has(id)).sort(),
    projectRemovals: normalizeProjectIds([...linked, ...attempted]).filter(id => !want.has(id)),
  };
}

/** Applies saved deltas to the meeting's current links. Removals are also
 * marked possibly linked so an unconfirmed one is cleaned up. */
export function restoreProjectLinks(current: readonly string[], additions: readonly string[], removals: readonly string[]) {
  const removed = new Set(normalizeProjectIds(removals));
  const linked = normalizeProjectIds(current);
  return {
    linked,
    desired: normalizeProjectIds([...linked, ...additions]).filter(id => !removed.has(id)),
    attempted: [...removed],
  };
}
