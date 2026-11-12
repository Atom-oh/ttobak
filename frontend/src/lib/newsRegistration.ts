// Pure helpers for news source registration (Insights news tab sources view
// and Settings › Crawler Sources). Only accountOverview is imported, so tests
// load this module in a vm context with a small require stub.
import { parseTermList } from '@/lib/accountOverview';

export const MAX_ALIASES = 20;
export const MAX_ALIAS_CHARS = 100;
export const MAX_SOURCE_NAME_CHARS = 200;
export const MAX_NEWS_QUERIES = 32;

export type NewsSourceKind = 'common' | 'account';

/** One item of `GET /api/news-sources`. */
export interface NewsSourceItem {
  sourceId: string;
  sourceName: string;
  kind: NewsSourceKind;
  accountId?: string;
  accountName?: string;
  newsQueries?: string[];
  aliases?: string[];
  customUrls?: string[];
  newsSources?: string[];
  status?: string;
  lastCrawledAt?: string;
  documentCount?: number;
  canEdit?: boolean;
  // The directory cut this item's lists; never prefill an edit form from it.
  listsTruncated?: boolean;
  /** Account kind: when "collect now" is allowed again; absent when allowed now. */
  nextCollectAllowedAt?: string;
}

/** One page of `GET /api/news-sources`. */
export interface NewsSourcesPage { sources: NewsSourceItem[]; nextCursor?: string; truncated?: boolean }

/** Search terms for the list, marked when the directory cut them. */
export function sourceTermsText(item: NewsSourceItem, fallback: string): string {
  const terms = (item.newsQueries ?? []).join(', ');
  if (item.listsTruncated) return `${terms || fallback} …`;
  return terms || fallback;
}

export const ALIAS_INVALID_MESSAGE = `별칭은 제어 문자 없이 ${MAX_ALIAS_CHARS}자 이내로 입력해 주세요.`;

/** Settings' per-alias rule: at most 100 code points, no control/format characters. */
export function aliasError(alias: string): string | null {
  return [...alias].length > MAX_ALIAS_CHARS || /[\p{Cc}\p{Cf}]/u.test(alias) ? ALIAS_INVALID_MESSAGE : null;
}

export interface CommonSourceFields {
  sourceName: string;
  awsServices: string[];
  newsQueries: string[];
  customUrls: string[];
  aliases: string[];
}

/** The `POST /api/crawler/sources` body Settings sends; shared so both forms stay identical. */
export function commonAddRequest(data: CommonSourceFields) {
  return {
    sourceName: data.sourceName,
    awsServices: data.awsServices,
    newsSources: [] as string[],
    newsQueries: data.newsQueries,
    customUrls: data.customUrls.length > 0 ? data.customUrls : undefined,
    aliases: data.aliases.length > 0 ? data.aliases : undefined,
  };
}

// Go White_Space, which strings.TrimSpace uses (unicode.IsSpace); JS trim()
// differs (it trims U+FEFF and keeps U+0085), so the ends are trimmed here.
const GO_TRIM = /^\p{White_Space}+|\p{White_Space}+$/gu;

/**
 * Port of `normalizeSourceID` in backend/internal/service/crawler.go, which
 * `CrawlerService.AddSource` uses to resolve a common source by name:
 * strings.ToLower (per code point, so no final-sigma context), TrimSpace,
 * ASCII spaces to hyphens, then keep unicode.IsLetter, unicode.IsDigit (Nd)
 * and '-'. Keep in step with that function and its TestNormalizeSourceID.
 */
export function normalizeSourceId(name: string): string {
  const lower = Array.from(name, (ch) => ch.toLowerCase()).join('');
  const trimmed = lower.replace(GO_TRIM, '').replaceAll(' ', '-');
  return Array.from(trimmed).filter((ch) => /^[\p{L}\p{Nd}-]$/u.test(ch)).join('');
}

export const ALREADY_SUBSCRIBED_MESSAGE = '이미 등록한 공통 뉴스 소스예요. 수정은 설정의 Crawler Sources에서 할 수 있어요.';

/**
 * The subscribed source a common registration would resolve to, or null.
 * AddSource replaces the caller's whole subscription for an existing
 * subscribed source, so the form must not submit in that case.
 */
export function subscribedSourceFor(sourceName: string, subscribedIds: readonly string[]): string | null {
  const id = normalizeSourceId(sourceName);
  return id && subscribedIds.includes(id) ? id : null;
}

export interface CommonNewsForm { sourceName: string; queries: string; aliases: string; customUrls: string }

function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' || url.protocol === 'http:';
  } catch {
    return false;
  }
}

/** Validates the Insights common news form and builds the Settings request (no AWS services). */
export function commonNewsRequest(form: CommonNewsForm):
  { request: ReturnType<typeof commonAddRequest>; error?: undefined } | { request?: undefined; error: string } {
  const sourceName = form.sourceName.trim();
  if (!sourceName) return { error: '뉴스 소스 이름을 입력해 주세요.' };
  if ([...sourceName].length > MAX_SOURCE_NAME_CHARS) return { error: '뉴스 소스 이름은 200자 이하로 입력해 주세요.' };
  const newsQueries = parseTermList(form.queries);
  if (newsQueries.length > MAX_NEWS_QUERIES) return { error: '검색어는 최대 32개까지 입력할 수 있습니다.' };
  const aliases: string[] = [];
  for (const alias of parseTermList(form.aliases)) {
    if (aliasError(alias)) return { error: ALIAS_INVALID_MESSAGE };
    if (!aliases.some((a) => a.toLowerCase() === alias.toLowerCase())) aliases.push(alias);
  }
  if (aliases.length > MAX_ALIASES) return { error: '별칭은 최대 20개까지 입력할 수 있습니다.' };
  const customUrls = [...new Set(form.customUrls.split(/\s+/).filter(Boolean))];
  if (customUrls.some((url) => !isHttpUrl(url))) return { error: '직접 수집할 주소는 http 또는 https로 시작해야 합니다.' };
  return { request: commonAddRequest({ sourceName, awsServices: [], newsQueries, customUrls, aliases }) };
}

/** Prefills the name with the picked customer while the user has not typed one. */
export function prefillSourceName(current: string, touched: boolean, accountName: string | undefined): string {
  return touched || !accountName ? current : accountName;
}

export interface AccountEditForm { sourceName: string; queries: string; aliases: string }

/** Edit prefill: one stored term per line, so every term round-trips. */
export function accountEditForm(source: { sourceName: string; newsQueries?: string[]; aliases?: string[] }): AccountEditForm {
  return { sourceName: source.sourceName, queries: (source.newsQueries ?? []).join('\n'), aliases: (source.aliases ?? []).join('\n') };
}

/** Edit parsing: one term per line only; commas stay inside a term. */
export function parseTermLines(text: string): string[] {
  return [...new Set(text.split('\n').map((line) => line.trim()).filter(Boolean))];
}

/**
 * Builds the `PUT .../news-sources/{id}` body from the fields whose text
 * differs from the exact prefilled text; an untouched field is never sent.
 * `unchanged` means there is nothing to save (the server rejects an empty body).
 */
export function accountEditRequest(form: AccountEditForm, prefilled: AccountEditForm):
  { body: { sourceName?: string; newsQueries?: string[]; aliases?: string[] }; error?: undefined; unchanged?: undefined }
  | { body?: undefined; error: string; unchanged?: undefined }
  | { body?: undefined; error?: undefined; unchanged: true } {
  const body: { sourceName?: string; newsQueries?: string[]; aliases?: string[] } = {};
  if (form.sourceName !== prefilled.sourceName) {
    const sourceName = form.sourceName.trim();
    if (!sourceName) return { error: '뉴스 소스 이름을 입력해 주세요.' };
    if ([...sourceName].length > MAX_SOURCE_NAME_CHARS) return { error: '뉴스 소스 이름은 200자 이하로 입력해 주세요.' };
    if (sourceName !== prefilled.sourceName) body.sourceName = sourceName;
  }
  if (form.queries !== prefilled.queries) {
    const newsQueries = parseTermLines(form.queries);
    if (newsQueries.length === 0) return { error: '검색어를 하나 이상 입력해 주세요.' };
    if (newsQueries.length > MAX_NEWS_QUERIES) return { error: '검색어는 최대 32개까지 입력할 수 있습니다.' };
    body.newsQueries = newsQueries;
  }
  if (form.aliases !== prefilled.aliases) {
    const aliases = parseTermLines(form.aliases);
    if (aliases.length > MAX_ALIASES) return { error: '별칭은 최대 20개까지 입력할 수 있습니다.' };
    body.aliases = aliases;
  }
  return Object.keys(body).length === 0 ? { unchanged: true } : { body };
}

// Not plain verbs: those literals collide with Material Symbol names (icon-font check).
export type RegistrationAction = 'create-account' | 'create-common' | 'edit-source' | 'remove-source' | 'load-sources';

/** Readable Korean text for a failed news source request; never a raw code. */
export function registrationErrorMessage(failure: unknown, action: RegistrationAction): string {
  const status = (failure as { status?: number } | null)?.status;
  if (status === 403) {
    return action === 'create-common'
      ? '이 이름은 공통 뉴스로 등록할 수 없습니다. 다른 이름을 입력해 주세요.'
      : '이 고객사의 직접 구성원만 뉴스 소스를 관리할 수 있습니다.';
  }
  if (status === 404) return action === 'load-sources' ? '뉴스 소스 목록을 아직 사용할 수 없습니다.' : '고객사나 뉴스 소스를 찾을 수 없습니다. 목록을 새로 고쳐 주세요.';
  if (status === 400) {
    if (action === 'create-account') return '등록할 수 없습니다. 입력 내용과 고객사별 소스 개수(최대 10개)를 확인해 주세요.';
    if (action === 'create-common') return '등록할 수 없습니다. 이름, 검색어, 별칭과 주소를 확인해 주세요.';
    return '저장할 수 없습니다. 이름과 검색어를 확인해 주세요.';
  }
  if (status === 429) return '요청이 많습니다. 잠시 후 다시 시도해 주세요.';
  const verb = action === 'remove-source' ? '삭제하지' : action === 'load-sources' ? '불러오지' : '저장하지';
  return `뉴스 소스를 ${verb} 못했습니다. 잠시 후 다시 시도해 주세요.`;
}

// ---- Insights news scope filter ----

export const SCOPE_ALL = '';
export const SCOPE_COMMON = 'common';
export const SCOPE_ACCOUNTS = 'account';
const SCOPE_ACCOUNT_PREFIX = 'account:';

export interface ScopeOption { value: string; label: string }

/** The three fixed entries, then one per customer that has a registered source, by name. */
export function newsScopeOptions(sources: readonly NewsSourceItem[] | null): ScopeOption[] {
  const options: ScopeOption[] = [
    { value: SCOPE_ALL, label: '전체' },
    { value: SCOPE_COMMON, label: '공통' },
    { value: SCOPE_ACCOUNTS, label: '고객사 전체' },
  ];
  const accounts = new Map<string, string>();
  for (const source of sources ?? []) {
    if (source.kind === 'account' && source.accountId && !accounts.has(source.accountId)) {
      accounts.set(source.accountId, source.accountName?.trim() || source.accountId);
    }
  }
  const sorted = [...accounts].sort((a, b) => a[1].localeCompare(b[1], 'ko') || a[0].localeCompare(b[0]));
  return options.concat(sorted.map(([id, name]) => ({ value: `${SCOPE_ACCOUNT_PREFIX}${id}`, label: name })));
}

/**
 * Maps a scope value to the `GET /api/insights` `kind` / `account` parameters.
 * The directory endpoint and these filters ship together, so without a
 * successful directory answer (`capable`) nothing is sent: an older server
 * ignores them and would show everything under a "filtered" label.
 */
export function newsScopeParams(scope: string, capable: boolean): { kind?: NewsSourceKind; account?: string } {
  if (!capable) return {};
  if (scope === SCOPE_COMMON) return { kind: 'common' };
  if (scope === SCOPE_ACCOUNTS) return { kind: 'account' };
  if (scope.startsWith(SCOPE_ACCOUNT_PREFIX) && scope.length > SCOPE_ACCOUNT_PREFIX.length) {
    return { kind: 'account', account: scope.slice(SCOPE_ACCOUNT_PREFIX.length) };
  }
  return {};
}

/**
 * The scope the news tab may use: all without directory capability, and all
 * when the settled directory (auto-paging finished) no longer offers the
 * selected entry, so a request never carries a scope the select cannot show.
 */
export function reconcileScope(scope: string, capable: boolean, settled: boolean, options: readonly ScopeOption[]): string {
  if (!capable) return SCOPE_ALL;
  if (settled && !options.some((option) => option.value === scope)) return SCOPE_ALL;
  return scope;
}

// ---- Directory list state (`GET /api/news-sources` pages) ----

export interface DirectoryState {
  // Bumped by every reload; responses from an older generation are ignored.
  generation: number;
  sources: NewsSourceItem[] | null;
  nextCursor: string;
  truncated: boolean;
  // A page of the current generation has loaded.
  loaded: boolean;
  // The current generation's automatic paging has finished or stopped.
  settled: boolean;
  // The directory answered successfully in this session and has not since
  // failed on a fresh load: the insights scope filters exist.
  capable: boolean;
  unavailable: boolean;
  error: string;
  loadingMore: boolean;
}

export const INITIAL_DIRECTORY: DirectoryState = {
  generation: 0, sources: null, nextCursor: '', truncated: false, loaded: false, settled: false,
  capable: false, unavailable: false, error: '', loadingMore: false,
};

/** Starts a fresh load; capability is kept until that load answers. */
export function directoryReload(state: DirectoryState): DirectoryState {
  return { ...INITIAL_DIRECTORY, generation: state.generation + 1, capable: state.capable };
}

export function directoryLoadingMore(state: DirectoryState, generation: number): DirectoryState {
  return generation === state.generation ? { ...state, loadingMore: true } : state;
}

/** Marks the current generation's automatic paging as finished. */
export function directorySettled(state: DirectoryState, generation: number): DirectoryState {
  return generation === state.generation ? { ...state, settled: true } : state;
}

/** Applies one page; the first page of a generation replaces, later ones append. */
export function directoryPageLoaded(state: DirectoryState, generation: number, page: NewsSourcesPage): DirectoryState {
  if (generation !== state.generation) return state;
  return {
    ...state,
    sources: [...(state.loaded ? state.sources ?? [] : []), ...(page.sources ?? [])],
    nextCursor: page.nextCursor ?? '',
    truncated: (state.loaded && state.truncated) || !!page.truncated,
    loaded: true, capable: true, unavailable: false, error: '', loadingMore: false,
  };
}

/**
 * Applies a failure. After a page of this generation loaded, the loaded pages
 * and capability stay (a later 404 is just an error); before that, 404 means
 * the endpoint is not deployed and any failure drops capability.
 */
export function directoryFailed(state: DirectoryState, generation: number, failure: unknown): DirectoryState {
  if (generation !== state.generation) return state;
  const message = registrationErrorMessage(failure, 'load-sources');
  if (state.loaded) return { ...state, loadingMore: false, settled: true, error: (failure as { status?: number } | null)?.status === 404 ? '다음 소스를 불러오지 못했습니다.' : message };
  const unavailable = (failure as { status?: number } | null)?.status === 404;
  return { ...state, sources: [], loaded: false, settled: true, capable: false, unavailable, loadingMore: false, error: unavailable ? '' : message };
}

// ---- Addressable sources view ----

/** `/insights?tab=news&sources=1[&account={id}]`, the link other pages use. */
export function newsSourcesHref(accountId?: string): string {
  const query = new URLSearchParams({ tab: 'news', sources: '1' });
  if (accountId) query.set('account', accountId);
  return `/insights?${query.toString()}`;
}

/** Whether the sources view is open, and the customer to preselect. */
export function parseNewsSourcesView(params: { get(name: string): string | null }): { open: boolean; accountId: string } {
  const open = (params.get('tab') ?? 'news') === 'news' && params.get('sources') === '1';
  return { open, accountId: open ? (params.get('account') ?? '').trim() : '' };
}
