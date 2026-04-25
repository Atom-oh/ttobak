'use client';
import { ReactNode, TableHTMLAttributes, ThHTMLAttributes, TdHTMLAttributes } from 'react';

export function Table(props: TableHTMLAttributes<HTMLTableElement> & { children?: ReactNode }) {
  return (
    <div className="my-4 overflow-x-auto rounded-xl border border-white/[0.06]">
      <table className="w-full text-sm border-collapse" {...props} />
    </div>
  );
}

export function THead(props: { children?: ReactNode }) {
  return <thead className="bg-white/[0.04]">{props.children}</thead>;
}

export function TBody(props: { children?: ReactNode }) {
  return <tbody className="[&>tr:nth-child(even)]:bg-white/[0.02]">{props.children}</tbody>;
}

export function TR(props: { children?: ReactNode }) {
  return <tr className="border-b border-white/[0.05] last:border-none">{props.children}</tr>;
}

export function TH(props: ThHTMLAttributes<HTMLTableCellElement> & { children?: ReactNode }) {
  return <th className="px-4 py-2.5 text-left text-xs font-semibold text-slate-900 dark:text-[#e4e1e9] whitespace-nowrap" {...props} />;
}

export function TD(props: TdHTMLAttributes<HTMLTableCellElement> & { children?: ReactNode }) {
  return <td className="px-4 py-2.5 text-sm text-slate-600 dark:text-[#bac9cc]" {...props} />;
}
