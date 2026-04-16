'use client';

import { uploadsApi } from './api';

export interface UploadProgress {
  loaded: number;
  total: number;
  percentage: number;
}

export interface UploadResult {
  key: string;
  url: string;
}

export async function uploadToS3(
  file: File,
  category: 'audio' | 'image' | 'file',
  onProgress?: (progress: UploadProgress) => void,
  meetingId?: string
): Promise<UploadResult> {
  // Get presigned URL from backend
  const { uploadUrl, key } = await uploadsApi.getPresignedUrl({
    fileName: file.name,
    fileType: file.type,
    category,
    meetingId,
  });

  // Upload directly to S3
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();

    xhr.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable && onProgress) {
        onProgress({
          loaded: event.loaded,
          total: event.total,
          percentage: Math.round((event.loaded / event.total) * 100),
        });
      }
    });

    xhr.addEventListener('load', () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        // Extract the base URL without query params
        const url = uploadUrl.split('?')[0];
        resolve({ key, url });
      } else {
        reject(new Error(`Upload failed with status ${xhr.status}`));
      }
    });

    xhr.addEventListener('error', () => {
      reject(new Error('Upload failed'));
    });

    xhr.addEventListener('abort', () => {
      reject(new Error('Upload aborted'));
    });

    xhr.open('PUT', uploadUrl);
    xhr.setRequestHeader('Content-Type', file.type);
    xhr.send(file);
  });
}

export async function uploadAudioBlob(
  blob: Blob,
  fileName: string,
  onProgress?: (progress: UploadProgress) => void
): Promise<UploadResult> {
  const file = new File([blob], fileName, { type: blob.type });
  return uploadToS3(file, 'audio', onProgress);
}

export async function uploadImage(
  file: File,
  onProgress?: (progress: UploadProgress) => void
): Promise<UploadResult> {
  return uploadToS3(file, 'image', onProgress);
}

function getCategoryFromMime(mimeType: string): 'image' | 'file' {
  if (mimeType.startsWith('image/')) return 'image';
  return 'file';
}

export async function uploadFile(
  file: File,
  onProgress?: (progress: UploadProgress) => void,
  meetingId?: string,
): Promise<UploadResult> {
  const category = getCategoryFromMime(file.type);
  return uploadToS3(file, category, onProgress, meetingId);
}

/** Upload audio blob to a presigned URL with retry logic */
export async function uploadAudioWithRetry(
  blob: Blob,
  presignedUrl: string,
  mimeType: string,
  maxRetries = 2,
): Promise<void> {
  for (let attempt = 0; attempt <= maxRetries; attempt++) {
    try {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 30000);
      const res = await fetch(presignedUrl, {
        method: 'PUT',
        body: blob,
        headers: { 'Content-Type': mimeType || 'audio/webm' },
        signal: controller.signal,
      });
      clearTimeout(timeout);
      if (res.ok) return;
      throw new Error(`Upload failed with status ${res.status}`);
    } catch (err) {
      if (attempt === maxRetries) {
        throw err instanceof Error ? err : new Error('Audio upload failed');
      }
      // Wait before retry
      await new Promise(r => setTimeout(r, 2000));
    }
  }
}

export async function notifyUploadComplete(
  meetingId: string,
  key: string,
  category: 'audio' | 'image' | 'file',
  metadata?: { fileName?: string; fileSize?: number; mimeType?: string },
): Promise<void> {
  await uploadsApi.notifyComplete({ meetingId, key, category, ...metadata });
}

export function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 Bytes';

  const k = 1024;
  const sizes = ['Bytes', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));

  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(2))} ${sizes[i]}`;
}
