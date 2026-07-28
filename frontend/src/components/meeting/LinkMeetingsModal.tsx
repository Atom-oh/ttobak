'use client';

/**
 * ADR-014 Phase 6 — picker modal that lets the user pick previous meetings
 * as predecessors of the current meeting. On save, calls
 * `meetingsApi.link()`. The backend prepends those meetings' summaries as
 * prior context the next time this meeting is summarized, so users get
 * cross-meeting continuity for follow-up discussions.
 *
 * Scope of this component:
 * - Multi-select previous meetings from `meetingsApi.list()`.
 * - Excludes the current meeting (cannot link to itself; backend would 400).
 * - Excludes meetings dated after the current one (linkage is for predecessors).
 * - Shows already-linked meetings as pre-checked but still toggleable.
 * - Persists the selection ordered chronologically (oldest → newest) so the
 *   summarize Lambda prepends them in the order they happened.
 */

import { useEffect, useMemo, useState } from 'react';
import { meetingsApi } from '@/lib/api';
import type { Meeting } from '@/types/meeting';

interface LinkMeetingsModalProps {
  meetingId: string;
  meetingDate: string;
  /** Currently linked predecessor ids (initial selection). */
  initialLinkedIds?: string[];
  onClose: () => void;
  /** Called with the final linked ids after a successful save. */
  onLinked: (linkedMeetingIds: string[]) => void;
}

function formatShortDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString('ko-KR', {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
    });
  } catch {
    return iso;
  }
}

export function LinkMeetingsModal({
  meetingId,
  meetingDate,
  initialLinkedIds = [],
  onClose,
  onLinked,
}: LinkMeetingsModalProps) {
  const [meetings, setMeetings] = useState<Meeting[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set(initialLinkedIds));
  const [query, setQuery] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // One-shot load — picker is used opportunistically so paginating in a
  // modal is overkill; backend list endpoint returns the most recent N
  // anyway. If a user has hundreds of meetings the search filter below
  // covers selection.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await meetingsApi.list({ tab: 'all', limit: 100 });
        if (!cancelled) {
          setMeetings(res.meetings ?? []);
          setLoading(false);
        }
      } catch (e) {
        if (!cancelled) {
          setError(e instanceof Error ? e.message : '미팅 목록을 불러오지 못했습니다');
          setLoading(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Filter out self + future meetings (linkage is predecessor-only) + status filter
  // matches the search box. Memoized so toggle doesn't re-filter.
  const candidates = useMemo(() => {
    const currentDate = meetingDate ? new Date(meetingDate).getTime() : Infinity;
    const q = query.trim().toLowerCase();
    return meetings
      .filter((m) => m.meetingId !== meetingId)
      .filter((m) => {
        // Allow if no date on the candidate (legacy) — backend will validate;
        // otherwise require candidate predates this meeting.
        if (!m.date) return true;
        return new Date(m.date).getTime() <= currentDate;
      })
      .filter((m) => !q || (m.title?.toLowerCase().includes(q) ?? false))
      .sort((a, b) => new Date(b.date || 0).getTime() - new Date(a.date || 0).getTime());
  }, [meetings, meetingId, meetingDate, query]);

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const handleSave = async () => {
    // Chronological order (oldest first) — matters because the summarize Lambda
    // prepends linked summaries in array order, and the LLM benefits from a
    // monotonic timeline.
    const ordered = Array.from(selected)
      .map((id) => meetings.find((m) => m.meetingId === id))
      .filter((m): m is Meeting => Boolean(m))
      .sort((a, b) => new Date(a.date || 0).getTime() - new Date(b.date || 0).getTime())
      .map((m) => m.meetingId);

    setSaving(true);
    setError(null);
    try {
      if (ordered.length === 0) {
        // Backend requires >=1; treating "save empty" as "remove all" requires
        // a separate endpoint. For now, treat empty save as a no-op close.
        onClose();
        return;
      }
      const res = await meetingsApi.link(meetingId, ordered);
      onLinked(res.linkedMeetingIds);
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : '저장에 실패했습니다');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="bg-white dark:bg-surface-lowest rounded-2xl shadow-2xl w-full max-w-lg max-h-[80vh] flex flex-col border border-slate-200 dark:border-white/10"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-slate-100 dark:border-white/5">
          <div>
            <h3 className="font-bold text-slate-900 dark:text-white">이전 미팅 연결</h3>
            <p className="text-xs text-slate-500 dark:text-text-muted mt-0.5">
              선택한 미팅의 요약이 다음 요약 생성 시 컨텍스트로 추가됩니다
            </p>
          </div>
          <button
            onClick={onClose}
            className="p-1.5 rounded-lg text-slate-400 hover:bg-slate-100 dark:hover:bg-white/5 transition-colors"
            aria-label="닫기"
          >
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        {/* Search */}
        <div className="px-5 py-3 border-b border-slate-100 dark:border-white/5">
          <div className="relative">
            <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-lg">
              search
            </span>
            <input
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="미팅 제목 검색"
              className="w-full pl-10 pr-3 py-2 text-sm bg-slate-50 dark:bg-white/[0.03] border border-slate-200 dark:border-white/10 rounded-lg focus:outline-none focus:ring-2 focus:ring-primary/30 placeholder:text-slate-400 dark:text-gray-100"
            />
          </div>
        </div>

        {/* List */}
        <div className="flex-1 overflow-y-auto px-2 py-2">
          {loading ? (
            <div className="px-4 py-8 text-sm text-slate-500 text-center">불러오는 중…</div>
          ) : candidates.length === 0 ? (
            <div className="px-4 py-8 text-sm text-slate-500 text-center">
              {query ? '검색 결과가 없습니다' : '연결할 수 있는 이전 미팅이 없습니다'}
            </div>
          ) : (
            <ul className="space-y-0.5">
              {candidates.map((m) => {
                const isSelected = selected.has(m.meetingId);
                return (
                  <li key={m.meetingId}>
                    <button
                      onClick={() => toggle(m.meetingId)}
                      className={`w-full flex items-start gap-3 px-3 py-2.5 rounded-lg text-left transition-colors ${
                        isSelected
                          ? 'bg-primary/5 dark:bg-primary/10'
                          : 'hover:bg-slate-50 dark:hover:bg-white/[0.03]'
                      }`}
                    >
                      <span
                        className={`flex-shrink-0 mt-0.5 size-5 rounded border-2 flex items-center justify-center transition-colors ${
                          isSelected
                            ? 'bg-primary border-primary'
                            : 'border-slate-300 dark:border-white/20'
                        }`}
                      >
                        {isSelected && (
                          <span className="material-symbols-outlined text-white text-[14px]">
                            check
                          </span>
                        )}
                      </span>
                      <div className="flex-1 min-w-0">
                        <div className="text-sm font-medium text-slate-900 dark:text-gray-100 truncate">
                          {m.title || '제목 없음'}
                        </div>
                        <div className="text-xs text-slate-400 dark:text-text-muted mt-0.5 flex items-center gap-2">
                          <span>{formatShortDate(m.date)}</span>
                          <span className="opacity-50">·</span>
                          <span className="font-mono opacity-70">{m.meetingId.slice(-8)}</span>
                        </div>
                      </div>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-5 py-4 border-t border-slate-100 dark:border-white/5">
          <div className="text-xs text-slate-500 dark:text-text-muted">
            {selected.size > 0 ? `${selected.size}개 선택됨` : '아직 선택 안 함'}
          </div>
          <div className="flex items-center gap-2">
            {error && (
              <span className="text-xs text-red-500 mr-2" title={error}>
                {error.length > 40 ? error.slice(0, 40) + '…' : error}
              </span>
            )}
            <button
              onClick={onClose}
              disabled={saving}
              className="px-3 py-1.5 text-sm text-slate-600 dark:text-text-muted hover:text-slate-900 dark:hover:text-white transition-colors disabled:opacity-50"
            >
              취소
            </button>
            <button
              onClick={handleSave}
              disabled={saving || loading}
              className="px-4 py-1.5 text-sm font-medium bg-primary text-white rounded-lg hover:opacity-90 transition-opacity disabled:opacity-50"
            >
              {saving ? '저장 중…' : '저장'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
