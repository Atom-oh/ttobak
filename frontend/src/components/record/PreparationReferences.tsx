'use client';

import { useState, type ReactNode } from 'react';

export function PreparationReferences({ children }: { children: ReactNode }) {
  const [initialized, setInitialized] = useState(false);
  return (
    <details onToggle={event => {
      if (event.currentTarget.open) setInitialized(true);
    }}
      className="mt-4 rounded-xl border border-slate-200 bg-white dark:border-white/10 dark:bg-surface-lowest">
      <summary className="cursor-pointer px-4 py-3 text-sm font-semibold text-primary">고객 Insight·지식 자료로 준비하기</summary>
      {initialized && <div className="h-[30rem]">{children}</div>}
    </details>
  );
}
