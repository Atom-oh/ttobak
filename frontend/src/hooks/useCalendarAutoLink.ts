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

/** Links a recording to the Account of the ongoing calendar event, unless an
 * Account came from the URL, an MCP request or the user. Calendar or Account
 * failures do nothing; creation still validates membership. */
export function useCalendarAutoLink({ userId, accountId, setAccountId, setTitle, recordingAccountId, isRecordingActive, applyToRecording, initialSource, enabled }: Options) {
  const sourceRef = useRef<AccountLinkSource>(initialSource);
  const latestRef = useRef({ userId, accountId, recordingAccountId, isRecordingActive, applyToRecording, setAccountId, setTitle, enabled });
  useEffect(() => {
    latestRef.current = { userId, accountId, recordingAccountId, isRecordingActive, applyToRecording, setAccountId, setTitle, enabled };
  });
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
      latest.setTitle(previous => previous.trim() ? previous : event.title);
      if (latest.isRecordingActive()) latest.applyToRecording(account.accountId);
      setAutoLink({ accountId: account.accountId, name: account.name });
    })().catch(() => {
      // No calendar, no access or a failed read: nothing is linked.
    }).finally(() => { inFlightRef.current = false; });
  }, []);

  // Once per page while enabled, as soon as the signed-in user is known.
  const checkedUserRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!enabled || !userId || checkedUserRef.current === userId) return;
    checkedUserRef.current = userId;
    check();
  }, [enabled, userId, check]);

  // Switching to upload mode drops an auto link: the file is not this meeting.
  useEffect(() => {
    if (enabled || sourceRef.current !== 'auto') return;
    sourceRef.current = 'none';
    checkedUserRef.current = undefined;
    latestRef.current.setAccountId('');
  }, [enabled]);

  /** URL, MCP or a user choice: auto-link never applies afterwards. */
  const markSource = useCallback((source: Exclude<AccountLinkSource, 'none' | 'auto'>) => {
    sourceRef.current = source;
    setAutoLink(null);
  }, []);

  /** The chip's change button: clear the auto link and let the user pick. */
  const dismiss = useCallback(() => {
    sourceRef.current = 'user';
    setAutoLink(null);
    const latest = latestRef.current;
    latest.setAccountId('');
    if (latest.isRecordingActive()) latest.applyToRecording('');
  }, []);

  return { autoLink: enabled ? autoLink : null, check, markSource, dismiss };
}
