'use client';
import { useState, useEffect, useRef } from 'react';
import { useTheme } from '@/hooks/useTheme';

interface CodeBlockProps {
  className?: string;
  children?: React.ReactNode;
}

export function CodeBlock({ className, children }: CodeBlockProps) {
  const code = typeof children === 'string' ? children.replace(/\n$/, '') : String(children ?? '').replace(/\n$/, '');
  const langMatch = className?.match(/language-(\w+)/);
  const language = langMatch?.[1] ?? '';
  const [html, setHtml] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const { theme } = useTheme();

  useEffect(() => {
    let cancelled = false;
    import('shiki').then(({ codeToHtml }) =>
      codeToHtml(code, { lang: language || 'text', theme: theme === 'dark' ? 'github-dark-default' : 'github-light-default' })
    ).then((result) => {
      if (!cancelled) setHtml(result);
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [code, language, theme]);

  const handleCopy = () => {
    navigator.clipboard.writeText(code);
    setCopied(true);
    if (timerRef.current) clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => setCopied(false), 2000);
  };

  // Shiki output is trusted (generated from code string, not user HTML input)
  return (
    <div className="my-4 rounded-xl border border-slate-200 dark:border-white/[0.08] bg-white dark:bg-[#0a0a0f] overflow-hidden">
      <div className="flex items-center justify-between px-4 py-2 border-b border-slate-200 dark:border-white/[0.08]">
        {language ? (
          <span className="bg-slate-100 dark:bg-white/[0.06] px-2 py-0.5 rounded text-[12px] text-text-muted font-mono">
            {language}
          </span>
        ) : (
          <span />
        )}
        <button
          onClick={handleCopy}
          className="text-[12px] text-text-muted hover:text-text-main transition-colors font-mono"
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <div className="p-4 overflow-x-auto text-sm leading-snug font-mono [&>pre]:!bg-transparent [&>pre]:!m-0 [&>pre]:!p-0">
        {html ? (
          <div dangerouslySetInnerHTML={{ __html: html }} />
        ) : (
          <pre className="!bg-transparent !m-0 !p-0 text-text-main"><code>{code}</code></pre>
        )}
      </div>
    </div>
  );
}

export function InlineCode({ children }: { children?: React.ReactNode }) {
  return (
    <code className="bg-slate-100 dark:bg-white/[0.05] text-text-main px-1.5 py-0.5 rounded text-[13px] font-mono">
      {children}
    </code>
  );
}
