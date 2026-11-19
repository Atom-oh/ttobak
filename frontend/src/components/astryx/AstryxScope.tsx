'use client';

import '@astryxdesign/core/reset.css';
import '@astryxdesign/core/astryx.css';
import { InternationalizationProvider } from '@astryxdesign/core/i18n';
import koKR from '@astryxdesign/core/locales/ko-KR.generated.js';
import { Theme } from '@astryxdesign/core/theme';
import { themes, defaultThemeSlug } from '@/astryx-themes';
import { useTheme } from '@/hooks/useTheme';

// Shipped Korean catalog for Astryx's own strings (field indicators, loading and copy labels).
const MESSAGES = { 'ko-KR': koKR };

/**
 * Renders Astryx components with the TTOBAK theme and Korean built-in strings,
 * following the app's light/dark toggle. Astryx CSS loads only on routes that
 * render this scope; globals.css declares the shared cascade-layer order (see DESIGN-SPEC).
 */
export function AstryxScope({ children }: { children: React.ReactNode }) {
  const { isDark } = useTheme();
  return (
    <InternationalizationProvider locale="ko-KR" messages={MESSAGES}>
      <Theme theme={themes[defaultThemeSlug]} mode={isDark ? 'dark' : 'light'}>
        {children}
      </Theme>
    </InternationalizationProvider>
  );
}
