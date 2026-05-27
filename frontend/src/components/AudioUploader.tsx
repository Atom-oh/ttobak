'use client';

import { useState, useRef, useCallback } from 'react';
import { uploadToS3, notifyUploadComplete, formatFileSize, type UploadProgress } from '@/lib/upload';

interface AudioUploaderProps {
  meetingId: string;
  onUploadComplete: () => void;
}

interface FileEntry {
  id: string;
  file: File;
  status: 'pending' | 'uploading' | 'done' | 'error';
  progress?: UploadProgress;
  error?: string;
}

const ACCEPTED_TYPES = [
  'audio/mpeg', 'audio/wav', 'audio/mp4', 'audio/webm',
  'audio/ogg', 'audio/flac', 'audio/x-m4a', 'audio/aac',
];
const MAX_SIZE = 500 * 1024 * 1024; // 500MB
const ACCEPT_STRING = '.mp3,.wav,.m4a,.webm,.ogg,.flac,.aac,.mp4';

function validateFile(file: File): string | null {
  if (!ACCEPTED_TYPES.includes(file.type) && !file.name.match(/\.(mp3|wav|m4a|webm|ogg|flac|aac|mp4)$/i)) {
    return '지원하지 않는 파일 형식입니다. mp3, wav, m4a, webm, ogg, flac을 사용해주세요.';
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

  const addFiles = useCallback((newFiles: File[]) => {
    const entries: FileEntry[] = [];
    for (const f of newFiles) {
      const err = validateFile(f);
      if (err) {
        setError(err);
        continue;
      }
      entries.push({ id: `${Date.now()}-${f.name}`, file: f, status: 'pending' });
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

    const totalParts = files.length;
    let allDone = true;

    for (let i = 0; i < files.length; i++) {
      const entry = files[i];
      if (entry.status === 'done') continue;

      setFiles((prev) =>
        prev.map((f) => f.id === entry.id ? { ...f, status: 'uploading' as const } : f)
      );

      try {
        const result = await uploadToS3(
          entry.file,
          'audio',
          (progress) => {
            setFiles((prev) =>
              prev.map((f) => f.id === entry.id ? { ...f, progress } : f)
            );
          },
          meetingId,
          totalParts > 1 ? i : undefined,
          totalParts > 1 ? totalParts : undefined,
        );
        await notifyUploadComplete(meetingId, result.key, 'audio', {
          fileName: entry.file.name,
          fileSize: entry.file.size,
          mimeType: entry.file.type,
          partIndex: totalParts > 1 ? i : undefined,
          totalParts: totalParts > 1 ? totalParts : undefined,
        });
        setFiles((prev) =>
          prev.map((f) => f.id === entry.id ? { ...f, status: 'done' as const } : f)
        );
      } catch (err) {
        allDone = false;
        setFiles((prev) =>
          prev.map((f) =>
            f.id === entry.id
              ? { ...f, status: 'error' as const, error: err instanceof Error ? err.message : '업로드 실패' }
              : f
          )
        );
      }
    }

    setUploading(false);
    if (allDone) onUploadComplete();
  }, [files, meetingId, onUploadComplete]);

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    addFiles(Array.from(e.dataTransfer.files));
  }, [addFiles]);

  const handleFileSelect = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) addFiles(Array.from(e.target.files));
    if (inputRef.current) inputRef.current.value = '';
    if (addMoreRef.current) addMoreRef.current.value = '';
  }, [addFiles]);

  if (files.length > 0) {
    return (
      <div className="w-full max-w-2xl mx-auto mt-8 space-y-3">
        {files.map((entry, idx) => (
          <div
            key={entry.id}
            className="bg-white dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 rounded-xl p-4 flex items-center gap-3"
          >
            <span className="text-xs font-bold text-slate-400 w-6 text-center">{idx + 1}</span>
            <span className="material-symbols-outlined text-primary/40">audio_file</span>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-slate-900 dark:text-gray-100 truncate">
                {entry.file.name}
              </p>
              <p className="text-xs text-slate-400">{formatFileSize(entry.file.size)}</p>
              {entry.status === 'uploading' && entry.progress && (
                <div className="mt-1.5 h-1.5 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                  <div
                    className="h-full bg-primary rounded-full transition-all duration-300"
                    style={{ width: `${entry.progress.percentage}%` }}
                  />
                </div>
              )}
              {entry.status === 'error' && (
                <p className="text-xs text-red-500 mt-1">{entry.error}</p>
              )}
            </div>
            {entry.status === 'done' && (
              <span className="material-symbols-outlined text-green-500">check_circle</span>
            )}
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

        {!uploading && (
          <div className="flex items-center gap-3">
            <button
              onClick={() => addMoreRef.current?.click()}
              className="flex items-center gap-1.5 px-3 py-2 text-sm text-slate-500 hover:text-primary border border-dashed border-slate-300 dark:border-slate-600 rounded-lg hover:border-primary/50 transition-colors"
            >
              <span className="material-symbols-outlined text-lg">add</span>
              파일 추가
            </button>
            <input
              ref={addMoreRef}
              type="file"
              accept={ACCEPT_STRING}
              onChange={handleFileSelect}
              className="hidden"
            />
            <div className="flex-1" />
            <button
              onClick={handleUploadAll}
              disabled={files.every((f) => f.status === 'done')}
              className="px-5 py-2.5 bg-primary text-white text-sm font-medium rounded-lg hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {files.length === 1 ? '업로드' : `${files.length}개 파일 업로드`}
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
        onDragOver={(e) => { e.preventDefault(); setIsDragging(true); }}
        onDragLeave={() => setIsDragging(false)}
        onDrop={handleDrop}
        onClick={() => inputRef.current?.click()}
        className={`cursor-pointer border-2 border-dashed rounded-xl p-8 text-center transition-all ${
          isDragging
            ? 'border-primary bg-primary/5'
            : 'border-slate-300 dark:border-slate-600 hover:border-primary/50 hover:bg-primary/5'
        }`}
      >
        <span className="material-symbols-outlined text-4xl text-primary/40 mb-2 block">
          audio_file
        </span>
        <p className="text-sm font-medium text-slate-900 dark:text-gray-100">
          오디오 파일을 드래그하거나 클릭하여 선택
        </p>
        <p className="text-xs text-slate-400 mt-1">
          mp3, wav, m4a, webm 지원 · 최대 500MB · 여러 파일 가능
        </p>
        <input
          ref={inputRef}
          type="file"
          accept={ACCEPT_STRING}
          multiple
          onChange={handleFileSelect}
          className="hidden"
        />
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
