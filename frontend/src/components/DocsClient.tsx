'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { docApi } from '@/lib/api';
import { uploadDocFile } from '@/lib/upload';
import type { AccountDocument } from '@/types/meeting';

const DOC_TYPES = ['note', 'blog'] as const;

export default function DocsClient() {
  const router = useRouter();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [documents, setDocuments] = useState<AccountDocument[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [showForm, setShowForm] = useState(false);
  const [title, setTitle] = useState('');
  const [docType, setDocType] = useState<typeof DOC_TYPES[number]>('note');
  const [creating, setCreating] = useState(false);
  const [uploading, setUploading] = useState(false);

  const fetchDocuments = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await docApi.list();
      setDocuments(res?.documents ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load documents');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchDocuments();
  }, [fetchDocuments]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setCreating(true);
    setError(null);
    try {
      const created = await docApi.put({ title: title.trim(), docType, markdown: `# ${title.trim()}\n\n` });
      setShowForm(false);
      setTitle('');
      router.push(`/docs/${created.docId}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create document');
    } finally {
      setCreating(false);
    }
  };

  const handleSlideUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    setUploading(true);
    setError(null);
    try {
      const { key } = await uploadDocFile(file);
      const created = await docApi.put({
        title: file.name.replace(/\.[^.]+$/, ''),
        docType: 'slide',
        fileKey: key,
        fileName: file.name,
        mimeType: file.type,
        fileSize: file.size,
      });
      router.push(`/docs/${created.docId}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to upload slide');
    } finally {
      setUploading(false);
    }
  };

  return (
    <div className="max-w-3xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">Documents</h2>
        <div className="flex items-center gap-2">
          <input ref={fileInputRef} type="file" accept=".pdf,.pptx,.ppt" className="hidden" onChange={handleSlideUpload} />
          <button
            onClick={() => fileInputRef.current?.click()}
            disabled={uploading}
            className="border border-slate-200 dark:border-white/10 text-slate-700 dark:text-text-secondary rounded-lg font-semibold text-sm px-4 py-2 flex items-center gap-1 disabled:opacity-50"
          >
            <span className="material-symbols-outlined text-lg">upload_file</span>
            {uploading ? 'Uploading…' : 'Upload Slide'}
          </button>
          <button
            onClick={() => setShowForm((v) => !v)}
            className="bg-primary hover:bg-primary-hover text-white rounded-lg font-semibold text-sm px-4 py-2 flex items-center gap-1"
          >
            <span className="material-symbols-outlined text-lg">add</span>New Document
          </button>
        </div>
      </div>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
          {error}
        </div>
      )}

      {showForm && (
        <form onSubmit={handleCreate} className="glass-panel rounded-xl p-5 mb-6 space-y-3">
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Title"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <div className="flex gap-2">
            {DOC_TYPES.map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setDocType(t)}
                className={`px-3 py-1.5 rounded-lg text-sm capitalize ${
                  docType === t ? 'bg-primary/10 text-primary' : 'text-slate-500 dark:text-text-muted'
                }`}
              >
                {t}
              </button>
            ))}
          </div>
          <button
            type="submit"
            disabled={creating || !title.trim()}
            className="bg-primary hover:bg-primary-hover text-white rounded-lg font-semibold text-sm px-4 py-2 disabled:opacity-50"
          >
            {creating ? 'Creating…' : 'Create'}
          </button>
        </form>
      )}

      {loading ? (
        <div className="flex items-center justify-center py-16">
          <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
        </div>
      ) : documents.length === 0 ? (
        <div className="text-center py-16 text-slate-400 dark:text-text-muted">
          <span className="material-symbols-outlined text-4xl mb-2 block">article</span>
          No documents yet. Create a note or upload a slide to start.
        </div>
      ) : (
        <div className="bg-white rounded-xl border border-slate-200 divide-y divide-slate-200 dark:glass-panel dark:divide-white/5">
          {documents.map((d) => (
            <button
              key={d.docId}
              onClick={() => router.push(`/docs/${d.docId}`)}
              className="w-full flex items-center justify-between p-4 text-left hover:bg-slate-50 dark:hover:bg-white/5"
            >
              <div className="flex items-center gap-3">
                <span className="material-symbols-outlined text-primary">
                  {d.docType === 'slide' ? 'slideshow' : 'article'}
                </span>
                <span className="font-medium text-slate-900 dark:text-text-main">{d.title}</span>
              </div>
              {d.docType && (
                <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary">
                  {d.docType}
                </span>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
