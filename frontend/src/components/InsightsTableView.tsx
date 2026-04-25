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
    <div className="glass-panel rounded-xl overflow-hidden">
      <div className="overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr className="bg-white/[0.04]">
              <th className="text-xs font-semibold text-[#849396] text-left px-4 py-3" style={{ width: '40%' }}>
                Title
              </th>
              <th className="text-xs font-semibold text-[#849396] text-left px-4 py-3" style={{ width: '12%' }}>
                Source
              </th>
              <th className="text-xs font-semibold text-[#849396] text-left px-4 py-3" style={{ width: '10%' }}>
                Date
              </th>
              <th className="text-xs font-semibold text-[#849396] text-left px-4 py-3" style={{ width: '28%' }}>
                Tags
              </th>
              <th className="text-xs font-semibold text-[#849396] text-left px-4 py-3" style={{ width: '5%' }}>
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
                  <span className="font-medium text-[#e4e1e9] line-clamp-1 text-sm">
                    {doc.title}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <span className="text-[#849396] text-xs">
                    {doc.source || (doc.type === 'news' ? 'News' : 'AWS Docs')}
                  </span>
                </td>
                <td className="px-4 py-3">
                  <span className="text-[#849396] text-xs whitespace-nowrap">
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
                          className={`text-xs px-2 py-0.5 rounded-full transition-colors ${
                            isActive
                              ? 'bg-[#00E5FF]/20 text-[#00E5FF]'
                              : 'bg-white/5 text-[#bac9cc] hover:bg-white/10'
                          }`}
                        >
                          {tag}
                        </button>
                      );
                    })}
                    {(doc.tags || []).length > 4 && (
                      <span className="text-xs text-[#849396]">
                        +{(doc.tags || []).length - 4}
                      </span>
                    )}
                  </div>
                </td>
                <td className="px-4 py-3">
                  {doc.inKB ? (
                    <span className="material-symbols-outlined text-sm text-emerald-400">check_circle</span>
                  ) : (
                    <span className="text-[#849396] text-xs">&mdash;</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="px-4 py-3 border-t border-white/[0.05] text-xs text-[#849396]">
        Showing {startIdx}-{endIdx} of {totalCount} documents
      </div>
    </div>
  );
}
