// SA dashboard onboarding: the owner-private SFDC account team joined with the
// mapped TTOBAK Account (ADR-023). Pure helpers and copy; the panel renders them.

export type OnboardingStatus = 'member' | 'pending' | 'none' | 'unknown';
export type OnboardingCoreRole = 'AM' | 'TAM' | 'SA';
export type OnboardingSummaryStatus = 'member' | 'pending' | 'missing';

export interface OnboardingMember {
  sfdcAccountId: string;
  alias: string;
  name: string;
  title: string;
  teamMemberRole: string;
  allTeamRoles: string;
  /** Mapped TTOBAK role, empty for anyone outside AM, TAM and SA. */
  role: string;
  status: OnboardingStatus;
}

export interface OnboardingDetail {
  sfdcAccountId: string;
  name: string;
  accountId?: string;
  mapped: boolean;
  accountReadable: boolean;
  truncated: boolean;
  members: OnboardingMember[];
}

export interface OnboardingSummaryItem {
  sfdcAccountId: string;
  name: string;
  accountId: string;
  roles: Partial<Record<OnboardingCoreRole, OnboardingSummaryStatus>>;
}

/** `unavailable`: mapped Accounts whose read failed (absent from older servers). */
export interface OnboardingSummary { items: OnboardingSummaryItem[]; truncated: boolean; unavailable?: number }

export type OnboardingInviteOutcome = 'added' | 'invited' | 'already_member' | 'failed' | 'not_attempted';

export interface OnboardingInviteResult { alias: string; outcome: OnboardingInviteOutcome; code?: string }

export interface OnboardingInviteItem { alias: string; role: string }

/** Server cap per invite request. */
export const ONBOARDING_INVITE_MAX = 20;

const CORE_ROLES: OnboardingCoreRole[] = ['AM', 'TAM', 'SA'];

/** Roles a member outside AM, TAM and SA can be invited as (assignable roles). */
export const ONBOARDING_OTHER_ROLES = ['AM', 'TAM', 'SSA', 'SA', 'SA Manager', 'AM Manager'] as const;

export const ONBOARDING_COPY = {
  title: '고객 온보딩',
  description: 'SFDC 계정 팀의 AM, TAM, SA를 TTOBAK 어카운트에 초대합니다. 이 목록은 나만 볼 수 있습니다.',
  empty: '연결된 SFDC 계정의 팀 정보가 없습니다. Mac에서 SFDC 수집을 실행하면 팀이 표시됩니다.',
  truncated: '연결된 어카운트가 100개를 넘어 앞의 100개만 표시합니다.',
  unavailable: (count: number) => `어카운트 ${count}개를 불러오지 못했습니다. 잠시 후 새로고침해 주세요.`,
  inviteTeam: '팀 초대',
  createCustomer: '고객 만들기',
  creating: '만드는 중…',
  createFailed: '고객을 만들지 못했습니다. 다시 시도하세요.',
  mapFailed: '고객은 만들었지만 SFDC 계정과 연결하지 못했습니다. 목록에서 직접 연결하세요.',
  panelTitle: (name: string) => `${name} 팀 초대`,
  close: '닫기',
  showAll: '모든 팀원 보기',
  noTeam: 'SFDC 팀 정보가 없습니다. Mac에서 SFDC 수집을 실행하세요.',
  unreadable: '이 어카운트의 멤버를 볼 수 없습니다. 어카운트 멤버만 초대할 수 있습니다.',
  unmapped: 'SFDC 계정이 TTOBAK 어카운트와 연결되지 않았습니다.',
  teamTruncated: '팀원이 100명을 넘어 앞의 100명만 표시합니다.',
  invite: (count: number) => count ? `${count}명 초대` : '초대',
  inviting: '초대하는 중…',
  inviteCap: `한 번에 ${ONBOARDING_INVITE_MAX}명까지 초대합니다. 나머지는 다시 초대하세요.`,
  loadFailed: '팀 정보를 불러오지 못했습니다.',
  inviteFailed: '초대 요청을 보내지 못했습니다.',
  retry: '다시 불러오기',
  roleOther: '역할 선택',
} as const;

const STATUS_TEXT: Record<OnboardingStatus, string> = {
  member: '멤버', pending: '초대 대기', none: '미초대', unknown: '확인 불가',
};

const SUMMARY_TEXT: Record<OnboardingSummaryStatus, string> = { member: '✓', pending: '초대 대기', missing: '미초대' };

export function statusText(status: OnboardingStatus): string {
  return STATUS_TEXT[status] ?? status;
}

/** "AM ✓ · TAM 초대 대기 · SA 미초대", in AM, TAM, SA order; absent roles are omitted. */
export function summaryText(roles: OnboardingSummaryItem['roles']): string {
  return CORE_ROLES.filter(role => roles[role]).map(role => `${role} ${SUMMARY_TEXT[roles[role]!]}`).join(' · ');
}

/**
 * Core-role members not yet invited are selected by default. `sent` holds
 * aliases invited in this session: one whose private record failed still reads
 * `none`, and must not be re-selected.
 */
export function defaultSelection(members: OnboardingMember[], sent: ReadonlySet<string> = new Set()): Set<string> {
  return new Set(members.filter(member => member.status === 'none' && !sent.has(member.alias) &&
    (CORE_ROLES as string[]).includes(member.role)).map(member => member.alias));
}

/**
 * The invite request for the selected members: only status `none`, with the
 * mapped role or the role chosen for an "other" member; members without a role
 * are skipped. At most ONBOARDING_INVITE_MAX items, in member order.
 */
export function invitePlan(members: OnboardingMember[], selected: Set<string>, chosenRoles: Record<string, string> = {}):
  { items: OnboardingInviteItem[]; capped: boolean } {
  const items: OnboardingInviteItem[] = [];
  for (const member of members) {
    if (!selected.has(member.alias) || member.status !== 'none') continue;
    const role = member.role || chosenRoles[member.alias] || '';
    if (!role) continue;
    items.push({ alias: member.alias, role });
  }
  return { items: items.slice(0, ONBOARDING_INVITE_MAX), capped: items.length > ONBOARDING_INVITE_MAX };
}

const CODE_TEXT: Record<string, string> = {
  RATE_LIMITED: '초대 요청이 많습니다. 한 시간 후 다시 시도하세요',
  FORBIDDEN: '이 어카운트에 초대할 권한이 없습니다',
  BAD_REQUEST: '초대할 수 없는 사용자입니다',
  CONFLICT: '어카운트가 병합 중입니다',
  INVITATION_GRANT_FAILED: '초대를 예약하지 못했습니다. 다시 시도하세요',
};

export function inviteResultText(result: OnboardingInviteResult): string {
  switch (result.outcome) {
    case 'added': return '멤버로 추가함';
    case 'invited': return result.code ? '초대함 (기록 저장 실패: 다시 초대하지 마세요)' : '초대함';
    case 'not_attempted': return '시간이 부족해 보내지 않았습니다. 다시 초대하세요';
    case 'already_member': return '이미 멤버';
    default: return `실패: ${CODE_TEXT[result.code ?? ''] ?? '알 수 없는 오류'}`;
  }
}
