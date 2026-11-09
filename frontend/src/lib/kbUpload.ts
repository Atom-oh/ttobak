// KB upload Account choice: the user must pick an Account or "personal"
// before a file can be sent; the server returns the headers it signed.

/** Sentinel for "없음(개인)": a personal KB file, no account-id metadata. */
export const KB_PERSONAL_CHOICE = '__personal__';

/** True once the user picked an Account or the personal option. */
export const kbUploadReady = (choice?: string): boolean => !!choice;

/** accountId for the upload request; undefined for a personal file. */
export const kbUploadAccountId = (choice: string): string | undefined =>
  choice === KB_PERSONAL_CHOICE ? undefined : choice;

/** Headers for the presigned PUT: Content-Type plus the server-signed headers. */
export const kbUploadPutHeaders = (
  contentType: string,
  signed?: Record<string, string>,
): Record<string, string> => ({ 'Content-Type': contentType, ...(signed ?? {}) });

/** Shown when the Account list fails to load, so "없음(개인)" is not taken as the only choice. */
export const KB_ACCOUNTS_LOAD_ERROR =
  'Account 목록을 불러오지 못했습니다. 지금은 없음(개인)만 선택할 수 있습니다. 새로고침 후 다시 시도해 주세요.';

/** Inline notice for the upload Account picker; undefined while loading or after a successful load. */
export const kbAccountListNotice = (load: 'loading' | 'loaded' | 'failed'): string | undefined =>
  load === 'failed' ? KB_ACCOUNTS_LOAD_ERROR : undefined;

/** Legacy binary .ppt is stored but never indexed; .pptx is indexed as Markdown slides/notes. */
export const KB_LEGACY_PPT_NOTICE =
  '구형 .ppt 파일은 저장되지만 검색(KB)에 반영되지 않습니다. PowerPoint에서 .pptx로 저장한 뒤 다시 업로드해 주세요.';

/** Upload notice when any selected file is a legacy .ppt; undefined otherwise. */
export const kbLegacyPptNotice = (fileNames: readonly string[]): string | undefined =>
  fileNames.some((name) => /\.ppt$/i.test(name)) ? KB_LEGACY_PPT_NOTICE : undefined;
