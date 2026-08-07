# ADR-029: 개인 문서의 사용자 단위 공유 — 복제가 아닌 참조, 읽기 전용

- Status: 승인됨 (Accepted)
- Date: 2026-08-04

## Context

Documents(개인 문서)는 지금까지 두 가지 공유 경로만 있었다:

1. **Account 팀 공유** (`POST /api/documents/{docId}/share-account`) — S3 객체를
   새 키로 `CopyObject`하고 별도 `AccountDocumentDTO`를 만드는 **복제**.
2. **무인증 공개 링크** (ADR-022) — 토큰을 아는 누구나.

빠진 것은 "팀 전체가 아니라 **특정 한 사람**에게 주고 싶다"는 가장 흔한 요구다.
미팅 노트와 리서치는 이미 사용자 단위 공유가 있고, 사용자의 요구는 명시적으로
"미팅 노트 공유와 똑같은 방식"이었다.

두 축을 결정해야 했다: (a) 복제 vs 참조, (b) 수정 권한 부여 여부.

## Decision

**참조 방식 + 읽기 전용.**

- 원본은 소유자 파티션에 한 부만 존재한다. 공유는 양방향 인덱스 두 행
  (수신자 쪽 + 문서 쪽)을 하나의 `TransactWriteItems`로 쓰는 것뿐이다.
  수신자의 조회는 소유자 파티션을 **읽어서** 응답한다 — 소유자가 고치면
  수신자도 즉시 최신 내용을 본다. 철회는 행 삭제 한 번으로 끝난다.
- 읽기 전용을 **세 계층**에서 강제한다: 리포지토리가 `Permission`을
  `PermissionRead`로 하드코딩하고, `ShareDocumentRequest`에 `permission` 필드가
  존재하지 않으며, 프론트엔드 `ShareButton`은 `readOnly` prop으로 view/edit
  토글 자체를 숨긴다. 편집·삭제·재공유·공개 링크 발급은 전부 소유자 전용.
- 소유자 검증은 별도 권한 체크가 아니라 **파티션 격리**로 표현한다:
  `getDoc(ctx, "USER#"+ownerID, docID)`가 소유자가 아닌 호출자에게
  `ErrNotFound`를 돌려준다 → 404. 존재 여부를 누설하는 403이 나올 경로가 없다.
- 수신자 상세 응답에서 `PublicShareToken`은 생략한다.
- 응답의 `sharedBy`(소유자 이메일)가 프론트엔드의 "내 문서가 아니라 읽기 전용"
  마커 역할을 겸한다.

### 전용 DynamoDB 접두어 (`SHAREDDOC#`)를 쓴 이유 — 이게 이 ADR의 핵심

리서치 공유처럼 기존 `SHARED#` 접두어를 재사용하는 게 자연스러워 보였지만
**실제 버그**였다. `ListSharesForUser` / `listSharesForUserPaginated`는
`begins_with(SK, "SHARED#")`만으로 행을 고르고 `EntityType` 조건이 없다.
리서치가 무사한 건 `ResearchService.ListResearch`가 사후에
`share.EntityType == "RESEARCH_SHARE"`로 필터링하기 때문이다. 그런데
`ListMeetings`의 `Tab == "shared"` 경로는 그 공유 행들을 **아무 EntityType
필터 없이** `BatchGetMeetings`로 넘긴다(해석 안 되는 ID만 조용히 건너뜀).
문서 공유를 `SHARED#`에 넣으면 공유 미팅 목록의 페이지 예산을 잡아먹고 커서
페이지네이션을 오염시킨다.

그래서 `SHAREDDOC#`(수신자 쪽) / `DOCSHARE_TO#`(문서 쪽) /
`EntityType=DOC_SHARE`를 새로 쓴다. `#` 때문에 `SHAREDDOC#`과 `SHARED#`는
`begins_with` 상 서로 겹치지 않는다.

## Considered Alternatives

- **복제 방식** — Share to Account와 대칭이라 코드 재사용이 쉽고 소유자 삭제에
  영향받지 않는다. 기각: 수신자가 낡은 사본을 보게 되고, 철회 개념이 성립하지
  않으며(이미 준 사본은 회수 불가), S3 저장 비용이 수신자 수만큼 늘어난다.
  사용자가 원한 건 "공유"지 "배포"가 아니다.
- **수정 권한 옵션 제공** — 미팅 공유에는 `read`/`edit`가 있다. 기각: 문서는
  단일 소유자 편집 모델이고 동시 편집 병합 전략이 없다. 이후 필요해지면
  `permission` 필드를 추가하는 확장은 열려 있다(하드코딩 한 줄 + 요청 필드).
- **`SHARED#` 접두어 재사용** — 위 Context 참조. 기각 이유가 곧 이 ADR의
  기술적 실체다.
- **소유자 문서 삭제 시 공유 행 캐스케이드 삭제** — 기각: 수신자 수에 비례한
  트랜잭션이 필요하고(`TransactWriteItems` 100개 상한), 목록 조회에서 한 줄로
  건너뛰는 것으로 충분하다.

## Consequences

**좋은 점**
- 철회가 진짜 철회다. 저장 중복 없음. 수신자는 항상 최신 내용.
- 읽기 전용이 3중으로 강제되어, 프론트엔드 UI를 우회해도 백엔드가 막는다.
- 404-only 오류 표면 — 문서 존재 여부가 새지 않는다.
- 공유 미팅 목록 페이지네이션에 영향 0.

**나쁜 점 / 남는 리스크**
- **고아 공유 행**: 소유자가 문서를 삭제하면 `SHAREDDOC#` 행이 남는다.
  목록에서 건너뛰므로 사용자에게 보이진 않지만 계속 누적된다. 대량이 되면
  주기적 정리 job이 필요하다(현재 미구현, 의도적).
- 수신자 목록 조회가 공유 건수만큼 소유자 파티션 `GetItem`을 반복한다
  (N+1). 개인 문서 공유 건수는 작다고 보고 배치화하지 않았다 — 눈에 띄면
  `BatchGetItem`으로 전환.
- Account 공유는 복제, 사용자 공유는 참조 — 같은 화면에 의미가 다른 두
  "공유"가 공존한다. UI 문구로만 구분된다.
