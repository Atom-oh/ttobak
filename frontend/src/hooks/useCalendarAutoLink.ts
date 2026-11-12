'use client';

import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react';
import { accountApi, calendarApi } from '@/lib/api';
import { isEditableAccount } from '@/lib/accountAccess';
import { autoLinkTitle, ongoingEventFor, ongoingEventWindow, readOngoingEvents, shouldAutoLink, type AccountLinkSource } from '@/lib/ongoingEvent';

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
  /** False while clearing the recording's Account would be refused (a link
   * to an existing draft is still pending); the change button then waits. */
  canDismiss?: boolean;
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
export function useCalendarAutoLink({ userId, accountId, setAccountId, title, setTitle, recordingAccountId, isRecordingActive, applyToRecording, initialSource, enabled, canDismiss = true }: Options) {
  const sourceRef = useRef<AccountLinkSource>(initialSource);
  const latestRef = useRef({ userId, accountId, title, recordingAccountId, isRecordingActive, applyToRecording, setAccountId, setTitle, enabled, canDismiss });
  useEffect(() => {
    latestRef.current = { userId, accountId, title, recordingAccountId, isRecordingActive, applyToRecording, setAccountId, setTitle, enabled, canDismiss };
  });
  /** Page values the auto link set and nobody changed since: the reference
   * Account and the title it filled. Owned values are cleared when the user
   * overrides, a new recording starts, a leftover is uploaded or the flow is
   * cancelled, so nothing inherits calendar values silently. */
  const autoAccountRef = useRef<string | null>(null);
  const autoTitleRef = useRef<string | null>(null);
  /** Each start owns one generation; stop, failure, a leftover upload and a
   * newer start advance it, so a late lookup result applies nothing. */
  const generationRef = useRef(0);
  const [autoLink, setAutoLink] = useState<{ accountId: string; name: string } | null>(null);

  const invalidate = useCallback(() => { generationRef.current += 1; }, []);

  const check = useCallback(() => {
    const generation = ++generationRef.current;
    const eligible = () => {
      const latest = latestRef.current;
      return latest.enabled && !!latest.userId && shouldAutoLink({
        accountId: latest.accountId, recordingAccountId: latest.recordingAccountId, source: sourceRef.current,
        generation, currentGeneration: generationRef.current,
      });
    };
    // The recording's links are reset by this start's draft creation; the
    // result-time check below sees the current ones.
    const latestAtStart = latestRef.current;
    if (!latestAtStart.enabled || !latestAtStart.userId || !shouldAutoLink({ accountId: latestAtStart.accountId, source: sourceRef.current })) return;
    const requestUserId = latestRef.current.userId!;
    const now = new Date();
    const window = ongoingEventWindow(now);
    void (async () => {
      const events = await readOngoingEvents(
        cursor => calendarApi.events({ ...window, limit: 50, cursor }, { expectedUserId: requestUserId }),
        () => generation === generationRef.current,
      );
      const event = events && ongoingEventFor(events, now);
      if (!event?.accountId || !eligible()) return;
      const { accounts } = await accountApi.list({ scope: 'direct', expectedUserId: requestUserId });
      const account = accounts.find(candidate => candidate.accountId === event.accountId && isEditableAccount(candidate));
      if (!account || latestRef.current.userId !== requestUserId || !eligible()) return;
      const latest = latestRef.current;
      const title = autoLinkTitle(event.title);
      sourceRef.current = 'auto';
      autoAccountRef.current = account.accountId;
      latest.setAccountId(account.accountId);
      if (!latest.title.trim() && title) {
        autoTitleRef.current = title;
        latest.setTitle(previous => previous.trim() ? previous : title);
      }
      if (latest.isRecordingActive()) latest.applyToRecording(account.accountId);
      setAutoLink({ accountId: account.accountId, name: account.name });
    })().catch(() => {
      // No calendar, no access or a failed read: nothing is linked.
    });
  }, []);

  /** Clears page values the auto link still owns: the Account only if it is
   * still the auto value, the title only if it is still the unedited filled
   * text. `latestRef` is updated too, so a caller in the same event handler
   * already sees the cleared values. Returns overrides for a draft created in
   * that handler, or undefined when nothing was owned. */
  const releaseAuto = useCallback((source: AccountLinkSource = 'none'): DroppedAutoLink | undefined => {
    const latest = latestRef.current;
    const autoAccount = autoAccountRef.current;
    const autoTitle = autoTitleRef.current;
    autoAccountRef.current = null;
    autoTitleRef.current = null;
    if (sourceRef.current === 'auto' || source !== 'none') sourceRef.current = source;
    if (autoAccount === null && autoTitle === null) return undefined;
    const overrides: DroppedAutoLink = { accountId: latest.accountId };
    if (autoAccount !== null && latest.accountId === autoAccount) {
      latest.setAccountId('');
      overrides.accountId = '';
    }
    if (autoTitle !== null && latest.title === autoTitle) {
      latest.setTitle(previous => previous === autoTitle ? '' : previous);
      overrides.title = '';
    }
    latestRef.current = { ...latest, accountId: overrides.accountId, title: overrides.title ?? latest.title };
    return overrides;
  }, []);

  // Switching to upload mode drops an auto link: the file is not this meeting.
  // The chip is hidden while disabled and never matches the cleared Account.
  useEffect(() => {
    if (enabled) return;
    releaseAuto();
  }, [enabled, releaseAuto]);

  /** URL, MCP or a user choice: auto-link never applies afterwards, and the
   * values it filled are released (the caller then sets its own). */
  const markSource = useCallback((source: Exclude<AccountLinkSource, 'none' | 'auto'>) => {
    releaseAuto(source);
    setAutoLink(null);
  }, [releaseAuto]);

  /** The chip's change button: clear the auto link and let the user pick. */
  const dismiss = useCallback(() => {
    if (!latestRef.current.canDismiss) return;
    releaseAuto('user');
    setAutoLink(null);
    const latest = latestRef.current;
    if (latest.isRecordingActive()) latest.applyToRecording('');
  }, [releaseAuto]);

  /** A new recording start, a leftover upload or a cancelled flow: end any
   * pending lookup and clear what the auto link still owns. Returns draft
   * overrides, or undefined when nothing was owned. */
  const dropAutoLink = useCallback((): DroppedAutoLink | undefined => {
    generationRef.current += 1;
    setAutoLink(null);
    return releaseAuto();
  }, [releaseAuto]);

  const source = useCallback(() => sourceRef.current, []);

  return { autoLink: enabled ? autoLink : null, check, invalidate, markSource, dismiss, dropAutoLink, source };
}
