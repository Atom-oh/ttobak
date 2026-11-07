// Account-team management of published meetings (ADR-016 2026-11-06
// amendment): the creator or a live account member manages everything except
// delete. The server's canManage flag is authoritative; these helpers only
// pick the right gates and predecessor candidates.

export interface MeetingCapabilities {
  isCreator: boolean;
  canManage: boolean;
  canDelete: boolean;
}

export function meetingCapabilities(meeting: { userId?: string; canManage?: boolean }, viewerId?: string): MeetingCapabilities {
  const isCreator = !!viewerId && meeting.userId === viewerId;
  const canManage = meeting.canManage ?? isCreator;
  return { isCreator, canManage, canDelete: isCreator };
}

/**
 * Predecessors must be the meeting creator's own meetings. Own list items
 * omit ownerUserId (they belong to the viewer); shared items name the creator.
 */
export function isPredecessorOwnerMatch(
  candidate: { isShared?: boolean; ownerUserId?: string },
  ownerUserId: string | undefined,
  viewerIsCreator: boolean,
): boolean {
  if (!ownerUserId) return true;
  return candidate.isShared ? candidate.ownerUserId === ownerUserId : viewerIsCreator;
}
