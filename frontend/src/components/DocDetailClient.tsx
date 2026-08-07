'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { usePathname } from 'next/navigation';
import dynamic from 'next/dynamic';
import { Marked } from 'marked';
import TurndownService from 'turndown';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { accountApi, docApi } from '@/lib/api';
import { uploadDocFile } from '@/lib/upload';
import { DocumentShareButton } from '@/components/ShareButton';
import type { AccountDocument, AccountSummary } from '@/types/meeting';

const MeetingEditor = dynamic(() => import('./MeetingEditor').then(m => ({ default: m.MeetingEditor })), {
  loading: () => <div className="animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />,
});

// Same markdown<->HTML conversion as AISummaryCard.tsx -- keeps stored
// `content` as canonical markdown (atx headings, `-` bullets) rather than
// raw HTML, matching what the vault export / Obsidian side expects.
const marked = new Marked();
const turndown = new TurndownService({ headingStyle: 'atx', bulletListMarker: '-', hr: '---' });

interface DocDetailClientProps {
  /** Set for the /accounts/{id}/docs/{docId} route; omit for a personal (account-less) /docs/{docId} route. */
  accountScoped?: boolean;
}

export function DocDetailClient({ accountScoped }: DocDetailClientProps) {
  const pathname = usePathname();
  // Parse both ids from the live pathname rather than trusting the route's
  // params prop -- this is a static export, so the placeholder page's
  // generateStaticParams value ('_') gets baked into that prop at build
  // time, and only usePathname() reflects the real browser URL on a fresh
  // load/refresh (same pattern as AccountDetailClient.tsx).
  const segments = pathname.split('/').filter(Boolean).map(decodeURIComponent);
  const docId = segments[segments.length - 1] || '';
  const accountId = accountScoped ? segments[segments.length - 3] : undefined;
  const { isLoading, isAuthenticated } = useAuth();

  const [doc, setDoc] = useState<AccountDocument | null>(null);
  const [titles, setTitles] = useState<string[]>([]);
  const [title, setTitle] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  // Tracks the freshest in-editor markdown (updated on every keystroke via
  // MeetingEditor's onChange, not just the debounced autosave) so a title
  // blur mid-edit sends the content the user is actually looking at instead
  // of the last-saved snapshot in `doc.content` -- otherwise a title save
  // firing between two autosave debounce windows would revert unsaved body
  // edits.
  const latestMarkdownRef = useRef('');
  // Serializes saves: autosave and title-blur both fire full-replace PUTs,
  // so two in-flight requests could reach the server out of order and have
  // the older one's stale content win the last-write-wins race. Only one
  // PUT is ever in flight; a save requested while one is in flight is
  // queued (latest wins) and fires immediately after the current one
  // settles, so the server always sees saves in the order they were made.
  const saveInFlightRef = useRef(false);
  const pendingSaveRef = useRef<{ markdown: string; nextTitle?: string } | null>(null);

  // Share-to-account (personal docs only -- account docs are already
  // account-scoped by definition).
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [shareAccountId, setShareAccountId] = useState('');
  const [sharing, setSharing] = useState(false);
  const [shareMsg, setShareMsg] = useState<string | null>(null);
  const [publicToken, setPublicToken] = useState<string | null>(null);
  const [publicBusy, setPublicBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  // Replace the uploaded file behind a slide doc (see updateDoc's fileKey
  // branch in the backend, ADR-020) -- separate ref/state from the title
  // editor's save flow above since this never touches markdown.
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [replacing, setReplacing] = useState(false);

  const fetchAll = useCallback(async () => {
    if (!docId || docId === '_') return;
    setLoading(true);
    setError(null);
    try {
      const [detail, list] = await Promise.all([
        accountId ? accountApi.getDocument(accountId, docId) : docApi.get(docId),
        accountId ? accountApi.listDocuments(accountId) : docApi.list(),
      ]);
      setDoc(detail);
      setTitle(detail.title);
      setPublicToken(detail.publicShareToken || null);
      latestMarkdownRef.current = detail.content ?? '';
      setTitles((list?.documents ?? []).filter((d) => d.docId !== docId).map((d) => d.title));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load document');
    } finally {
      setLoading(false);
    }
  }, [accountId, docId]);

  useEffect(() => {
    if (isAuthenticated) fetchAll();
  }, [isAuthenticated, fetchAll]);

  useEffect(() => {
    if (accountScoped || !isAuthenticated) return;
    accountApi.list().then((r) => setAccounts(r?.accounts ?? [])).catch(() => {});
  }, [accountScoped, isAuthenticated]);

  const handleShareToAccount = useCallback(async () => {
    if (!shareAccountId) return;
    setSharing(true);
    setShareMsg(null);
    try {
      await docApi.shareToAccount(docId, shareAccountId);
      setShareMsg('팀에 공유되었습니다.');
    } catch (err) {
      setShareMsg(err instanceof Error ? err.message : '공유에 실패했습니다.');
    } finally {
      setSharing(false);
    }
  }, [docId, shareAccountId]);

  const handleTogglePublicShare = useCallback(async () => {
    setPublicBusy(true);
    try {
      if (publicToken) {
        await docApi.revokePublicShare(docId);
        setPublicToken(null);
      } else {
        const { token } = await docApi.createPublicShare(docId);
        setPublicToken(token);
      }
    } catch (err) {
      setShareMsg(err instanceof Error ? err.message : '공개 링크 처리에 실패했습니다.');
    } finally {
      setPublicBusy(false);
    }
  }, [docId, publicToken]);

  const handleCopyPublicLink = useCallback(() => {
    if (!publicToken || typeof window === 'undefined') return;
    const url = `${window.location.origin}/api/public/docs/${publicToken}`;
    navigator.clipboard.writeText(url).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }, [publicToken]);

  // saveContent always sends markdown (even "" for a slide, whose content
  // is always empty) -- that's safe only because the title input below is
  // disabled for slides (isSlide), so handleTitleBlur, the only other
  // caller that could reach a slide with no fileKey change, can never
  // fire for one. If slide titles ever become editable, this must switch
  // to omitting markdown for that save instead of sending "" (which would
  // trip updateDoc's slide-destructive-conversion guard).
  const saveContent = useCallback(async (markdown: string, nextTitle?: string) => {
    if (!doc) return;
    if (saveInFlightRef.current) {
      // A save is already in flight -- queue this one instead of firing a
      // second concurrent PUT (latest markdown wins). nextTitle falls back
      // to whatever was already queued (not the bare argument) so a title
      // rename queued by a blur isn't lost if a title-less body autosave
      // queues again before the in-flight save drains it.
      pendingSaveRef.current = { markdown, nextTitle: nextTitle ?? pendingSaveRef.current?.nextTitle };
      return;
    }
    saveInFlightRef.current = true;
    setSaving(true);
    setError(null);
    try {
      // Full-replace PUT: send every field the backend would otherwise
      // overwrite with an empty value (path in particular -- update is not
      // a partial patch, see ADR-020).
      const req = { title: nextTitle ?? title, docType: doc.docType, path: doc.path, markdown };
      const updated = accountId
        ? await accountApi.updateDocument(accountId, docId, req)
        : await docApi.update(docId, req);
      // Deliberately do NOT write `markdown` into doc.content here (same
      // as AISummaryCard's handleAutoSave) -- MeetingEditor's own effect
      // resets its DOM to the `content` prop whenever that prop changes.
      // If content changed while this save's network round-trip was in
      // flight (the user kept typing), feeding this save's markdown back
      // in would revert those newer, not-yet-saved keystrokes. Once
      // mounted, the editor is the sole owner of live content between
      // saves; only non-content metadata needs syncing here.
      setDoc((prev) => (prev ? { ...prev, ...updated } : prev));
      setSavedAt(new Date().toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' }));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save document');
    } finally {
      saveInFlightRef.current = false;
      setSaving(false);
      const pending = pendingSaveRef.current;
      if (pending) {
        pendingSaveRef.current = null;
        saveContent(pending.markdown, pending.nextTitle);
      }
    }
  }, [accountId, docId, doc, title]);

  const handleChange = useCallback((html: string) => {
    latestMarkdownRef.current = turndown.turndown(html);
  }, []);

  const handleAutoSave = useCallback((html: string) => {
    saveContent(turndown.turndown(html));
  }, [saveContent]);

  const handleTitleBlur = useCallback(() => {
    if (doc && title.trim() && title !== doc.title) {
      saveContent(latestMarkdownRef.current, title.trim());
    }
  }, [doc, title, saveContent]);

  const handleReplaceFile = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file || !doc) return;
    setReplacing(true);
    setError(null);
    try {
      const { key } = await uploadDocFile(file);
      // Omit markdown entirely (not even "") -- updateDoc's slide/note
      // exclusivity check treats a non-nil markdown as "also changing the
      // body", which conflicts with fileKey (see saveContent's comment above).
      const req = {
        title: doc.title,
        docType: doc.docType,
        path: doc.path,
        fileKey: key,
        fileName: file.name,
        mimeType: file.type,
        fileSize: file.size,
      };
      if (accountId) {
        await accountApi.updateDocument(accountId, docId, req);
      } else {
        await docApi.update(docId, req);
      }
      // updateDoc's response doesn't re-presign downloadUrl/previewUrl for
      // the new fileKey (only GetDocument does) -- refetch instead of
      // merging the response into state, or the viewer/Download link would
      // keep pointing at the superseded file.
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : '파일 교체에 실패했습니다.');
    } finally {
      setReplacing(false);
    }
  }, [doc, accountId, docId, fetchAll]);

  if (isLoading) {
    return (
      <AppLayout activePath={accountId ? '/accounts' : '/docs'}>
        <div className="p-6 animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />
      </AppLayout>
    );
  }

  if (!isAuthenticated) {
    if (typeof window !== 'undefined') window.location.href = '/';
    return null;
  }

  if (loading) {
    return (
      <AppLayout activePath={accountId ? '/accounts' : '/docs'}>
        <div className="p-6 animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />
      </AppLayout>
    );
  }

  if (!doc) {
    return (
      <AppLayout activePath={accountId ? '/accounts' : '/docs'}>
        <div className="p-6 text-red-500">{error || 'Document not found'}</div>
      </AppLayout>
    );
  }

  // downloadUrl mirrors the backend's canonical fileKey check (server only
  // presigns one when FileKey is set) -- more reliable than fileName alone,
  // which a client could in principle omit while still sending a fileKey.
  // docType is a free string the server never enforces (e.g. via MCP), so
  // it's deliberately NOT used here -- a docType:"slide" doc that actually
  // has markdown and no file must still open in the editor, not be hidden
  // behind a slide view with nothing to show.
  const isSlide = !!doc.downloadUrl;
  const isPdf = (doc.fileName ?? '').toLowerCase().endsWith('.pdf');
  // A doc someone shared with this user directly: view + download only. The
  // backend enforces this too (no update/delete/share path accepts a
  // non-owner), so hiding the controls is UX, not the security boundary.
  const isReceivedShare = !!doc.sharedBy;

  return (
    <AppLayout activePath={accountId ? '/accounts' : '/docs'}>
      <div className={`${isSlide ? 'max-w-none' : 'max-w-3xl'} mx-auto p-6`}>
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
          </div>
        )}
        {isReceivedShare && (
          <div className="flex items-center gap-2 glass-panel rounded-xl p-3 mb-4 text-sm text-slate-600 dark:text-text-secondary">
            <span className="material-symbols-outlined text-primary" aria-hidden="true">visibility</span>
            <span>{doc.sharedBy} 님이 공유한 문서입니다 — 읽기 전용</span>
          </div>
        )}
        <div className="flex items-center gap-3 mb-6">
          {/* disabled for slides so handleTitleBlur (and its saveContent
              call sending markdown: "") can never fire on one -- see the
              comment on saveContent before removing this. */}
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            onBlur={handleTitleBlur}
            disabled={isSlide || isReceivedShare}
            className="flex-1 text-2xl font-bold bg-transparent border-none outline-none focus:ring-0 text-slate-900 dark:text-text-main disabled:text-slate-500"
          />
          {doc.docType && (
            <span className="text-xs px-2 py-1 rounded-full bg-primary/10 text-primary dark:bg-accent/10 dark:text-accent">
              {doc.docType}
            </span>
          )}
          {saving && <span className="text-xs text-slate-400 animate-pulse">Saving...</span>}
          {savedAt && !saving && <span className="text-xs text-slate-400">Saved {savedAt}</span>}
        </div>

        {!accountScoped && !isReceivedShare && (
          <div className="mb-6">
            <DocumentShareButton docId={docId} />
          </div>
        )}

        {!accountScoped && !isReceivedShare && accounts.length > 0 && (
          <div className="flex flex-col sm:flex-row sm:items-center gap-2 mb-6">
            <select
              value={shareAccountId}
              onChange={(e) => setShareAccountId(e.target.value)}
              className="flex-1 px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
            >
              <option value="">어카운트 선택…</option>
              {accounts.map((a) => (
                <option key={a.accountId} value={a.accountId}>{a.name}</option>
              ))}
            </select>
            <button
              onClick={handleShareToAccount}
              disabled={sharing || !shareAccountId}
              className="text-sm px-4 py-2 rounded-lg bg-primary text-white hover:opacity-90 disabled:opacity-50"
            >
              {sharing ? '공유 중…' : '팀에 공유'}
            </button>
            {shareMsg && <p className="text-xs text-slate-500 dark:text-text-muted">{shareMsg}</p>}
          </div>
        )}

        {isSlide ? (
          <div className="space-y-4">
            {isPdf && !doc.previewUrl ? (
              // A directly-uploaded PDF has no previewUrl sidecar (that's
              // convert-doc's PPTX->PDF output only, ADR-022) -- its
              // downloadUrl is its own file, under docs/*, which gets
              // ADR-027's `/media/*` `CSP: sandbox` header (mitigating
              // stored-XSS from arbitrary client-supplied Content-Type on
              // that prefix). Framing that URL in an iframe is documented
              // browser behavior to disable the built-in PDF viewer, so
              // skip the preview and point at the Download button below
              // instead of an iframe that may silently render blank.
              // `!doc.previewUrl` is defensive, not load-bearing (the
              // backend never sets both on the same doc) -- it just means
              // this branch can't ever shadow a real sidecar if that
              // invariant ever changes.
              <div className="flex items-center gap-2 glass-panel rounded-xl p-4 text-sm text-slate-500 dark:text-text-muted">
                <span className="material-symbols-outlined" aria-hidden="true">description</span>
                <span>이 PDF는 미리보기를 지원하지 않습니다 — 아래 다운로드 버튼으로 확인해주세요.</span>
              </div>
            ) : doc.previewUrl ? (
              <iframe
                src={doc.previewUrl}
                title={doc.title}
                className="w-full h-[70vh] rounded-xl border border-slate-200 dark:border-slate-700"
              />
            ) : !isPdf ? (
              <div className="flex items-center justify-between glass-panel rounded-xl p-4">
                <div className="flex items-center gap-2 text-sm text-slate-500 dark:text-text-muted">
                  <span className="material-symbols-outlined animate-spin">progress_activity</span>
                  <span>PDF로 변환 중입니다. 잠시 후 새로고침 해주세요.</span>
                </div>
                <button
                  onClick={() => fetchAll()}
                  className="text-sm px-3 py-1.5 rounded-lg border border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5"
                >
                  새로고침
                </button>
              </div>
            ) : null}
            <div className="flex items-center justify-between glass-panel rounded-xl p-4">
              <div className="flex items-center gap-2 text-sm text-slate-600 dark:text-text-secondary">
                <span className="material-symbols-outlined text-primary">description</span>
                <span>{doc.fileName}</span>
              </div>
              <div className="flex items-center gap-2">
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".pdf,.pptx,.ppt"
                  className="hidden"
                  onChange={handleReplaceFile}
                />
                {!isReceivedShare && (
                <button
                  onClick={() => fileInputRef.current?.click()}
                  disabled={replacing}
                  className="text-sm px-3 py-1.5 rounded-lg border border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5 disabled:opacity-50"
                >
                  {replacing ? '교체 중…' : '파일 변경'}
                </button>
                )}
                {doc.downloadUrl && (
                  <a
                    href={doc.downloadUrl}
                    download={doc.fileName}
                    className="text-sm px-3 py-1.5 rounded-lg bg-primary text-white hover:opacity-90"
                  >
                    Download
                  </a>
                )}
              </div>
            </div>

            {!accountScoped && !isReceivedShare && (
              <div className="flex items-center gap-2 glass-panel rounded-xl p-4">
                <button
                  onClick={handleTogglePublicShare}
                  disabled={publicBusy}
                  className="text-sm px-3 py-1.5 rounded-lg border border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5 disabled:opacity-50"
                >
                  {publicToken ? '공개 링크 해제' : '공개 링크 만들기'}
                </button>
                {publicToken && (
                  <button
                    onClick={handleCopyPublicLink}
                    className="text-sm px-3 py-1.5 rounded-lg bg-primary text-white hover:opacity-90"
                  >
                    {copied ? '복사됨!' : '링크 복사'}
                  </button>
                )}
              </div>
            )}
          </div>
        ) : (
          <MeetingEditor
            content={marked.parse(doc.content ?? '', { async: false }) as string}
            onChange={handleChange}
            onAutoSave={handleAutoSave}
            autoSaveDelay={2000}
            readOnly={isReceivedShare}
            wikilinkTitles={titles}
          />
        )}
      </div>
    </AppLayout>
  );
}
