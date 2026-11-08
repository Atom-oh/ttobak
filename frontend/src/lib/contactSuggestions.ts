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
