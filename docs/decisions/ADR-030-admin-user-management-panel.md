# ADR-030: 관리자 사용자 관리 패널 — 로그인 추적, 3-상태 휴면, 세션 강제 만료

- Status: 승인됨 (Accepted)
- Date: 2026-08-18

## Context

Cognito self sign-up이 금지된(사내 정책) 초대 전용 서비스에서, 관리자에게 주어진
운영 수단은 `POST /api/settings/invite-user` 하나뿐이었다. 초대 이후의 운영 —
가입자 목록 확인, 첫 로그인을 안 한 사용자 식별, 초대 메일 재발송, 비밀번호
분실 대응, 미사용 계정 정리, 실사용자 여부 판단 — 은 AWS 콘솔/CLI 없이 불가능했다.

설정 페이지에 관리자 전용 사용자 관리 섹션(`GET/DELETE/PUT/POST
/api/settings/users*`)을 추가하면서 세 가지가 결정적 설계 지점이었다:
Cognito가 로그인 시각을 기본 제공하지 않는다는 점, `AdminResetUserPassword`가
호출 즉시 사용자를 잠글 수 있다는 점, `AdminDisableUser`가 이미 발급된 JWT를
무효화하지 않는다는 점.

## Decision

### 1. 로그인 추적: PostAuthentication 트리거 + 별도 DynamoDB 항목

Cognito `PostAuthentication` 트리거(`infra/lambda/post-authentication`)가
`USER#{sub}/LOGIN` 항목에 `lastLoginAt`을 기록한다.

**`USER#{sub}/PROFILE`이 아니라 별도 항목을 쓴 이유**: `GetOrCreateUser`
(`repository/dynamodb.go`)는 `GetItem` 결과가 존재하면 즉시 반환하며 email/
GSI2PK/GSI2SK를 채우지 않는다. 트리거가 사용자의 첫 로그인 시점에 `PROFILE`을
먼저 `UpdateItem`하면 스텁 항목이 영구히 남아 그 사용자는 이메일 기반 검색/공유에서
평생 빠진다. 별도 항목(`SK: LOGIN`, GSI 키 없음)은 이 위험을 스키마 결합 없이
구조적으로 제거한다.

**fail-open 설계가 최우선 제약이었다**: PostAuthentication 트리거가 에러를
던지거나 타임아웃되면 Cognito가 인증 자체를 실패시킨다 — 이 Lambda의 버그나
DynamoDB 스로틀이 전체 사용자의 로그인 차단으로 직결된다. 대응:
- 전체를 단일 `try/catch`로 감싸고 `return event`를 catch 밖 마지막 단일
  출구로 둔다.
- DynamoDB 호출을 1.5초 `AbortController`로 감싼다 — Cognito의 트리거 예산은
  약 5초로 연장 불가이므로 그 안쪽에서 확실히 반환해야 한다.
- `reservedConcurrentExecutions`를 설정하지 않는다 — 동시성 제한은 로그인
  급증을 스로틀 → 로그인 차단으로 바꾼다.
- `DISABLED=1` 환경변수 킬 스위치로 재배포 없이 즉시 무력화 가능하게 한다.

**알려진 한계**: 리프레시 토큰 흐름에서는 트리거가 발화하지 않는다. 장기
리프레시 토큰으로 계속 사용 중인 사용자가 "기록 없음"으로 보일 수 있다 —
휴면 판정은 "재인증 안 함" 기준이며 "제품 미사용" 기준이 아니다.

### 2. 휴면 판정은 3-상태다 (2-상태 "있음/없음"이면 안 됨)

1. `lastLoginAt` 있고 90일 초과 → **휴면**
2. `lastLoginAt` 없고 `FORCE_CHANGE_PASSWORD` → **초대 대기** (휴면 아님)
3. `lastLoginAt` 없고 `CONFIRMED` → **기록 없음** (휴면 아님)

3번이 결정적이다. 이 기능 배포 시점에 기존 사용자 전원이 `lastLoginAt`을
갖지 않으므로, "없으면 휴면"이라는 단순 규칙은 패널 전체를 휴면으로 칠해버린다.
결측값을 90일 비교에 넣지 않는 것으로 이를 피한다.

### 3. 비밀번호 처리는 상태별로 분기하고, 찾기 UI를 함께 구현했다

- `FORCE_CHANGE_PASSWORD`(초대 후 첫 로그인 전) → "초대 메일 재발송"
  (`AdminCreateUser` + `MessageAction=RESEND`, `Username`은 반드시 sub —
  이메일 alias로는 기존 사용자 대상 작업이 통하지 않는다)
- `CONFIRMED`(가입 완료) → "비밀번호 강제 재설정" (`AdminResetUserPassword`)

**탐색 중 발견한 선행 결함**: `frontend/src/lib/auth.ts`의 `forgotPassword`/
`confirmForgotPassword`는 구현되어 있었으나 이를 호출하는 컴포넌트가 전혀
없었다(`app/page.tsx`가 `<LoginForm />`을 `onForgotPassword` 없이 렌더링).
`AdminResetUserPassword`는 사용자에게 인증 코드 메일을 보내지만, 그 코드를
입력할 화면이 없으면 사용자는 오히려 로그인 불가 상태로 잠긴다. 따라서
`ForgotPasswordForm.tsx`(로그인 화면의 "비밀번호를 잊으셨나요?" 모달)를 이
기능의 선행 요건으로 함께 구현했다 — 자력 복구 경로와 관리자 강제 재설정
경로가 동일한 UI를 공유한다.

각 액션 직전에 `AdminGetUser`로 현재 상태를 다시 조회해, 상태가 안 맞으면
(`FORCE_CHANGE_PASSWORD`에 강제 재설정, `CONFIRMED`에 초대 재발송) 400으로
거부한다 — UI가 넘긴 상태를 신뢰하지 않는다.

### 4. 비활성화/삭제는 세션을 강제로 끊는다

`AdminDisableUser`는 신규 로그인만 막고, 이미 발급된 access/ID 토큰은 API가
로컬 검증만 하므로(Cognito 재확인 없음) 만료 시각(기본 60분)까지 그대로
유효하다 — 실제 보안 공백이다. 두 액션 모두 `AdminUserGlobalSignOut`을
동반한다. 삭제는 **삭제 전에** 사인아웃해야 한다(삭제된 사용자는 사인아웃
대상이 될 수 없다). 사인아웃 실패는 비치명적으로 처리한다 — 주 작업(비활성화/
삭제)이 이미 성공했으므로 500이 아니라 200 + 경고 필드로 응답한다.

### 5. 안전장치와 그 한계

본인 계정, 그리고 `admins` 그룹의 마지막 1인에 대한 삭제/비활성화를 400으로
거부한다. 판별은 매 요청마다 `ListUsersInGroup`을 다시 조회해서 하므로 두
관리자가 동시에 서로를 제거하는 극히 좁은(수 ms) TOCTOU 창이 이론상 존재한다.
DynamoDB 락 항목 같은 진짜 해결책은 관리자가 1~2명뿐인 현 규모에 과함 —
대신 액션 완료 후 그룹을 재조회해 비어 있으면 경고를 얹는 사후 감지만
둔다. 관리자 0명 풀은 영구 불능이 아니라 `aws cognito-idp
admin-add-user-to-group`으로 복구 가능하다.

### 6. 삭제는 Cognito 계정만 지운다

DynamoDB의 회의/문서 데이터는 보존한다. 다만 삭제 시 프로필 항목의
`GSI2PK`/`GSI2SK`(이메일 검색 인덱스)는 떼어낸다 — 그대로 두면 같은 이메일을
나중에 재초대할 때 새 sub의 `PROFILE`과 옛 sub의 `PROFILE`이 같은
`GSI2PK`를 갖게 되어 `GetUserByEmail`이 죽은 사용자로 비결정적으로
해석될 수 있다. 항목 자체는 지우지 않으므로 기존 회의/문서와의 연결은
유지된다.

## Considered Alternatives

- **로그인 추적을 API 미들웨어에서 하기** — 새 Lambda 트리거 없이 기존
  `middleware.Auth`가 인증된 요청마다 비동기로 기록. blast radius가 0이라는
  장점이 있지만, "마지막 API 호출"이 "로그인"보다 부정확한 신호다. 트리거의
  fail-open 위험을 상쇄할 만큼 얻는 게 없다고 보고 트리거를 채택.
- **비밀번호 재설정 시 이메일 자동 안내를 이번 범위에 포함** — 기각(범위 초과):
  `AdminResetUserPassword` 자체가 이미 Cognito를 통해 코드 메일을 보내므로,
  필요했던 건 그 코드를 받는 화면(`ForgotPasswordForm`)뿐이었다.
- **삭제 시 DynamoDB 데이터도 함께 정리** — 기각: 관리자 실수 한 번으로
  회의/문서가 영구 소실되는 위험이 "Cognito 계정만 삭제"보다 크다고 판단.
- **에러 코드 신설(`CONFLICT` 등) + 프론트 코드 전파** — 기각(범위 초과):
  기존 관례가 409도 `BAD_REQUEST`로 재사용하고 프론트가 `message`만
  읽으므로, 이번 6개 엔드포인트만을 위해 그 관례를 깨지 않았다. 후속 정리
  후보로 남김.

## Consequences

**좋은 점**
- 로그인 추적이 인증 흐름과 완전히 분리된 항목에 쓰여 기존 사용자 조회/검색
  경로를 손댈 위험이 0이다.
- Cognito 호출이 SDK-shape 인터페이스(`cognitoAdminAPI`) 뒤에 있어
  `InviteUser`/`SearchUsers`를 포함해 처음으로 실제 AWS 계정 없이 단위
  테스트가 가능해졌다.
- 비활성화/삭제가 즉시 세션을 끊어, "비활성화했는데 최대 1시간 더 쓸 수
  있다"는 잠재 보안 공백을 닫았다.

**나쁜 점 / 남는 리스크**
- 리프레시 토큰으로만 계속 로그인하는 사용자는 `lastLoginAt`이 갱신되지
  않아 "기록 없음"으로 보일 수 있다 — 완전한 사용 시각이 아니라 재인증
  시각이라는 점을 UI/문서에 명시했다.
- TOCTOU 창은 이론상 남아 있다(사후 감지로만 완화).
- 계정 삭제 후 같은 이메일 재초대 시 옛 `PROFILE` 항목이 고아로 남는다
  (GSI만 떼어냄) — 대량이 되면 정리 필요.
- 레거시 `CognitoListUsers` IAM 문의 `Resource: '*'`는 이번에 손대지 않음 —
  "신규 무조건 와일드카드 금지" 방침과 계속 상충한 채로 남는다.
