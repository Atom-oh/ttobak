'use client';

import { useState, useEffect, useRef, useId } from 'react';

interface MermaidBlockProps {
  code: string;
}

export function MermaidBlock({ code }: MermaidBlockProps) {
  const [svg, setSvg] = useState<string>('');
  const [error, setError] = useState<string>('');
  const uniqueId = useId().replace(/:/g, '-');
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const mermaid = (await import('mermaid')).default;
        mermaid.initialize({
          startOnLoad: false,
          // Explicit, even though 'strict' is mermaid's default: diagram
          // source here is LLM output over untrusted meeting content, so a
          // future mermaid upgrade or config merge must never silently
          // enable click handlers / script in rendered SVG.
          securityLevel: 'strict',
          theme: 'dark',
          themeVariables: {
            primaryColor: '#8b85f7',
            primaryTextColor: '#e4e1e9',
            primaryBorderColor: '#8b85f7',
            lineColor: '#8a8f98',
            secondaryColor: '#1a1a24',
            tertiaryColor: '#0e0e13',
            background: '#131022',
            mainBkg: '#1a1a24',
            nodeBorder: '#8b85f7',
            clusterBkg: '#0e0e13',
            titleColor: '#e4e1e9',
            edgeLabelBackground: '#131022',
          },
          fontFamily: 'Inter, sans-serif',
        });
        const { svg: renderedSvg } = await mermaid.render(`mermaid-${uniqueId}`, code.trim());
        if (!cancelled) setSvg(renderedSvg);
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'Mermaid render failed');
      }
    })();
    return () => { cancelled = true; };
  }, [code, uniqueId]);

  if (error) {
    return (
      <div className="my-4 rounded-xl border border-red-500/20 bg-red-500/5 p-4">
        <div className="text-xs text-red-400 mb-2">Diagram render error</div>
        <pre className="text-xs text-text-muted overflow-x-auto">{code}</pre>
      </div>
    );
  }

  if (!svg) {
    return (
      <div className="my-4 rounded-xl border border-white/[0.06] bg-[#0a0a0f] p-8 flex items-center justify-center">
        <div className="animate-spin rounded-full h-6 w-6 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  return (
    <div
      ref={containerRef}
      className="my-4 rounded-xl border border-white/[0.06] bg-[#0a0a0f] p-4 overflow-x-auto [&>svg]:mx-auto [&>svg]:max-w-full"
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}
