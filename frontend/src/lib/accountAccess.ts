import type { Account, AccountSummary, AccountDocument } from '@/types/meeting';

/** Mutation selectors use direct membership, including legacy direct DTOs. */
export function isEditableAccount(account: AccountSummary): boolean {
  return account.accessPermission !== 'read' && !account.inheritedFromAccountId && !!account.role;
}

export function accountCapabilities(account: Account | null, userId?: string) {
  const directMember = !!userId && !!account?.members.some(member => member.userId === userId);
  const canEdit = directMember && account?.accessPermission !== 'read' && !account?.inheritedFromAccountId;
  return {
    directMember,
    canEdit,
    isOwner: canEdit && account?.ownerUserId === userId,
  };
}

export function canEditDocument(document: AccountDocument | null): boolean {
  return !!document && !document.sharedBy && document.accessPermission !== 'read'
    && (!document.inheritedFromAccountId || document.accessPermission === 'edit');
}
