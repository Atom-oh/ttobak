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
    (`AWS:SourceArn: distribution/*` 와일드카드 — 자기 계정 한정. 배포 후
    정확한 distribution ID로 조일 수 있음).
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
  로컬 개발과 FrontendStack보다 먼저 배포된 Lambda가 깨지지 않는다.
- **프론트엔드 변경 없음**: URL은 불투명하게 소비되며, 같은 origin이 되면서
  다운로드 경로의 CORS 의존도 사라진다 (버킷 CORS는 업로드 PUT용으로 유지).

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
  트래픽은 여전히 CloudFront 경유만 가능).
- 새 Go 의존성: `aws-sdk-go-v2/feature/cloudfront/sign`, `service/ssm`.

## 배포 순서 (per-stack `--exclusively`)

1. Out-of-band: 키쌍 생성, `/ttobak/cloudfront/signing-key` SecureString 등록.
2. `TtobakStorageStack` (버킷 정책) → 3. `TtobakAiStack` (apiRole SSM 권한) →
4. `TtobakGatewayStack` (`MEDIA_BASE_URL` env) → 5. `TtobakFrontendStack`
(KeyGroup/behavior/key-pair-id 파라미터) → 6. api Lambda 빌드·배포.
Lambda가 5보다 먼저 배포되어도 폴백 덕에 무해.
