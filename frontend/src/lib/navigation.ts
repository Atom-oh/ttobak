// Sidebar and mobile menu-sheet structure, grouped by what the user is doing
// (billing service design §12). Existing URLs stay; only labels and grouping
// change. Billing is not listed here: it is a runtime-gated hand-off rendered
// by BillingInsightButton until the in-app Billing service exists.

export interface NavItem {
  href: string;
  icon: string;
  label: string;
}

export interface NavGroup {
  /** Empty for the ungrouped items (Assistant on top, Settings at the end). */
  name: string;
  items: NavItem[];
}

export const NAV_GROUPS: NavGroup[] = [
  { name: '', items: [{ href: '/chat', icon: 'smart_toy', label: 'Assistant' }] },
  {
    name: '업무',
    items: [
      { href: '/', icon: 'video_camera_front', label: 'Meetings' },
      { href: '/accounts', icon: 'corporate_fare', label: 'Accounts' },
      { href: '/projects', icon: 'work', label: 'Projects' },
      { href: '/docs', icon: 'article', label: 'Docs' },
    ],
  },
  {
    name: '지식',
    items: [
      { href: '/kb', icon: 'library_books', label: 'Library' },
      { href: '/insights?tab=research', icon: 'travel_explore', label: 'Deep Research' },
      { href: '/insights', icon: 'newspaper', label: 'News & Updates' },
      { href: '/dictionary', icon: 'dictionary', label: 'Custom Dictionary' },
    ],
  },
];

export const SETTINGS_ITEM: NavItem = { href: '/settings', icon: 'settings', label: 'Settings' };

/**
 * Whether a menu item is the current page. `activePath` is what the page
 * passes to AppLayout; `tab` is the `?tab=` query on /insights. News and
 * Deep Research share the /insights page, so the tab decides between them;
 * a research detail page passes `/insights/research`.
 */
export function isNavActive(href: string, activePath: string, tab: string | null): boolean {
  if (href === '/insights?tab=research') {
    return activePath === '/insights/research' || (activePath === '/insights' && tab === 'research');
  }
  if (href === '/insights') return activePath === '/insights' && tab !== 'research';
  return activePath === href;
}
