'use client';

import { useEffect, useId, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { toMermaid } from '@/lib/accountGraph';
import { TRUNCATED_NOTICE } from '@/lib/accountOverview';
import { useTheme } from '@/hooks/useTheme';
import { ZoomPanViewport } from '@/components/markdown/ZoomPanViewport';

type Props = { accountId: string; userId: string; refreshKey: unknown };

/** Relationship graph; fetched and rendered only while mounted (i.e. expanded). */
export function AccountGraph({ accountId, userId, refreshKey }: Props) {
  const [svg, setSvg] = useState('');
  const [source, setSource] = useState('');
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const uniqueId = useId().replace(/:/g, '-');
  const generation = useRef(0);
  const { theme } = useTheme();

  useEffect(() => {
    const controller = new AbortController();
    const run = ++generation.current;
    setLoading(true);
    setError('');
    (async () => {
      try {
        const graph = await accountApi.graph(accountId, { signal: controller.signal, expectedUserId: userId });
        const code = toMermaid(graph);
        const mermaid = (await import('mermaid')).default;
        mermaid.initialize({ startOnLoad: false, securityLevel: 'strict',
          secure: ['secure', 'securityLevel', 'startOnLoad', 'maxTextSize', 'theme', 'themeVariables', 'themeCSS', 'fontFamily'],
          theme: theme === 'dark' ? 'dark' : 'default', fontFamily: 'Inter, sans-serif' });
        const { svg: rendered } = await mermaid.render(`account-graph-${uniqueId}-${run}`, code);
        if (controller.signal.aborted) return;
        setSource(code);
        setTruncated(!!graph.truncated);
        setSvg(rendered);
        setLoading(false);
      } catch (e) {
        if (controller.signal.aborted) return;
        setError('관계 그래프를 불러오지 못했습니다');
        setLoading(false);
      }
    })();
    return () => controller.abort();
  }, [accountId, userId, refreshKey, theme, uniqueId]);

  if (error) return <p role="alert" className="text-sm text-error">{error}</p>;
  return <div>
    {truncated && <p role="status" className="mb-2 text-xs text-text-secondary">{TRUNCATED_NOTICE}</p>}
    {loading && !svg && <div className="flex justify-center p-6"><div className="h-6 w-6 animate-spin rounded-full border-2 border-primary border-t-transparent" /></div>}
    {svg && <div className="rounded-xl border border-slate-200 bg-white dark:border-white/[0.06] dark:bg-[#0a0a0f]">
      <ZoomPanViewport resetKey={source}>
        <div className="p-4 [&>svg]:mx-auto" dangerouslySetInnerHTML={{ __html: svg }} />
      </ZoomPanViewport>
    </div>}
  </div>;
}
