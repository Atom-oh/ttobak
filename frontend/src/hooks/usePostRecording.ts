'use client';

import { useState, useCallback, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { meetingsApi, uploadsApi } from '@/lib/api';
import { uploadAudioWithRetry } from '@/lib/upload';
import type { PostRecordingStep } from '@/components/record/PostRecordingBanner';

function formatDefaultTitle(date: Date): string {
  const month = date.getMonth() + 1;
  const day = date.getDate();
  const hour = date.getHours();
  const minute = date.getMinutes();
  return minute > 0
    ? `${month}월 ${day}일 ${hour}시 ${minute}분 미팅`
    : `${month}월 ${day}일 ${hour}시 미팅`;
}

/** Race a promise against a timeout */
function withTimeout<T>(promise: Promise<T>, ms: number, label: string): Promise<T> {
  return Promise.race([
    promise,
    new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error(`${label} timed out (${ms / 1000}s)`)), ms),
    ),
  ]);
}

interface UsePostRecordingOptions {
  meetingTitle: string;
  sttProvider: 'transcribe' | 'nova-sonic';
}

export function usePostRecording({
  meetingTitle,
  sttProvider,
}: UsePostRecordingOptions) {
  const router = useRouter();
  const [step, setStep] = useState<PostRecordingStep | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [serverMeetingId, setServerMeetingId] = useState<string | null>(null);

  // Hold the recording blob while user writes notes
  const pendingBlobRef = useRef<{ blob: Blob; mimeType: string } | null>(null);

  /** Create a draft meeting at recording start for crash recovery */
  const createDraftMeeting = useCallback(async (): Promise<string | null> => {
    try {
      const result = await withTimeout(
        meetingsApi.create({
          title: meetingTitle || formatDefaultTitle(new Date()),
          sttProvider,
          status: 'recording',
        }),
        15000, 'Create draft meeting',
      );
      setServerMeetingId(result.meetingId);
      return result.meetingId;
    } catch (err) {
      console.error('Failed to create draft meeting:', err);
      return null;
    }
  }, [meetingTitle, sttProvider]);

  /** Resume the save+upload flow after notes step */
  const resumeUploadFlow = useCallback(async (blob: Blob, mimeType: string) => {
    try {
      let meetingId = serverMeetingId;

      if (meetingId) {
        setStep('saving');
        await withTimeout(
          meetingsApi.update(meetingId, {
            title: meetingTitle || formatDefaultTitle(new Date()),
            status: 'transcribing',
          }),
          15000, 'Save transcript',
        );
      } else {
        // Fallback: draft creation failed, create meeting now
        setStep('creating');
        const result = await withTimeout(
          meetingsApi.create({ title: meetingTitle || formatDefaultTitle(new Date()), sttProvider }),
          15000, 'Create meeting',
        );
        meetingId = result.meetingId;
        setServerMeetingId(meetingId);

        setStep('saving');
        await withTimeout(
          meetingsApi.update(meetingId, {
            status: 'transcribing',
          }),
          15000, 'Save transcript',
        );
      }

      // Upload final audio
      setStep('uploading');
      const resolvedMime = mimeType || 'audio/webm';
      const ext = resolvedMime.includes('mp4') ? 'm4a'
                : resolvedMime.includes('ogg') ? 'ogg'
                : 'webm';
      const fileName = `recording_${Date.now()}.${ext}`;
      const { uploadUrl, key } = await withTimeout(
        uploadsApi.getPresignedUrl({
          fileName,
          fileType: resolvedMime,
          category: 'audio',
          meetingId,
        }),
        15000, 'Get upload URL',
      );
      await uploadAudioWithRetry(blob, uploadUrl, mimeType);
      await withTimeout(
        uploadsApi.notifyComplete({ meetingId, key, category: 'audio' }),
        15000, 'Notify upload complete',
      );

      // Redirect
      setStep(null);
      router.push(`/meeting/${meetingId}`);
    } catch (err) {
      console.error('Failed to process recording:', err);
      setErrorMessage(err instanceof Error ? err.message : 'Failed to process recording');
      setStep('error');
    }
  }, [meetingTitle, sttProvider, router, serverMeetingId]);

  /** Called when recording blob is ready — pause for notes input */
  const handleBlobReady = useCallback(async (blob: Blob, mimeType: string) => {
    pendingBlobRef.current = { blob, mimeType };
    setStep('notes');
  }, []);

  /** User submitted notes — save to meeting then resume upload */
  const handleNotesSubmit = useCallback(async (notes: string) => {
    const pending = pendingBlobRef.current;
    if (!pending) return;
    pendingBlobRef.current = null;

    try {
      // Save notes to meeting if we have a draft
      if (serverMeetingId && notes.trim()) {
        await withTimeout(
          meetingsApi.update(serverMeetingId, { notes: notes.trim() }),
          15000, 'Save meeting notes',
        );
      }
    } catch (err) {
      console.warn('Failed to save notes, continuing with upload:', err);
    }

    await resumeUploadFlow(pending.blob, pending.mimeType);
  }, [serverMeetingId, resumeUploadFlow]);

  /** User skipped notes — resume upload immediately */
  const handleNotesSkip = useCallback(async () => {
    const pending = pendingBlobRef.current;
    if (!pending) return;
    pendingBlobRef.current = null;
    await resumeUploadFlow(pending.blob, pending.mimeType);
  }, [resumeUploadFlow]);

  /** Legacy callback for iOS native capture fallback */
  const handleRecordingComplete = useCallback(async () => {
    try {
      setStep('creating');
      const result = await meetingsApi.create({
        title: meetingTitle || formatDefaultTitle(new Date()),
      });
      setStep('redirecting');
      router.push(`/meeting/${result.meetingId}`);
    } catch (err) {
      console.error('Failed to create meeting:', err);
      setErrorMessage(err instanceof Error ? err.message : 'Failed to create meeting');
      setStep('error');
    }
  }, [meetingTitle, router]);

  const handleRetry = useCallback(() => {
    setStep(null);
    setErrorMessage(null);
  }, []);

  return {
    step,
    errorMessage,
    serverMeetingId,
    createDraftMeeting,
    handleBlobReady,
    handleNotesSubmit,
    handleNotesSkip,
    handleRecordingComplete,
    handleRetry,
  };
}
