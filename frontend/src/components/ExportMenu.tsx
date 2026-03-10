'use client';

import { useState, useRef, useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { exportApi, settingsApi } from '@/lib/api';

interface ExportMenuProps {
  meetingId: string;
  onExportStart?: () => void;
  onExportComplete?: () => void;
}

export function ExportMenu({ meetingId, onExportStart, onExportComplete }: ExportMenuProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [exporting, setExporting] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const router = useRouter();

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const handleExport = async (format: 'pdf' | 'markdown' | 'notion' | 'obsidian') => {
    setError(null);
    setExporting(format);
    onExportStart?.();

    try {
      if (format === 'notion') {
        // Check if Notion is configured
        const integrations = await settingsApi.getIntegrations();
        if (!integrations.notion?.configured) {
          setError('Notion is not configured. Please set up the integration in Settings.');
          router.push('/settings');
          return;
        }

        const response = await exportApi.export(meetingId, 'notion');
        if (response.notionUrl) {
          window.open(response.notionUrl, '_blank');
        }
      } else if (format === 'pdf') {
        const response = await exportApi.export(meetingId, 'pdf');
        if (response.url) {
          window.open(response.url, '_blank');
        }
      } else if (format === 'obsidian') {
        const response = await exportApi.obsidian(meetingId);
        // Download as .md file with YAML frontmatter
        const blob = new Blob([response.content], { type: 'text/markdown' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = response.filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      } else if (format === 'markdown') {
        // Client-side download - fetch meeting data and create markdown
        const response = await exportApi.export(meetingId, 'obsidian');
        // Remove YAML frontmatter for plain markdown
        let content = response.content || '';
        if (content.startsWith('---')) {
          const endIndex = content.indexOf('---', 3);
          if (endIndex !== -1) {
            content = content.substring(endIndex + 3).trim();
          }
        }
        // Convert wikilinks to standard markdown links
        content = content.replace(/\[\[([^\]]+)\]\]/g, '$1');

        const blob = new Blob([content], { type: 'text/markdown' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `meeting-${meetingId}.md`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      }

      setIsOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Export failed');
    } finally {
      setExporting(null);
      onExportComplete?.();
    }
  };

  const menuItems = [
    { key: 'pdf', label: 'PDF', icon: 'picture_as_pdf' },
    { key: 'markdown', label: 'Markdown', icon: 'description' },
    { key: 'notion', label: 'Notion', icon: 'open_in_new' },
    { key: 'obsidian', label: 'Obsidian', icon: 'link' },
  ] as const;

  return (
    <div ref={menuRef} className="relative">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="flex items-center gap-2 px-3 py-1.5 rounded-lg border border-slate-200 dark:border-slate-700 text-sm font-semibold text-slate-700 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors"
      >
        <span className="material-symbols-outlined text-lg">download</span>
        Export
      </button>

      {isOpen && (
        <div className="absolute right-0 mt-2 w-48 bg-white dark:bg-slate-800 rounded-xl shadow-lg border border-slate-200 dark:border-slate-700 p-1 z-50">
          {error && (
            <div className="px-3 py-2 text-xs text-red-600 dark:text-red-400 border-b border-slate-100 dark:border-slate-700">
              {error}
            </div>
          )}
          {menuItems.map((item) => (
            <button
              key={item.key}
              onClick={() => handleExport(item.key)}
              disabled={exporting !== null}
              className="w-full flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 text-sm text-slate-700 dark:text-slate-300 disabled:opacity-50 transition-colors"
            >
              {exporting === item.key ? (
                <span className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent" />
              ) : (
                <span className="material-symbols-outlined text-lg text-slate-400">
                  {item.icon}
                </span>
              )}
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
