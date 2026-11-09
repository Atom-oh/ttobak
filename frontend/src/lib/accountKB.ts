export type AccountKBKind = 'upload' | 'meeting' | 'news' | 'tech';
export type AccountKBIndexState = 'INDEXED' | 'PENDING' | 'FAILED' | 'UNTRACKED';

export interface AccountKBItem {
  kind: AccountKBKind;
  id: string;
  title: string;
  date?: string;
  uploadedBy?: string;
  indexState: AccountKBIndexState;
  canDownload: boolean;
}

export interface AccountKBPage {
  items: AccountKBItem[];
  nextCursor?: string;
}

export const ACCOUNT_KB_KINDS: AccountKBKind[] = ['upload', 'meeting', 'news', 'tech'];

export const ACCOUNT_KB_KIND_LABELS: Record<AccountKBKind, string> = {
  upload: '업로드',
  meeting: '미팅',
  news: '뉴스',
  tech: '기술',
};

export const ACCOUNT_KB_STATE_LABELS: Record<AccountKBIndexState, string> = {
  INDEXED: '반영됨',
  PENDING: '대기',
  FAILED: '실패',
  UNTRACKED: '미추적',
};

export const ACCOUNT_KB_COPY = {
  download: '다운로드',
  untag: '태그 해제',
  empty: '표시할 자료가 없습니다.',
  more: '더 보기',
} as const;

/** UI affordance only; the server enforces uploader-or-owner on untag. */
export function canUntagKBItem(item: AccountKBItem, userId: string | undefined, isOwner: boolean): boolean {
  return item.kind === 'upload' && !!userId && (item.uploadedBy === userId || isOwner);
}

/** Meeting items open the meeting page; other kinds have no detail route and render plain titles. */
export function kbItemHref(item: AccountKBItem): string | undefined {
  return item.kind === 'meeting' ? `/meeting/${encodeURIComponent(item.id)}` : undefined;
}
