export const ACCOUNT_TABS = ['general', 'meetings', 'projects', 'docs', 'billing'] as const;
export type AccountTab = (typeof ACCOUNT_TABS)[number];

export const ACCOUNT_TAB_LABELS: Record<AccountTab, string> = {
  general: '개요',
  meetings: '미팅',
  projects: '프로젝트',
  docs: '문서',
  billing: 'Billing',
};

export function visibleAccountTabs(billingVisible: boolean): AccountTab[] {
  return ACCOUNT_TABS.filter((tab) => tab !== 'billing' || billingVisible);
}

export function parseAccountTab(value: string | null, billingVisible: boolean): AccountTab {
  const tab = ACCOUNT_TABS.find((t) => t === value);
  if (!tab || (tab === 'billing' && !billingVisible)) return 'general';
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
