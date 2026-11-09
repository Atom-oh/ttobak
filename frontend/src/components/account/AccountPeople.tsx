'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { LOAD_MORE_LABEL, TRUNCATED_NOTICE } from '@/lib/accountOverview';
import {
  MENTION_IGNORE_LABEL, MENTION_PICK_LABEL, MENTION_RESOLVE_LABEL, MENTIONS_EMPTY_TEXT, PEOPLE_ACTION_FAILED,
  PEOPLE_CONTACTS_UNAVAILABLE, PEOPLE_EMPTY_TEXT, PEOPLE_PANEL_TITLE, PEOPLE_RESOLVED_HEADING, PEOPLE_TRUNCATED_NOTE,
  PEOPLE_UNRESOLVED_HEADING, meetingCountText, mergeMentionPages, mergePeoplePages, parsePersonOption,
  personOptionValue, roleSummary,
} from '@/lib/accountPeople';
import type { AccountContact, AccountPeoplePage, AccountSpeakerTeamMember, MentionGroup, MentionGroupsPage } from '@/types/meeting';

export interface AccountPeopleData { people: AccountPeoplePage; mentions: MentionGroupsPage }

type Options = { members: AccountSpeakerTeamMember[]; contacts: AccountContact[]; teamCursor: string; contactCursor: string };

function failureStatus(failure: unknown): number | undefined {
  return (failure as { status?: number })?.status;
}

/**
 * Resolved people and unresolved mention groups of the account (ADR-065).
 * The parent loads the first pages; actions call `onChanged` to reload them.
 */
export function AccountPeople({ accountId, userId, data, onChanged, onAccessLost }: {
  accountId: string;
  userId: string;
  data: AccountPeopleData;
  onChanged: () => void;
  onAccessLost: () => void;
}) {
  const [people, setPeople] = useState(data.people.people ?? []);
  const [peopleCursor, setPeopleCursor] = useState(data.people.nextCursor || '');
  const [groups, setGroups] = useState(data.mentions.groups ?? []);
  const [groupCursor, setGroupCursor] = useState(data.mentions.nextCursor || '');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [note, setNote] = useState('');
  const [picking, setPicking] = useState('');
  const [choice, setChoice] = useState('');
  const [options, setOptions] = useState<Options | null>(null);
  const controller = useRef<AbortController | null>(null);

  useEffect(() => {
    setPeople(data.people.people ?? []);
    setPeopleCursor(data.people.nextCursor || '');
    setGroups(data.mentions.groups ?? []);
    setGroupCursor(data.mentions.nextCursor || '');
  }, [data]);
  useEffect(() => () => controller.current?.abort(), []);

  const fail = useCallback((failure: unknown) => {
    const status = failureStatus(failure);
    if (status === 403) { onAccessLost(); return; }
    setError(status === 503 ? PEOPLE_CONTACTS_UNAVAILABLE : PEOPLE_ACTION_FAILED);
  }, [onAccessLost]);

  const run = async (key: string, action: (signal: AbortSignal) => Promise<void>) => {
    controller.current?.abort();
    const active = new AbortController();
    controller.current = active;
    setBusy(key); setError('');
    try {
      await action(active.signal);
    } catch (failure) {
      if (!active.signal.aborted) fail(failure);
    } finally {
      if (!active.signal.aborted) setBusy('');
    }
  };

  const morePeople = () => run('more-people', async signal => {
    const page = await accountApi.people.list(accountId, peopleCursor, { signal, expectedUserId: userId });
    if (signal.aborted) return;
    setPeople(previous => mergePeoplePages(previous, page.people ?? []));
    setPeopleCursor(page.nextCursor || '');
  });

  const moreGroups = () => run('more-groups', async signal => {
    const page = await accountApi.people.mentions(accountId, groupCursor, { signal, expectedUserId: userId });
    if (signal.aborted) return;
    setGroups(previous => mergeMentionPages(previous, page.groups ?? []));
    setGroupCursor(page.nextCursor || '');
  });

  const loadOptions = (teamCursor: string, contactCursor: string, append: boolean) => run('options', async signal => {
    const [team, contacts] = await Promise.all([
      append && !teamCursor ? null : accountApi.speakerTeam(accountId, userId, teamCursor, signal),
      append && !contactCursor ? null : accountApi.contacts(accountId, userId, contactCursor, signal),
    ]);
    if (signal.aborted) return;
    setOptions(previous => ({
      members: [...(append ? previous?.members ?? [] : []), ...(team?.members ?? [])],
      contacts: [...(append ? previous?.contacts ?? [] : []), ...(contacts?.contacts ?? [])],
      teamCursor: team ? team.nextCursor || '' : previous?.teamCursor || '',
      contactCursor: contacts ? contacts.nextCursor || '' : previous?.contactCursor || '',
    }));
  });

  const startPick = (group: MentionGroup) => {
    setPicking(group.key); setChoice(''); setNote('');
    if (!options) loadOptions('', '', false);
  };

  const resolve = (group: MentionGroup) => {
    const target = parsePersonOption(choice);
    if (!target) return;
    void run(`resolve:${group.key}`, async signal => {
      const result = await accountApi.people.resolveMention(accountId, group.key, target, { signal, expectedUserId: userId });
      if (signal.aborted) return;
      setPicking(''); setChoice('');
      setNote(result?.remaining ? PEOPLE_TRUNCATED_NOTE : '');
      onChanged();
    });
  };

  const ignore = (group: MentionGroup) => run(`ignore:${group.key}`, async signal => {
    await accountApi.people.ignoreMention(accountId, group.key, { signal, expectedUserId: userId });
    if (signal.aborted) return;
    setPicking(''); setNote('');
    onChanged();
  });

  const truncated = data.people.truncated || data.mentions.truncated;
  return <section className="mb-8" aria-labelledby="account-people-heading">
    <h3 id="account-people-heading" className="mb-3 text-base font-bold text-text-main">{PEOPLE_PANEL_TITLE}</h3>
    <div className="glass-panel space-y-4 rounded-xl p-4">
      {truncated && <p role="status" className="text-xs text-text-secondary">{TRUNCATED_NOTICE}</p>}
      {error && <p role="alert" className="text-sm text-error">{error}</p>}
      {note && <p role="status" className="text-sm text-text-secondary">{note}</p>}

      <div>
        <h4 className="mb-2 text-sm font-semibold text-text-main">{PEOPLE_RESOLVED_HEADING}</h4>
        {people.length === 0 && <p className="text-sm text-text-secondary">{PEOPLE_EMPTY_TEXT}</p>}
        <ul className="space-y-1">
          {people.map(person => <li key={`${person.kind}:${person.id}`} className="flex flex-wrap items-center gap-2 text-sm">
            <span className="material-symbols-outlined text-base text-text-secondary" aria-hidden="true">{person.kind === 'member' ? 'account_circle' : 'person'}</span>
            <span className="min-w-0 break-all text-text-main">{person.label}</span>
            <span className="text-xs text-text-secondary">{meetingCountText(person.meetings)}</span>
            {roleSummary(person.roles) && <span className="text-xs text-text-secondary">{roleSummary(person.roles)}</span>}
          </li>)}
        </ul>
        {peopleCursor && <button type="button" disabled={!!busy} onClick={() => void morePeople()}
          className="mt-2 text-sm text-primary disabled:opacity-40">{LOAD_MORE_LABEL}</button>}
      </div>

      <div className="border-t border-border-subtle pt-4">
        <h4 className="mb-2 text-sm font-semibold text-text-main">{PEOPLE_UNRESOLVED_HEADING}</h4>
        {groups.length === 0 && <p className="text-sm text-text-secondary">{MENTIONS_EMPTY_TEXT}</p>}
        <ul className="space-y-2">
          {groups.map(group => <li key={group.key} className="border-b border-border-subtle pb-2 text-sm last:border-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="min-w-0 break-all font-medium text-text-main">{group.text}</span>
              <span className="text-xs text-text-secondary">{meetingCountText(group.meetings)}</span>
              <span className="ml-auto flex gap-2">
                <button type="button" disabled={!!busy} onClick={() => startPick(group)}
                  className="text-xs font-semibold text-primary disabled:opacity-40">{MENTION_RESOLVE_LABEL}</button>
                <button type="button" disabled={!!busy} onClick={() => void ignore(group)}
                  className="text-xs text-text-secondary disabled:opacity-40">{MENTION_IGNORE_LABEL}</button>
              </span>
            </div>
            {picking === group.key && <div className="mt-2 flex flex-wrap items-center gap-2">
              <select aria-label={`${group.text} ${MENTION_PICK_LABEL}`} value={choice} onChange={event => setChoice(event.target.value)}
                className="min-w-0 max-w-full rounded-lg border border-border-subtle bg-surface px-2 py-1.5 text-xs text-text-main">
                <option value="">{MENTION_PICK_LABEL}</option>
                {!!options?.members.length && <optgroup label="Account 팀">{options.members.map(member =>
                  <option key={member.userId} value={personOptionValue('member', member.userId)}>{[member.name, member.email].filter(Boolean).join(' · ')}</option>)}</optgroup>}
                {!!options?.contacts.length && <optgroup label="고객 연락처">{options.contacts.map(contact =>
                  <option key={contact.contactId} value={personOptionValue('contact', contact.contactId)}>{[contact.name, contact.company, contact.title].filter(Boolean).join(' · ')}</option>)}</optgroup>}
              </select>
              <button type="button" disabled={!!busy || !parsePersonOption(choice)} onClick={() => resolve(group)}
                className="rounded-lg bg-primary px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-40">{MENTION_RESOLVE_LABEL}</button>
              <button type="button" onClick={() => setPicking('')} className="text-xs text-text-secondary">취소</button>
              {(options?.teamCursor || options?.contactCursor) && <button type="button" disabled={!!busy}
                onClick={() => void loadOptions(options.teamCursor, options.contactCursor, true)}
                className="text-xs text-primary disabled:opacity-40">{LOAD_MORE_LABEL}</button>}
            </div>}
          </li>)}
        </ul>
        {groupCursor && <button type="button" disabled={!!busy} onClick={() => void moreGroups()}
          className="mt-2 text-sm text-primary disabled:opacity-40">{LOAD_MORE_LABEL}</button>}
      </div>
    </div>
  </section>;
}
