'use client';

import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react';
import { accountApi, calendarApi } from '@/lib/api';
import { isEditableAccount } from '@/lib/accountAccess';
import { ongoingEventFor, ongoingEventWindow, shouldAutoLink, type AccountLinkSource } from '@/lib/ongoingEvent';

interface Options {
  userId?: string;
  /** The record page's `referenceAccountId`. */
  accountId: string;
  setAccountId: (accountId: string) => void;
  /** The record page's current title. */
  title: string;
  /** Functional updates keep a title set while the lookup was pending. */
  setTitle: Dispatch<SetStateAction<string>>;
  /** Account already chosen for the recording in progress, if any. */
  recordingAccountId?: string;
  /** True between a recording start and its post-recording step. */
  isRecordingActive: () => boolean;
  /** Applies a change to the recording in progress (draft or pending link). */
  applyToRecording: (accountId: string) => void;
  initialSource: AccountLinkSource;
  /** False in upload mode: an uploaded file is not the ongoing meeting. */
  enabled: boolean;
}

/** Overrides for a draft that must not inherit an auto link. */
export interface DroppedAutoLink {
  accountId: string;
  title?: string;
}

/** Links a recording to the Account of the ongoing calendar event, unless an
 * Account came from the URL, an MCP request or the user. It is applied only by
 * `check()` at recording start, never on mount, so a leftover upload or an
 * unrelated draft is not linked. Calendar or Account failures do nothing;
 * creation still validates membership. */
export function useCalendarAutoLink({ userId, accountId, setAccountId, title, setTitle, recordingAccountId, isRecordingActive, applyToRecording, initialSource, enabled }: Options) {
  const sourceRef = useRef<AccountLinkSource>(initialSource);
  const latestRef = useRef({ userId, accountId, title, recordingAccountId, isRecordingActive, applyToRecording, setAccountId, setTitle, enabled });
  useEffect(() => {
    latestRef.current = { userId, accountId, title, recordingAccountId, isRecordingActive, applyToRecording, setAccountId, setTitle, enabled };
  });
  /** The title the auto link filled, while it may still be on the page. */
  const autoTitleRef = useRef<string | null>(null);
  const inFlightRef = useRef(false);
  const [autoLink, setAutoLink] = useState<{ accountId: string; name: string } | null>(null);

  const check = useCallback(() => {
    const eligible = () => {
      const latest = latestRef.current;
      return latest.enabled && !!latest.userId && shouldAutoLink({ accountId: latest.accountId, recordingAccountId: latest.recordingAccountId, source: sourceRef.current });
    };
    if (inFlightRef.current || !eligible()) return;
    const requestUserId = latestRef.current.userId!;
    inFlightRef.current = true;
    const now = new Date();
    void (async () => {
      const page = await calendarApi.events({ ...ongoingEventWindow(now), limit: 50 }, { expectedUserId: requestUserId });
      const event = ongoingEventFor(page.events, now);
      if (!event?.accountId) return;
      const { accounts } = await accountApi.list({ scope: 'direct', expectedUserId: requestUserId });
      const account = accounts.find(candidate => candidate.accountId === event.accountId && isEditableAccount(candidate));
      if (!account || latestRef.current.userId !== requestUserId || !eligible()) return;
      const latest = latestRef.current;
      sourceRef.current = 'auto';
      latest.setAccountId(account.accountId);
      if (!latest.title.trim()) autoTitleRef.current = event.title;
      latest.setTitle(previous => previous.trim() ? previous : event.title);
      if (latest.isRecordingActive()) latest.applyToRecording(account.accountId);
      setAutoLink({ accountId: account.accountId, name: account.name });
    })().catch(() => {
      // No calendar, no access or a failed read: nothing is linked.
    }).finally(() => { inFlightRef.current = false; });
  }, []);

  /** Clears the auto-linked Account and reverts the title it filled, unless
   * the title was edited since. Returns whether that title was still shown. */
  const clearAuto = useCallback((source: AccountLinkSource): boolean => {
    const latest = latestRef.current;
    const autoTitle = autoTitleRef.current;
    const titleIsAuto = autoTitle !== null && latest.title === autoTitle;
    sourceRef.current = source;
    autoTitleRef.current = null;
    latest.setAccountId('');
    if (autoTitle !== null) latest.setTitle(previous => previous === autoTitle ? '' : previous);
    return titleIsAuto;
  }, []);

  // Switching to upload mode drops an auto link: the file is not this meeting.
  // The chip is hidden while disabled and never matches the cleared Account.
  useEffect(() => {
    if (enabled || sourceRef.current !== 'auto') return;
    clearAuto('none');
  }, [enabled, clearAuto]);

  /** URL, MCP or a user choice: auto-link never applies afterwards. */
  const markSource = useCallback((source: Exclude<AccountLinkSource, 'none' | 'auto'>) => {
    sourceRef.current = source;
    setAutoLink(null);
  }, []);

  /** The chip's change button: clear the auto link and let the user pick. */
  const dismiss = useCallback(() => {
    clearAuto('user');
    setAutoLink(null);
    const latest = latestRef.current;
    if (latest.isRecordingActive()) latest.applyToRecording('');
  }, [clearAuto]);

  /** A leftover upload is not the ongoing meeting: drop an auto link and its
   * title. Returns draft overrides, or undefined when nothing was auto-linked. */
  const dropAutoLink = useCallback((): DroppedAutoLink | undefined => {
    if (sourceRef.current !== 'auto') return undefined;
    const titleIsAuto = clearAuto('none');
    setAutoLink(null);
    return titleIsAuto ? { accountId: '', title: '' } : { accountId: '' };
  }, [clearAuto]);

  const source = useCallback(() => sourceRef.current, []);

  return { autoLink: enabled ? autoLink : null, check, markSource, dismiss, dropAutoLink, source };
}
