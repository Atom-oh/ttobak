export const ACCOUNT_TABS = ['general', 'meetings', 'events', 'insights', 'projects', 'docs', 'kb', 'billing'] as const;
export type AccountTab = (typeof ACCOUNT_TABS)[number];

export const ACCOUNT_TAB_LABELS: Record<AccountTab, string> = {
  general: '개요',
  meetings: '미팅',
  events: '일정',
  insights: '인사이트',
  projects: '프로젝트',
  docs: '문서',
  kb: '자료(KB)',
  billing: 'Billing',
};

/** Events and KB are visible to direct members only, like contacts. */
export function visibleAccountTabs(billingVisible: boolean, eventsVisible = false): AccountTab[] {
  return ACCOUNT_TABS.filter((tab) => (tab !== 'billing' || billingVisible) && (tab !== 'events' || eventsVisible) && (tab !== 'kb' || eventsVisible));
}

export function parseAccountTab(value: string | null, billingVisible: boolean, eventsVisible = false): AccountTab {
  const tab = ACCOUNT_TABS.find((t) => t === value);
  if (!tab || (tab === 'billing' && !billingVisible) || (tab === 'events' && !eventsVisible) || (tab === 'kb' && !eventsVisible)) return 'general';
  return tab;
}

/** Query string (with leading `?`, or empty) selecting `tab` while keeping every other param. */
export function accountTabHref(search: string, tab: AccountTab): string {
  const params = new URLSearchParams(search);
  if (tab === 'general') params.delete('tab');
  else params.set('tab', tab);
  const query = params.toString();
  return query ? `?${query}` : '';
}
