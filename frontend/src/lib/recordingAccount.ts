import { accountApi } from './api';
import { accountCapabilities } from './accountAccess';

/** Reference browsing never grants the direct membership required by creation. */
export async function resolveRecordingAccountId(accountId?: string, userId?: string): Promise<string | undefined> {
  if (!accountId || !userId) return undefined;
  const account = await accountApi.get(accountId);
  return account.accountId === accountId && accountCapabilities(account, userId).canEdit
    ? accountId : undefined;
}
