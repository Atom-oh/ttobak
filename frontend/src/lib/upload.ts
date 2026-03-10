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
  category: 'audio' | 'image',
  onProgress?: (progress: UploadProgress) => void
): Promise<UploadResult> {
  // Get presigned URL from backend
  const { uploadUrl, key } = await uploadsApi.getPresignedUrl({
    fileName: file.name,
    fileType: file.type,
    category,
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

export async function notifyUploadComplete(
  meetingId: string,
  key: string,
  category: 'audio' | 'image'
): Promise<void> {
  await uploadsApi.notifyComplete({ meetingId, key, category });
}

export function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 Bytes';

  const k = 1024;
  const sizes = ['Bytes', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));

  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(2))} ${sizes[i]}`;
}
