'use client';

import { useRouter } from 'next/navigation';
import type { CrawledDocument } from '@/types/meeting';

interface InsightsTableViewProps {
  documents: CrawledDocument[];
  totalCount: number;
  page: number;
  limit: number;
  onTagClick?: (tag: string) => void;
  selectedTags?: string[];
}

function formatDate(value: string | number): string {
  if (!value) return '';
  const date = typeof value === 'number'
    ? new Date(value > 1e12 ? value : value * 1000)
    : new Date(value);
  if (isNaN(date.getTime())) return String(value).slice(0, 20);
  return date.toLocaleDateString('ko-KR', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
  });
}

export function InsightsTableView({
  documents,
  totalCount,
  page,
  limit,
  onTagClick,
  selectedTags = [],
}: InsightsTableViewProps) {
  const router = useRouter();

  const startIdx = (page - 1) * limit + 1;
  const endIdx = Math.min(page * limit, totalCount);

  return (
    <div className="glass-panel max-w-full overflow-hidden rounded-lg">
      <div className="overflow-x-auto">
        <table className="w-full min-w-[920px] table-fixed">
          <thead>
            <tr className="bg-white/[0.04]">
              <th className="text-xs font-semibold text-text-muted text-left px-4 py-3" style={{ width: '40%' }}>
                Title
              </th>
              <th className="text-xs font-semibold text-text-muted text-left px-4 py-3" style={{ width: '12%' }}>
                Source
              </th>
              <th className="text-xs font-semibold text-text-muted text-left px-4 py-3" style={{ width: '10%' }}>
                Date
              </th>
              <th className="text-xs font-semibold text-text-muted text-left px-4 py-3" style={{ width: '28%' }}>
                Tags
              </th>
              <th className="text-xs font-semibold text-text-muted text-left px-4 py-3" style={{ width: '5%' }}>
                KB
              </th>
            </tr>
          </thead>
          <tbody>
            {documents.map((doc, idx) => (
              <tr
                key={doc.docHash || doc.url || String(idx)}
                onClick={() => doc.sourceId && doc.docHash && router.push(`/insights/${doc.sourceId}/${doc.docHash}`)}
                className="border-t border-white/[0.05] hover:bg-white/[0.03] cursor-pointer transition-colors"
              >
                <td className="px-4 py-3">
                  <span className="line-clamp-2 break-words text-sm font-medium text-text-main">
                    {doc.title}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <span className="text-text-muted text-xs">
                    {doc.source || (doc.type === 'news' ? 'News' : 'AWS Docs')}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <span className="text-text-muted text-xs whitespace-nowrap">
                    {formatDate(doc.pubDate || doc.crawledAt)}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <div className="flex flex-wrap gap-1">
                    {(doc.tags || []).slice(0, 4).map((tag) => {
                      const isActive = selectedTags.includes(tag);
                      return (
                        <button
                          key={tag}
                          onClick={(e) => {
                            e.stopPropagation();
                            onTagClick?.(tag);
                          }}
                          aria-pressed={isActive}
                          className={`rounded px-2 py-0.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 ${
                            isActive
                              ? 'bg-primary/20 text-primary'
                              : 'bg-white/5 text-text-secondary hover:bg-white/10'
                          }`}
                        >
                          {tag}
                        </button>
                      );
                    })}
                    {(doc.tags || []).length > 4 && (
                      <span className="text-xs text-text-muted">
                        +{(doc.tags || []).length - 4}
                      </span>
                    )}
                  </div>
                </td>
                <td className="px-4 py-3">
                  {doc.inKB ? (
                    <span className="material-symbols-outlined text-sm text-emerald-400">check_circle</span>
                  ) : (
                    <span className="text-text-muted text-xs">&mdash;</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="px-4 py-3 border-t border-white/[0.05] text-xs text-text-muted">
        Showing {startIdx}-{endIdx} of {totalCount} documents
      </div>
    </div>
  );
}
