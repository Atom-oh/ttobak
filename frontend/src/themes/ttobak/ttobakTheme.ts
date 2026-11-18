import {defineTheme} from '@astryxdesign/core/theme';
import {neutralTheme} from '@astryxdesign/theme-neutral';

/**
 * Astryx neutral with the TTOBAK brand accent and text colors and the app's
 * system font stack, so Astryx components sit beside the Tailwind UI without a
 * second palette or a web font download. Rebuild after editing:
 * `npx astryx theme build src/themes/ttobak/ttobakTheme.ts`.
 */
const SYSTEM_FALLBACKS =
  '-apple-system, BlinkMacSystemFont, "Segoe UI", "Apple SD Gothic Neo", "Malgun Gothic", Roboto, sans-serif';

export const ttobakTheme = defineTheme({
  name: 'ttobak',
  extends: neutralTheme,
  // typography replaces the base config whole, so neutral's scale, heading
  // weights and code stack are repeated with the family swapped.
  typography: {
    scale: {base: 14, ratio: 1.2},
    body: {family: 'system-ui', fallbacks: SYSTEM_FALLBACKS},
    heading: {family: 'system-ui', fallbacks: SYSTEM_FALLBACKS, weights: {3: 'bold', 4: 'bold'}},
    code: {
      family: 'ui-monospace',
      fallbacks: '"SF Mono", Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
    },
  },
  // Light/dark pairs mirror globals.css (--primary, --text-main, --text-secondary).
  tokens: {
    '--color-accent': ['#3211d4', '#8b85f7'],
    '--color-text-primary': ['#0f172a', '#e7e7ec'],
    '--color-text-secondary': ['#64748b', '#b3b8c2'],
  },
});
