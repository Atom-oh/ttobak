'use client';

import '@astryxdesign/core/reset.css';
import '@astryxdesign/core/astryx.css';
import { Theme } from '@astryxdesign/core/theme';
import { themes, defaultThemeSlug } from '@/astryx-themes';
import { useTheme } from '@/hooks/useTheme';

/**
 * Renders Astryx components with the TTOBAK theme, following the app's
 * light/dark toggle. Astryx CSS loads only on routes that render this scope;
 * globals.css declares the shared cascade-layer order (see DESIGN-SPEC).
 */
export function AstryxScope({ children }: { children: React.ReactNode }) {
  const { isDark } = useTheme();
  return (
    <Theme theme={themes[defaultThemeSlug]} mode={isDark ? 'dark' : 'light'}>
      {children}
    </Theme>
  );
}
