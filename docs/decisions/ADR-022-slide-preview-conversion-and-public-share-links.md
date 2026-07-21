# ADR-022: PPTX In-Browser Preview via LibreOffice Conversion, and Unauthenticated Public Slide Links

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — **supersedes ADR-020's "PPTX is download-only (no in-browser viewer)" decision**

## Context
ADR-020 (Document Hub v2) deliberately shipped PPTX as download-only, rejecting an in-browser viewer to avoid adding PDF.js or a conversion pipeline. Two follow-on product needs make that ceiling too low:

1. **In-browser preview parity with PDF.** Most decks a Solutions Architect uploads are PPTX, not PDF — download-only means the majority of uploaded slides can't be previewed at all in the doc detail page, only downloaded.
2. **Team/public sharing needs a preview surface that doesn't require downloading first.** Sharing a slide to an account (`ShareUserDocumentToAccount`) or minting a public link (`CreateUserDocPublicShare`) is pointless if the recipient's only option is "download this file from a stranger" — a preview is the whole value of a share.

This also required deciding how a public link should be exposed at the API/CloudFront layer, since it's the first genuinely unauthenticated route in this codebase (root `CLAUDE.md`'s Security Policy previously had no exception clause at all).

## Decision

### 1. PPTX preview via a LibreOffice conversion Lambda (supersedes ADR-020 §PDF viewing)
A new container-image Lambda (`backend/cmd/convert-doc`, 6th Go Lambda) runs headless LibreOffice (`soffice --headless --convert-to pdf`) to convert an uploaded PPTX/PPT to a PDF sidecar, reusing the existing native `<iframe>` PDF viewer (ADR-020's viewer decision is otherwise unchanged — still no PDF.js).

- **Trigger**: EventBridge S3 rule on the `docs/` prefix, filtered to `.ppt`/`.pptx` (mirrors the existing upload-triggered Lambda pattern, e.g. `ttobak-transcribe`).
- **Output key**: a deterministic sidecar key (`service.SidecarPDFKey`) rather than a DynamoDB write — the doc record may not exist yet when conversion finishes (the presigned PUT and the `docApi.put()` call that creates the record are two separate requests), so the read path (`UploadService.GeneratePreviewPDFURL`) just `HeadObject`s the sidecar on demand instead of assuming ordering.
- **IAM**: scoped to `docs/*` read + `docs-pdf/*` write only (`ai-stack.ts`'s `grantRead`/`grantPut` with explicit prefixes) — narrower than the bucket-wide `grantReadWrite` other upload categories (`audio`/`image`/`file`/`doc`) share, because this role additionally runs a third-party document parser (see next point).
- **Untrusted-input hardening**: LibreOffice parses attacker-controllable file content and can fetch resources a document references. The `soffice` subprocess has every `AWS_*` environment variable stripped before exec (Lambda injects temporary credentials as env vars for the Go SDK; the subprocess has no legitimate need for them) — a parser vulnerability or SSRF-style resource fetch can't use them to reach beyond `convert-doc`'s own already-narrow IAM scope.
- **Container arch**: `--platform=linux/arm64` pinned explicitly in the Dockerfile's runtime stage (in addition to CDK's `DockerImageCode.fromImageAsset` platform option), since the Dockerfile can also be built directly (e.g. by CI) without going through CDK's asset bundler.

### 2. Unauthenticated public slide links (new Security Policy exception)
`CreateUserDocPublicShare` mints a random 128-bit token (`crypto/rand`); `GET /api/public/docs/{token}` serves that one slide with no caller-identity check at all — neither the API Gateway JWT authorizer nor the Lambda@Edge JWT check that gate every other `/api/*` route.

- **CloudFront**: a `/api/public/*` behavior (`GET`/`HEAD` only, caching disabled) is registered *before* the general `/api/*` behavior — CloudFront matches path patterns in insertion order, so this bypasses the Lambda@Edge `edgeLambdas` JWT check that the `/api/*` behavior attaches. Scoped to exactly this path prefix.
- **API Gateway**: `/api/public/docs/{token}` is registered as its own literal-segment route (not part of the `/api/{proxy+}` catch-all) with no `authorizer` — HTTP API matches by path specificity, so this route wins over the catch-all regardless of registration order.
- **Application-layer gate**: `DocumentHandler.PublicGetDoc` does its own token lookup and re-checks `doc.PublicShareToken != token` before serving — fail-closed even if the infra-layer bypass were ever misconfigured to be broader than intended.
- **Revocation**: `RevokeUserDocPublicShare` clears the token immediately, but an already-issued presigned S3 URL remains valid for its 1-hour TTL. Accepted for this version; a shorter TTL dedicated to the public path is a tracked follow-up (see Consequences).
- **Scope discipline**: root `CLAUDE.md`'s Security Policy now documents this as the *one* deliberate exception and says explicitly that the `/api/public/` prefix must stay limited to this one handler — a second unauthenticated route anywhere else is the violation the policy exists to prevent, not a precedent this ADR sets.

## Consequences

### Positive
- PPTX slides get the same in-browser preview experience as PDF, without adding PDF.js.
- Sharing a slide (to a team account or via public link) now has something to actually preview instead of a bare download prompt.
- `convert-doc`'s IAM scope is narrower than the existing bucket-wide grant pattern, and its subprocess is hardened against credential exfiltration despite parsing untrusted input — a stricter bar than most of this codebase's current upload paths.

### Negative
- LibreOffice adds a multi-GB container image and cold-start cost distinct from the other 5 Go Lambdas' fast zip cold starts; acceptable since conversion is async (EventBridge-triggered, not on the request path).
- Public link revocation has up to a 1-hour tail via already-issued presigned URLs (see Decision §2). A public-route-specific short-TTL presign is a follow-up, not yet implemented.
- The public gate's only rate-limiting is 128-bit token entropy (`crypto/rand`) — no explicit CloudWatch alarm or WAF rule for anomalous request volume against `/api/public/*` yet.
- `convert-doc/main.go`'s bucket configuration now fails fast (`log.Fatal`) on a missing `BUCKET_NAME` rather than falling back to a placeholder bucket name — intentional (see the code comment), but means a misconfigured deploy fails at cold start instead of at first invocation.

## References
- [ADR-020: Document Hub v2](ADR-020-doc-hub-v2-personal-docs-wikilinks-slides.md) — the "PPTX is download-only" decision this ADR supersedes; all other ADR-020 decisions (native `<iframe>` PDF viewer, no PDF.js, account-document plumbing reuse) are unchanged.
- `backend/cmd/convert-doc/{main.go,Dockerfile}`, `infra/lib/ai-stack.ts` (`convertDocRole`), `infra/lib/gateway-stack.ts` (`convertDocFunction`, `DocSlideUploadRule`, `/api/public/docs/{token}` route), `infra/lib/frontend-stack.ts` (`/api/public/*` CloudFront behavior) — implementation.
- `backend/internal/service/account.go` (`ShareUserDocumentToAccount`, `CreateUserDocPublicShare`, `ResolvePublicShare`), `backend/internal/handler/document.go` (`PublicGetDoc`).

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — **ADR-020의 "PPTX는 다운로드만(뷰어 없음)" 결정을 대체**

## 배경
ADR-020(문서 허브 v2)은 PDF.js나 변환 파이프라인 도입을 피하기 위해 PPTX를 의도적으로 다운로드 전용으로 출시했습니다. 두 가지 후속 제품 요구사항이 이 한계를 낮게 만들었습니다:

1. **PDF와 동등한 인앱 미리보기.** AWS SA가 업로드하는 덱은 대부분 PPTX이지 PDF가 아닙니다 — 다운로드 전용이면 업로드된 슬라이드 대다수가 문서 상세 페이지에서 전혀 미리보기되지 않고 다운로드만 가능합니다.
2. **팀/공개 공유는 먼저 다운로드하지 않아도 되는 미리보기 화면이 필요합니다.** 슬라이드를 계정에 공유(`ShareUserDocumentToAccount`)하거나 공개 링크를 발급(`CreateUserDocPublicShare`)해도, 받는 사람의 유일한 선택이 "낯선 사람의 파일을 다운로드"라면 공유의 의미가 없습니다 — 미리보기가 공유의 핵심 가치입니다.

이는 또한 공개 링크를 API/CloudFront 계층에서 어떻게 노출할지 결정해야 했는데, 이 코드베이스 최초의 진짜 무인증 라우트이기 때문입니다(루트 `CLAUDE.md`의 보안 정책에는 이전까지 예외 조항이 전혀 없었습니다).

## 결정

### 1. LibreOffice 변환 Lambda를 통한 PPTX 미리보기 (ADR-020 §PDF 뷰어 대체)
신규 컨테이너 이미지 Lambda(`backend/cmd/convert-doc`, 6번째 Go Lambda)가 headless LibreOffice(`soffice --headless --convert-to pdf`)를 실행해 업로드된 PPTX/PPT를 PDF 사이드카로 변환하고, 기존 네이티브 `<iframe>` PDF 뷰어를 재사용합니다(ADR-020의 뷰어 결정 자체는 그대로 — PDF.js 여전히 미도입).

- **트리거**: `docs/` prefix에 대한 EventBridge S3 규칙, `.ppt`/`.pptx`로 필터링(기존 업로드 트리거 Lambda 패턴, 예: `ttobak-transcribe`와 동일).
- **출력 키**: DynamoDB 쓰기가 아니라 결정론적 사이드카 키(`service.SidecarPDFKey`) — 변환 완료 시점에 문서 레코드가 아직 없을 수 있어(presigned PUT과 레코드를 만드는 `docApi.put()` 호출은 별개 요청), read 경로(`UploadService.GeneratePreviewPDFURL`)는 순서를 가정하지 않고 그때그때 사이드카를 `HeadObject`합니다.
- **IAM**: `docs/*` 읽기 + `docs-pdf/*` 쓰기로만 스코프(`ai-stack.ts`의 명시적 prefix `grantRead`/`grantPut`) — 다른 업로드 카테고리(`audio`/`image`/`file`/`doc`)가 공유하는 버킷 전체 `grantReadWrite`보다 좁습니다. 이 role이 추가로 서드파티 문서 파서를 실행하기 때문입니다(다음 항목 참조).
- **신뢰 불가 입력 방어**: LibreOffice는 공격자가 통제 가능한 파일 콘텐츠를 파싱하며 문서가 참조하는 리소스를 가져올 수 있습니다. `soffice` 자식 프로세스는 exec 전에 모든 `AWS_*` 환경변수를 제거합니다(Lambda는 Go SDK를 위해 임시 자격증명을 환경변수로 주입하는데, 이 서브프로세스는 이를 쓸 정당한 이유가 없음) — 파서 취약점이나 SSRF 형태의 리소스 요청이 있어도 `convert-doc` 자체의 이미 좁은 IAM 범위를 벗어날 수 없습니다.
- **컨테이너 아키텍처**: CDK의 `DockerImageCode.fromImageAsset` platform 옵션과 별개로 Dockerfile 런타임 스테이지에 `--platform=linux/arm64`를 명시 고정 — CI 등에서 CDK 에셋 번들러를 거치지 않고 Dockerfile을 직접 빌드할 수도 있기 때문입니다.

### 2. 무인증 공개 슬라이드 링크 (신규 보안 정책 예외)
`CreateUserDocPublicShare`가 무작위 128비트 토큰(`crypto/rand`)을 발급하고, `GET /api/public/docs/{token}`이 해당 슬라이드 하나를 호출자 신원 확인 없이 제공합니다 — 다른 모든 `/api/*` 라우트를 게이트하는 API Gateway JWT authorizer도, Lambda@Edge JWT 검사도 없습니다.

- **CloudFront**: `/api/public/*` behavior(`GET`/`HEAD`만, 캐싱 비활성)가 일반 `/api/*` behavior *앞에* 등록됩니다 — CloudFront는 경로 패턴을 등록 순서대로 매칭하므로, `/api/*` behavior에 붙은 Lambda@Edge `edgeLambdas` JWT 검사를 건너뜁니다. 정확히 이 경로 prefix로만 범위가 제한됩니다.
- **API Gateway**: `/api/public/docs/{token}`은 `/api/{proxy+}` catch-all의 일부가 아니라 자체 리터럴 세그먼트 라우트로 등록되며 `authorizer`가 없습니다 — HTTP API는 경로 구체성으로 매칭하므로 등록 순서와 무관하게 이 라우트가 catch-all보다 우선합니다.
- **애플리케이션 계층 게이트**: `DocumentHandler.PublicGetDoc`이 자체적으로 토큰을 조회하고 서빙 전에 `doc.PublicShareToken != token`을 재검증합니다 — 인프라 계층 우회가 의도보다 넓게 잘못 설정되더라도 fail-closed로 동작합니다.
- **철회**: `RevokeUserDocPublicShare`는 토큰을 즉시 지우지만, 이미 발급된 presigned S3 URL은 1시간 TTL 동안 유효합니다. 이번 버전에서는 수용하며, 공개 경로 전용 짧은 TTL은 추적 중인 후속 과제입니다(결과 참조).
- **범위 규율**: 루트 `CLAUDE.md`의 보안 정책이 이제 이를 *유일한* 의도적 예외로 문서화하며, `/api/public/` prefix는 이 핸들러 하나로만 유지되어야 한다고 명시합니다 — 다른 곳에 두 번째 무인증 라우트가 생기면 그것은 이 ADR이 만든 선례가 아니라 정책이 막고자 하는 위반입니다.

## 결과

### 긍정적
- PPTX 슬라이드가 PDF.js 도입 없이도 PDF와 동일한 인앱 미리보기 경험을 갖게 됨.
- 슬라이드 공유(팀 계정 또는 공개 링크)가 이제 단순 다운로드 프롬프트가 아니라 실제로 미리볼 것이 있음.
- `convert-doc`의 IAM 범위는 기존 버킷 전체 grant 패턴보다 좁고, 신뢰 불가 입력을 파싱함에도 자격증명 유출에 대해 강화됨 — 이 코드베이스의 현재 대다수 업로드 경로보다 엄격한 기준.

### 부정적
- LibreOffice가 다른 5개 Go Lambda의 빠른 zip 콜드스타트와는 별개로 수 GB 규모 컨테이너 이미지와 콜드스타트 비용을 추가함. 변환이 비동기(EventBridge 트리거, 요청 경로 아님)라 수용 가능.
- 공개 링크 철회에 이미 발급된 presigned URL로 인한 최대 1시간의 잔존 접근 창이 있음(결정 §2 참조). 공개 경로 전용 짧은 TTL presign은 아직 구현되지 않은 후속 과제.
- 공개 게이트의 유일한 속도 제한은 128비트 토큰 엔트로피(`crypto/rand`)뿐 — `/api/public/*`에 대한 비정상 요청량 CloudWatch 알람이나 WAF 규칙은 아직 없음.
- `convert-doc/main.go`의 버킷 설정이 이제 `BUCKET_NAME` 누락 시 placeholder 버킷명으로 폴백하는 대신 즉시 실패(`log.Fatal`)함 — 의도된 것(코드 주석 참조)이나, 배포 설정 오류가 첫 호출이 아니라 콜드스타트 시점에 실패한다는 뜻.

## 참고
- [ADR-020: 문서 허브 v2](ADR-020-doc-hub-v2-personal-docs-wikilinks-slides.md) — 이 ADR이 대체하는 "PPTX는 다운로드만" 결정. ADR-020의 다른 결정(네이티브 `<iframe>` PDF 뷰어, PDF.js 미도입, account-document 배관 재사용)은 변경 없음.
- `backend/cmd/convert-doc/{main.go,Dockerfile}`, `infra/lib/ai-stack.ts`(`convertDocRole`), `infra/lib/gateway-stack.ts`(`convertDocFunction`, `DocSlideUploadRule`, `/api/public/docs/{token}` 라우트), `infra/lib/frontend-stack.ts`(`/api/public/*` CloudFront behavior) — 구현.
- `backend/internal/service/account.go`(`ShareUserDocumentToAccount`, `CreateUserDocPublicShare`, `ResolvePublicShare`), `backend/internal/handler/document.go`(`PublicGetDoc`).
