'use client';

import { useState, useEffect, useRef, useCallback } from 'react';
import { kbApi } from '@/lib/api';
import type { KBFile } from '@/types/meeting';

function formatFileSize(bytes?: number): string {
  if (!bytes) return '-';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatDate(dateString: string): string {
  const date = new Date(dateString);
  return date.toLocaleDateString('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
  });
}

function getFileIcon(fileType: string): string {
  if (fileType.includes('pdf')) return 'picture_as_pdf';
  if (fileType.includes('presentation') || fileType.includes('ppt')) return 'slideshow';
  if (fileType.includes('markdown') || fileType.includes('md')) return 'description';
  if (fileType.includes('word') || fileType.includes('doc')) return 'article';
  return 'insert_drive_file';
}

export function KBFileList() {
  const [files, setFiles] = useState<KBFile[]>([]);
  const [loading, setLoading] = useState(true);
  const [uploading, setUploading] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isDragging, setIsDragging] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const fetchFiles = useCallback(async () => {
    try {
      setLoading(true);
      const response = await kbApi.listFiles();
      setFiles(response.files || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load files');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchFiles();
  }, [fetchFiles]);

  const handleUpload = async (selectedFiles: FileList | null) => {
    if (!selectedFiles || selectedFiles.length === 0) return;

    setUploading(true);
    setError(null);

    try {
      for (const file of Array.from(selectedFiles)) {
        // Get presigned URL
        const { uploadUrl } = await kbApi.upload({
          fileName: file.name,
          fileType: file.type,
        });

        // Upload to S3
        await fetch(uploadUrl, {
          method: 'PUT',
          body: file,
          headers: {
            'Content-Type': file.type,
          },
        });
      }

      // Refresh file list
      await fetchFiles();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to upload file');
    } finally {
      setUploading(false);
    }
  };

  const handleSync = async () => {
    setSyncing(true);
    setError(null);

    try {
      await kbApi.sync();
      // Optionally show success message
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to sync knowledge base');
    } finally {
      setSyncing(false);
    }
  };

  const handleDelete = async (fileId: string) => {
    if (!confirm('Are you sure you want to delete this file?')) return;

    try {
      await kbApi.deleteFile(fileId);
      setFiles((prev) => prev.filter((f) => f.fileId !== fileId));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete file');
    }
  };

  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(true);
  };

  const handleDragLeave = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    handleUpload(e.dataTransfer.files);
  };

  return (
    <div className="space-y-6">
      {/* Upload Area */}
      <div
        className={`border-2 border-dashed rounded-xl p-8 text-center transition-colors ${
          isDragging
            ? 'border-primary bg-primary/5'
            : 'border-slate-300 dark:border-slate-700 hover:border-primary/50'
        }`}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        <input
          ref={fileInputRef}
          type="file"
          className="hidden"
          multiple
          accept=".md,.pdf,.ppt,.pptx"
          onChange={(e) => handleUpload(e.target.files)}
        />

        <span className="material-symbols-outlined text-4xl text-slate-400 mb-3">cloud_upload</span>
        <p className="text-slate-600 dark:text-slate-400 mb-2">
          {isDragging ? 'Drop files here' : 'Drag and drop files here'}
        </p>
        <p className="text-xs text-slate-400 mb-4">Supports: Markdown, PDF, PowerPoint</p>
        <button
          onClick={() => fileInputRef.current?.click()}
          disabled={uploading}
          className="px-4 py-2 bg-primary text-white rounded-lg font-semibold text-sm hover:bg-primary/90 disabled:opacity-50 transition-colors"
        >
          {uploading ? (
            <span className="flex items-center gap-2">
              <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent" />
              Uploading...
            </span>
          ) : (
            'Browse Files'
          )}
        </button>
      </div>

      {/* Error Message */}
      {error && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
          {error}
        </div>
      )}

      {/* Sync Button */}
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">
          Uploaded Files ({files.length})
        </h3>
        <button
          onClick={handleSync}
          disabled={syncing || files.length === 0}
          className="flex items-center gap-2 px-3 py-1.5 text-sm font-semibold text-primary border border-primary rounded-lg hover:bg-primary/10 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {syncing ? (
            <>
              <span className="animate-spin rounded-full h-4 w-4 border-2 border-primary border-t-transparent" />
              Syncing...
            </>
          ) : (
            <>
              <span className="material-symbols-outlined text-lg">sync</span>
              Sync to Knowledge Base
            </>
          )}
        </button>
      </div>

      {/* File List */}
      {loading ? (
        <div className="flex items-center justify-center py-12">
          <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
        </div>
      ) : files.length === 0 ? (
        <div className="text-center py-12 text-slate-400">
          <span className="material-symbols-outlined text-4xl mb-2">folder_open</span>
          <p className="text-sm">No files uploaded yet</p>
        </div>
      ) : (
        <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 divide-y divide-slate-200 dark:divide-slate-800">
          {files.map((file) => (
            <div key={file.fileId} className="flex items-center justify-between py-3 px-4">
              <div className="flex items-center gap-3 min-w-0">
                <span className="material-symbols-outlined text-slate-400">
                  {getFileIcon(file.fileType)}
                </span>
                <div className="min-w-0">
                  <p className="text-sm font-medium text-slate-900 dark:text-white truncate">
                    {file.fileName}
                  </p>
                  <p className="text-xs text-slate-400">
                    {formatFileSize(file.size)} &bull; {formatDate(file.uploadedAt)}
                  </p>
                </div>
              </div>
              <button
                onClick={() => handleDelete(file.fileId)}
                className="p-2 text-slate-400 hover:text-red-500 transition-colors"
                title="Delete file"
              >
                <span className="material-symbols-outlined text-xl">delete</span>
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
