'use client';

import { useEffect, useState } from 'react';
import { MAC_INSTALL_COMMAND, publishedVersion } from '@/lib/macRelease';
import { CopyButton } from '@/components/McpGuide';

export function MacAppInstall() {
  const [version, setVersion] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      try {
        const res = await fetch('/mac/latest.json', { cache: 'no-store', signal: controller.signal });
        if (!res.ok) return;
        setVersion(publishedVersion(await res.json()));
      } catch {
        // Hide the version line on any failure.
      }
    })();
    return () => controller.abort();
  }, []);

  return (
    <div className="space-y-3">
      <p className="text-sm text-slate-500 dark:text-text-muted">
        Apple Silicon Mac(macOS 14.2 이상)에서 터미널에 아래 명령어를 붙여넣어 설치합니다.
      </p>
      <div className="flex min-w-0 items-start gap-2 rounded-lg border border-slate-200 bg-slate-50 px-3 py-2.5 dark:border-white/10 dark:bg-surface-lowest">
        <pre className="min-w-0 flex-1 overflow-x-auto py-1 font-mono text-xs leading-relaxed text-slate-700 dark:text-text-secondary"><code>{MAC_INSTALL_COMMAND}</code></pre>
        <CopyButton text={MAC_INSTALL_COMMAND} label="설치 명령어" />
      </div>
      {version && (
        <p className="text-xs text-slate-500 dark:text-text-muted">현재 버전 v{version}</p>
      )}
    </div>
  );
}
