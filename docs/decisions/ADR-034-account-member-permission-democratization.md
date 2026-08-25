# ADR-034: Account 멤버 추가/역할변경 권한을 owner 전용에서 멤버 전체로 개방

- Status: 승인됨 (Accepted)
- Date: 2026-08-25

## Context

Account 설정에서 멤버 추가(`POST /api/accounts/{accountId}/members`)와 역할
변경(`PUT .../members/{userId}`)이 모두 owner 전용이었다
(`AccountService.AddMember`/`UpdateMemberRole`, `requester.Role != model.RoleOwner`
→ `ErrForbidden`). 실사용에서 owner가 자리를 비우면 팀에 새 인원을 넣거나
역할을 조정할 방법이 없어 병목이 된다는 사용자 요청으로 이 제한을 없앤다.

이 저장소에는 이미 **별개의, 더 정교한** 기능이 병합돼 있다: 이메일이 아직
로그인하지 않은 초대 상태(Cognito 계정은 있지만 DynamoDB `PROFILE` row가
없는 상태)일 때 `AddMember`/`ShareMeetingByEmail`이 즉시 실패하는 대신
`PendingShare`를 큐에 쌓고, 그 사람이 실제 로그인하는 순간
`MaterializePendingShares`가 자동으로 실제 `AccountMember`/`Share`로
전환한다(설계 스펙: `docs/superpowers/specs/2026-08-04-pending-email-invites-design.md`,
CLAUDE.md의 "PendingShare queue" 절). 이 ADR은 그 메커니즘을 건드리지 않는다
— 대상은 오직 "누가 `AddMember`/`UpdateMemberRole`을 호출할 수 있는가"라는
권한 게이트뿐이다.

## Decision

`AddMember`와 `UpdateMemberRole`의 게이트를 owner 전용에서 `requireMember`
(이 account의 멤버라면 역할 무관하게 통과, 기존에 `ListAccountMeetings` 등
읽기 경로가 쓰던 헬퍼)로 바꾼다. **`RemoveMember`와
`RevokePendingMember`(큐에 쌓인 미확정 초대 취소)는 owner 전용으로 그대로
둔다** — 추가/역할 변경은 되돌리기 쉽지만(다시 추가하거나 다시 바꾸면 그만)
삭제/취소는 접근을 회수하는 되돌리기 어려운 작업이고 `RemoveMember`는 미팅
Share cleanup까지 연쇄시킬 수 있어 비용이 비대칭적이다.

역할 허용목록(`model.AssignableRoles`, `owner` 제외)은 두 경로 모두
그대로 유지한다 — 어떤 멤버도 자신이나 남을 owner로 승격시킬 수 없다.
`AddMember`가 미등록/미로그인 이메일을 만났을 때의 동작(즉시 초대 vs
`PendingShare` 큐잉)은 이 ADR의 대상이 아니며 현재 병합된 그대로 유지된다.

## Considered Alternatives

- **매니저 역할군(SA Manager/AM Manager)만 추가 권한 부여**: 지금 Account
  모델은 owner 외 모든 역할이 읽기 권한상 완전히 동등하다(`requireMember`).
  역할별 권한 차등을 새로 도입해야 해서 이번 범위를 넘음. 기각.
- **삭제/pending-취소도 함께 개방**: 되돌릴 수 없는 작업이라는 비대칭성 때문에
  사용자가 명시적으로 "삭제는 owner만"으로 결정. `RevokePendingMember`도
  같은 논리(취소도 접근 회수의 일종)로 owner 전용 유지.

## Consequences

- Account owner가 자리를 비워도 다른 멤버가 새 인원을 추가하거나 역할을
  조정할 수 있다. 삭제와 pending 초대 취소만 owner의 승인을 거친다.
- 프런트엔드(`AccountDetailClient`)는 역할 변경 select를 "이 account의
  멤버라면 누구나" 노출하고, 삭제 버튼만 owner에게 노출한다.
- 기존 owner 전용 테스트(`TestAddMember_NonOwnerForbidden`,
  `TestUpdateMemberRole_NonOwnerForbidden`, 대응하는 handler 테스트)는
  의미가 반전돼 "멤버가 성공한다"로 교체됐고, 대신 "이 account의 멤버가
  전혀 아닌 사람은 여전히 403"이라는 새 테스트로 원래 취지를 보존한다.
