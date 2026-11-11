'use client';

import { useEffect } from 'react';
import Link from 'next/link';
import {
  LATEST_RELEASE, RELEASES, REPO_URL, markSeen, type ReleaseItem, type ReleaseItemKind,
} from '@/lib/releaseNotes';

const KIND: Record<ReleaseItemKind, { label: string; icon: string; classes: string }> = {
  new: { label: '새 기능', icon: 'auto_awesome', classes: 'bg-primary/10 text-primary' },
  improved: { label: '개선', icon: 'trending_up', classes: 'bg-blue-50 text-blue-700 dark:bg-blue-500/10 dark:text-blue-300' },
  fixed: { label: '수정', icon: 'build', classes: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300' },
};

function Item({ item }: { item: ReleaseItem }) {
  const kind = KIND[item.kind];
  return (
    <li className="border-b border-slate-100 py-4 last:border-0 dark:border-white/5">
      <div className="flex flex-wrap items-center gap-2">
        <span className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-semibold ${kind.classes}`}>
          <span className="material-symbols-outlined text-sm" aria-hidden="true">{kind.icon}</span>
          {kind.label}
        </span>
        {item.mac && (
          <span className="rounded-full border border-slate-200 px-2 py-0.5 text-xs text-slate-500 dark:border-white/10 dark:text-text-muted">Mac 앱</span>
        )}
        <h3 className="text-sm font-semibold text-slate-900 dark:text-text-main">{item.title}</h3>
      </div>
      <p className="mt-1.5 text-sm leading-6 text-slate-600 dark:text-text-secondary">{item.body}</p>
      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-slate-500 dark:text-text-muted">
        <span>위치: {item.where}</span>
        {item.to && (
          <Link href={item.to} className="inline-flex items-center gap-0.5 font-semibold text-primary hover:underline">
            바로 가기
            <span className="material-symbols-outlined text-sm" aria-hidden="true">arrow_forward</span>
          </Link>
        )}
        {item.refs.length > 0 && (
          <span className="flex flex-wrap gap-1.5">
            {item.refs.map(n => (
              <a key={n} href={`${REPO_URL}/pull/${n}`} target="_blank" rel="noreferrer" className="hover:underline">#{n}</a>
            ))}
          </span>
        )}
      </div>
    </li>
  );
}

export function Changelog() {
  // Opening the page acknowledges the latest release; the nav dot clears.
  useEffect(() => markSeen(LATEST_RELEASE.version), []);

  return (
    <div className="mx-auto w-full max-w-4xl space-y-6 px-4 py-6 pb-28 lg:px-12 lg:py-12">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 dark:text-text-main dark:font-headline lg:text-3xl">변경 내역</h1>
          <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-text-secondary">
            릴리스마다 바뀐 점과 새 기능을 어디서 쓰는지 정리했습니다. 기능은 계정 권한, 앱 설정과 Mac 앱 여부에 따라 보이지 않을 수 있습니다.
          </p>
        </div>
        <a
          href={`${REPO_URL}/blob/main/CHANGELOG.md`}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 hover:bg-slate-50 dark:border-white/10 dark:bg-surface-lowest dark:text-text-secondary dark:hover:bg-white/5"
        >
          CHANGELOG
          <span className="material-symbols-outlined text-base" aria-hidden="true">open_in_new</span>
        </a>
      </div>

      {RELEASES.map(release => (
        <section
          key={release.version}
          aria-labelledby={`release-${release.version}`}
          className="rounded-2xl border border-slate-200 bg-white dark:border-white/10 dark:bg-surface-lowest"
        >
          <header className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-slate-100 px-5 pb-3 pt-4 dark:border-white/5">
            <h2 id={`release-${release.version}`} className="text-lg font-bold text-slate-900 dark:text-text-main">
              <a href={`${REPO_URL}/releases/tag/v${release.version}`} target="_blank" rel="noreferrer" className="hover:underline">
                v{release.version}
              </a>
            </h2>
            <time dateTime={release.date} className="text-xs text-slate-500 dark:text-text-muted">{release.date}</time>
            {release === LATEST_RELEASE && (
              <span className="rounded-full bg-primary/10 px-2 py-0.5 text-xs font-semibold text-primary">최신</span>
            )}
            <p className="w-full text-sm text-slate-600 dark:text-text-secondary">{release.summary}</p>
          </header>
          <ul className="px-5">
            {release.items.map(item => <Item key={item.title} item={item} />)}
          </ul>
        </section>
      ))}
    </div>
  );
}
