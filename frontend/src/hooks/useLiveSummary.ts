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
  // Subset of detectedQuestions the detector flagged as immediately
  // answerable via search — LiveQAPanel auto-fires the first new one.
  const [proactiveQuestions, setProactiveQuestions] = useState<string[]>([]);

  const lastSummaryWordCountRef = useRef(0);
  const summaryIntervalRef = useRef(summaryInterval);
  const liveSummaryRef = useRef('');
  const askedQuestionsRef = useRef<string[]>([]);
  // Every summarizeLive request currently in flight, so a caller about to
  // read liveSummaryRef (e.g. usePostRecording saving on recording stop) can
  // await ALL of them first -- without this, a summary triggered just before
  // stop resolves into the ref only after the save PUT already fired,
  // silently dropping that increment (or, if it's the meeting's very first
  // summary, the entire live summary). A Set, not a single "latest" ref:
  // if request N (latest-started) fails while an earlier request N-1 is
  // still pending, tracking only N would let flushPendingSummary return the
  // instant N settles -- before N-1's still-in-flight, eventually-successful
  // response ever reaches the ref.
  const pendingSummariesRef = useRef<Set<Promise<void>>>(new Set());
  // Requests can resolve out of order (a later-started call's response can
  // arrive before an earlier one's). Each checkThreshold call captures the
  // post-increment generation. Two refs, not one:
  //   - summaryGenerationRef: the last STARTED generation (strictly
  //     monotonic -- reset() bumps it forward, never back to 0, so a
  //     straggling response from a PREVIOUS recording can never collide
  //     with a fresh recording's generation numbers and overwrite its ref
  //     with the old meeting's summary).
  //   - appliedGenerationRef: the generation of the last response actually
  //     written into liveSummaryRef. A response applies only if its
  //     generation is newer than what's already applied -- comparing
  //     against "last STARTED" instead would drop a valid, successful
  //     response whenever a newer request had already been fired but then
  //     failed (its catch() never touches this ref), silently losing the
  //     only summary that actually succeeded.
  const summaryGenerationRef = useRef(0);
  const appliedGenerationRef = useRef(0);

  // Keep refs in sync
  useEffect(() => { summaryIntervalRef.current = summaryInterval; }, [summaryInterval]);

  const reset = useCallback(() => {
    setLiveSummary('');
    setIsGenerating(false);
    setLastSummaryWordCount(0);
    setDetectedQuestions([]);
    setProactiveQuestions([]);
    lastSummaryWordCountRef.current = 0;
    liveSummaryRef.current = '';
    askedQuestionsRef.current = [];
    pendingSummariesRef.current.clear();
    // Bump forward, never back to 0: any request still in flight from the
    // recording that just ended is immediately stale against this new
    // baseline, and the next recording's generations start strictly above
    // it -- so a late response from the OLD recording can never satisfy
    // "generation > appliedGenerationRef.current" for the NEW one.
    summaryGenerationRef.current += 1;
    appliedGenerationRef.current = summaryGenerationRef.current;
  }, []);

  /**
   * Await every in-flight summarizeLive request before reading
   * liveSummaryRef.current -- call this right before persisting the live
   * summary on recording stop. Bounded by `timeoutMs` so a slow/stuck
   * request can't block finishing the recording indefinitely; on timeout
   * the ref is read as-is (best-effort, same behavior as before this fix).
   */
  const flushPendingSummary = useCallback(async (timeoutMs = 8000) => {
    const pending = Array.from(pendingSummariesRef.current);
    if (pending.length === 0) return;
    await Promise.race([
      Promise.all(pending),
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

    const generation = ++summaryGenerationRef.current;
    const summaryPromise: Promise<void> = summaryApi.summarizeLive(
      meetingId,
      allTranscriptText,
      liveSummaryRef.current || undefined,
    )
      .then((res) => {
        // Apply only if this is newer than the last APPLIED response, not
        // merely older than the last STARTED one -- a newer request that
        // later failed must not cost us this (older but successful) result.
        if (generation <= appliedGenerationRef.current) return;
        appliedGenerationRef.current = generation;
        setLiveSummary(res.summary);
        liveSummaryRef.current = res.summary;
      })
      .catch((err) => console.error('Summary failed:', err))
      .finally(() => {
        // Referencing `summaryPromise` here is safe despite being inside its
        // own initializer: this callback only runs asynchronously, after
        // the `const` assignment below has completed.
        pendingSummariesRef.current.delete(summaryPromise);
      });
    pendingSummariesRef.current.add(summaryPromise);

    const detectPromise = qaApi.detectQuestions(
      trimmedContext,
      askedQuestionsRef.current,
      liveSummaryRef.current || undefined,
    )
      .then((res) => {
        if (res.questions.length > 0) setDetectedQuestions(res.questions);
        if (res.proactive && res.proactive.length > 0) setProactiveQuestions(res.proactive);
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
    proactiveQuestions,
    askedQuestionsRef,
    checkThreshold,
    addAskedQuestion,
    flushPendingSummary,
    reset,
  };
}
