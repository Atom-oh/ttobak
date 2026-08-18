'use client';

import { useState, useRef, useEffect, useCallback } from 'react';
import { meetingsApi } from '@/lib/api';
import type { SimRun, SimRequirement, SimOption } from '@/types/meeting';

const POLL_INTERVAL_MS = 5000;
// 20 minutes -- mirrors the backend's ReconcileStuckSimRun threshold, so
// the frontend stops polling around the same time the server would start
// reporting the run as errored on the next fetch anyway.
const MAX_POLLS = 240;

interface UseSimulationOptions {
  meetingId: string;
  simRun?: SimRun;
  onUpdate: (simRun: SimRun | undefined) => void;
}

/**
 * Owns the cost/sizing simulator's (ADR-031) client-side lifecycle:
 * extract -> confirm -> run -> poll. Polling is a plain 5s interval against
 * GetMeeting, matching ResearchDetailClient's existing job-poll pattern --
 * this is a minutes-long job, not a token stream, so it gets a poll rather
 * than a new WebSocket action.
 */
export function useSimulation({ meetingId, simRun, onUpdate }: UseSimulationOptions) {
  const [isExtracting, setIsExtracting] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pollCountRef = useRef(0);

  const extract = useCallback(async () => {
    setError(null);
    setIsExtracting(true);
    try {
      const run = await meetingsApi.extractSimRequirements(meetingId);
      onUpdate(run);
    } catch (e) {
      setError(e instanceof Error ? e.message : '요구사항 추출에 실패했습니다');
    } finally {
      setIsExtracting(false);
    }
  }, [meetingId, onUpdate]);

  const run = useCallback(async (requirements: SimRequirement[], options: SimOption[]) => {
    setError(null);
    setIsSubmitting(true);
    try {
      const result = await meetingsApi.createSimulation(meetingId, { requirements, options });
      pollCountRef.current = 0;
      onUpdate(result);
    } catch (e) {
      setError(e instanceof Error ? e.message : '시뮬레이션 실행에 실패했습니다');
    } finally {
      setIsSubmitting(false);
    }
  }, [meetingId, onUpdate]);

  const isActive = simRun?.status === 'queued' || simRun?.status === 'running';

  useEffect(() => {
    if (!isActive) return;

    const interval = setInterval(async () => {
      pollCountRef.current += 1;
      if (pollCountRef.current >= MAX_POLLS) {
        clearInterval(interval);
        return;
      }
      try {
        const meeting = await meetingsApi.get(meetingId);
        onUpdate(meeting.simRun);
      } catch (e) {
        console.error('Simulation poll failed:', e);
      }
    }, POLL_INTERVAL_MS);

    return () => clearInterval(interval);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- onUpdate identity churn shouldn't restart the interval
  }, [meetingId, isActive]);

  return { extract, run, isExtracting, isSubmitting, error, setError };
}
