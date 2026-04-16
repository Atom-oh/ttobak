'use client';

import { useState, useRef, useCallback } from 'react';
import { uploadToS3, notifyUploadComplete, formatFileSize, type UploadProgress } from '@/lib/upload';

interface AudioUploaderProps {
  meetingId: string;
  onUploadComplete: () => void;
}

const ACCEPTED_TYPES = [
  'audio/mpeg', 'audio/wav', 'audio/mp4', 'audio/webm',
  'audio/ogg', 'audio/flac', 'audio/x-m4a', 'audio/aac',
];
const MAX_SIZE = 500 * 1024 * 1024; // 500MB
const ACCEPT_STRING = '.mp3,.wav,.m4a,.webm,.ogg,.flac,.aac,.mp4';

export function AudioUploader({ meetingId, onUploadComplete }: AudioUploaderProps) {
  const [isDragging, setIsDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [progress, setProgress] = useState<UploadProgress | null>(null);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const validateFile = (file: File): string | null => {
    if (!ACCEPTED_TYPES.includes(file.type) && !file.name.match(/\.(mp3|wav|m4a|webm|ogg|flac|aac|mp4)$/i)) {
      return '지원하지 않는 파일 형식입니다. mp3, wav, m4a, webm, ogg, flac을 사용해주세요.';
    }
    if (file.size > MAX_SIZE) {
      return `파일 크기가 너무 큽니다 (${formatFileSize(file.size)}). 최대 500MB까지 지원합니다.`;
    }
    return null;
  };

  const handleUpload = useCallback(async (file: File) => {
    const validationError = validateFile(file);
    if (validationError) {
      setError(validationError);
      return;
    }

    setError(null);
    setUploading(true);
    setProgress(null);

    try {
      const result = await uploadToS3(file, 'audio', setProgress, meetingId);
      await notifyUploadComplete(meetingId, result.key, 'audio');
      onUploadComplete();
    } catch (err) {
      setError(err instanceof Error ? err.message : '업로드에 실패했습니다.');
    } finally {
      setUploading(false);
    }
  }, [meetingId, onUploadComplete]);

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    const file = e.dataTransfer.files[0];
    if (file) handleUpload(file);
  }, [handleUpload]);

  const handleFileSelect = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) handleUpload(file);
    if (inputRef.current) inputRef.current.value = '';
  }, [handleUpload]);

  if (uploading) {
    return (
      <div className="w-full max-w-2xl mx-auto mt-8">
        <div className="bg-white dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 rounded-xl p-6">
          <div className="flex items-center gap-3 mb-3">
            <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent" />
            <span className="text-sm font-medium text-slate-900 dark:text-gray-100">
              오디오 업로드 중...
            </span>
          </div>
          {progress && (
            <>
              <div className="h-2 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                <div
                  className="h-full bg-primary rounded-full transition-all duration-300"
                  style={{ width: `${progress.percentage}%` }}
                />
              </div>
              <p className="text-xs text-slate-400 mt-2">
                {formatFileSize(progress.loaded)} / {formatFileSize(progress.total)} ({progress.percentage}%)
              </p>
            </>
          )}
        </div>
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
          mp3, wav, m4a, webm 지원 · 최대 500MB
        </p>
        <input
          ref={inputRef}
          type="file"
          accept={ACCEPT_STRING}
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
