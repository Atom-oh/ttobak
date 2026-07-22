'use client';

import { useState, useRef, useCallback, useEffect } from 'react';
import { summaryApi, qaApi } from '@/lib/api';

interface UseLiveSummaryOptions {
  summaryInterval: number;
}

export function useLiveSummary({ summaryInterval }: UseLiveSummaryOptions) {
  const [liveSummary, setLiveSummary] = useState('');
  const [isGenerating, setIsGenerating] = useState(false);
  const [lastSummaryWordCount, setLastSummaryWordCount] = useState(0);
  const [detectedQuestions, setDetectedQuestions] = useState<string[]>([]);

  const lastSummaryWordCountRef = useRef(0);
  const summaryIntervalRef = useRef(summaryInterval);
  const liveSummaryRef = useRef('');
  const askedQuestionsRef = useRef<string[]>([]);
  // The most recently started summarizeLive request, so a caller about to
  // read liveSummaryRef (e.g. usePostRecording saving on recording stop)
  // can await it first -- without this, a summary triggered just before stop
  // resolves into the ref only after the save PUT already fired, silently
  // dropping that increment (or, if it's the meeting's very first summary,
  // the entire live summary).
  const pendingSummaryRef = useRef<Promise<void> | null>(null);

  // Keep refs in sync
  useEffect(() => { summaryIntervalRef.current = summaryInterval; }, [summaryInterval]);

  const reset = useCallback(() => {
    setLiveSummary('');
    setIsGenerating(false);
    setLastSummaryWordCount(0);
    setDetectedQuestions([]);
    lastSummaryWordCountRef.current = 0;
    liveSummaryRef.current = '';
    askedQuestionsRef.current = [];
    pendingSummaryRef.current = null;
  }, []);

  /**
   * Await the most recently started summarizeLive request (if any) before
   * reading liveSummaryRef.current -- call this right before persisting the
   * live summary on recording stop. Bounded by `timeoutMs` so a slow/stuck
   * request can't block finishing the recording indefinitely; on timeout
   * the ref is read as-is (best-effort, same behavior as before this fix).
   */
  const flushPendingSummary = useCallback(async (timeoutMs = 8000) => {
    const pending = pendingSummaryRef.current;
    if (!pending) return;
    await Promise.race([
      pending,
      new Promise((resolve) => setTimeout(resolve, timeoutMs)),
    ]);
  }, []);

  /**
   * Called by useRecordingSession when a new final transcript arrives.
   * Checks if word count threshold is met and triggers summary + question detection.
   */
  const checkThreshold = useCallback((
    newTotalWordCount: number,
    allTranscriptText: string,
    meetingId: string,
  ) => {
    if (newTotalWordCount - lastSummaryWordCountRef.current < summaryIntervalRef.current) {
      return;
    }

    lastSummaryWordCountRef.current = newTotalWordCount;
    setLastSummaryWordCount(newTotalWordCount);
    setIsGenerating(true);

    const trimmedContext = allTranscriptText.length > 2000
      ? allTranscriptText.slice(-2000)
      : allTranscriptText;

    const summaryPromise = summaryApi.summarizeLive(
      meetingId,
      allTranscriptText,
      liveSummaryRef.current || undefined,
    )
      .then((res) => {
        setLiveSummary(res.summary);
        liveSummaryRef.current = res.summary;
      })
      .catch((err) => console.error('Summary failed:', err));
    pendingSummaryRef.current = summaryPromise;

    const detectPromise = qaApi.detectQuestions(
      trimmedContext,
      askedQuestionsRef.current,
      liveSummaryRef.current || undefined,
    )
      .then((res) => {
        if (res.questions.length > 0) setDetectedQuestions(res.questions);
      })
      .catch(() => {}); // silent fail

    Promise.all([summaryPromise, detectPromise])
      .finally(() => setIsGenerating(false));
  }, []);

  const addAskedQuestion = useCallback((q: string) => {
    askedQuestionsRef.current.push(q);
  }, []);

  return {
    liveSummary,
    liveSummaryRef,
    isGenerating,
    lastSummaryWordCount,
    detectedQuestions,
    askedQuestionsRef,
    checkThreshold,
    addAskedQuestion,
    flushPendingSummary,
    reset,
  };
}
