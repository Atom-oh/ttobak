// Customer update drafts: API types, Korean UI copy and pure helpers.
// Every string from the API is rendered as text only (never as HTML).

export type UpdateSourceKind = 'slack' | 'rss' | 'site' | 'youtube';
export type DismissReason = 'not_relevant' | 'already_known' | 'not_now';
export type UpdateDraftStatus = 'new' | 'adopted' | 'dismissed';

export interface UpdateSource {
  sourceId: string;
  kind: UpdateSourceKind;
  label: string;
  url: string;
  localSourceId: string;
  status: 'active' | 'disabled';
  createdAt: string;
  backfilledAt: string;
}

export interface UpdateDraftOppty {
  projectId: string;
  name: string;
  stage: string;
}

export interface UpdateDraft {
  accountId: string;
  urlHash: string;
  sourceUrl: string;
  sourceTitle: string;
  why: string;
  body: string;
  editedBody: string;
  opportunities: UpdateDraftOppty[];
  links: string[];
  status: UpdateDraftStatus;
  dismissReason: DismissReason | '';
  revision: number;
  runId: string;
  createdAt: string;
  updatedAt: string;
  expiresAt: number;
}

export interface UpdateDraftsPage {
  drafts: UpdateDraft[];
  nextCursor: string;
}

export interface AddUpdateSourceInput {
  kind: UpdateSourceKind;
  label?: string;
  url?: string;
  localSourceId?: string;
}

export interface PatchUpdateSourceInput {
  label?: string;
  status?: 'active' | 'disabled';
}

export interface PatchUpdateDraftInput {
  action: 'edit' | 'adopt' | 'dismiss' | 'reopen';
  body?: string;
  reason?: DismissReason;
  revision: number;
}

/** Mirrors the `copy` map of contracts/customer-updates/ui-copy-ko-v1.json. */
export const COPY = {
  'tab.drafts.label': '초안',
  'nav.drafts': '고객 초안',
  'settings.sources.header': '고객 업데이트 소스',
  'settings.sources.slackHint': 'Mac 앱에서 채널을 선택할 수 있어요',
  'settings.sources.backfillHelp': '처음 등록하면 최근 3일치만 가져옵니다.',
  'settings.sources.urlLabel': '주소',
  'settings.sources.urlPlaceholder': 'https://example.com/feed',
  'settings.sources.labelLabel': '이름',
  'settings.sources.labelPlaceholder': '예: 고객사 공식 블로그',
  'settings.sources.kindLabel': '종류',
  'settings.sources.add': '추가',
  'settings.sources.adding': '추가 중...',
  'settings.sources.pause': '일시중지',
  'settings.sources.resume': '재개',
  'settings.sources.delete': '삭제',
  'settings.sources.deleteConfirm': '이 소스를 삭제할까요?',
  'settings.sources.empty': '등록된 소스가 없습니다.',
  'settings.sources.statusActive': '수집 중',
  'settings.sources.statusDisabled': '일시중지됨',
  'settings.sources.errorHttps': '주소는 https로 시작해야 합니다.',
  'settings.sources.errorLabel': '이름은 1자 이상 100자 이하로 입력해 주세요.',
  'settings.sources.loadFailed': '소스를 불러오지 못했습니다.',
  'settings.sources.errorAddress': '이 주소는 사용할 수 없습니다. 내부 주소이거나 올바르지 않은 주소입니다.',
  'settings.sources.errorLimit': '소스는 30개까지 등록할 수 있습니다.',
  'settings.sources.actionFailed': '소스를 변경하지 못했습니다. 다시 시도해 주세요.',
  'source.kind.slack': 'Slack 채널',
  'source.kind.rss': 'RSS',
  'source.kind.site': '사이트',
  'source.kind.youtube': 'YouTube',
  'dismiss.not_relevant': '관련 없음',
  'dismiss.already_known': '이미 앎',
  'dismiss.not_now': '시기 아님',
  'card.badge': '초안',
  'card.source': '출처',
  'card.why': '왜 관련 있나',
  'card.copy': '복사',
  'card.edit': '수정',
  'card.adopt': '채택함',
  'card.dismiss': '버림',
  'card.conflict': '다른 곳에서 수정되었습니다. 다시 확인해 주세요.',
  'drafts.handled': '처리한 초안',
  'drafts.toggle': '이 고객의 초안 받기',
  'drafts.empty': '아직 초안이 없습니다. 설정에서 소스를 등록하면 관련 있는 소식만 초안으로 쌓입니다.',
  'drafts.more': '더 보기',
  'card.save': '저장',
  'card.cancel': '취소',
  'card.reopen': '다시 열기',
  'card.copied': '복사했어요',
  'card.copyFailed': '복사하지 못했습니다.',
  'card.actionFailed': '처리하지 못했습니다. 다시 시도해 주세요.',
  'card.bodyLabel': '초안 내용',
  'drafts.loadFailed': '초안을 불러오지 못했습니다.',
  'drafts.settingFailed': '설정을 바꾸지 못했습니다.',
  'drafts.title': '고객 초안',
  'drafts.pageHint': '관련 있는 고객 소식으로 만든 초안을 고객별로 모아 보여드려요.',
  'mac.links.setupLabel': 'Slack 링크',
  'mac.links.busy': '이미 수집 중',
  'mac.links.load': '채널 불러오기',
  'mac.links.save': '저장',
  'mac.links.note': 'Slack 글 본문은 Mac 밖으로 나가지 않고, 글에 포함된 공개 링크만 올라갑니다. 처음에는 최근 3일치만 가져옵니다.',
  'mac.links.updateApp': 'Mac 앱을 업데이트하면 Slack 채널을 선택할 수 있어요.',
  'mac.links.tooMany': '채널은 20개까지 선택할 수 있습니다.',
  'intake.runNow': '지금 수집',
} as const;

export const SOURCE_KIND_LABELS: Record<UpdateSourceKind, string> = {
  slack: COPY['source.kind.slack'],
  rss: COPY['source.kind.rss'],
  site: COPY['source.kind.site'],
  youtube: COPY['source.kind.youtube'],
};

export const DISMISS_REASON_LABELS: Record<DismissReason, string> = {
  not_relevant: COPY['dismiss.not_relevant'],
  already_known: COPY['dismiss.already_known'],
  not_now: COPY['dismiss.not_now'],
};

/** Returns the draft with `opportunities` and `links` as arrays even when the server sent null or omitted them. */
export function normalizeDraft(d: UpdateDraft): UpdateDraft {
  const raw = d as UpdateDraft & { opportunities?: unknown; links?: unknown };
  return {
    ...d,
    opportunities: Array.isArray(raw.opportunities) ? raw.opportunities : [],
    links: Array.isArray(raw.links) ? raw.links : [],
  };
}

/** Normalizes every draft of a page; a missing list or cursor becomes empty. */
export function normalizeDraftsPage(page: UpdateDraftsPage): UpdateDraftsPage {
  const raw = (page ?? {}) as { drafts?: unknown; nextCursor?: unknown };
  return {
    drafts: Array.isArray(raw.drafts) ? (raw.drafts as UpdateDraft[]).map(normalizeDraft) : [],
    nextCursor: typeof raw.nextCursor === 'string' ? raw.nextCursor : '',
  };
}

const draftKey = (d: Pick<UpdateDraft, 'accountId' | 'urlHash'>) => `${d.accountId}:${d.urlHash}`;

/**
 * Appends a later page in server order. Drafts already shown keep their
 * position; a draft repeated by the next page (the list shifted) is skipped.
 */
export function appendDraftPage(shown: UpdateDraft[], more: UpdateDraft[]): UpdateDraft[] {
  const seen = new Set(shown.map(draftKey));
  const out = [...shown];
  for (const d of more) {
    const key = draftKey(d);
    if (!seen.has(key)) {
      seen.add(key);
      out.push(d);
    }
  }
  return out;
}

/**
 * Reloads from the first page up to `pages` pages (at least one), stopping
 * early at the last page, so a reload keeps what the user had loaded.
 */
export async function loadDraftPages(
  fetchPage: (cursor: string | undefined) => Promise<UpdateDraftsPage>,
  pages: number,
): Promise<{ drafts: UpdateDraft[]; nextCursor: string; pages: number }> {
  let drafts: UpdateDraft[] = [];
  let cursor: string | undefined;
  let loaded = 0;
  const want = Math.max(1, Math.floor(pages) || 1);
  do {
    const page = normalizeDraftsPage(await fetchPage(cursor));
    drafts = appendDraftPage(drafts, page.drafts);
    cursor = page.nextCursor || undefined;
    loaded++;
  } while (cursor && loaded < want);
  return { drafts, nextCursor: cursor ?? '', pages: loaded };
}

/**
 * Merges a re-read from the first page into what is shown: shown drafts keep
 * their visible order with the server copy when the re-read has one, then
 * drafts not yet shown follow in server order.
 */
export function mergeRereadDrafts(shown: UpdateDraft[], reread: UpdateDraft[]): UpdateDraft[] {
  const fresh = new Map(reread.map((d) => [draftKey(d), d]));
  return appendDraftPage(shown.map((d) => fresh.get(draftKey(d)) ?? d), reread);
}

/**
 * More after a status change: the saved offset cursor is stale because the
 * changed draft moved in the server order. Re-reads from the first page through
 * the shown drafts plus one page, merges, and returns the re-read's cursor.
 */
export async function loadMoreAfterStatusChange(
  fetchPage: (cursor: string | undefined) => Promise<UpdateDraftsPage>,
  shown: UpdateDraft[],
): Promise<{ drafts: UpdateDraft[]; nextCursor: string; pages: number }> {
  let reread: UpdateDraft[] = [];
  let cursor: string | undefined;
  let pageSize = 0;
  let pages = 0;
  do {
    const page = normalizeDraftsPage(await fetchPage(cursor));
    pageSize ||= page.drafts.length;
    reread = appendDraftPage(reread, page.drafts);
    cursor = page.nextCursor || undefined;
    pages++;
  } while (cursor && reread.length < shown.length + pageSize);
  return { drafts: mergeRereadDrafts(shown, reread), nextCursor: cursor ?? '', pages };
}

/** True when `next` changes the status of a draft that is shown. */
export function draftStatusChanged(shown: UpdateDraft[], next: UpdateDraft): boolean {
  return shown.some((d) => draftKey(d) === draftKey(next) && d.status !== next.status);
}

/**
 * The revision an edit save submits: the one the edit started from. After a
 * 409 the user's next explicit save targets the latest revision shown.
 */
export function editSaveRevision(base: number, latest: number, conflicted: boolean): number {
  return conflicted ? latest : base;
}

/** The empty-state message shows only when nothing is loaded and no page remains. */
export function showDraftsEmpty(drafts: UpdateDraft[], nextCursor: string): boolean {
  return drafts.length === 0 && !nextCursor;
}

/** Korean message for a failed source add: 400 unusable address, 409 source limit, otherwise generic. */
export function sourceAddErrorMessage(err: unknown): string {
  const status = (err as { status?: unknown } | null)?.status;
  if (status === 400) return COPY['settings.sources.errorAddress'];
  if (status === 409) return COPY['settings.sources.errorLimit'];
  return COPY['settings.sources.actionFailed'];
}

/** The text to copy: the edited body when present, otherwise the generated body. */
export function draftCopyText(d: Pick<UpdateDraft, 'body' | 'editedBody'>): string {
  return d.editedBody || d.body;
}

const KST_DAY = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit' });

/** Groups drafts by KST calendar day of createdAt: newest day first, newest draft first. */
export function groupDraftsByDay(drafts: UpdateDraft[]): { day: string; drafts: UpdateDraft[] }[] {
  const stamped = drafts
    .map((draft) => ({ draft, at: Date.parse(draft.createdAt) }))
    .filter((entry) => Number.isFinite(entry.at))
    .sort((a, b) => b.at - a.at);
  const groups: { day: string; drafts: UpdateDraft[] }[] = [];
  for (const { draft, at } of stamped) {
    const day = KST_DAY.format(new Date(at));
    const last = groups[groups.length - 1];
    if (last && last.day === day) last.drafts.push(draft);
    else groups.push({ day, drafts: [draft] });
  }
  return groups;
}

/** Only https links are rendered as anchors; anything else stays plain text. */
export function isHttpsUrl(url: string): boolean {
  return typeof url === 'string' && url.startsWith('https://');
}

/** Splits drafts into the open ones (`new`) and the handled ones (adopted or dismissed). */
export function splitHandledDrafts(drafts: UpdateDraft[]): { open: UpdateDraft[]; handled: UpdateDraft[] } {
  const open: UpdateDraft[] = [];
  const handled: UpdateDraft[] = [];
  for (const draft of drafts) (draft.status === 'new' ? open : handled).push(draft);
  return { open, handled };
}

/** Groups drafts by account, keeping the order in which each account first appears. */
export function groupDraftsByAccount(drafts: UpdateDraft[]): { accountId: string; drafts: UpdateDraft[] }[] {
  const groups: { accountId: string; drafts: UpdateDraft[] }[] = [];
  const index = new Map<string, number>();
  for (const draft of drafts) {
    const at = index.get(draft.accountId);
    if (at === undefined) {
      index.set(draft.accountId, groups.length);
      groups.push({ accountId: draft.accountId, drafts: [draft] });
    } else groups[at].drafts.push(draft);
  }
  return groups;
}

/** Display name for an account the page could not resolve: the id, shortened. */
export function shortAccountId(accountId: string): string {
  return accountId.length > 8 ? accountId.slice(0, 8) : accountId;
}

/** Replaces the draft with the same account and URL hash; other drafts are untouched. */
export function replaceDraft(drafts: UpdateDraft[], next: UpdateDraft): UpdateDraft[] {
  return drafts.map((d) => (d.accountId === next.accountId && d.urlHash === next.urlHash ? next : d));
}

/** Best-effort kind for a pasted URL; the user can still change it. */
export function guessSourceKind(url: string): 'rss' | 'site' | 'youtube' {
  let parsed: URL;
  try {
    parsed = new URL(url.trim());
  } catch {
    return 'site';
  }
  const host = parsed.hostname.toLowerCase();
  if (host === 'youtu.be' || host === 'youtube.com' || host.endsWith('.youtube.com')) return 'youtube';
  const path = parsed.pathname.toLowerCase();
  if (/(^|\/)(feed|rss|atom)(\/|\.xml)?$/.test(path) || /\.(rss|atom|xml)$/.test(path)) return 'rss';
  return 'site';
}

export const SOURCE_LABEL_MAX = 100;

/** Client-side check before adding a source; the server stays the authority. Returns a COPY message or ''. */
export function validateSourceForm(url: string, label: string): string {
  let parsed: URL;
  try {
    parsed = new URL(url.trim());
  } catch {
    return COPY['settings.sources.errorHttps'];
  }
  if (parsed.protocol !== 'https:') return COPY['settings.sources.errorHttps'];
  const length = Array.from(label.trim()).length;
  if (length < 1 || length > SOURCE_LABEL_MAX) return COPY['settings.sources.errorLabel'];
  return '';
}
