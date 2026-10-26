import type { AccountCreationRequest, AccountCreationResponse } from '@/types/meeting';

export interface CreationAttempt {
  fingerprint: string;
  requestId: string;
}

// Same normalized input keeps its UUID after a lost response. Edited input is
// a different operation and cannot reuse the former receipt capability.
export function accountCreationAttempt(
  request: AccountCreationRequest, previous: CreationAttempt | null, newId: () => string,
): CreationAttempt {
  const fingerprint = JSON.stringify(request);
  return previous?.fingerprint === fingerprint
    ? previous : { fingerprint, requestId: newId() };
}

export function accountCreationNotice(receipt: AccountCreationResponse, ownerEmail: string, creatorUserId: string): string {
  const creation = `${receipt.name} Account를 만들었습니다.`;
  const ownership = ownerEmail ? ` Owner: ${ownerEmail}.` : '';
  const invitation = receipt.ownerInvitationRequested
    ? ' 초대 메일 전송을 요청했습니다.'
    : receipt.ownerInvitationPending ? ' 기존 초대로 가입할 수 있습니다.' : '';
  const membership = receipt.creatorMember
    ? receipt.ownerUserId === creatorUserId
      ? ' 생성자는 Owner로 참여합니다.'
      : ' 생성자는 일반 멤버(AM)로 참여합니다.'
    : ' 생성자는 멤버로 등록되지 않았습니다. 필요한 미팅이나 문서는 별도로 공유받을 수 있습니다.';
  return creation + ownership + invitation + membership;
}
