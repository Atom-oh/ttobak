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
- **Untrusted-input hardening (partial — see Consequences)**: LibreOffice parses attacker-controllable file content and can fetch resources a document references. The `soffice` subprocess has every `AWS_*` environment variable stripped before exec (Lambda injects temporary credentials as env vars for the Go SDK; the subprocess has no legitimate need for them). This closes the *accidental* exposure path (an env dump, crash log, or error message leaking the process's own environment) but is **not** a real barrier against a determined RCE: a same-UID child can still recover the parent Go process's env via `/proc/<ppid>/environ` regardless of its own stripped env — env-stripping alone does nothing about SSRF or local-file-read via a document's linked/remote content. See Consequences for the tracked residual risk.
- **Network isolation**: the Lambda is placed in `PRIVATE_ISOLATED` subnets of the pre-existing VPC (`ec2.Vpc.fromLookup`, the same VPC `WhisperStack` already uses — no new VPC, no NAT cost) with a dedicated S3 gateway VPC endpoint (`gateway-stack.ts`'s `ConvertDocS3Endpoint`, scoped to just those subnets' route tables). `PRIVATE_ISOLATED` has no route to an internet gateway or NAT — a compromised `soffice` process has no network path to exfiltrate credentials over, fetch a document's linked/remote content via SSRF, or reach anything outside this VPC at all; the S3 gateway endpoint is the one hole punched through, and it only reaches S3. This closes the *network* half of the RCE surface. It does **not** close the *IAM* half (see Consequences: the `docs/*` read grant is still cross-tenant) or local-file-read via content embedded directly in the uploaded document itself (no network call needed for that).
- **Container arch**: `--platform=linux/arm64` pinned explicitly in the Dockerfile's runtime stage (in addition to CDK's `DockerImageCode.fromImageAsset` platform option), since the Dockerfile can also be built directly (e.g. by CI) without going through CDK's asset bundler.

### 2. Unauthenticated public slide links (new Security Policy exception)
`CreateUserDocPublicShare` mints a random 128-bit token (`crypto/rand`); `GET /api/public/docs/{token}` serves that one slide with no caller-identity check at all — neither the API Gateway JWT authorizer nor the Lambda@Edge JWT check that gate every other `/api/*` route.

- **CloudFront**: a `/api/public/*` behavior (`GET`/`HEAD` only, caching disabled) is registered *before* the general `/api/*` behavior — CloudFront matches path patterns in insertion order, so this bypasses the Lambda@Edge `edgeLambdas` JWT check that the `/api/*` behavior attaches. Scoped to exactly this path prefix.
- **API Gateway**: `/api/public/docs/{token}` is registered as its own literal-segment route (not part of the `/api/{proxy+}` catch-all) with no `authorizer` — HTTP API matches by path specificity, so this route wins over the catch-all regardless of registration order.
- **Application-layer gate**: `DocumentHandler.PublicGetDoc` does its own token lookup and re-checks `doc.PublicShareToken != token` before serving — fail-closed even if the infra-layer bypass were ever misconfigured to be broader than intended.
- **Revocation**: `RevokeUserDocPublicShare` clears the token immediately. The presigned S3 URL this route hands out uses a dedicated 5-minute TTL (`service.PublicShareURLTTL`) rather than the 1-hour default used everywhere else in the API, so a revoke closes most of the exposure window — a URL issued in the seconds before revoke can still work for up to 5 minutes, which is accepted as a known residual (see Consequences), not eliminated.
- **Atomic mint**: `CreateUserDocPublicShare`'s token write is a conditional `UpdateItem` (`SetPublicShareTokenIfAbsent`, `attribute_not_exists(publicShareToken) OR publicShareToken = ""`), not a plain overwrite — two concurrent mint requests (e.g. a double-click) would otherwise both read an empty token, each write their own `PublicShare` pointer, and have the second document write silently clobber the first, leaving the first caller holding a token whose pointer exists but no longer matches the doc. The loser of the race deletes its own orphaned pointer and re-reads the doc for the winner's token instead.
- **Scope discipline**: root `CLAUDE.md`'s Security Policy now documents this as the *one* route registered under a CloudFront behavior with no Lambda@Edge JWT check *and* no API Gateway authorizer, and says explicitly that the `/api/public/` prefix must stay limited to this one handler — a second unauthenticated route anywhere else is the violation the policy exists to prevent, not a precedent this ADR sets. (This is a narrower claim than "the only unauthenticated route in the codebase" — the handler-layer `GetAllowedDomains` also serves without checking caller identity, e.g. for the pre-login signup page, but it's registered under the general `/api/*` CloudFront behavior rather than `/api/public/*`; whether that combination is internally consistent is outside this ADR's scope.)

## Consequences

### Positive
- PPTX slides get the same in-browser preview experience as PDF, without adding PDF.js.
- Sharing a slide (to a team account or via public link) now has something to actually preview instead of a bare download prompt.
- `convert-doc`'s IAM scope is narrower than the existing bucket-wide grant pattern, and its subprocess is hardened against credential exfiltration despite parsing untrusted input — a stricter bar than most of this codebase's current upload paths.

### Negative
- LibreOffice adds a multi-GB container image and cold-start cost distinct from the other 5 Go Lambdas' fast zip cold starts; acceptable since conversion is async (EventBridge-triggered, not on the request path).
- Public link revocation has up to a 5-minute tail via already-issued presigned URLs (see Decision §2's dedicated short TTL) — narrower than the 1-hour default used elsewhere, but not zero.
- The public gate's only rate-limiting is 128-bit token entropy (`crypto/rand`) — no explicit CloudWatch alarm or WAF rule for anomalous request volume against `/api/public/*` yet.
- `convert-doc/main.go`'s bucket configuration now fails fast (`log.Fatal`) on a missing `BUCKET_NAME` rather than falling back to a placeholder bucket name — intentional (see the code comment), but means a misconfigured deploy fails at cold start instead of at first invocation.
- **LibreOffice remains a real RCE surface against untrusted input — network path closed, IAM blast radius still open.** Env-stripping (Decision §1) only closes the accidental-leak path, not a determined exploit chain. The `PRIVATE_ISOLATED` VPC placement (Decision §1) removes the network half of that chain — no internet/NAT route means a compromised `soffice` process cannot SSRF a document's linked/remote content, exfiltrate anything over the network, or reach the Lambda metadata/credential-adjacent surface remotely; the only reachable network endpoint is the dedicated S3 gateway endpoint. What remains open: (a) the IAM role's `docs/*` read grant is still bucket-wide across all users, not scoped to the triggering event's own key, so an RCE could still read other users' uploaded documents *via the S3 gateway endpoint it's still allowed to use* — network isolation narrows what's reachable, it doesn't narrow what the credentials on that reachable path can do; (b) local-file-read or macro execution against content embedded directly in the malicious document itself needs no network call, so it's unaffected by this change. Tracked follow-up, not yet implemented: disable remote/linked-content and macro loading in the LibreOffice profile (closes (b)); scoping the IAM grant to the triggering key specifically would need per-invocation credentials (e.g. `sts:AssumeRole` with a downscoping session policy), a larger change deferred for now (addresses (a)).

## References
- [ADR-020: Document Hub v2](ADR-020-doc-hub-v2-personal-docs-wikilinks-slides.md) — the "PPTX is download-only" decision this ADR supersedes; all other ADR-020 decisions (native `<iframe>` PDF viewer, no PDF.js, account-document plumbing reuse) are unchanged.
- `backend/cmd/convert-doc/{main.go,Dockerfile}`, `infra/lib/ai-stack.ts` (`convertDocRole`), `infra/lib/gateway-stack.ts` (`convertDocFunction`, `ConvertDocVpc`/`ConvertDocS3Endpoint`/`ConvertDocSg` for the `PRIVATE_ISOLATED` placement, `DocSlideUploadRule`, `/api/public/docs/{token}` route), `infra/lib/frontend-stack.ts` (`/api/public/*` CloudFront behavior) — implementation.
- `backend/internal/service/account.go` (`ShareUserDocumentToAccount`, `CreateUserDocPublicShare`, `ResolvePublicShare`), `backend/internal/handler/document.go` (`PublicGetDoc`).
- **Pre-merge verification**: the VPC (`vpc-04e77172c67f19814`, `WhisperStack`'s pre-existing "FsiDemo" VPC, reused via `ec2.Vpc.fromLookup`) and its `PRIVATE_ISOLATED` subnet group were confirmed against `infra/cdk.context.json`'s cached lookup, and `cdk synth` for `TtobakGatewayStack` was run successfully — but neither confirms there isn't already a same-service S3 gateway endpoint on the exact same route tables in the live account (CDK's cached VPC lookup doesn't enumerate existing endpoints). Run `cdk diff TtobakGatewayStack --exclusively` before deploying to catch that as a diff instead of a failed deploy.

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
- **신뢰 불가 입력 방어(부분적 — 결과 참조)**: LibreOffice는 공격자가 통제 가능한 파일 콘텐츠를 파싱하며 문서가 참조하는 리소스를 가져올 수 있습니다. `soffice` 자식 프로세스는 exec 전에 모든 `AWS_*` 환경변수를 제거합니다(Lambda는 Go SDK를 위해 임시 자격증명을 환경변수로 주입하는데, 이 서브프로세스는 이를 쓸 정당한 이유가 없음). 이는 *우발적* 노출 경로(환경변수 덤프, 크래시 로그, 에러 메시지에 프로세스 자신의 환경이 새는 경우)만 막습니다 — 본격적인 RCE에 대한 실질적 방벽은 **아닙니다**: 같은 UID의 자식 프로세스는 자기 env가 지워졌어도 `/proc/<ppid>/environ`으로 부모 Go 프로세스의 env를 회수할 수 있고, env 제거 자체는 문서의 링크/원격 콘텐츠를 통한 SSRF나 로컬 파일 읽기를 막지 못합니다. 잔존 위험은 결과 절 참조.
- **네트워크 격리**: 이 Lambda는 기존 VPC(`ec2.Vpc.fromLookup`, `WhisperStack`이 이미 쓰는 그 VPC — 신규 VPC/NAT 비용 없음)의 `PRIVATE_ISOLATED` 서브넷에 배치되며, 해당 서브넷의 라우트 테이블에만 연결된 전용 S3 게이트웨이 VPC 엔드포인트(`gateway-stack.ts`의 `ConvertDocS3Endpoint`)를 둡니다. `PRIVATE_ISOLATED`는 인터넷 게이트웨이/NAT로의 라우트가 전혀 없습니다 — 손상된 `soffice` 프로세스가 자격증명을 네트워크로 유출하거나, SSRF로 문서의 링크/원격 콘텐츠를 가져오거나, 이 VPC 밖의 어떤 것에도 접근할 네트워크 경로가 없습니다. 뚫린 유일한 구멍은 S3 게이트웨이 엔드포인트이고, 그마저 S3에만 닿습니다. 이는 RCE 표면의 *네트워크* 절반을 막습니다. *IAM* 절반(결과 참조 — `docs/*` 읽기 권한은 여전히 cross-tenant)이나, 업로드된 문서 자체에 내장된 콘텐츠에 대한 로컬 파일 읽기(네트워크 호출이 필요 없음)는 막지 못합니다.
- **컨테이너 아키텍처**: CDK의 `DockerImageCode.fromImageAsset` platform 옵션과 별개로 Dockerfile 런타임 스테이지에 `--platform=linux/arm64`를 명시 고정 — CI 등에서 CDK 에셋 번들러를 거치지 않고 Dockerfile을 직접 빌드할 수도 있기 때문입니다.

### 2. 무인증 공개 슬라이드 링크 (신규 보안 정책 예외)
`CreateUserDocPublicShare`가 무작위 128비트 토큰(`crypto/rand`)을 발급하고, `GET /api/public/docs/{token}`이 해당 슬라이드 하나를 호출자 신원 확인 없이 제공합니다 — 다른 모든 `/api/*` 라우트를 게이트하는 API Gateway JWT authorizer도, Lambda@Edge JWT 검사도 없습니다.

- **CloudFront**: `/api/public/*` behavior(`GET`/`HEAD`만, 캐싱 비활성)가 일반 `/api/*` behavior *앞에* 등록됩니다 — CloudFront는 경로 패턴을 등록 순서대로 매칭하므로, `/api/*` behavior에 붙은 Lambda@Edge `edgeLambdas` JWT 검사를 건너뜁니다. 정확히 이 경로 prefix로만 범위가 제한됩니다.
- **API Gateway**: `/api/public/docs/{token}`은 `/api/{proxy+}` catch-all의 일부가 아니라 자체 리터럴 세그먼트 라우트로 등록되며 `authorizer`가 없습니다 — HTTP API는 경로 구체성으로 매칭하므로 등록 순서와 무관하게 이 라우트가 catch-all보다 우선합니다.
- **애플리케이션 계층 게이트**: `DocumentHandler.PublicGetDoc`이 자체적으로 토큰을 조회하고 서빙 전에 `doc.PublicShareToken != token`을 재검증합니다 — 인프라 계층 우회가 의도보다 넓게 잘못 설정되더라도 fail-closed로 동작합니다.
- **철회**: `RevokeUserDocPublicShare`는 토큰을 즉시 지웁니다. 이 라우트가 발급하는 presigned S3 URL은 다른 곳(1시간 기본값)과 달리 전용 5분 TTL(`service.PublicShareURLTTL`)을 씁니다 — 철회로 대부분의 노출 창이 닫히지만, 철회 직전 몇 초 내 발급된 URL은 최대 5분까지 계속 동작할 수 있어 완전히 제거된 것은 아닙니다(결과 참조, 알려진 잔존 위험).
- **원자적 발급**: `CreateUserDocPublicShare`의 토큰 쓰기는 단순 덮어쓰기가 아니라 조건부 `UpdateItem`(`SetPublicShareTokenIfAbsent`, `attribute_not_exists(publicShareToken) OR publicShareToken = ""`)입니다 — 동시 발급 요청 2건(예: 더블클릭)이 있으면 그렇지 않을 경우 둘 다 빈 토큰을 읽고 각자 `PublicShare` 포인터를 만든 뒤, 두 번째 문서 쓰기가 첫 번째를 조용히 덮어써 첫 요청자가 받은 토큰의 포인터는 존재하지만 문서와 더 이상 일치하지 않는 상태가 됩니다. 경쟁에서 진 쪽은 자신의 orphan 포인터를 지우고 문서를 재조회해 승자의 토큰을 대신 반환합니다.
- **범위 규율**: 루트 `CLAUDE.md`의 보안 정책이 이제 이를 Lambda@Edge JWT 체크와 API Gateway authorizer 둘 다 없는 *유일한* CloudFront behavior로 문서화하며, `/api/public/` prefix는 이 핸들러 하나로만 유지되어야 한다고 명시합니다 — 다른 곳에 두 번째 무인증 라우트가 생기면 그것은 이 ADR이 만든 선례가 아니라 정책이 막고자 하는 위반입니다. (이는 "코드베이스에서 유일한 무인증 라우트"보다 좁은 주장입니다 — 핸들러 계층의 `GetAllowedDomains`도 로그인 전 가입 페이지용으로 호출자 신원 확인 없이 서빙하지만, `/api/public/*`가 아니라 일반 `/api/*` CloudFront behavior에 등록돼 있습니다; 그 조합 자체의 내부 일관성 여부는 이 ADR의 범위 밖입니다.)

## 결과

### 긍정적
- PPTX 슬라이드가 PDF.js 도입 없이도 PDF와 동일한 인앱 미리보기 경험을 갖게 됨.
- 슬라이드 공유(팀 계정 또는 공개 링크)가 이제 단순 다운로드 프롬프트가 아니라 실제로 미리볼 것이 있음.
- `convert-doc`의 IAM 범위는 기존 버킷 전체 grant 패턴보다 좁고, 신뢰 불가 입력을 파싱함에도 자격증명 유출에 대해 강화됨 — 이 코드베이스의 현재 대다수 업로드 경로보다 엄격한 기준.

### 부정적
- LibreOffice가 다른 5개 Go Lambda의 빠른 zip 콜드스타트와는 별개로 수 GB 규모 컨테이너 이미지와 콜드스타트 비용을 추가함. 변환이 비동기(EventBridge 트리거, 요청 경로 아님)라 수용 가능.
- 공개 링크 철회에 이미 발급된 presigned URL로 인한 최대 5분의 잔존 접근 창이 있음(결정 §2의 전용 짧은 TTL 참조) — 다른 곳의 1시간 기본값보다 훨씬 짧지만 0은 아님.
- 공개 게이트의 유일한 속도 제한은 128비트 토큰 엔트로피(`crypto/rand`)뿐 — `/api/public/*`에 대한 비정상 요청량 CloudWatch 알람이나 WAF 규칙은 아직 없음.
- `convert-doc/main.go`의 버킷 설정이 이제 `BUCKET_NAME` 누락 시 placeholder 버킷명으로 폴백하는 대신 즉시 실패(`log.Fatal`)함 — 의도된 것(코드 주석 참조)이나, 배포 설정 오류가 첫 호출이 아니라 콜드스타트 시점에 실패한다는 뜻.
- **LibreOffice는 신뢰 불가 입력에 대한 실질적 RCE 표면으로 남아있음 — 네트워크 경로는 막혔으나 IAM 피해 범위는 여전히 열려있음.** env 제거(결정 §1)는 우발적 유출 경로만 막습니다. `PRIVATE_ISOLATED` VPC 배치(결정 §1)는 이 익스플로잇 체인의 네트워크 절반을 제거합니다 — 인터넷/NAT 라우트가 없으므로 손상된 `soffice`가 문서의 링크/원격 콘텐츠를 SSRF로 가져오거나, 네트워크로 뭔가를 유출하거나, Lambda 메타데이터/자격증명 인접 표면에 원격으로 접근할 수 없습니다; 도달 가능한 네트워크 엔드포인트는 전용 S3 게이트웨이 엔드포인트뿐입니다. 여전히 열려있는 것: (a) IAM role의 `docs/*` 읽기 권한이 트리거한 이벤트의 키 하나가 아니라 모든 사용자에 걸쳐 버킷 전체이므로, RCE가 발생하면 *여전히 허용된 S3 게이트웨이 엔드포인트를 통해* 다른 사용자가 업로드한 문서까지 읽힐 수 있음 — 네트워크 격리는 도달 가능한 대상을 좁혔을 뿐, 그 도달 가능한 경로에서 자격증명이 할 수 있는 일을 좁히지는 못함; (b) 악성 문서 자체에 직접 내장된 콘텐츠에 대한 로컬 파일 읽기나 매크로 실행은 네트워크 호출이 필요 없어 이번 변경의 영향을 받지 않음. 아직 미구현인 추적 과제: LibreOffice 프로필에서 원격/링크 콘텐츠 로딩과 매크로 실행을 비활성화(→ (b) 해결); IAM 권한을 트리거 키 단위로 좁히려면 호출당 자격증명(예: 다운스코핑 세션 정책을 적용한 `sts:AssumeRole`)이 필요한 더 큰 변경이라 지금은 보류(→ (a) 해결).

## 참고
- [ADR-020: 문서 허브 v2](ADR-020-doc-hub-v2-personal-docs-wikilinks-slides.md) — 이 ADR이 대체하는 "PPTX는 다운로드만" 결정. ADR-020의 다른 결정(네이티브 `<iframe>` PDF 뷰어, PDF.js 미도입, account-document 배관 재사용)은 변경 없음.
- `backend/cmd/convert-doc/{main.go,Dockerfile}`, `infra/lib/ai-stack.ts`(`convertDocRole`), `infra/lib/gateway-stack.ts`(`convertDocFunction`, `PRIVATE_ISOLATED` 배치를 위한 `ConvertDocVpc`/`ConvertDocS3Endpoint`/`ConvertDocSg`, `DocSlideUploadRule`, `/api/public/docs/{token}` 라우트), `infra/lib/frontend-stack.ts`(`/api/public/*` CloudFront behavior) — 구현.
- `backend/internal/service/account.go`(`ShareUserDocumentToAccount`, `CreateUserDocPublicShare`, `ResolvePublicShare`), `backend/internal/handler/document.go`(`PublicGetDoc`).
- **병합 전 검증 필요**: VPC(`vpc-04e77172c67f19814`, `WhisperStack`이 쓰는 기존 "FsiDemo" VPC를 `ec2.Vpc.fromLookup`으로 재사용)와 그 `PRIVATE_ISOLATED` 서브넷 그룹은 `infra/cdk.context.json`의 캐시된 lookup으로 확인했고 `TtobakGatewayStack`의 `cdk synth`도 성공했으나, 실제 계정에 동일 라우트 테이블을 대상으로 하는 S3 게이트웨이 엔드포인트가 이미 존재하지 않는지는 이것만으로 확인되지 않음(CDK의 캐시된 VPC lookup은 기존 엔드포인트 목록까지는 열거하지 않음). 배포 전 `cdk diff TtobakGatewayStack --exclusively`를 실행해 실패한 배포가 아니라 diff로 먼저 확인할 것.
