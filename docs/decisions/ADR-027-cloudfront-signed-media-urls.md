# ADR-027: CloudFront 서명 URL로 다운로드 도메인 통일 (S3 버킷 주소 은닉)

- Status: Accepted
- Date: 2026-07-24
- Related: ADR-022 (공개 공유 링크), CLAUDE.md Security Policy

## Context

브라우저에 내려주는 모든 다운로드 URL(문서 `downloadUrl`/`previewUrl`, 미팅
`audioUrl`, 첨부 `url`, 그리고 비인증 공개 공유의 302 Location)이 raw S3
presigned URL이라 `ttobak-assets-{account}.s3....amazonaws.com` 버킷 주소가
그대로 노출됐다. 특히 고객에게 전달되는 공개 공유 링크에서 S3 주소가 보이는
것이 문제였다.

## Decision

데이터 버킷 GET을 기존 CloudFront 배포의 `/media/*` behavior로 라우팅하고,
S3 presign 대신 **CloudFront 서명 URL**(trusted key group, canned policy)을
발급한다. URL 형태: `https://{domain}/media/{s3Key}?Expires=...&Signature=...&Key-Pair-Id=...`

### 왜 서명 URL인가 (vs signed cookies / Lambda@Edge JWT)

- 기존 S3 presign과 동일한 **capability-URL 모델** 유지: URL을 가진 사람은
  만료까지 접근 가능. 비인증 공개 공유(ADR-022)의 5분 TTL 철회 시맨틱이
  그대로 보존된다 (`PublicShareURLTTL`은 변경 없음).
- `<img>`/`<audio>`/`<iframe>`/302 redirect 등 헤더를 못 싣는 소비처와 호환.
- Signed cookies는 TTL이 경로별로 다른 현재 구조(1시간 기본 + 5분 공개 공유)와
  맞지 않고, 302로 외부인에게 전달되는 공개 공유에 쓸 수 없다.

### 구현 구조

- **Viewer 인증**: `cloudfront.PublicKey` + `KeyGroup`, `/media/*` behavior의
  `trustedKeyGroups` (`infra/lib/frontend-stack.ts`). **Origin 인증**: OAC —
  둘은 각각 viewer/origin 레이어라 공존한다.
- **prefix 충돌 회피**: 버킷 키(`docs/...`)가 SPA 라우트(`/docs/{id}`)와
  루트에서 충돌하므로 전용 `/media/*` prefix를 쓰고, 전용 CloudFront
  Function(`ttobak-media-prefix-strip`)이 origin으로 가기 전 `/media`를 벗긴다.
  SPA router 함수와는 별개 behavior라 서로 간섭 없음.
- **순환 의존 차단 2건**:
  - FrontendStack은 데이터 버킷을 `Bucket.fromBucketName`(결정적 이름
    `ttobak-assets-{account}`)으로 import — StorageStack↔FrontendStack 참조
    없음. 대신 OAC read 정책은 버킷 소유자인 StorageStack이 직접 부착
    (`AWS:SourceArn: distribution/*` 와일드카드 — 자기 계정 한정. 배포 순서
    7단계에서 정확한 distribution ID로 조여야 하는 필수 단계, 선택 사항
    아님 — 아래 "배포 순서" 참조).
  - key-pair-id는 배포 후에만 알 수 있으므로 FrontendStack이 고정 이름 SSM
    파라미터 `/ttobak/cloudfront/key-pair-id`로 발행하고, api Lambda가
    런타임(cold start)에 이름으로 읽는다 — 어느 스택도 FrontendStack을
    참조하지 않는다.
- **키 관리**: RSA 키쌍은 out-of-band로 1회 생성(ttobak-agentcore-research-role과
  같은 수동 리소스 패턴). 공개키 PEM만 리포에 커밋
  (`infra/lib/cloudfront-signing-pub.pem`), 개인키는 SecureString
  `/ttobak/cloudfront/signing-key`에만 존재 (기본 `aws/ssm` KMS 키 —
  추가 `kms:Decrypt` 불필요).
- **백엔드**: 모든 GET presign이 단일 choke point
  `UploadService.GeneratePresignedDownloadURLWithTTL`을 지나므로, 그 내부만
  교체 (`cfSigner != nil`이면 CloudFront 서명, 아니면 기존 S3 presign).
  핸들러/시그니처 변경 없음. 서명기는 `backend/internal/service/cfsign.go`
  (`feature/cloudfront/sign` SDK), `cmd/api/main.go`에서 `MEDIA_BASE_URL`
  env가 있을 때만 SSM 2회 조회 후 주입.
- **Fallback**: SSM 파라미터 부재/실패 시 경고 로그 후 S3 presign으로 폴백 —
  로컬 개발과 FrontendStack보다 먼저 배포된 Lambda가 깨지지 않는다. Cold
  start 실패는 영구 고정이 아니다 — `UploadService`가 재시도 콜백을 들고
  있어, 이후 요청에서 워밍된 인스턴스가 최대 5분(`cfSignerRetryInterval`)
  간격으로 SSM을 다시 조회한다. FrontendStack 배포가 api Lambda 배포보다
  늦게 끝나는 CI 순서에서도, 다음 key-pair-id 발행 이후 몇 분 내에 같은
  워밍 인스턴스가 CloudFront 서명으로 자동 전환된다 (재기동 불필요).
- **Origin 경계(동일 origin 위험)**: 다운로드가 앱과 같은 origin
  (`https://{domain}/media/...`)이 되므로, 업로드 콘텐츠(사용자가 임의
  `Content-Type`을 지정할 수 있는 `docs/`/`files/` 등)가 stored-XSS로
  Cognito 토큰(`localStorage`)을 노릴 수 있는 경로가 이론상 열린다. `/media/*`
  behavior에 `ResponseHeadersPolicy`(`MediaResponseHeadersPolicy`:
  `X-Content-Type-Options: nosniff` + `Content-Security-Policy: sandbox`)를
  부착해 완화 — `sandbox`는 스크립트 실행/폼 제출/팝업을 막아 오디오·이미지
  인라인 렌더링은 그대로 유지하면서도, `Content-Disposition: attachment`
  강제보다 기존 기능을 덜 깨는 선택이다. **단, `sandbox`는 iframe으로
  렌더링되는 문서에서 브라우저 내장 PDF 뷰어를 비활성화시키는 것으로 알려진
  동작이라 `docs-pdf/*`의 `previewUrl` iframe 미리보기를 깨뜨릴 수 있다** —
  그래서 `docs-pdf/*`는 `/media/*`보다 먼저 매치되는(CloudFront는 삽입
  순서로 first-match) 별도의 더 구체적인 behavior `/media/docs-pdf/*`로
  분리해 `DocsPdfResponseHeadersPolicy`(`nosniff`만, `sandbox` 없음)를 쓴다.
  `docs-pdf/*`는 convert-doc(LibreOffice, ADR-022)만 쓰는 경로라 클라이언트가
  임의 `Content-Type`을 지정할 수 있는 위험이 원래부터 없으므로, `sandbox`를
  빼도 이 완화책의 목적을 훼손하지 않는다. 이 두 behavior의 삽입 순서와
  분리 자체가 load-bearing 불변식이므로 `infra/test/frontend-stack.test.ts`가
  순서와 각 behavior가 서로 다른 `ResponseHeadersPolicy`를 참조하는지를
  직접 assert한다 — "중복"으로 보고 지우면 PDF 미리보기가 조용히 깨진다.
- **프론트엔드 변경 없음** (원래 결정): URL은 불투명하게 소비되며, 같은
  origin이 되면서 다운로드 경로의 CORS 의존도 사라진다 (버킷 CORS는 업로드
  PUT용으로 유지). **개정 (CSP 헤더 수정 시)**: 이 결정은 절반만 유지된다 —
  `/media/*`의 `docs/*` 하위(직접 업로드된 PDF, `docs-pdf/*` 변환 사이드카가
  아닌 원본)는 sandbox CSP를 받으며, sandbox는 iframe으로 렌더링되는 문서의
  브라우저 내장 PDF 뷰어를 비활성화시키는 것으로 알려진 동작이다.
  `DocDetailClient.tsx`가 직접 업로드 PDF의 iframe 미리보기를 제거하고
  "다운로드 버튼을 이용해주세요" 안내로 대체한 것은 이 CSP 완화책의 직접적인
  결과다 — ADR-022가 전제한 "PDF도 PPTX 변환본과 동등하게 미리보기된다"는
  가정이 더 이상 성립하지 않는다(변환 사이드카는 `docs-pdf/*`라 영향받지
  않음). `docs-pdf/*`를 쓰지 않는 프론트엔드 코드에는 여전히 영향이 없다.

## Scope 제외 (의도적)

- **업로드(PUT) presign은 그대로 raw S3 도메인** — 백그라운드 XHR에서만
  쓰여 고객에게 노출되지 않음 (사용자 확인 완료). KB 버킷 업로드도 동일.
- **키 로테이션은 문서화만**: KeyGroup은 키 2개를 담을 수 있으므로, 새 키
  추가 → SSM 값 교체 → Lambda 재기동 → 옛 키 제거 순으로 무중단 로테이션
  가능. 자동화는 만들지 않음.

## Consequences

- 다운로드 URL이 전부 `https://{domain}/media/...`로 통일 — 버킷 주소 비노출.
- `/media/*`는 CACHING_DISABLED (사용자별 객체 + 요청별 서명 파라미터라 캐시
  가치 없음). Range 요청(오디오 seek)은 그대로 통과.
- 데이터 버킷에 `cloudfront.amazonaws.com` service principal의 GetObject
  정책이 추가됨 — `AWS:SourceArn` 조건으로 자기 계정 CloudFront 한정이며,
  버킷 BLOCK_ALL public access는 변경 없음 (Security Policy 준수: 공개
  트래픽은 여전히 CloudFront 경유만 가능). 리소스는 `/media/*`가 실제로
  서빙하는 프리픽스(`audio/`, `images/`, `files/`, `docs/`, `docs-pdf/`)로
  한정 — `transcripts/*`(내부 STT 파이프라인 산출물, `{userId}` 세그먼트
  없음, 다운로드 URL로 노출된 적 없음)는 제외해 같은 계정의 다른
  distribution이 읽을 수 있는 표면을 최소화한다.
- 새 Go 의존성: `aws-sdk-go-v2/feature/cloudfront/sign`, `service/ssm`.

## 배포 순서 (per-stack `--exclusively`)

1. Out-of-band: 키쌍 생성, `/ttobak/cloudfront/signing-key` SecureString 등록.
2. `TtobakStorageStack` (버킷 정책 — `AWS:SourceArn`은 배포 시점에
   `MediaDistributionIdLookupFn` 커스텀 리소스가 SSM 파라미터
   `/ttobak/cloudfront/media-distribution-id`를 조회해 결정. FrontendStack이
   아직 없어 그 파라미터가 존재하지 않는 이 시점에는 `ParameterNotFound`를
   Lambda가 직접 잡아 같은 계정 와일드카드로 폴백 — **단, 이 폴백은 래칫이다**:
   Lambda는 실 ID를 볼 때마다 그 값을 자신이 소유한 별도 상태 파라미터
   `/ttobak/cloudfront/media-distribution-id-last-known-good`에도 함께
   기록하고, 원본 파라미터가 없을 때는 와일드카드보다 먼저 이 상태
   파라미터를 확인한다. 즉 정책이 한 번 정확한 ID로 조여진 뒤에는 원본
   파라미터가 나중에 삭제/개명되더라도 마지막으로 확인된 실 ID를 계속
   사용하며, 실제로 한 번도 실 ID를 본 적 없는 최초 배포에서만 와일드카드가
   쓰인다 — "조여진 정책이 조용히 다시 열리는" 경로를 코드 레벨에서 막는다) →
3. `TtobakAiStack` (apiRole SSM 권한) →
4. `TtobakGatewayStack` (`MEDIA_BASE_URL` env) → 5. `TtobakFrontendStack`
(KeyGroup/behavior/key-pair-id 파라미터 + 신규 `media-distribution-id`
파라미터 발행) → 6. api Lambda 빌드·배포.
Lambda가 5보다 먼저 배포되어도 폴백 덕에 무해.
7. **(자동, `deploy-infra.yml`의 마지막 스텝)** `TtobakStorageStack`을
   `TtobakFrontendStack` 바로 다음, **같은 워크플로우 실행 안에서** 한 번 더
   배포한다. `deploy-infra.yml`은 각 스택을 딱 한 번씩만 배포하므로, 이
   재배포 스텝이 없으면 Storage가 Frontend보다 먼저(항목 2) 배포된 뒤로는
   다시 배포될 계기가 없어 와일드카드가 다음에 우연히 Storage를 건드리는
   push까지 무기한 열려 있게 된다. 이 마지막 재배포에서는 5에서 발행된
   `media-distribution-id` 파라미터가 같은 실행 안에 이미 존재하므로
   `MediaDistributionIdLookupFn`이 그 실제 distribution ID를 즉시 읽어와
   버킷 정책의 `AWS:SourceArn`을 정확한 ID로 조인다 — `cdk.json` 편집도
   `aws s3api put-bucket-policy` 수동 실행도 필요 없다. 이 커스텀 리소스는
   `Timestamp` 프로퍼티를 매 synth마다 바꿔 CloudFormation이 매 배포마다
   실제 Update를 강제하므로("no-op"으로 건너뛰지 않음 — `--exclusively`가
   보통 diff 없으면 no-op이라는 일반 규칙의 예외), 한 번 조여진 뒤에도 계속
   최신 값을 유지한다. 워크플로우 앞 단계(예: FrontendStack 배포)가 실패해
   이 마지막 스텝까지 도달하지 못하는 경우에만 와일드카드가 그 실행 동안
   유지되며 — 이 PR 이전에는 아예 닫히는 경로가 없었으므로 여전히 순개선이다
   — 이는 `infra/test/storage-stack.test.ts`가 SourceArn이 리터럴
   와일드카드가 아니라 이 커스텀 리소스의 `Fn::GetAtt` 참조임을 assert해
   회귀를 막는다.
