'use client';
import { ReactNode, createElement } from 'react';

function slugify(text: string): string {
  return text.toLowerCase().replace(/[^a-z0-9가-힣\s-]/g, '').replace(/\s+/g, '-').replace(/-+/g, '-').trim();
}

interface HeadingProps { level: 1 | 2 | 3 | 4 | 5 | 6; children: ReactNode; }

export function Heading({ level, children }: HeadingProps) {
  const text = typeof children === 'string' ? children : '';
  const id = slugify(text);
  const base = 'text-slate-900 dark:text-[#e4e1e9] font-bold scroll-mt-20';
  const styles: Record<number, string> = {
    1: 'text-[28px] leading-tight mt-8 mb-4',
    2: 'text-[22px] leading-snug mt-10 mb-4 flex items-center gap-3',
    3: 'text-[18px] leading-snug mt-8 mb-3 font-semibold',
    4: 'text-[16px] leading-snug mt-6 mb-2 font-medium',
    5: 'text-[14px] leading-snug mt-4 mb-2 font-medium',
    6: 'text-[13px] leading-snug mt-4 mb-2 font-medium text-slate-600 dark:text-[#bac9cc]',
  };

  if (level === 2) {
    return (
      <h2 id={id} className={`${base} ${styles[2]} group`}>
        <span className="w-1 h-6 rounded-sm bg-[#00E5FF] shrink-0" />
        <span>{children}</span>
        <a href={`#${id}`} className="opacity-0 group-hover:opacity-50 transition-opacity text-[#00E5FF] text-base ml-1">#</a>
      </h2>
    );
  }

  return createElement(`h${level}`, { id, className: `${base} ${styles[level] || styles[4]}` }, children);
}

export function createHeadingComponent(level: 1 | 2 | 3 | 4 | 5 | 6) {
  return function HeadingWrapper(props: { children?: ReactNode }) {
    return <Heading level={level}>{props.children}</Heading>;
  };
}
