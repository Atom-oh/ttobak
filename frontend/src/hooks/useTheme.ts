'use client';

import { useEffect, useState, useCallback } from 'react';

export type Theme = 'dark' | 'light';

function readTheme(): Theme {
  // Defensive only -- by the time this is ever called (inside the effect
  // below, or its MutationObserver callback), we're always on the client
  // post-mount, so `document` always exists. Kept as a guard against a
  // future caller running during SSR/prerender rather than as a
  // hydration-correctness mechanism (see useTheme's initial state below
  // for that).
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
  // Initial state is a FIXED constant, not a read of the real DOM: the
  // static-export prerender always bakes its markup assuming 'dark'
  // (there's no `document` at prerender time to read a real value from),
  // so the client's first hydration render must match that exact
  // assumption -- reading the actual class here would diverge for a
  // light-preference user (layout.tsx's inline bootstrap script already
  // applied their real theme to <html> before hydration even runs) and
  // produce a hydration mismatch. Synced to the real value in the effect
  // below, once mounted -- a one-frame flash to the correct theme, never
  // a mismatch.
  const [theme, setTheme] = useState<Theme>('dark');

  useEffect(() => {
    const root = document.documentElement;
    // Sync to the real, post-hydration DOM state -- this is the
    // deliberate "constant SSR value, then correct on mount" pattern
    // this hook relies on for hydration safety, not the derived-state
    // anti-pattern this lint rule usually guards against.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setTheme(readTheme());
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
