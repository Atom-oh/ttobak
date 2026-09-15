'use client';

import { memo, type ReactNode } from 'react';
import ReactMarkdown, { type Components } from 'react-markdown';
import remarkGfm from 'remark-gfm';

function PreviewHeading({ children }: { children?: ReactNode }) {
  return <span className="mr-1 font-semibold text-slate-700 dark:text-text-main">{children}</span>;
}

const components: Components = {
  h1: PreviewHeading,
  h2: PreviewHeading,
  h3: PreviewHeading,
  h4: PreviewHeading,
  h5: PreviewHeading,
  h6: PreviewHeading,
  p: ({ children }) => <p className="m-0">{children}</p>,
  ul: ({ children }) => <ul className="m-0 list-disc pl-4">{children}</ul>,
  ol: ({ children, start }) => <ol start={start} className="m-0 list-decimal pl-4">{children}</ol>,
  li: ({ children }) => <li className="m-0">{children}</li>,
  blockquote: ({ children }) => (
    <blockquote className="m-0 border-l-2 border-slate-300 pl-2 italic dark:border-white/20">
      {children}
    </blockquote>
  ),
  strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
  pre: ({ children }) => <div className="whitespace-pre-wrap">{children}</div>,
  code: ({ children }) => (
    <code className="rounded bg-slate-100 px-0.5 font-mono text-[0.875em] dark:bg-white/10">
      {children}
    </code>
  ),
  // The whole card is already a Link. Preview content must not add nested links,
  // controls, or image requests; the detail view provides the full rendering.
  a: ({ children }) => <span>{children}</span>,
  img: ({ alt }) => alt ? <span>{alt}</span> : null,
  input: ({ checked }) => <span aria-hidden="true">{checked ? '☑' : '☐'} </span>,
  table: ({ children }) => <table className="border-collapse text-left">{children}</table>,
  th: ({ children }) => <th className="pr-2 font-semibold">{children}</th>,
  td: ({ children }) => <td className="pr-2">{children}</td>,
  hr: () => <hr className="my-0 border-slate-200 dark:border-white/10" />,
};

const remarkPlugins = [remarkGfm];

export const MeetingSummaryPreview = memo(function MeetingSummaryPreview({ content }: { content: string }) {
  return (
    <div
      data-meeting-summary-preview
      className="mb-4 max-h-[3.25em] overflow-hidden text-sm leading-relaxed text-slate-600 [overflow-wrap:anywhere] line-clamp-2 lg:max-h-[4.875em] lg:line-clamp-3 dark:text-text-secondary"
    >
      <ReactMarkdown remarkPlugins={remarkPlugins} components={components} skipHtml>
        {content}
      </ReactMarkdown>
    </div>
  );
});
