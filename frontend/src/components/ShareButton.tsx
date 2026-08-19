'use client';

import { useState, useRef, useEffect, type ReactNode } from 'react';
import { usersApi, meetingsApi, docApi } from '@/lib/api';
import { NotionPushSection } from '@/components/meeting/NotionPushSection';
import type { User, SharedUser } from '@/types/meeting';

interface ShareButtonProps {
  entityId: string;
  sharedWith?: SharedUser[];
  onShare?: (user: SharedUser) => void;
  onUnshare?: (userId: string) => void;
  shareApi: (id: string, data: { email: string; permission: 'read' | 'edit' }) => Promise<unknown>;
  unshareApi: (id: string, userId: string) => Promise<unknown>;
  label?: string;
  /**
   * Optional slot rendered between the user list and the Done footer.
   * Used by meeting share to embed `NotionPushSection`; keeps the generic
   * `ShareButton` agnostic of meeting-specific exporters.
   */
  extraSection?: ReactNode;
  /**
   * Hides the view/edit toggle and always shares with 'read'. Document
   * shares are read-only by design (the backend hardcodes it too), so
   * offering the toggle would promise an edit permission that never lands.
   */
  readOnly?: boolean;
  /**
   * Shows a free-text "share/invite by email" row when a search yields no
   * match (or none of the matches equal the typed email) and the query
   * looks like an email. Only meeting share supports this today --
   * ShareMeetingByEmail queues a PendingShare for an invited-but-not-yet-
   * logged-in target (see backend/internal/model.PendingShare); doc/
   * research share have no such path and would just 404. Defaults to
   * false so those callers get the old search-only behavior unchanged.
   */
  allowEmailInvite?: boolean;
  /**
   * Cancels a queued PendingShare invite by email (there's no userId yet
   * for one, so `unshareApi` can't be reused). Only wired when
   * allowEmailInvite is -- pending's other current caller, doc/research
   * share, has no such route.
   */
  revokePendingApi?: (id: string, email: string) => Promise<unknown>;
}

// shareApi's return shape varies by entity (meeting share vs. doc share), so
// this only recognizes the one shape that carries a pending flag (meeting
// share's `{ sharedWith: { pending } }`, see model.SharedWithResponse) --
// anything else (including doc share's response) is treated as a normal,
// already-materialized share.
function isPendingShareResult(result: unknown): boolean {
  if (!result || typeof result !== 'object') return false;
  const sharedWith = (result as { sharedWith?: unknown }).sharedWith;
  if (!sharedWith || typeof sharedWith !== 'object') return false;
  return (sharedWith as { pending?: unknown }).pending === true;
}

// Pulls the real userId/email back out of a non-pending share result --
// needed because a free-text email can resolve to an already-registered
// user (one usersApi.search just didn't happen to surface), in which case
// the share succeeds immediately and this is the only place the real
// userId exists; falling back to the input's own placeholder userId ('')
// would add an unrevoke-able ghost row to the list.
function extractSharedWithUser(result: unknown): { userId?: string; email?: string } | null {
  if (!result || typeof result !== 'object') return null;
  const sharedWith = (result as { sharedWith?: unknown }).sharedWith;
  if (!sharedWith || typeof sharedWith !== 'object') return null;
  return sharedWith as { userId?: string; email?: string };
}

// A loose but sufficient check for "looks like a real email" -- this only
// gates whether to show the free-text share row below, not any backend
// validation, so it doesn't need to be a strict RFC 5322 match.
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

// Backwards-compatible wrapper for meetings
interface MeetingShareButtonProps {
  meetingId: string;
  sharedWith?: SharedUser[];
  onShare?: (user: SharedUser) => void;
  onUnshare?: (userId: string) => void;
}

const EMPTY_SHARED: SharedUser[] = [];

export function MeetingShareButton({
  meetingId,
  sharedWith = EMPTY_SHARED,
  onShare,
  onUnshare,
}: MeetingShareButtonProps) {
  return (
    <ShareButton
      entityId={meetingId}
      sharedWith={sharedWith}
      onShare={onShare}
      onUnshare={onUnshare}
      shareApi={meetingsApi.share}
      unshareApi={meetingsApi.unshare}
      label="Share meeting"
      extraSection={<NotionPushSection meetingId={meetingId} />}
      allowEmailInvite
      revokePendingApi={meetingsApi.revokePendingShare}
    />
  );
}

/**
 * Per-person document sharing (read-only, by reference). Owns its
 * `sharedWith` list because the doc detail response doesn't carry one --
 * `docApi.listShares` is owner-only, so a 403/404 just leaves it empty.
 */
export function DocumentShareButton({ docId }: { docId: string }) {
  const [sharedWith, setSharedWith] = useState<SharedUser[]>(EMPTY_SHARED);

  useEffect(() => {
    let cancelled = false;
    docApi.listShares(docId)
      .then(({ shares }) => {
        if (cancelled) return;
        setSharedWith(shares.map((s) => ({
          userId: s.userId,
          email: s.email,
          permission: 'read' as const,
          sharedAt: '',
        })));
      })
      .catch(() => undefined);
    return () => { cancelled = true; };
  }, [docId]);

  return (
    <ShareButton
      entityId={docId}
      sharedWith={sharedWith}
      onShare={(u) => setSharedWith((prev) => [...prev, u])}
      onUnshare={(userId) => setSharedWith((prev) => prev.filter((s) => s.userId !== userId))}
      shareApi={(id, { email }) => docApi.share(id, { email })}
      unshareApi={docApi.unshare}
      label="문서 공유 (읽기 전용)"
      readOnly
    />
  );
}

export function ShareButton({
  entityId,
  sharedWith = EMPTY_SHARED,
  onShare,
  onUnshare,
  shareApi,
  unshareApi,
  label = 'Share',
  extraSection,
  readOnly,
  allowEmailInvite = false,
  revokePendingApi,
}: ShareButtonProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<User[]>([]);
  const [isSearching, setIsSearching] = useState(false);
  const [selectedPermission, setSelectedPermission] = useState<'read' | 'edit'>('read');
  const [isSharing, setIsSharing] = useState(false);
  const [pendingNotice, setPendingNotice] = useState<string | null>(null);
  // The email a pending notice is about, so its Cancel button can call
  // revokePendingApi without needing a listing feature -- the invite was
  // just sent in this exact call, so the email is already known here.
  const [pendingNoticeEmail, setPendingNoticeEmail] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const searchTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  // usersApi.search already filters out anyone in `sharedWith` (see the
  // effect below), so a typed email that's already shared is absent from
  // searchResults for that reason, not because it's unknown -- the
  // free-text row must check `sharedWith` directly, not just
  // searchResults, or it offers to "invite" someone already shared.
  const isAlreadyShared = (query: string) =>
    sharedWith.some((s) => s.email.toLowerCase() === query.trim().toLowerCase());

  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (modalRef.current && !modalRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    }

    if (isOpen) {
      document.addEventListener('mousedown', handleClickOutside);
    }
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
    };
  }, [isOpen]);

  useEffect(() => {
    if (searchTimeoutRef.current) {
      clearTimeout(searchTimeoutRef.current);
    }

    if (searchQuery.length < 2) {
      setSearchResults([]);
      return;
    }

    setIsSearching(true);
    searchTimeoutRef.current = setTimeout(async () => {
      try {
        const { users } = await usersApi.search(searchQuery);
        const filtered = users.filter(
          (u) => !sharedWith.some((s) => s.userId === u.userId)
        );
        setSearchResults(filtered);
      } catch {
        setSearchResults([]);
      } finally {
        setIsSearching(false);
      }
    }, 300);

    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current);
      }
    };
  }, [searchQuery, sharedWith]);

  const handleShare = async (user: User) => {
    setIsSharing(true);
    setPendingNotice(null);
    setPendingNoticeEmail(null);
    try {
      const permission = readOnly ? 'read' : selectedPermission;
      const result = await shareApi(entityId, { email: user.email, permission });
      if (isPendingShareResult(result)) {
        // Invited but not yet logged in: no real Share row exists yet
        // (see PendingShare), so don't optimistically add a fake entry to
        // sharedWith -- it'll appear for real once they sign in.
        setPendingNotice(`${user.email}님은 아직 초대를 수락하지 않았습니다. 로그인하면 자동으로 공유됩니다.`);
        setPendingNoticeEmail(user.email);
      } else {
        // A free-text email can resolve to an already-registered user
        // (search just didn't happen to surface them) -- prefer the
        // response's real userId/email over the input's own placeholder
        // ('' for a free-text entry) so the row stays unshare-able.
        const resolved = extractSharedWithUser(result);
        onShare?.({
          userId: resolved?.userId || user.userId,
          email: resolved?.email || user.email,
          name: user.name,
          permission: readOnly ? 'read' : selectedPermission,
          sharedAt: new Date().toISOString(),
        });
      }
      setSearchQuery('');
      setSearchResults([]);
    } catch (err) {
      console.error('Failed to share:', err);
    } finally {
      setIsSharing(false);
    }
  };

  const handleUnshare = async (userId: string) => {
    try {
      await unshareApi(entityId, userId);
      onUnshare?.(userId);
    } catch (err) {
      console.error('Failed to unshare:', err);
    }
  };

  const handleRevokePending = async () => {
    if (!revokePendingApi || !pendingNoticeEmail) return;
    try {
      await revokePendingApi(entityId, pendingNoticeEmail);
      setPendingNotice(null);
      setPendingNoticeEmail(null);
    } catch (err) {
      console.error('Failed to revoke pending share:', err);
    }
  };

  return (
    <div className="relative">
      <button
        onClick={() => { setPendingNotice(null); setPendingNoticeEmail(null); setIsOpen(true); }}
        className="flex items-center gap-2 px-4 py-2 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg text-sm font-medium hover:bg-slate-50 dark:hover:bg-slate-700 transition-colors"
      >
        <span className="material-symbols-outlined text-lg">share</span>
        Share
        {sharedWith.length > 0 && (
          <span className="bg-primary/10 text-primary text-xs font-bold px-1.5 py-0.5 rounded-full">
            {sharedWith.length}
          </span>
        )}
      </button>

      {isOpen && (
        <div
          ref={modalRef}
          className="absolute right-0 top-full mt-2 w-80 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded-xl shadow-xl z-50"
        >
          <div className="p-4 border-b border-slate-200 dark:border-slate-700">
            <h3 className="font-bold text-slate-900 dark:text-white mb-3">{label}</h3>

            <div className="relative">
              <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-lg">
                search
              </span>
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search by name or email"
                className="w-full pl-10 pr-4 py-2 bg-slate-100 dark:bg-slate-800 border-none rounded-lg text-sm focus:ring-2 focus:ring-primary/20"
              />
            </div>

            {!readOnly && (
            <div className="flex gap-2 mt-3">
              <button
                onClick={() => setSelectedPermission('read')}
                className={`flex-1 py-1.5 text-xs font-medium rounded-lg transition-colors ${
                  selectedPermission === 'read'
                    ? 'bg-primary text-white'
                    : 'bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-400'
                }`}
              >
                Can view
              </button>
              <button
                onClick={() => setSelectedPermission('edit')}
                className={`flex-1 py-1.5 text-xs font-medium rounded-lg transition-colors ${
                  selectedPermission === 'edit'
                    ? 'bg-primary text-white'
                    : 'bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-400'
                }`}
              >
                Can edit
              </button>
            </div>
            )}
          </div>

          {pendingNotice && (
            <div className="px-4 py-3 text-sm text-amber-700 dark:text-amber-400 bg-amber-50 dark:bg-amber-950/30 border-b border-slate-200 dark:border-slate-700 flex items-center justify-between gap-3">
              <span>{pendingNotice}</span>
              {revokePendingApi && (
                <button
                  type="button"
                  onClick={handleRevokePending}
                  className="shrink-0 font-semibold underline hover:no-underline"
                >
                  취소
                </button>
              )}
            </div>
          )}

          {searchQuery.length > 0 && searchQuery.length < 2 && (
            <div className="px-4 py-3 text-center text-slate-400 text-sm">
              2글자 이상 입력해주세요
            </div>
          )}

          {searchQuery.length >= 2 && (
            <div className="max-h-48 overflow-y-auto">
              {isSearching ? (
                <div className="p-4 text-center">
                  <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent mx-auto" />
                </div>
              ) : searchResults.length > 0 ? (
                <>
                  {searchResults.map((user) => (
                    <button
                      key={user.userId}
                      onClick={() => handleShare(user)}
                      disabled={isSharing}
                      className="w-full flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
                    >
                      <div className="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center text-primary text-sm font-bold">
                        {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                      </div>
                      <div className="flex-1 text-left">
                        <p className="text-sm font-medium text-slate-900 dark:text-white">
                          {user.name || user.email}
                        </p>
                        {user.name && (
                          <p className="text-xs text-slate-500">{user.email}</p>
                        )}
                      </div>
                      <span className="material-symbols-outlined text-primary">add</span>
                    </button>
                  ))}
                  {allowEmailInvite && EMAIL_RE.test(searchQuery.trim()) && !isAlreadyShared(searchQuery) && !searchResults.some((u) => u.email.toLowerCase() === searchQuery.trim().toLowerCase()) && (
                    <button
                      onClick={() => handleShare({ userId: '', email: searchQuery.trim() })}
                      disabled={isSharing}
                      className="w-full flex items-center gap-2 px-4 py-3 text-left border-t border-slate-200 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
                    >
                      <span className="material-symbols-outlined text-primary">mail</span>
                      <p className="text-sm text-slate-700 dark:text-slate-300 truncate">이 이메일로 공유: {searchQuery.trim()}</p>
                    </button>
                  )}
                </>
              ) : allowEmailInvite && EMAIL_RE.test(searchQuery.trim()) && !isAlreadyShared(searchQuery) ? (
                // usersApi.search only finds PROFILE rows (SearchUsersByEmail
                // via GSI2) -- an invited-but-never-logged-in email has none
                // by definition, so it can never show up as a search result
                // (searchResults.length is 0 here). Without this fallback,
                // the pending-share path this component's
                // isPendingShareResult/pendingNotice logic exists for would
                // be unreachable from the UI. Gated on allowEmailInvite so
                // doc/research share (no pending support on the backend)
                // don't show a row that just 404s -- and on !isAlreadyShared
                // so typing an email that's already in `sharedWith` (which
                // is exactly why it's absent from searchResults above, not
                // because it doesn't exist) doesn't offer to "invite" it a
                // second time.
                <button
                  onClick={() => handleShare({ userId: '', email: searchQuery.trim() })}
                  disabled={isSharing}
                  className="w-full flex items-center gap-2 px-4 py-3 text-left hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
                >
                  <span className="material-symbols-outlined text-primary">mail</span>
                  <p className="text-sm text-slate-700 dark:text-slate-300 truncate">이 이메일로 공유: {searchQuery.trim()}</p>
                </button>
              ) : (
                <div className="p-4 text-center text-slate-500 text-sm">
                  No users found
                </div>
              )}
            </div>
          )}

          {sharedWith.length > 0 && (
            <div className="border-t border-slate-200 dark:border-slate-700">
              <p className="px-4 py-2 text-xs font-bold text-slate-500 uppercase tracking-wider">
                Shared with
              </p>
              <div className="max-h-48 overflow-y-auto">
                {sharedWith.map((user) => (
                  <div
                    key={user.userId}
                    className="flex items-center gap-3 px-4 py-2"
                  >
                    <div className="w-8 h-8 rounded-full bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-slate-500 text-sm font-bold">
                      {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-slate-900 dark:text-white truncate">
                        {user.name || user.email}
                      </p>
                      <p className="text-xs text-slate-500">
                        {user.permission === 'edit' ? 'Can edit' : 'Can view'}
                      </p>
                    </div>
                    <button
                      onClick={() => handleUnshare(user.userId)}
                      className="text-slate-400 hover:text-red-500 transition-colors"
                    >
                      <span className="material-symbols-outlined text-lg">close</span>
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}

          {extraSection}

          <div className="p-3 border-t border-slate-200 dark:border-slate-700">
            <button
              onClick={() => setIsOpen(false)}
              className="w-full py-2 text-sm font-medium text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-white transition-colors"
            >
              Done
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
