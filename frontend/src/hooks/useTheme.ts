'use client';

import { useEffect, useState, useCallback } from 'react';

export type Theme = 'dark' | 'light';

function readTheme(): Theme {
  // Static export prerender has no `document` -- fall back to the same
  // default the inline bootstrap script in layout.tsx uses
  // (`!t && prefers-color-scheme:dark`), so SSR/prerender output never
  // disagrees with what the client paints on first frame.
  if (typeof document === 'undefined') return 'dark';
  return document.documentElement.classList.contains('dark') ? 'dark' : 'light';
}

/**
 * Reads the current `.dark` class on <html> and keeps in sync via a
 * MutationObserver. Sidebar's theme toggle only flips that class + writes
 * localStorage -- it doesn't emit any event -- so this observer is the only
 * reliable way for a component (mermaid, shiki, ...) to learn the theme
 * changed, including from a toggle added elsewhere later.
 */
export function useTheme() {
  const [theme, setTheme] = useState<Theme>(() => readTheme());

  useEffect(() => {
    const root = document.documentElement;
    const observer = new MutationObserver(() => setTheme(readTheme()));
    observer.observe(root, { attributes: true, attributeFilter: ['class'] });
    return () => observer.disconnect();
  }, []);

  const toggle = useCallback(() => {
    const root = document.documentElement;
    const next = !root.classList.contains('dark');
    root.classList.toggle('dark', next);
    localStorage.setItem('theme', next ? 'dark' : 'light');
    setTheme(next ? 'dark' : 'light');
  }, []);

  return { theme, isDark: theme === 'dark', toggle };
}
