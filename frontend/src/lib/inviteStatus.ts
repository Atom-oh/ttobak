export const INVITE_PENDING_MESSAGE = '일부 초대가 아직 처리 중입니다. 기존 데이터는 계속 사용할 수 있습니다.';

export interface InviteStatusInput {
  pendingGrants: number;
  retryPending: boolean;
  projectContinuation: string;
}

// The top banner reports unfinished grant work only. A server with project
// invitations switched off has nothing to finish, so it shows no banner.
export function inviteStatusMessage({ pendingGrants, retryPending, projectContinuation }: InviteStatusInput): string | null {
  return pendingGrants > 0 || retryPending || projectContinuation ? INVITE_PENDING_MESSAGE : null;
}
