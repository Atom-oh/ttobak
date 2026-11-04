import type { Components } from 'react-markdown';
import type { AnchorHTMLAttributes, HTMLAttributes, ReactNode, TableHTMLAttributes, ThHTMLAttributes, TdHTMLAttributes } from 'react';

// Compact Markdown elements for chat answers. Tailwind's preflight resets lists,
// headings, tables and code, and the project has no typography plugin, so `prose`
// classes style nothing: every element sets its own spacing here. Code stays plain
// (no syntax highlighting) so a streaming answer re-renders cheaply on each delta.
type Props<T> = T & { children?: ReactNode; node?: unknown };

// Real heading tags (structure for screen readers), sized for a chat bubble.
const heading = (Tag: 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6', size: string) =>
  function ChatHeading({ children }: Props<HTMLAttributes<HTMLHeadingElement>>) {
    return <Tag className={`${size} font-semibold text-slate-900 dark:text-text-main mt-3 mb-1 first:mt-0`}>{children}</Tag>;
  };

export const chatMarkdownComponents: Components = {
  h1: heading('h1', 'text-[15px]'),
  h2: heading('h2', 'text-[15px]'),
  h3: heading('h3', 'text-sm'),
  h4: heading('h4', 'text-sm'),
  h5: heading('h5', 'text-sm'),
  h6: heading('h6', 'text-sm'),

  p({ children }: Props<HTMLAttributes<HTMLParagraphElement>>) {
    return <p className="my-1.5 first:mt-0 last:mb-0 leading-relaxed">{children}</p>;
  },
  ul({ children }: Props<HTMLAttributes<HTMLUListElement>>) {
    return <ul className="list-disc pl-5 my-1.5 first:mt-0 last:mb-0 space-y-0.5 marker:text-slate-400">{children}</ul>;
  },
  ol({ children }: Props<HTMLAttributes<HTMLOListElement>>) {
    return <ol className="list-decimal pl-5 my-1.5 first:mt-0 last:mb-0 space-y-0.5 marker:text-slate-400">{children}</ol>;
  },
  li({ children }: Props<HTMLAttributes<HTMLLIElement>>) {
    return <li className="leading-relaxed pl-0.5 [&>ul]:my-0.5 [&>ol]:my-0.5">{children}</li>;
  },
  strong({ children }: Props<HTMLAttributes<HTMLElement>>) {
    return <strong className="font-semibold text-slate-900 dark:text-text-main">{children}</strong>;
  },
  blockquote({ children }: Props<HTMLAttributes<HTMLQuoteElement>>) {
    return (
      <blockquote className="my-2 border-l-2 border-slate-300 dark:border-white/20 pl-3 text-slate-500 dark:text-text-muted [&>p]:my-1">
        {children}
      </blockquote>
    );
  },
  hr() {
    return <hr className="my-3 border-slate-200 dark:border-white/10" />;
  },
  a({ href, children }: Props<AnchorHTMLAttributes<HTMLAnchorElement>>) {
    const external = typeof href === 'string' && /^https?:/i.test(href);
    return (
      <a
        href={href}
        className="text-primary underline underline-offset-2 decoration-primary/40 hover:decoration-primary break-words"
        {...(external ? { target: '_blank', rel: 'noopener noreferrer' } : {})}
      >
        {children}
      </a>
    );
  },

  table({ children }: Props<TableHTMLAttributes<HTMLTableElement>>) {
    return (
      <div className="my-2 overflow-x-auto rounded-lg border border-slate-200 dark:border-white/10">
        <table className="w-full border-collapse text-[13px]">{children}</table>
      </div>
    );
  },
  thead({ children }: Props<HTMLAttributes<HTMLTableSectionElement>>) {
    return <thead className="bg-slate-50 dark:bg-white/[0.04]">{children}</thead>;
  },
  th({ children, style }: Props<ThHTMLAttributes<HTMLTableCellElement>>) {
    return (
      <th style={style} className="px-2.5 py-1.5 text-left font-semibold text-slate-900 dark:text-text-main whitespace-nowrap border-b border-slate-200 dark:border-white/10">
        {children}
      </th>
    );
  },
  td({ children, style }: Props<TdHTMLAttributes<HTMLTableCellElement>>) {
    return <td style={style} className="px-2.5 py-1.5 align-top border-b border-slate-100 dark:border-white/[0.06]">{children}</td>;
  },

  pre({ children }: Props<HTMLAttributes<HTMLPreElement>>) {
    return (
      <pre className="my-2 overflow-x-auto rounded-lg bg-slate-100 dark:bg-white/[0.06] p-3 text-[12.5px] leading-snug [&>code]:bg-transparent [&>code]:p-0">
        {children}
      </pre>
    );
  },
  code({ children, className }: Props<HTMLAttributes<HTMLElement>>) {
    return <code className={`${className ?? ''} rounded bg-slate-100 dark:bg-white/[0.08] px-1 py-0.5 font-mono text-[12.5px]`.trim()}>{children}</code>;
  },
};
