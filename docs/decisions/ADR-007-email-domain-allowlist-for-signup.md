# ADR-007: Email Domain Allowlist for Signup Restriction

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Proposed

## Context

Ttobak is currently open to any user with a valid email address. Cognito User Pool accepts signups from any domain, and there is no Pre Sign-Up Lambda trigger to filter registrations. For enterprise or team deployments, administrators need the ability to restrict signups to specific email domains (e.g., `amazon.com`, `samsung.com`) so that only authorized members of an organization can create accounts.

The restriction must support multiple domains (array), be configurable at runtime without redeployment, and provide clear error messages to users whose domain is not allowed.

## Options Considered

### Option 1: Cognito Pre Sign-Up Lambda Trigger

Add a Lambda function as a Cognito Pre Sign-Up trigger. The Lambda reads the allowed domain list from DynamoDB (using the existing `PK: CONFIG, SK: ALLOWED_DOMAINS` pattern) and rejects signups from unlisted domains.

- **Pros**: Enforcement at the identity provider level; impossible to bypass from the frontend; works for all signup methods (API, hosted UI); Cognito natively supports Pre Sign-Up triggers
- **Cons**: Adds a new Lambda function to the CDK stack; cold start adds ~100ms to signup latency; error messages from Cognito triggers are less customizable (generic "PreSignUp failed" unless carefully formatted)

### Option 2: Backend API Validation Only

Add domain validation in the frontend `signUp` function and/or a backend pre-check endpoint (`POST /api/auth/validate-email`). The backend reads allowed domains from DynamoDB and rejects before calling Cognito.

- **Pros**: Full control over error messages; no new Lambda; can show domain suggestions in the UI
- **Cons**: Not enforced at the Cognito level; a direct Cognito SDK call bypasses the check; defense-in-depth violation; two places to maintain validation logic

### Option 3: Cognito Pre Sign-Up + Frontend Pre-Check (Chosen)

Combine Option 1 and a lightweight frontend pre-check. The Cognito Pre Sign-Up Lambda is the enforcement gate (cannot be bypassed). Additionally, the frontend reads the allowed domain list from a public API (`GET /api/auth/allowed-domains`) and shows a clear error before attempting signup, providing better UX.

- **Pros**: Defense-in-depth (Cognito enforces, frontend provides UX); clear error messages at both layers; allowed domains configurable via settings UI or API; supports array of domains
- **Cons**: Two validation points to maintain; adds one Lambda + one API endpoint; slightly more complexity

### Option 4: Cognito User Pool Advanced Security with Custom Attributes

Use Cognito's built-in email domain filtering via a custom attribute and admin-only signup flow.

- **Pros**: No custom Lambda needed; native Cognito feature
- **Cons**: Requires admin-created accounts (no self-service signup); does not support dynamic domain lists; poor UX for onboarding new team members

## Decision

Use Option 3: Cognito Pre Sign-Up Lambda + Frontend Pre-Check.

The Pre Sign-Up Lambda reads allowed domains from DynamoDB (`PK: CONFIG, SK: ALLOWED_DOMAINS`). If the list is empty or not set, all domains are allowed (open registration, current behavior). When domains are configured, only emails matching one of the listed domains can complete signup.

The frontend calls `GET /api/auth/allowed-domains` (public, no auth required) to display allowed domains on the signup page and validate client-side before attempting Cognito signup.

Administrators configure the domain list via `PUT /api/settings/allowed-domains` (authenticated, owner-only or admin role).

### Data Model

```
DynamoDB Item:
  PK: "CONFIG"
  SK: "ALLOWED_DOMAINS"
  domains: ["amazon.com", "samsung.com"]
  updatedAt: "2026-04-22T..."
  updatedBy: "user-id"
```

### API Endpoints

```
GET  /api/auth/allowed-domains    (public)  -> { domains: string[], enforced: boolean }
PUT  /api/settings/allowed-domains (auth)   -> { domains: string[] }
```

### Pre Sign-Up Lambda Logic

```
1. Read CONFIG#ALLOWED_DOMAINS from DynamoDB
2. If domains list is empty -> allow signup (autoConfirmUser: false)
3. Extract domain from event.request.userAttributes.email
4. If domain is in allowed list -> allow
5. Otherwise -> throw Error("이 이메일 도메인은 허용되지 않습니다")
```

## Consequences

### Positive
- Organizations can restrict access to authorized members only
- Dynamic configuration without redeployment
- Defense-in-depth: Cognito-level enforcement + frontend UX
- Backward compatible: empty domain list = open registration (no breaking change)
- Existing users are not affected (Pre Sign-Up only triggers on new registrations)

### Negative
- Adds a new Lambda function to the Auth CDK stack
- Pre Sign-Up Lambda cold start adds ~100-200ms to first signup in a window
- Admin UI for managing domains must be built (settings page addition)
- Domain list must be kept in sync between DynamoDB reads (Pre Sign-Up Lambda) and the API response; eventual consistency is acceptable since domain changes are infrequent

## References
- [AWS Cognito Pre Sign-Up Lambda Trigger](https://docs.aws.amazon.com/cognito/latest/developerguide/user-pool-lambda-pre-sign-up.html)
- Existing settings pattern: `backend/internal/handler/settings.go` (Notion integration uses similar DynamoDB CONFIG pattern)
- Existing key prefix: `model.PrefixConfig = "CONFIG"` in `backend/internal/model/meeting.go:183`

---

<a id="korean"></a>

# 한국어

## 상태
제안됨

## 배경

현재 Ttobak은 유효한 이메일 주소만 있으면 누구나 가입할 수 있습니다. Cognito User Pool은 모든 도메인의 가입을 허용하며, 가입을 필터링하는 Pre Sign-Up Lambda 트리거가 설정되어 있지 않습니다. 기업 또는 팀 배포 환경에서는 관리자가 특정 이메일 도메인(예: `amazon.com`, `samsung.com`)으로 가입을 제한하여 조직의 인가된 구성원만 계정을 생성할 수 있어야 합니다.

이 제한은 여러 도메인을 배열로 지원해야 하고, 재배포 없이 런타임에 설정 가능해야 하며, 허용되지 않는 도메인의 사용자에게 명확한 오류 메시지를 제공해야 합니다.

## 검토한 옵션

### 옵션 1: Cognito Pre Sign-Up Lambda 트리거

Cognito Pre Sign-Up 트리거로 Lambda 함수를 추가합니다. Lambda는 DynamoDB에서 허용 도메인 목록을 읽어(기존 `PK: CONFIG, SK: ALLOWED_DOMAINS` 패턴 활용) 목록에 없는 도메인의 가입을 거부합니다.

- **장점**: ID 제공자 수준에서 적용되어 프론트엔드에서 우회 불가능; 모든 가입 방식에 적용됨; Cognito가 기본 지원하는 트리거
- **단점**: CDK 스택에 새 Lambda 함수 추가 필요; 콜드 스타트로 가입 지연 ~100ms; 트리거의 오류 메시지 커스터마이징이 제한적

### 옵션 2: 백엔드 API 검증만

프론트엔드 `signUp` 함수 및/또는 백엔드 사전 검증 엔드포인트(`POST /api/auth/validate-email`)에 도메인 검증을 추가합니다. 백엔드가 DynamoDB에서 허용 도메인을 읽어 Cognito 호출 전에 거부합니다.

- **장점**: 오류 메시지 완전 제어 가능; 새 Lambda 불필요; UI에서 도메인 제안 표시 가능
- **단점**: Cognito 수준에서 적용되지 않음; 직접 Cognito SDK 호출로 우회 가능; 심층 방어 위반; 두 곳에서 검증 로직 유지 필요

### 옵션 3: Cognito Pre Sign-Up + 프론트엔드 사전 검증 (선택됨)

옵션 1과 프론트엔드 사전 검증을 결합합니다. Cognito Pre Sign-Up Lambda가 시행 게이트(우회 불가)이고, 프론트엔드는 공개 API(`GET /api/auth/allowed-domains`)에서 허용 도메인 목록을 읽어 가입 시도 전에 명확한 오류를 표시하여 더 나은 UX를 제공합니다.

- **장점**: 심층 방어(Cognito 시행 + 프론트엔드 UX); 양쪽 모두 명확한 오류 메시지; 설정 UI 또는 API로 허용 도메인 구성 가능; 도메인 배열 지원
- **단점**: 두 곳에서 검증 유지 필요; Lambda 1개 + API 엔드포인트 1개 추가; 약간의 복잡성 증가

### 옵션 4: Cognito User Pool 고급 보안과 사용자 정의 속성

Cognito의 내장 이메일 도메인 필터링을 사용자 정의 속성과 관리자 전용 가입 플로우로 활용합니다.

- **장점**: 사용자 정의 Lambda 불필요; Cognito 기본 기능
- **단점**: 관리자가 계정 생성 필요(자가 가입 불가); 동적 도메인 목록 미지원; 신규 팀원 온보딩 UX 불량

## 결정

옵션 3: Cognito Pre Sign-Up Lambda + 프론트엔드 사전 검증을 사용합니다.

Pre Sign-Up Lambda는 DynamoDB(`PK: CONFIG, SK: ALLOWED_DOMAINS`)에서 허용 도메인을 읽습니다. 목록이 비어 있거나 설정되지 않은 경우 모든 도메인을 허용합니다(개방형 가입, 현재 동작). 도메인이 설정된 경우 목록에 있는 도메인의 이메일만 가입을 완료할 수 있습니다.

프론트엔드는 `GET /api/auth/allowed-domains`(공개, 인증 불필요)를 호출하여 가입 페이지에 허용 도메인을 표시하고 Cognito 가입 시도 전에 클라이언트 측에서 검증합니다.

관리자는 `PUT /api/settings/allowed-domains`(인증 필요, 소유자 전용 또는 관리자 역할)를 통해 도메인 목록을 설정합니다.

### 데이터 모델

```
DynamoDB Item:
  PK: "CONFIG"
  SK: "ALLOWED_DOMAINS"
  domains: ["amazon.com", "samsung.com"]
  updatedAt: "2026-04-22T..."
  updatedBy: "user-id"
```

### API 엔드포인트

```
GET  /api/auth/allowed-domains    (공개)   -> { domains: string[], enforced: boolean }
PUT  /api/settings/allowed-domains (인증)  -> { domains: string[] }
```

### Pre Sign-Up Lambda 로직

```
1. DynamoDB에서 CONFIG#ALLOWED_DOMAINS 읽기
2. 도메인 목록이 비어 있으면 -> 가입 허용 (autoConfirmUser: false)
3. event.request.userAttributes.email에서 도메인 추출
4. 도메인이 허용 목록에 있으면 -> 허용
5. 그렇지 않으면 -> Error("이 이메일 도메인은 허용되지 않습니다") throw
```

## 영향

### 긍정적
- 조직이 인가된 구성원만 접근하도록 제한 가능
- 재배포 없이 동적 설정 가능
- 심층 방어: Cognito 수준 시행 + 프론트엔드 UX
- 하위 호환: 빈 도메인 목록 = 개방형 가입 (변경사항 없음)
- 기존 사용자에게 영향 없음 (Pre Sign-Up은 신규 가입에서만 트리거)

### 부정적
- Auth CDK 스택에 새 Lambda 함수 추가
- Pre Sign-Up Lambda 콜드 스타트로 첫 가입 시 ~100-200ms 지연
- 도메인 관리를 위한 관리자 UI 구축 필요 (설정 페이지 추가)
- DynamoDB 읽기(Pre Sign-Up Lambda)와 API 응답 간 도메인 목록 동기화 필요; 도메인 변경이 드물기 때문에 최종 일관성 허용

## 참고 자료
- [AWS Cognito Pre Sign-Up Lambda Trigger](https://docs.aws.amazon.com/cognito/latest/developerguide/user-pool-lambda-pre-sign-up.html)
- 기존 설정 패턴: `backend/internal/handler/settings.go` (Notion 연동에서 유사한 DynamoDB CONFIG 패턴 사용)
- 기존 키 접두사: `model.PrefixConfig = "CONFIG"` (`backend/internal/model/meeting.go:183`)
