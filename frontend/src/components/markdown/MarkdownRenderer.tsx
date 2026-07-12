'use client';

import ReactMarkdown, { Components, defaultUrlTransform } from 'react-markdown';
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
    img: [...(defaultSchema.attributes?.img ?? []), 'loading'],
  },
  // rehype-sanitize's default href allowlist (http/https/irc/ircs/mailto/xmpp)
  // strips the ADR-013 "transcript://{segmentId}" deep links entirely — the
  // <a> tag survives but loses its href, so the timestamp renders unclickable
  // instead of scrolling to the transcript segment.
  protocols: {
    ...defaultSchema.protocols,
    href: [...(defaultSchema.protocols?.href ?? []), 'transcript'],
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
    return <p className="text-[15px] leading-relaxed text-slate-600 dark:text-text-secondary my-3">{children}</p>;
  },

  ul({ children }: { children?: ReactNode }) {
    return <ul className="list-disc pl-6 my-3 space-y-1 marker:text-[#849396]">{children}</ul>;
  },

  ol({ children }: { children?: ReactNode }) {
    return <ol className="list-decimal pl-6 my-3 space-y-1 marker:text-[#849396]">{children}</ol>;
  },

  li({ children }: { children?: ReactNode }) {
    return <li className="text-[15px] leading-relaxed text-slate-600 dark:text-text-secondary pl-1">{children}</li>;
  },

  a(props: AnchorHTMLAttributes<HTMLAnchorElement> & { children?: ReactNode }) {
    const { href, children, ...rest } = props;
    const isExternal = href?.startsWith('http');
    // ADR-013: summary deep links to transcript segments. The backend emits
    // `transcript://{segmentId}`; we resolve that to the corresponding DOM
    // anchor `#ts-{segmentId}` rendered by TranscriptSection and intercept
    // clicks for smooth-scroll + brief highlight.
    const isTranscript = href?.startsWith('#ts-') || href?.startsWith('transcript://');
    if (isTranscript) {
      const segmentId = href!.startsWith('transcript://')
        ? href!.slice('transcript://'.length)
        : href!.slice('#ts-'.length);
      const onClick: React.MouseEventHandler<HTMLAnchorElement> = (e) => {
        e.preventDefault();
        const el = document.getElementById(`ts-${segmentId}`);
        if (!el) return;
        el.scrollIntoView({ behavior: 'smooth', block: 'center' });
        el.classList.add('ring-2', 'ring-primary/50');
        window.setTimeout(() => {
          el.classList.remove('ring-2', 'ring-primary/50');
        }, 1800);
      };
      return (
        <a
          href={`#ts-${segmentId}`}
          onClick={onClick}
          className="text-[#3211d4] dark:text-primary underline underline-offset-2 decoration-dotted decoration-[#3211d4]/40 dark:decoration-primary/40 hover:decoration-solid hover:decoration-[#3211d4] dark:hover:decoration-primary transition-colors text-[13px] tabular-nums ml-1 align-baseline"
          title="회의록의 해당 발언으로 이동"
        >
          {children}
        </a>
      );
    }
    return (
      <a
        href={href}
        className="text-[#3211d4] dark:text-primary underline underline-offset-2 decoration-[#3211d4]/30 dark:decoration-primary/30 hover:decoration-[#3211d4] dark:hover:decoration-primary transition-colors inline-flex items-center gap-0.5"
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
    return <strong className="font-semibold text-slate-900 dark:text-text-main">{children}</strong>;
  },

  em({ children }: { children?: ReactNode }) {
    return <em className="italic text-slate-500 dark:text-text-muted">{children}</em>;
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

// react-markdown's default urlTransform strips any protocol outside
// http(s)/irc(s)/mailto/xmpp regardless of the rehype-sanitize schema above —
// it runs independently, so the ADR-013 "transcript://{segmentId}" deep link
// would still be blanked out to "" without this override.
function urlTransform(url: string): string {
  return url.startsWith('transcript://') ? url : defaultUrlTransform(url);
}

export function MarkdownRenderer({ content }: MarkdownRendererProps) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkCallout]}
      rehypePlugins={[rehypeRaw, [rehypeSanitize, customSchema]]}
      urlTransform={urlTransform}
      components={components}
    >
      {content}
    </ReactMarkdown>
  );
}
