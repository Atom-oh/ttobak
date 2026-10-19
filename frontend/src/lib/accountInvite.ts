// Account member add with an admin invite fallback. Kept dependency-free so
// scripts/account-invite.test.mjs can load it directly.
//
// The backend answers AddMember for an email with neither a TTOBAK profile nor
// a Cognito user with 404 INVITATION_REQUIRED. Creating that Cognito user is
// admin-only (POST /api/settings/invite-user, RequireAdmin), so only an admin
// caller sends the invite and then retries; the retry queues a PendingShare
// that materializes on the invitee's first verified login.

export type MemberAddOutcome =
  | { email: string; status: 'added' }
  | { email: string; status: 'pending'; invited: boolean }
  | { email: string; status: 'needs-admin' }
  | { email: string; status: 'failed'; invited: boolean; message: string };

export interface MemberInviteDeps {
  addMember: (email: string, role: string) => Promise<{ pending?: boolean } | undefined>;
  inviteUser: (email: string) => Promise<unknown>;
  // Cosmetic gate only: the invite endpoint enforces admin server-side.
  isAdmin: boolean;
}

const errorField = (err: unknown, key: 'code' | 'status'): unknown =>
  typeof err === 'object' && err !== null && key in err ? (err as Record<string, unknown>)[key] : undefined;

const errorMessage = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

const added = (email: string, result: { pending?: boolean } | undefined, invited: boolean): MemberAddOutcome =>
  result?.pending ? { email, status: 'pending', invited } : { email, status: 'added' };

export async function addMemberWithInvite(deps: MemberInviteDeps, email: string, role: string): Promise<MemberAddOutcome> {
  try {
    return added(email, await deps.addMember(email, role), false);
  } catch (err) {
    if (errorField(err, 'code') !== 'INVITATION_REQUIRED') {
      return { email, status: 'failed', invited: false, message: errorMessage(err, 'Failed to add member') };
    }
    if (!deps.isAdmin) return { email, status: 'needs-admin' };
  }
  // Cognito usernames are created lowercased by the invite handler; retry with
  // the same form so the backend's AdminGetUser lookup finds the new user.
  const inviteEmail = email.trim().toLowerCase();
  let invited = false;
  try {
    await deps.inviteUser(inviteEmail);
    invited = true;
  } catch (err) {
    // 409: a Cognito user appeared since the first attempt; adding can proceed.
    if (errorField(err, 'status') !== 409) {
      return { email: inviteEmail, status: 'failed', invited: false, message: errorMessage(err, 'Failed to send invitation') };
    }
  }
  try {
    return added(inviteEmail, await deps.addMember(inviteEmail, role), invited);
  } catch (err) {
    return { email: inviteEmail, status: 'failed', invited, message: errorMessage(err, 'Failed to add member') };
  }
}

// What the account detail page shows for one outcome. `refresh` is applied
// before the messages because fetchAll clears the error banner on entry; a
// failure after a sent invite must stay visible so the admin can retry.
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
    case 'needs-admin':
      return { refresh: false, error: `${o.email}님은 아직 가입하지 않았습니다. 관리자에게 사용자 초대를 요청한 뒤 다시 추가하세요.` };
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
  const needsAdmin = outcomes.filter((o) => o.status === 'needs-admin').map((o) => o.email);
  const failed = outcomes.filter((o): o is Extract<MemberAddOutcome, { status: 'failed' }> => o.status === 'failed');
  const notice: string[] = [];
  if (invited.length) notice.push(`초대 메일 발송을 요청했습니다: ${invited.join(', ')}. 로그인과 이메일 인증 후 계정에 자동 추가됩니다.`);
  if (queued.length) notice.push(`가입 대기 중인 사용자로 추가를 예약했습니다: ${queued.join(', ')}.`);
  const error: string[] = [];
  if (needsAdmin.length) error.push(`가입하지 않은 사용자라 추가하지 못했습니다: ${needsAdmin.join(', ')}. 관리자에게 초대를 요청한 뒤 계정 화면에서 다시 추가하세요.`);
  for (const f of failed) {
    error.push(f.invited
      ? `${f.email}: 초대 메일 발송은 요청했지만 계정 추가에 실패했습니다 (${f.message}). 계정 화면에서 다시 추가하세요.`
      : `${f.email}: ${f.message}`);
  }
  return { notice: notice.join(' '), error: error.join(' ') };
}
