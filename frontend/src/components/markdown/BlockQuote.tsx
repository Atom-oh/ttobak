'use client';
import { ReactNode } from 'react';

export function BlockQuote({ children }: { children?: ReactNode }) {
  return (
    <blockquote className="my-4 rounded-lg bg-[#00E5FF]/[0.03] border-none pl-0 not-italic">
      <div className="flex">
        <div className="w-[3px] rounded-full bg-[#00E5FF]/40 shrink-0" />
        <div className="pl-4 py-3 pr-4 text-sm text-slate-500 dark:text-[#849396] italic leading-relaxed [&>p]:m-0">
          {children}
        </div>
      </div>
    </blockquote>
  );
}
