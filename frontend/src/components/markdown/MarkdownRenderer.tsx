'use client';

import ReactMarkdown, { Components } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize';
import { ReactNode, AnchorHTMLAttributes, HTMLAttributes } from 'react';

import { createHeadingComponent } from './Heading';
import { BlockQuote } from './BlockQuote';
import { Table, THead, TBody, TR, TH, TD } from './DataTable';
import { CodeBlock, InlineCode } from './CodeBlock';
import { MermaidBlock } from './MermaidBlock';
import { Callout } from './Callout';
import { remarkCallout } from './remarkCallout';

const customSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    div: [
      ...(defaultSchema.attributes?.div ?? []),
      'data-callout',
      'data-callout-title',
      'className',
    ],
    img: [...(defaultSchema.attributes?.img ?? []), 'src', 'alt', 'loading'],
  },
};

const components: Components = {
  h1: createHeadingComponent(1),
  h2: createHeadingComponent(2),
  h3: createHeadingComponent(3),
  h4: createHeadingComponent(4),
  h5: createHeadingComponent(5),
  h6: createHeadingComponent(6),

  blockquote: BlockQuote as Components['blockquote'],

  table: Table as Components['table'],
  thead: THead as Components['thead'],
  tbody: TBody as Components['tbody'],
  tr: TR as Components['tr'],
  th: TH as Components['th'],
  td: TD as Components['td'],

  code(props: HTMLAttributes<HTMLElement> & { className?: string; children?: ReactNode }) {
    const { className, children, ...rest } = props;
    const lang = className?.replace('language-', '');
    if (lang === 'mermaid') {
      return <MermaidBlock code={String(children).replace(/\n$/, '')} />;
    }
    const text = String(children ?? '');
    const isBlock = className?.startsWith('language-') || text.includes('\n');
    if (isBlock) {
      return <CodeBlock className={className}>{children}</CodeBlock>;
    }
    return <InlineCode>{children}</InlineCode>;
  },

  div(props: HTMLAttributes<HTMLDivElement> & { 'data-callout'?: string; 'data-callout-title'?: string; children?: ReactNode }) {
    if (props['data-callout']) {
      return (
        <Callout data-callout={props['data-callout']} data-callout-title={props['data-callout-title']}>
          {props.children}
        </Callout>
      );
    }
    return <div {...props} />;
  },

  p({ children }: { children?: ReactNode }) {
    return <p className="text-[15px] leading-relaxed text-slate-600 dark:text-[#bac9cc] my-3">{children}</p>;
  },

  ul({ children }: { children?: ReactNode }) {
    return <ul className="list-disc pl-6 my-3 space-y-1 marker:text-[#849396]">{children}</ul>;
  },

  ol({ children }: { children?: ReactNode }) {
    return <ol className="list-decimal pl-6 my-3 space-y-1 marker:text-[#849396]">{children}</ol>;
  },

  li({ children }: { children?: ReactNode }) {
    return <li className="text-[15px] leading-relaxed text-slate-600 dark:text-[#bac9cc] pl-1">{children}</li>;
  },

  a(props: AnchorHTMLAttributes<HTMLAnchorElement> & { children?: ReactNode }) {
    const { href, children, ...rest } = props;
    const isExternal = href?.startsWith('http');
    return (
      <a
        href={href}
        className="text-[#3211d4] dark:text-[#00E5FF] underline underline-offset-2 decoration-[#3211d4]/30 dark:decoration-[#00E5FF]/30 hover:decoration-[#3211d4] dark:hover:decoration-[#00E5FF] transition-colors inline-flex items-center gap-0.5"
        {...(isExternal ? { target: '_blank', rel: 'noopener noreferrer' } : {})}
        {...rest}
      >
        {children}
        {isExternal && (
          <span className="material-symbols-outlined text-[14px] opacity-60">open_in_new</span>
        )}
      </a>
    );
  },

  hr() {
    return <hr className="border-slate-200 dark:border-white/10 my-8" />;
  },

  strong({ children }: { children?: ReactNode }) {
    return <strong className="font-semibold text-slate-900 dark:text-[#e4e1e9]">{children}</strong>;
  },

  em({ children }: { children?: ReactNode }) {
    return <em className="italic text-slate-500 dark:text-[#849396]">{children}</em>;
  },

  img(props: React.ImgHTMLAttributes<HTMLImageElement>) {
    return (
      <img
        {...props}
        loading="lazy"
        className="rounded-xl border border-slate-200 dark:border-white/10 shadow-sm my-4 max-w-full h-auto max-h-[400px] object-contain"
      />
    );
  },
};

interface MarkdownRendererProps {
  content: string;
}

export function MarkdownRenderer({ content }: MarkdownRendererProps) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkCallout]}
      rehypePlugins={[rehypeRaw, [rehypeSanitize, customSchema]]}
      components={components}
    >
      {content}
    </ReactMarkdown>
  );
}
