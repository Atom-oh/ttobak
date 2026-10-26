// The authorized add-member API owns user invitation and grant queuing.
// Kept dependency-free so scripts/account-invite.test.mjs can load it directly.

export type MemberAddOutcome =
  | { email: string; status: 'added' }
  | { email: string; status: 'pending'; invited: boolean }
  | { email: string; status: 'failed'; invited: boolean; message: string };

export interface MemberInviteDeps {
  addMember: (email: string, role: string) => Promise<{ pending?: boolean; invited?: boolean } | undefined>;
}

const errorField = (err: unknown, key: 'code' | 'status'): unknown =>
  typeof err === 'object' && err !== null && key in err ? (err as Record<string, unknown>)[key] : undefined;

const errorMessage = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

export async function addMemberWithInvite(deps: MemberInviteDeps, email: string, role: string): Promise<MemberAddOutcome> {
  const inviteEmail = email.trim().toLowerCase();
  try {
    const result = await deps.addMember(inviteEmail, role);
    return result?.pending
      ? { email: inviteEmail, status: 'pending', invited: result.invited === true }
      : { email: inviteEmail, status: 'added' };
  } catch (err) {
    return {
      email: inviteEmail, status: 'failed',
      invited: errorField(err, 'code') === 'INVITATION_GRANT_FAILED',
      message: errorField(err, 'code') === 'INVITATION_REQUIRED'
        ? '초대 기능에 연결하지 못했습니다. 잠시 후 다시 추가하세요.'
        : errorMessage(err, '멤버를 추가하지 못했습니다.'),
    };
  }
}

// What the account detail page shows for one outcome. `refresh` is applied
// before the messages because fetchAll clears the error banner on entry; a
// failure after a sent invite must stay visible so the member can retry.
export function planMemberOutcome(o: MemberAddOutcome): { refresh: boolean; notice?: string; noticeEmail?: string; error?: string } {
  switch (o.status) {
    case 'added':
      return { refresh: true };
    case 'pending':
      return {
        refresh: true,
        noticeEmail: o.email,
        notice: o.invited
          ? `${o.email}님에게 초대 메일 발송을 요청하고 계정 추가를 예약했습니다. 로그인과 이메일 인증 후 자동으로 추가됩니다. 메일이 보이지 않으면 스팸함을 확인하세요.`
          : `${o.email}님의 계정 추가를 예약했습니다. 로그인과 이메일 인증 후 자동으로 추가됩니다. 이 작업으로 초대 이메일이 발송되지는 않습니다.`,
      };
    case 'failed':
      return o.invited
        ? { refresh: true, error: `${o.email}님에게 초대 메일 발송은 요청했지만 계정 추가에 실패했습니다: ${o.message}. 다시 추가하세요.` }
        : { refresh: false, error: o.message };
  }
}

// Korean summary of a batch for the account list banner; empty when nothing
// needs the user's attention.
export function summarizeMemberOutcomes(outcomes: MemberAddOutcome[]): { notice: string; error: string } {
  const invited = outcomes.filter((o) => o.status === 'pending' && o.invited).map((o) => o.email);
  const queued = outcomes.filter((o) => o.status === 'pending' && !o.invited).map((o) => o.email);
  const failed = outcomes.filter((o): o is Extract<MemberAddOutcome, { status: 'failed' }> => o.status === 'failed');
  const notice: string[] = [];
  if (invited.length) notice.push(`초대 메일 발송을 요청했습니다: ${invited.join(', ')}. 로그인과 이메일 인증 후 계정에 자동 추가됩니다.`);
  if (queued.length) notice.push(`가입 대기 중인 사용자로 추가를 예약했습니다: ${queued.join(', ')}.`);
  const error: string[] = [];
  for (const f of failed) {
    error.push(f.invited
      ? `${f.email}: 초대 메일 발송은 요청했지만 계정 추가에 실패했습니다 (${f.message}). 계정 화면에서 다시 추가하세요.`
      : `${f.email}: ${f.message}`);
  }
  return { notice: notice.join(' '), error: error.join(' ') };
}
