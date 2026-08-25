'use client';

import { useState, useEffect, useRef, useId } from 'react';
import { ZoomPanViewport } from './ZoomPanViewport';
import { DiagramLightbox } from './DiagramLightbox';
import { useTheme } from '@/hooks/useTheme';

interface MermaidBlockProps {
  code: string;
}

// Mirrors globals.css's dark-mode tokens (`.dark { ... }`) -- kept as an
// explicit palette rather than read via getComputedStyle because mermaid
// needs keys (mainBkg, clusterBkg, edgeLabelBackground) with no 1:1 token,
// so a derivation would be needed either way, and this avoids any paint
// timing risk.
const DARK_THEME_VARIABLES = {
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
};

// Mirrors globals.css's :root tokens (light mode).
const LIGHT_THEME_VARIABLES = {
  primaryColor: '#3211d4',
  primaryTextColor: '#0f172a',
  primaryBorderColor: '#3211d4',
  lineColor: '#64748b',
  secondaryColor: '#f1f5f9',
  tertiaryColor: '#e2e8f0',
  background: '#ffffff',
  mainBkg: '#f8fafc',
  nodeBorder: '#3211d4',
  clusterBkg: '#f1f5f9',
  titleColor: '#0f172a',
  edgeLabelBackground: '#ffffff',
};

export function MermaidBlock({ code }: MermaidBlockProps) {
  const [svg, setSvg] = useState<string>('');
  const [error, setError] = useState<string>('');
  const [fullscreen, setFullscreen] = useState(false);
  const uniqueId = useId().replace(/:/g, '-');
  const containerRef = useRef<HTMLDivElement>(null);
  const { theme } = useTheme();

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
          theme: theme === 'dark' ? 'dark' : 'default',
          themeVariables: theme === 'dark' ? DARK_THEME_VARIABLES : LIGHT_THEME_VARIABLES,
          fontFamily: 'Inter, sans-serif',
        });
        const { svg: renderedSvg } = await mermaid.render(`mermaid-${uniqueId}`, code.trim());
        if (!cancelled) setSvg(renderedSvg);
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'Mermaid render failed');
      }
    })();
    return () => { cancelled = true; };
  }, [code, uniqueId, theme]);

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
      <div className="my-4 rounded-xl border border-slate-200 dark:border-white/[0.06] bg-white dark:bg-[#0a0a0f] p-8 flex items-center justify-center">
        <div className="animate-spin rounded-full h-6 w-6 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  return (
    <div ref={containerRef} className="my-4 rounded-xl border border-slate-200 dark:border-white/[0.06] bg-white dark:bg-[#0a0a0f]">
      {/*
        [&>svg]:max-w-full only bounds the INITIAL fit — it must not apply
        inside the zoom transform, or scaling up re-clamps the SVG back to
        the container width and zoom has no visible effect. That clamp was
        the actual reason diagrams were unreadable before this component
        gained pan/zoom: nodes just got proportionally smaller as the
        diagram grew, with no way to see them at full size.
      */}
      <ZoomPanViewport resetKey={code} onFullscreen={() => setFullscreen(true)}>
        <div
          className="p-4 [&>svg]:mx-auto"
          dangerouslySetInnerHTML={{ __html: svg }}
        />
      </ZoomPanViewport>
      {fullscreen && (
        <DiagramLightbox resetKey={code} onClose={() => setFullscreen(false)}>
          <div className="p-4" dangerouslySetInnerHTML={{ __html: svg }} />
        </DiagramLightbox>
      )}
    </div>
  );
}
