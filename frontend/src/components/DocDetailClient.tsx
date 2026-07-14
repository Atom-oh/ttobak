'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { usePathname } from 'next/navigation';
import dynamic from 'next/dynamic';
import { Marked } from 'marked';
import TurndownService from 'turndown';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { accountApi, docApi } from '@/lib/api';
import type { AccountDocument } from '@/types/meeting';

const MeetingEditor = dynamic(() => import('./MeetingEditor').then(m => ({ default: m.MeetingEditor })), {
  loading: () => <div className="animate-pulse bg-slate-100 dark:bg-slate-800 rounded-xl h-64" />,
});

// Same markdown<->HTML conversion as AISummaryCard.tsx -- keeps stored
// `content` as canonical markdown (atx headings, `-` bullets) rather than
// raw HTML, matching what the vault export / Obsidian side expects.
const marked = new Marked();
const turndown = new TurndownService({ headingStyle: 'atx', bulletListMarker: '-', hr: '---' });

interface DocDetailClientProps {
  /** Omit for a personal (account-less) document. */
  accountId?: string;
}

export function DocDetailClient({ accountId }: DocDetailClientProps) {
  const pathname = usePathname();
  const docId = decodeURIComponent(pathname.split('/').filter(Boolean).pop() || '');
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
  // Guards against out-of-order network responses: autosave and title-blur
  // both fire full-replace PUTs with no cancellation, so a slow, older
  // request completing after a newer one must not clobber state with
  // stale title/content. Only the response matching the most recently
  // *started* save is ever applied.
  const saveSeqRef = useRef(0);

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

  const saveContent = useCallback(async (markdown: string, nextTitle?: string) => {
    if (!doc) return;
    const seq = ++saveSeqRef.current;
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
      if (seq !== saveSeqRef.current) return; // a newer save has since started; this response is stale
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
      if (seq !== saveSeqRef.current) return;
      setError(err instanceof Error ? err.message : 'Failed to save document');
    } finally {
      if (seq === saveSeqRef.current) setSaving(false);
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

  const isSlide = !!doc.fileName;
  const isPdf = (doc.fileName ?? '').toLowerCase().endsWith('.pdf');

  return (
    <AppLayout activePath={accountId ? '/accounts' : '/docs'}>
      <div className="max-w-3xl mx-auto p-6">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
          </div>
        )}
        <div className="flex items-center gap-3 mb-6">
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            onBlur={handleTitleBlur}
            disabled={isSlide}
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

        {isSlide ? (
          <div className="space-y-4">
            {isPdf && doc.downloadUrl && (
              <iframe
                src={doc.downloadUrl}
                title={doc.title}
                className="w-full h-[70vh] rounded-xl border border-slate-200 dark:border-slate-700"
              />
            )}
            <div className="flex items-center justify-between glass-panel rounded-xl p-4">
              <div className="flex items-center gap-2 text-sm text-slate-600 dark:text-text-secondary">
                <span className="material-symbols-outlined text-primary">description</span>
                <span>{doc.fileName}</span>
              </div>
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
        ) : (
          <MeetingEditor
            content={marked.parse(doc.content ?? '', { async: false }) as string}
            onChange={handleChange}
            onAutoSave={handleAutoSave}
            autoSaveDelay={2000}
            wikilinkTitles={titles}
          />
        )}
      </div>
    </AppLayout>
  );
}
