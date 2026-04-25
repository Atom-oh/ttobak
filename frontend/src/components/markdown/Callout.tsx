'use client';
import { ReactNode } from 'react';

interface CalloutConfig {
  color: string;
  bg: string;
  icon: string;
}

const CALLOUT_CONFIG: Record<string, CalloutConfig> = {
  summary: { color: '#00E5FF', bg: 'rgba(0, 229, 255, 0.05)', icon: 'lightbulb' },
  warning: { color: '#EA9619', bg: 'rgba(234, 150, 25, 0.05)', icon: 'warning' },
  tip:     { color: '#4DC290', bg: 'rgba(77, 194, 144, 0.05)', icon: 'check_circle' },
  danger:  { color: '#EF4444', bg: 'rgba(239, 68, 68, 0.05)', icon: 'dangerous' },
  info:    { color: '#3B82F6', bg: 'rgba(59, 130, 246, 0.05)', icon: 'info' },
};

const DEFAULT_CONFIG: CalloutConfig = CALLOUT_CONFIG.info;

interface CalloutProps {
  'data-callout'?: string;
  'data-callout-title'?: string;
  children?: ReactNode;
}

export function Callout(props: CalloutProps) {
  const type = props['data-callout'] ?? 'info';
  const title = props['data-callout-title'] ?? type.charAt(0).toUpperCase() + type.slice(1);
  const config = CALLOUT_CONFIG[type] ?? DEFAULT_CONFIG;

  return (
    <div className="my-4 rounded-xl overflow-hidden flex" style={{ backgroundColor: config.bg }}>
      <div className="w-1 shrink-0 rounded-l-xl" style={{ backgroundColor: config.color }} />
      <div className="px-4 py-3 flex-1 min-w-0">
        <div className="flex items-center gap-2 mb-1">
          <span
            className="material-symbols-outlined text-[18px]"
            style={{ color: config.color }}
          >
            {config.icon}
          </span>
          <span
            className="text-sm font-semibold"
            style={{ color: config.color }}
          >
            {title}
          </span>
        </div>
        <div className="text-sm text-[#bac9cc] leading-relaxed [&>p]:m-0 [&>p+p]:mt-2">
          {props.children}
        </div>
      </div>
    </div>
  );
}
