'use client';

// Unified meeting uploader: stage audio AND documents (PDF/PPT/…)/images in one
// list, then upload everything with a single "완료" button. Nothing is uploaded
// (and audio analysis is NOT triggered) until 완료 is pressed. Audio files are
// uploaded as multi-part (indexed among the AUDIO files only) so the transcribe
// pipeline merges them; documents become meeting attachments and are auto-promoted
// to the Knowledge Base so the AI chat can retrieve them (RAG).

import { useState, useRef, useCallback } from 'react';
import { uploadToS3, notifyUploadComplete, formatFileSize, type UploadProgress } from '@/lib/upload';
import { kbApi } from '@/lib/api';

interface AudioUploaderProps {
  meetingId: string;
  onUploadComplete: () => void;
}

type FileKind = 'audio' | 'image' | 'document';

interface FileEntry {
  id: string;
  file: File;
  kind: FileKind;
  status: 'pending' | 'uploading' | 'done' | 'error';
  progress?: UploadProgress;
  error?: string;
  kbFailed?: boolean; // document uploaded but KB promotion failed (chat RAG unavailable)
}

const AUDIO_RE = /\.(mp3|wav|m4a|webm|ogg|flac|aac|mp4)$/i;
const DOC_RE = /\.(pdf|ppt|pptx|doc|docx|txt|md|csv|xls|xlsx)$/i;
const MAX_SIZE = 500 * 1024 * 1024; // 500MB
const ACCEPT_STRING =
  '.mp3,.wav,.m4a,.webm,.ogg,.flac,.aac,.mp4,.pdf,.ppt,.pptx,.doc,.docx,.txt,.md,.csv,.xls,.xlsx,image/*';

function classifyFile(file: File): FileKind {
  if (file.type.startsWith('audio/') || AUDIO_RE.test(file.name)) return 'audio';
  if (file.type.startsWith('image/')) return 'image';
  return 'document';
}

// `accept` is only a picker hint — drag&drop bypasses it. Enforce an explicit
// whitelist so arbitrary files can't be classified as 'document' and pushed to
// the KB. Audio/image are recognized by extension or MIME; documents by extension.
function isAllowedFile(file: File): boolean {
  if (file.type.startsWith('audio/') || AUDIO_RE.test(file.name)) return true;
  if (file.type.startsWith('image/')) return true;
  return DOC_RE.test(file.name);
}

function iconFor(kind: FileKind): string {
  return kind === 'audio' ? 'audio_file' : kind === 'image' ? 'image' : 'description';
}

function validateFile(file: File): string | null {
  if (!isAllowedFile(file)) {
    return `지원하지 않는 파일 형식입니다: ${file.name}. 오디오·이미지·문서(PDF/PPT/DOC/XLS/CSV/TXT/MD)만 업로드할 수 있습니다.`;
  }
  if (file.size > MAX_SIZE) {
    return `파일 크기가 너무 큽니다 (${formatFileSize(file.size)}). 최대 500MB까지 지원합니다.`;
  }
  return null;
}

export function AudioUploader({ meetingId, onUploadComplete }: AudioUploaderProps) {
  const [isDragging, setIsDragging] = useState(false);
  const [files, setFiles] = useState<FileEntry[]>([]);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const addMoreRef = useRef<HTMLInputElement>(null);
  const idCounter = useRef(0);

  const addFiles = useCallback((newFiles: File[]) => {
    const entries: FileEntry[] = [];
    for (const f of newFiles) {
      const err = validateFile(f);
      if (err) {
        setError(err);
        continue;
      }
      entries.push({
        id: `f${idCounter.current++}-${f.name}`,
        file: f,
        kind: classifyFile(f),
        status: 'pending',
      });
    }
    if (entries.length > 0) {
      setError(null);
      setFiles((prev) => [...prev, ...entries]);
    }
  }, []);

  const removeFile = useCallback((id: string) => {
    setFiles((prev) => prev.filter((f) => f.id !== id));
  }, []);

  const handleUploadAll = useCallback(async () => {
    if (files.length === 0) return;
    setUploading(true);
    setError(null);

    // Multi-part indexing applies to AUDIO files only — the transcribe pipeline
    // counts audio parts. Documents/images are independent attachments.
    // Precompute index once (stable) rather than indexOf per entry.
    const audioIndex = new Map<string, number>();
    files.filter((f) => f.kind === 'audio').forEach((f, i) => audioIndex.set(f.id, i));
    const audioTotal = audioIndex.size;
    let allDone = true;

    for (const entry of files) {
      if (entry.status === 'done') continue;
      setFiles((prev) => prev.map((f) => (f.id === entry.id ? { ...f, status: 'uploading' as const } : f)));

      try {
        const category: 'audio' | 'image' | 'file' =
          entry.kind === 'audio' ? 'audio' : entry.kind === 'image' ? 'image' : 'file';

        const isMultiAudio = entry.kind === 'audio' && audioTotal > 1;
        const partIndex = isMultiAudio ? audioIndex.get(entry.id) : undefined;
        const totalParts = isMultiAudio ? audioTotal : undefined;

        const result = await uploadToS3(
          entry.file,
          category,
          (progress) => setFiles((prev) => prev.map((f) => (f.id === entry.id ? { ...f, progress } : f))),
          meetingId,
          partIndex,
          totalParts,
        );
        await notifyUploadComplete(meetingId, result.key, category, {
          fileName: entry.file.name,
          fileSize: entry.file.size,
          mimeType: entry.file.type,
          partIndex,
          totalParts,
        });

        // Auto-promote documents into the Knowledge Base for chat RAG (best-effort).
        // The file is already attached; a KB failure is surfaced per-row, not fatal.
        let kbFailed = false;
        if (entry.kind === 'document') {
          try {
            await kbApi.copyAttachment(result.key);
          } catch (kbErr) {
            kbFailed = true;
            console.warn('KB promote failed (non-fatal):', kbErr);
          }
        }

        setFiles((prev) =>
          prev.map((f) => (f.id === entry.id ? { ...f, status: 'done' as const, kbFailed } : f)),
        );
      } catch (err) {
        allDone = false;
        setFiles((prev) =>
          prev.map((f) =>
            f.id === entry.id
              ? { ...f, status: 'error' as const, error: err instanceof Error ? err.message : '업로드 실패' }
              : f,
          ),
        );
      }
    }

    setUploading(false);
    if (allDone) onUploadComplete();
  }, [files, meetingId, onUploadComplete]);

  const handleDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      setIsDragging(false);
      addFiles(Array.from(e.dataTransfer.files));
    },
    [addFiles],
  );

  const handleFileSelect = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      if (e.target.files) addFiles(Array.from(e.target.files));
      if (inputRef.current) inputRef.current.value = '';
      if (addMoreRef.current) addMoreRef.current.value = '';
    },
    [addFiles],
  );

  if (files.length > 0) {
    const docCount = files.filter((f) => f.kind === 'document').length;
    return (
      <div className="w-full max-w-2xl mx-auto mt-8 space-y-3">
        {files.map((entry, idx) => (
          <div
            key={entry.id}
            className="bg-white dark:bg-surface-lowest border border-slate-200 dark:border-white/10 rounded-xl p-4 flex items-center gap-3"
          >
            <span className="text-xs font-bold text-slate-400 w-6 text-center">{idx + 1}</span>
            <span className="material-symbols-outlined text-primary/40">{iconFor(entry.kind)}</span>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-slate-900 dark:text-gray-100 truncate">{entry.file.name}</p>
              <p className="text-xs text-slate-400">
                {formatFileSize(entry.file.size)}
                {entry.kind === 'document' && ' · 문서 (KB 등록)'}
                {entry.kind === 'image' && ' · 이미지'}
              </p>
              {entry.status === 'uploading' && entry.progress && (
                <div className="mt-1.5 h-1.5 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                  <div
                    className="h-full bg-primary rounded-full transition-all duration-300"
                    style={{ width: `${entry.progress.percentage}%` }}
                  />
                </div>
              )}
              {entry.status === 'error' && <p className="text-xs text-red-500 mt-1">{entry.error}</p>}
              {entry.status === 'done' && entry.kbFailed && (
                <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">
                  첨부됨 · 지식베이스 등록 실패 — 채팅 검색에서 제외됩니다.
                </p>
              )}
            </div>
            {entry.status === 'done' && <span className="material-symbols-outlined text-green-500">check_circle</span>}
            {entry.status === 'uploading' && (
              <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent" />
            )}
            {entry.status === 'pending' && !uploading && (
              <button
                onClick={() => removeFile(entry.id)}
                className="text-slate-400 hover:text-red-500 transition-colors"
              >
                <span className="material-symbols-outlined text-lg">close</span>
              </button>
            )}
          </div>
        ))}

        {docCount > 0 && (
          <p className="text-xs text-slate-400 dark:text-text-muted px-1">
            문서 {docCount}개는 미팅에 첨부되고 지식베이스에 등록돼 채팅에서 검색됩니다.
          </p>
        )}

        {!uploading && (
          <div className="flex items-center gap-3">
            <button
              onClick={() => addMoreRef.current?.click()}
              className="flex items-center gap-1.5 px-3 py-2 text-sm text-slate-500 hover:text-primary border border-dashed border-slate-300 dark:border-slate-600 rounded-lg hover:border-primary/50 transition-colors"
            >
              <span className="material-symbols-outlined text-lg">add</span>
              파일 추가
            </button>
            <input ref={addMoreRef} type="file" accept={ACCEPT_STRING} multiple onChange={handleFileSelect} className="hidden" />
            <div className="flex-1" />
            <button
              onClick={handleUploadAll}
              disabled={files.every((f) => f.status === 'done')}
              className="px-5 py-2.5 bg-primary text-white text-sm font-medium rounded-lg hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              완료 ({files.length})
            </button>
          </div>
        )}

        {error && (
          <div className="px-4 py-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg flex items-start gap-2">
            <span className="material-symbols-outlined text-red-500 text-lg mt-0.5">error</span>
            <p className="text-sm text-red-700 dark:text-red-300">{error}</p>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="w-full max-w-2xl mx-auto mt-8">
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setIsDragging(true);
        }}
        onDragLeave={() => setIsDragging(false)}
        onDrop={handleDrop}
        onClick={() => inputRef.current?.click()}
        className={`cursor-pointer border-2 border-dashed rounded-xl p-8 text-center transition-all ${
          isDragging
            ? 'border-primary bg-primary/5'
            : 'border-slate-300 dark:border-slate-600 hover:border-primary/50 hover:bg-primary/5'
        }`}
      >
        <span className="material-symbols-outlined text-4xl text-primary/40 mb-2 block">upload_file</span>
        <p className="text-sm font-medium text-slate-900 dark:text-gray-100">
          음성·문서 파일을 드래그하거나 클릭하여 추가
        </p>
        <p className="text-xs text-slate-400 mt-1">
          오디오(mp3·m4a·wav…) + PDF·PPT·문서 · 여러 개 담은 뒤 &quot;완료&quot;로 한 번에 업로드 · 최대 500MB
        </p>
        <input ref={inputRef} type="file" accept={ACCEPT_STRING} multiple onChange={handleFileSelect} className="hidden" />
      </div>

      {error && (
        <div className="mt-3 px-4 py-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg flex items-start gap-2">
          <span className="material-symbols-outlined text-red-500 text-lg mt-0.5">error</span>
          <p className="text-sm text-red-700 dark:text-red-300">{error}</p>
        </div>
      )}
    </div>
  );
}
