# SP3. Public 문서 페이지 (비밀 토큰 URL) — 설계

> Status: Draft · Date: 2026-07-15 · Author: (brainstormed with Claude)
> 로드맵: [2026-07-09-work-assistant-roadmap-design.md](2026-07-09-work-assistant-roadmap-design.md) §4 SP3

## 1. 범위 (이번 SP3)

- **대상**: SP2에서 만든 문서(note/blog/slide — Account 소속 + 개인 문서)만. 미팅노트는 이번 범위 밖(후속 SP로 미룸 — 참석자/전사/첨부파일 등 추가로 고려할 게 많아 별도 설계가 낫다).
- **PPTX 슬라이드**: 공개 토글 시도 시 400으로 거부. PDF 슬라이드와 노트/블로그만 공개 가능. PPTX→PDF 자동변환은 **별도 서브프로젝트**(가칭 SP-PPTX)로 분리 — LibreOffice headless 변환 파이프라인이 필요한 큰 작업이라 SP3 완료를 막지 않는다. 완료되면 SP3의 "PPTX 400 거부" 지점을 변환 파이프라인 호출로 교체.
- **Out**: 공개 문서 목록/검색 페이지(비밀 URL 모델과 상충), 공개 페이지 댓글/상호작용, 토큰 TTL(명시적 철회까지 무기한 — 필요해지면 후속 추가), 미팅노트 공개.

## 2. 데이터 모델 & 토큰 라이프사이클

`AccountDocument`(`backend/internal/model/account.go`)에 필드 추가:

```go
PublicToken string `dynamodbav:"publicToken,omitempty" json:"-"`
```

`json:"-"` — 원본 토큰은 절대 일반 GET 응답에 그대로 노출하지 않는다(FileKey와 동일 원칙). 대신 owner가 보는 `AccountDocumentDetail`에 계산된 필드를 추가:

```go
PublicURL string `json:"publicUrl,omitempty"` // PublicToken이 있을 때만 "https://{domain}/p/{token}"으로 채움
```

**토큰 형식**: `crypto/rand`로 32바이트 생성 → base64url 인코딩(~43자, 추측 불가능한 길이).

**저장 방식 — 별도 reverse-lookup 아이템** (GSI 신설 대신):

```
PK: PUBLIC#{token}
SK: PUBLIC#{token}
targetPK: string   // 원본 문서의 PK (ACCOUNT#{id} 또는 USER#{id})
targetDocID: string
```

이유: DynamoDB GSI 추가는 CDK/infra 변경이 필요한 스키마 마이그레이션이다. 단일 아이템 추가는 기존 `ttobak-main` 테이블 안에서 완전히 흡수되며 CDK 변경이 전혀 없다(SP2가 `docs/` prefix로 IAM/CORS 변경 없이 확장했던 것과 같은 패턴).

**라이프사이클**:
- **활성화 (POST)**: 문서에 `PublicToken`이 이미 있으면(= 이미 공개 중) 그대로 반환 — idempotent, 재발급하지 않는다. `PublicToken`이 없으면(첫 활성화든, 비활성화 후 재활성화든) 새 토큰 생성 → `PUBLIC#{token}` 아이템 write → 문서에 set. 즉 "새 토큰 발급"이 일어나는 유일한 경로는 "현재 비공개(`PublicToken`이 비어 있음) → 활성화" 전이뿐이다 — 이미 공개 중에 POST를 반복 호출해도(더블클릭 등) 토큰이 바뀌지 않고, 비활성화를 거쳐야만 다음 활성화에서 새 토큰이 나온다(그래서 예전에 유출된 링크가 재공개 시 저절로 살아나지 않는다).
- **비활성화 (DELETE)**: `PublicToken`이 있으면 `PUBLIC#{token}` 아이템 delete → 문서의 `PublicToken` clear → 204. 이미 비공개(`PublicToken`이 비어 있음) 상태에서 DELETE를 호출해도 에러 없이 204(idempotent) — "이미 원하는 상태"이므로 실패로 취급하지 않는다.
- **문서 자체 삭제 시**: `deleteDoc`이 대상 문서에 `PublicToken`이 있으면 `PUBLIC#{token}` 아이템도 함께 삭제한다(S3 정리와 동일한 best-effort 원칙 — 문서 DB 아이템 삭제가 우선, reverse-lookup 정리는 실패해도 삭제 자체를 막지 않음). 이걸 안 하면 문서가 지워진 뒤에도 `PUBLIC#{token}` 아이템이 남아, `GET /api/public/documents/{token}`이 존재하지 않는 `targetPK`/`targetDocID`를 가리키는 고아 레코드를 조회하려다 실패하는 경로가 생긴다 — 이 경우도 404로 응답하면 사용자 관점에서는 문제없지만, 고아 아이템이 테이블에 영구히 남는 것은 피한다.

## 3. API

기존 계정/개인 문서 라우트 패턴을 그대로 따른다 (`backend/cmd/api/main.go`):

```
POST   /api/documents/{docId}/public                     인증 필요, 개인 문서 소유자
DELETE /api/documents/{docId}/public
POST   /api/accounts/{accountId}/documents/{docId}/public  인증 필요, 계정 멤버(기존 requireMember)
DELETE /api/accounts/{accountId}/documents/{docId}/public

GET    /api/public/documents/{token}   인증 없음(신규 라우터 그룹, 아래 §5)
```

`POST .../public` 응답: `{ "token": "...", "url": "https://{domain}/p/{token}" }`. `docType: "slide"`이고 `fileKey`가 `.pptx`로 끝나면 400(`ErrInvalidInput`, message: "PPTX slides cannot be made public yet — export to PDF first").

`GET /api/public/documents/{token}` 응답(비인증, 최소 정보만):

```json
{
  "title": "...",
  "docType": "note",
  "content": "# ...",       // 슬라이드면 빈 문자열
  "downloadUrl": "...",     // 슬라이드일 때만, presigned GET, 1시간 유효
  "updatedAt": "2026-07-15T09:00:00Z"
}
```

`docId`, `sourceUserId`, `accountId`/계정명, `path`, `links` 등은 응답에 포함하지 않는다 — 작성자 식별 정보와 내부 조직 구조를 공개 페이지에서 완전히 배제한다는 이번 설계의 핵심 결정. 존재하지 않거나 비활성화된 토큰은 404.

## 4. 인증 우회 (Lambda@Edge)

`infra/lib/frontend-stack.ts`의 `additionalBehaviors`에 `/api/public/*`를 **`/api/*`보다 먼저** 등록한다:

```ts
additionalBehaviors: {
  '/api/public/*': {
    origin: apiOrigin,
    viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
    allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD_OPTIONS,
    cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
    originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
    // Lambda@Edge 미연결 — 이 behavior가 /api/* 보다 먼저 평가되므로
    // /api/public/* 요청은 JWT 검증 함수(EdgeAuthFunction)를 아예 타지 않는다.
  },
  '/api/*': { /* 기존, edgeLambdas 연결 유지 */ },
},
```

CloudFront는 behavior를 **등록 순서대로** 평가해 첫 매치를 사용한다(가장 구체적인 패턴이 자동으로 우선하지 않음) — 순서가 안전성의 핵심이다. `EdgeAuthFunction`(JWT 검증 Lambda) 코드 자체는 전혀 수정하지 않는다 — 인증이 필요한 모든 다른 라우트의 회귀 위험을 원천적으로 없앤다.

`GET /api/public/documents/{token}`은 `chi` 라우터에서 `middleware.Auth`가 걸리지 않은 **별도 라우트 그룹**에 등록한다(기존 `r.Group`에 인증 미들웨어를 씌우는 패턴과 동일하되, 이 그룹만 미들웨어를 안 씌움).

## 5. 프론트엔드

- **`DocDetailClient.tsx`**: 제목 옆에 "공개" 토글 스위치 추가. PPTX 슬라이드면 비활성화 + 툴팁("PDF로 내보낸 후 공개 가능"). 켜져 있으면 `publicUrl` + 복사 버튼 표시.
- **신규 `app/p/[docToken]/page.tsx`** + **`PublicDocClient.tsx`**: `generateStaticParams` 플레이스홀더 패턴(`app/docs/[docId]/page.tsx`와 동일) + `usePathname()`으로 실제 토큰 파싱(SP2 라운드에서 확정한 패턴, `DocDetailClient`의 `accountScoped` 이슈와 동일 이유로 라우트 param이 아니라 pathname을 신뢰). 인증 체크 없음 — `AuthProvider` 밖에서 독립적으로 렌더링.
- **렌더링**: `MarkdownRenderer.tsx`(기존, `rehype-sanitize` 내장) 재사용. 위키링크(`[[문서명]]`)는 표준 마크다운 문법이 아니므로 `ReactMarkdown`이 그냥 일반 텍스트로 렌더링함 — 별도의 "링크 비활성화" 처리가 필요 없다. 슬라이드는 `downloadUrl`로 PDF `<iframe>` + 다운로드 버튼(`DocDetailClient`의 기존 슬라이드 뷰 패턴 재사용).
- **`infra/lib/frontend-stack.ts`의 CloudFront Function**: `knownPages`에 동적 라우트 `/p/{token}` 인식 추가 (기존 `/docs/{docId}` 패턴과 동일한 방식).

## 6. 테스트

- `service/account_test.go` (또는 신규 `public_test.go`): 첫 활성화 시 토큰 발급, 활성화 중 재-POST가 idempotent(토큰 불변), 비활성화 후 재활성화 시 새 토큰, 비공개 상태에서 DELETE가 idempotent(에러 없음), 문서 삭제 시 `PUBLIC#{token}` 아이템도 함께 삭제, PPTX 거부, 존재하지 않거나 삭제된 토큰 조회 시 404.
- `handler` 테스트: POST/DELETE public happy path + 403(비멤버/비소유자), GET public이 인증 없이 200.
- `infra/test`: `cdk synth`로 `/api/public/*` behavior가 `/api/*`보다 먼저 나열되는지, `edgeLambdas`가 없는지 확인.

## 7. 검증

1. `go vet`/`go test`, 프론트 `lint`/`build`.
2. 수동 E2E: 노트 공개 토글 → `/p/{token}` 무인증 접속 → 렌더링 확인 → 비공개 전환 → 같은 URL 404 → 재공개 → 새 토큰 발급(URL 바뀜) 확인. PPTX 슬라이드 공개 시도 → 400.
3. `cdk synth TtobakFrontendStack --exclusively`로 behavior 순서 확인 후 배포.
