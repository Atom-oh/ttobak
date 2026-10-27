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
