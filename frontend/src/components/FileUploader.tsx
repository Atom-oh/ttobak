'use client';

import { useState, useRef, useCallback } from 'react';
import { uploadFile, notifyUploadComplete, formatFileSize, UploadProgress } from '@/lib/upload';
import { ApiError } from '@/lib/api';

interface FileUploaderProps {
  meetingId?: string;
  onUploadComplete?: (files: { name: string; url: string; mimeType?: string }[]) => void;
  onAttachmentsChanged?: () => void | Promise<void>;
  onError?: (error: string) => void;
  accept?: string;
  multiple?: boolean;
  maxFiles?: number;
  maxSize?: number; // in bytes
}

interface UploadingFile {
  file: File;
  progress: number;
  status: 'pending' | 'uploading' | 'complete' | 'warning' | 'error';
  error?: string;
  url?: string;
}

function getFileIcon(mimeType: string, fileName: string): string {
  if (mimeType.startsWith('image/')) return 'image';
  if (mimeType.startsWith('video/')) return 'videocam';
  if (mimeType.startsWith('audio/')) return 'audio_file';
  const ext = fileName.split('.').pop()?.toLowerCase() || '';
  if (['ppt', 'pptx'].includes(ext)) return 'slideshow';
  if (['xls', 'xlsx', 'csv'].includes(ext)) return 'table_chart';
  return 'description';
}

export function FileUploader({
  meetingId,
  onUploadComplete,
  onAttachmentsChanged,
  onError,
  accept = 'image/*,video/*,audio/*,.md,.ppt,.pptx,.docx,.doc,.pdf,.txt,.json,.csv,.xls,.xlsx',
  multiple = true,
  maxFiles = 10,
  maxSize = 500 * 1024 * 1024, // 500MB
}: FileUploaderProps) {
  const [isDragging, setIsDragging] = useState(false);
  const [files, setFiles] = useState<UploadingFile[]>([]);
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const uploadingRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleFiles = useCallback(
    async (fileList: FileList) => {
      if (uploadingRef.current) return;
      const newFiles = Array.from(fileList).slice(0, maxFiles - files.length);

      // Validate files
      const validFiles = newFiles.filter((file) => {
        if (file.size > maxSize) {
          onError?.(`${file.name} exceeds maximum size of ${formatFileSize(maxSize)}`);
          return false;
        }
        return true;
      });

      if (validFiles.length === 0) return;
      uploadingRef.current = true;
      setUploading(true);
      setRefreshError(null);

      const uploadingFiles: UploadingFile[] = validFiles.map((file) => ({
        file,
        progress: 0,
        status: 'pending',
      }));

      setFiles((prev) => [...prev, ...uploadingFiles]);

      // Upload files sequentially
      const results: { name: string; url: string; mimeType?: string }[] = [];
      let notified = false;

      for (let i = 0; i < validFiles.length; i++) {
        const file = validFiles[i];
        const fileIndex = files.length + i;

        setFiles((prev) =>
          prev.map((f, idx) =>
            idx === fileIndex ? { ...f, status: 'uploading' } : f
          )
        );

        try {
          const result = await uploadFile(file, (progress: UploadProgress) => {
            setFiles((prev) =>
              prev.map((f, idx) =>
                idx === fileIndex ? { ...f, progress: progress.percentage } : f
              )
            );
          }, meetingId);

          // Notify backend that upload is complete
          if (meetingId) {
            const category = file.type.startsWith('image/') ? 'image' as const : 'file' as const;
            notified = true;
            try {
              await notifyUploadComplete(meetingId, result.key, category, {
                fileName: file.name, fileSize: file.size, mimeType: file.type,
              });
            } catch (error) {
              const message = error instanceof ApiError && error.code === 'UNSUPPORTED_FORMAT'
                ? '파일은 업로드되었지만 이 형식은 텍스트 추출을 지원하지 않습니다.'
                : `파일 전송 후 처리를 확인하지 못했습니다. 첨부 목록에서 상태를 확인해 주세요. ${error instanceof Error ? error.message : ''}`;
              setFiles((previous) => previous.map((entry, index) => index === fileIndex
                ? { ...entry, status: 'warning', progress: 100, url: result.url, error: message } : entry));
              continue;
            }
          }

          setFiles((prev) =>
            prev.map((f, idx) =>
              idx === fileIndex
                ? { ...f, status: 'complete', progress: 100, url: result.url }
                : f
            )
          );

          results.push({ name: file.name, url: result.url, mimeType: file.type });
        } catch (err) {
          setFiles((prev) =>
            prev.map((f, idx) =>
              idx === fileIndex
                ? {
                    ...f,
                    status: 'error',
                    error: err instanceof Error ? err.message : 'Upload failed',
                  }
                : f
            )
          );
        }
      }

      if (notified && onAttachmentsChanged) {
        try { await onAttachmentsChanged(); }
        catch (error) { setRefreshError(error instanceof Error ? error.message : '첨부 목록을 갱신하지 못했습니다.'); }
      }
      uploadingRef.current = false;
      setUploading(false);
      if (results.length > 0) {
        onUploadComplete?.(results);
      }
    },
    [files.length, maxFiles, maxSize, meetingId, onUploadComplete, onAttachmentsChanged, onError]
  );

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
    if (e.dataTransfer.files.length > 0) {
      handleFiles(e.dataTransfer.files);
    }
  };

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files.length > 0) {
      handleFiles(e.target.files);
    }
  };

  const removeFile = (index: number) => {
    if (uploadingRef.current) return;
    setFiles((prev) => prev.filter((_, i) => i !== index));
  };

  return (
    <div className="space-y-4">
      {/* Drop Zone */}
      <div
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        onClick={() => { if (!uploading) fileInputRef.current?.click(); }}
        aria-disabled={uploading}
        className={`border-2 border-dashed rounded-xl p-8 text-center cursor-pointer transition-all ${
          isDragging
            ? 'border-primary bg-primary/5'
            : 'border-slate-200 dark:border-slate-700 hover:border-primary/40 hover:bg-slate-50 dark:hover:bg-slate-800/50'
        }`}
      >
        <input
          ref={fileInputRef}
          type="file"
          accept={accept}
          multiple={multiple}
          disabled={uploading}
          onChange={handleInputChange}
          className="hidden"
        />
        <span
          className={`material-symbols-outlined text-4xl mb-2 ${
            isDragging ? 'text-primary' : 'text-slate-400'
          }`}
        >
          cloud_upload
        </span>
        <p className="text-slate-600 dark:text-slate-400 font-medium">
          {uploading ? '파일을 처리하는 중…' : isDragging ? '여기에 파일을 놓으세요' : '파일을 드래그하거나 클릭하여 첨부'}
        </p>
        <p className="text-slate-400 text-sm mt-1">
          이미지, 문서, 동영상, 음성 파일 (최대 {formatFileSize(maxSize)})
        </p>
      </div>
      {refreshError && <p role="alert" className="text-sm text-red-600 dark:text-red-300">{refreshError}</p>}

      {/* File List */}
      {files.length > 0 && (
        <div className="space-y-2">
          {files.map((file, index) => (
            <div
              key={index}
              className="flex items-center gap-3 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg p-3"
            >
              {/* Thumbnail or Icon */}
              <div className="w-12 h-12 bg-slate-100 dark:bg-slate-700 rounded-lg flex items-center justify-center overflow-hidden flex-shrink-0">
                {file.file.type.startsWith('image/') && file.url ? (
                  <img
                    src={file.url}
                    alt={file.file.name}
                    className="w-full h-full object-cover"
                  />
                ) : (
                  <span className="material-symbols-outlined text-slate-400">
                    {getFileIcon(file.file.type, file.file.name)}
                  </span>
                )}
              </div>

              {/* File Info */}
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium text-slate-900 dark:text-white truncate">
                  {file.file.name}
                </p>
                <p className="text-xs text-slate-500">{formatFileSize(file.file.size)}</p>

                {/* Progress Bar */}
                {file.status === 'uploading' && (
                  <div className="mt-1 h-1 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                    <div
                      className="h-full bg-primary transition-all"
                      style={{ width: `${file.progress}%` }}
                    />
                  </div>
                )}

                {/* Error Message */}
                {(file.status === 'error' || file.status === 'warning') && (
                  <p role="alert" className={`mt-1 text-xs ${file.status === 'warning' ? 'text-amber-700 dark:text-amber-200' : 'text-red-500'}`}>{file.error}</p>
                )}
              </div>

              {/* Status/Actions */}
              <div className="flex-shrink-0">
                {file.status === 'complete' && (
                  <span className="material-symbols-outlined text-green-500">check_circle</span>
                )}
                {file.status === 'warning' && <span className="material-symbols-outlined text-amber-600" aria-label="처리 확인 필요">warning</span>}
                {file.status === 'uploading' && (
                  <span className="text-xs text-slate-500">{file.progress}%</span>
                )}
                {file.status === 'error' && (
                  <button
                          onClick={() => removeFile(index)}
                    disabled={uploading}
                    className="text-slate-400 hover:text-red-500"
                  >
                    <span className="material-symbols-outlined">close</span>
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
