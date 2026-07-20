'use client';

import { useEffect, useMemo, useState } from 'react';
import { accountApi } from '@/lib/api';
import { INSIGHT_TYPES } from '@/types/meeting';
import type { AccountInsight, AccountResearchRef, AccountSummary } from '@/types/meeting';

interface Props {
  accountId?: string;
  /** Controlled picker mode: when accountId is empty and this is provided,
   * the account <select> calls back here instead of using local state. The
   * record page needs this -- it renders two ReferencePanel instances
   * (desktop aside + mobile bottom sheet) that must share one selection,
   * and the mobile sheet unmounts every time it's closed. Meeting detail
   * doesn't need it: its aside never unmounts, so uncontrolled local state
   * is enough there. */
  onAccountChange?: (accountId: string) => void;
  /** Recent tail of the live transcript (record page only). When provided,
   * items are scored against it and relevant ones float to the top. */
  transcriptTail?: string;
}

interface ScoredItem {
  key: string;
  kind: 'research' | 'insight';
  title: string;
  text?: string;
  href?: string;
  type?: string;
  score: number;
}

// Tokenizes the tail of a live transcript for keyword matching. Korean-safe:
// splits on punctuation/whitespace only, no stemming/NLP -- pure substring
// matching in scoreItems below.
// ponytail: substring scoring; upgrade to per-chunk KB semantic search if
// precision on longer meetings ever matters.
function tokenize(text: string): string[] {
  const tokens = text
    .slice(-600)
    .split(/[\s.,!?~()[\]{}"'`:;·…]+/)
    .map((t) => t.trim())
    .filter((t) => t.length >= 2);
  return Array.from(new Set(tokens)).slice(0, 50);
}

function scoreItem(tokens: string[], tail: string, title: string, text: string, entities: string[]): number {
  if (tokens.length === 0) return 0;
  let score = 0;
  for (const e of entities) {
    if (e.length >= 2 && tail.includes(e)) score += 2;
  }
  const haystack = `${title} ${text}`;
  for (const t of tokens) {
    if (haystack.includes(t)) score += 1;
  }
  return score;
}

export default function ReferencePanel({ accountId: propAccountId, onAccountChange, transcriptTail }: Props) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [localAccountId, setLocalAccountId] = useState('');
  const [insights, setInsights] = useState<AccountInsight[]>([]);
  const [research, setResearch] = useState<AccountResearchRef[]>([]);
  const [activeType, setActiveType] = useState('');
  const [loading, setLoading] = useState(false);

  const accountId = propAccountId || localAccountId;
  const setPickedAccountId = onAccountChange || setLocalAccountId;

  useEffect(() => {
    if (propAccountId) return; // meeting already linked -- no picker needed
    accountApi.list().then((r) => setAccounts(r?.accounts ?? [])).catch(() => {});
  }, [propAccountId]);

  useEffect(() => {
    if (!accountId) {
      setInsights([]);
      setResearch([]);
      return;
    }
    setLoading(true);
    Promise.all([accountApi.insights(accountId), accountApi.research(accountId)])
      .then(([ins, res]) => {
        setInsights(ins?.insights ?? []);
        setResearch(res?.research ?? []);
      })
      .catch(() => {
        setInsights([]);
        setResearch([]);
      })
      .finally(() => setLoading(false));
  }, [accountId]);

  const tokens = useMemo(() => (transcriptTail ? tokenize(transcriptTail) : []), [transcriptTail]);

  const scoredResearch = useMemo<ScoredItem[]>(() => {
    return research.map((r) => ({
      key: `research-${r.researchId}`,
      kind: 'research' as const,
      title: r.topic,
      text: r.summary,
      href: `/insights/research/${r.researchId}`,
      score: tokens.length ? scoreItem(tokens, transcriptTail || '', r.topic, r.summary || '', []) : 0,
    }));
  }, [research, tokens, transcriptTail]);

  const shownInsights = activeType ? insights.filter((i) => i.type === activeType) : insights;

  const scoredInsights = useMemo<ScoredItem[]>(() => {
    return shownInsights.map((ins, idx) => ({
      key: `insight-${idx}`,
      kind: 'insight' as const,
      title: ins.type,
      text: ins.text,
      type: ins.type,
      score: tokens.length ? scoreItem(tokens, transcriptTail || '', ins.type, ins.text, ins.entities || []) : 0,
    }));
  }, [shownInsights, tokens, transcriptTail]);

  const sortedResearch = tokens.length
    ? [...scoredResearch].sort((a, b) => b.score - a.score)
    : scoredResearch;
  const sortedInsights = tokens.length
    ? [...scoredInsights].sort((a, b) => b.score - a.score)
    : scoredInsights;

  if (!accountId) {
    return (
      <div className="p-4">
        <p className="text-sm text-slate-500 dark:text-text-muted mb-3">
          참조할 어카운트를 선택하세요.
        </p>
        <select
          value={accountId}
          onChange={(e) => setPickedAccountId(e.target.value)}
          className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
        >
          <option value="">어카운트 선택…</option>
          {accounts.map((a) => (
            <option key={a.accountId} value={a.accountId}>{a.name}</option>
          ))}
        </select>
      </div>
    );
  }

  return (
    <div className="p-4 space-y-6">
      {loading && (
        <div className="flex justify-center py-4">
          <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent" />
        </div>
      )}

      {/* Linked research */}
      {sortedResearch.length > 0 && (
        <section>
          <h4 className="text-xs font-semibold text-slate-500 dark:text-text-muted mb-2">리서치</h4>
          <div className="space-y-2">
            {sortedResearch.map((item) => (
              <a
                key={item.key}
                href={item.href}
                className={`block rounded-lg p-2.5 text-sm border transition-colors ${
                  item.score > 0
                    ? 'border-l-4 border-l-amber-400 border-slate-200 dark:border-white/10 bg-amber-50/50 dark:bg-amber-500/5'
                    : 'border-slate-200 dark:border-white/10 hover:bg-slate-50 dark:hover:bg-white/5'
                }`}
              >
                <div className="flex items-center gap-1.5 font-medium text-slate-900 dark:text-text-main">
                  <span className="material-symbols-outlined text-primary text-base">neurology</span>
                  {item.title}
                </div>
                {item.text && (
                  <p className="text-xs text-slate-500 dark:text-text-muted mt-1 line-clamp-2">{item.text}</p>
                )}
              </a>
            ))}
          </div>
        </section>
      )}

      {/* Account insights */}
      <section>
        <div className="flex items-center justify-between mb-2">
          <h4 className="text-xs font-semibold text-slate-500 dark:text-text-muted">인사이트</h4>
        </div>
        <div className="flex flex-wrap gap-1.5 mb-2">
          <button
            onClick={() => setActiveType('')}
            className={`text-xs px-2.5 py-1 rounded-full border ${activeType === '' ? 'bg-primary text-white border-primary' : 'border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-muted'}`}
          >
            전체
          </button>
          {INSIGHT_TYPES.map((t) => (
            <button
              key={t}
              onClick={() => setActiveType(t)}
              className={`text-xs px-2.5 py-1 rounded-full border ${activeType === t ? 'bg-primary text-white border-primary' : 'border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-muted'}`}
            >
              {t}
            </button>
          ))}
        </div>
        {sortedInsights.length === 0 ? (
          <p className="text-sm text-slate-400 dark:text-text-muted">인사이트가 없습니다.</p>
        ) : (
          <div className="space-y-2">
            {sortedInsights.map((item) => (
              <div
                key={item.key}
                className={`rounded-lg p-2.5 text-sm border ${
                  item.score > 0
                    ? 'border-l-4 border-l-amber-400 border-slate-200 dark:border-white/10 bg-amber-50/50 dark:bg-amber-500/5'
                    : 'border-slate-200 dark:border-white/10'
                }`}
              >
                <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">{item.type}</span>
                <p className="text-sm text-slate-700 dark:text-text-secondary mt-1">{item.text}</p>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
