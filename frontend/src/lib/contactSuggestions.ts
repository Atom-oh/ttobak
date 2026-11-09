import type { AccountContact, AccountSpeakerTeamMember } from '@/types/meeting';

export interface SpeakerSuggestions {
  scope: string;
  members: AccountSpeakerTeamMember[];
  contacts: AccountContact[];
  nextCursor: string;
  teamCursor: string;
}

export function suggestionScope(accountId: string, userId: string, membershipRevision: number): string {
  return JSON.stringify([accountId, userId, membershipRevision]);
}

export function visibleSuggestions(value: SpeakerSuggestions | null, scope: string): SpeakerSuggestions {
  return value?.scope === scope ? value : { scope, members: [], contacts: [], nextCursor: '', teamCursor: '' };
}

/** A speaker label the user filled from a member or contact suggestion. */
export interface SpeakerPick { label: string; kind: 'member' | 'contact'; id: string; name: string }

/**
 * Picks to bind after a speaker-name save. A pick counts only while the saved
 * name still equals the picked name (an edited name binds nothing); the last
 * pick per label wins.
 */
export function bindingsToSave(saved: Record<string, string>, picks: SpeakerPick[]): SpeakerPick[] {
  const last = new Map<string, SpeakerPick>();
  for (const pick of picks) last.set(pick.label, pick);
  return Array.from(last.values()).filter(pick =>
    Object.prototype.hasOwnProperty.call(saved, pick.label) && saved[pick.label] === pick.name);
}
