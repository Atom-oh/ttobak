# ADR-020: Document Hub v2 — Personal Documents, Wikilink Index, Slide Uploads

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — implemented on `feat/sp2-doc-hub-v2`. Extends `AccountDocument` (ADR-017/018) with `docType: note|blog|slide`, account-less personal documents, a wikilink index, and slide upload/view/download.

## Context
[The master roadmap](../superpowers/specs/2026-07-09-work-assistant-roadmap-design.md) (SP2) asks for a Notion-style document hub: notes/blogs editable in-browser, wikilinks (`[[title]]`) as a future graph's data source, and slide (PPTX/PDF) upload+viewer — all reusing the existing account-document plumbing rather than a new subsystem.

## Decision
- **Personal documents**: `PK: USER#{userId}, SK: DOC#{docId}` — reuses the `AccountDocument` struct with `AccountID` empty and a new `EntityTypeUserDoc`. Ownership is implicit in the PK (always built from the authenticated caller), so personal-document endpoints (`/api/documents...`) skip the account-membership check entirely; there is no separate personal-doc model or table.
- **docType stays a free string.** The UI offers `note`/`blog`/`slide`; the server never enum-validates, so existing `"prep"`/`"reference"` values and MCP's `ttobak_put_document` are untouched.
- **Wikilink index = a `links []string` attribute on the document item itself**, parsed server-side (regex over `[[target]]`/`[[target|alias]]`/`[[target#heading]]`, normalized + deduped) on every create/update — including via MCP, since it runs in the shared `putDoc`/`updateDoc` core. No separate edge/graph items are created yet; a future graph view (SP4) can build its edges directly from this list without a migration.
- **Slides are documents with `FileKey` set** (S3 key, `Content` empty). `FileKey` must be prefixed `docs/{userId}/` — checked server-side, since it is the only place a client-supplied S3 key is trusted. The presigned-upload `PutDocument`/`UpdateDocument` call **is** the completion record; there is no `/api/upload/complete` step for this category (unlike audio/image/file uploads, which need one to trigger downstream processing — slides have none).
- **PDF viewing = a browser-native `<iframe>` of a presigned GET URL.** No PDF.js. PPTX is download-only (no in-browser viewer). Known ceiling: iOS Safari's PDF iframe renders only page 1 — the always-present download button covers it.
- **Vault export** now sweeps personal docs plus every account membership, placing markdown-content documents under `_Private/Docs/` or `Accounts/{name}/Docs/` with `ttobak_id` frontmatter (closing the ADR-017 loop guard the same way meetings do). Slides are skipped — the vault export is markdown-only.

## Consequences

### Positive
- Zero new DynamoDB tables/GSIs; zero IAM/CORS changes -- verified the api Lambda's S3 access is one bucket-wide `bucket.grantReadWrite(apiRole)` (`infra/lib/ai-stack.ts`) and the bucket CORS rule is origin-scoped, not prefix-scoped (`storage-stack.ts`), so the new `docs/` prefix needed no policy change. CloudFront's SPA router function (`frontend-stack.ts`) *did* need updating -- it rewrites dynamic-segment URLs (e.g. `/docs/{docId}`) to the static placeholder page Next.js actually built, and didn't know about this PR's new routes yet.
- MCP `ttobak_put_document` gets wikilink indexing "for free" since it shares the same core path as the web UI.
- Slide upload reuses the existing presigned-URL pattern (`GeneratePresignedUploadURL`/`GeneratePresignedDownloadURL`) with one new category, not a parallel upload subsystem.

### Negative
- `links` is a flat list on the document, not a queryable edge table — a future graph view (SP4) will need to build its own index/query path from a full document scan if it needs reverse-lookup ("who links to X") at scale.
- No wikilink validity checking — a link to a renamed/deleted title silently goes stale until the UI renders it (broken-link display is deferred to SP2's own follow-up, per the roadmap's noted open question).

### Risks
- `FileKey` prefix check is the only ownership control on slide uploads; it does not verify the object actually exists in S3 at put time (a doc can reference a not-yet-uploaded or since-deleted key). Acceptable: the presigned PUT already scoped the key to the caller, and a missing object just fails the eventual GET.

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| `links` attribute on the doc item (chosen) | No new items, ships with existing put/update path | Not a queryable edge index |
| Separate `LINK#` edge items per wikilink | Queryable both directions, graph-ready | New item type, write amplification for a feature (SP4) not yet built |
| PDF.js viewer | Full pagination, consistent cross-browser rendering | New dependency + bundle size for a feature the iframe already covers adequately |

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — `feat/sp2-doc-hub-v2`에서 구현. ADR-017/018의 `AccountDocument`를 `docType: note|blog|slide`, 개인(Account 미소속) 문서, 위키링크 인덱스, 슬라이드 업로드/뷰/다운로드로 확장.

## 맥락
[마스터 로드맵](../superpowers/specs/2026-07-09-work-assistant-roadmap-design.md)(SP2)은 Notion 스타일 문서 허브를 요청한다: 브라우저에서 편집 가능한 노트/블로그, 향후 그래프의 데이터 소스가 될 위키링크(`[[제목]]`), 슬라이드(PPTX/PDF) 업로드+뷰어 — 새 서브시스템이 아니라 기존 account-document 배관을 재사용해야 한다.

## 결정
- **개인 문서**: `PK: USER#{userId}, SK: DOC#{docId}` — 기존 `AccountDocument` struct를 AccountID 빈 값으로 재사용, 신규 `EntityTypeUserDoc` 추가. 소유권이 PK에 내재(항상 인증된 호출자로부터 구성)하므로 개인 문서 엔드포인트(`/api/documents...`)는 계정 멤버십 확인을 완전히 생략 — 별도 개인 문서 모델/테이블 없음.
- **docType은 free string 유지.** UI가 `note`/`blog`/`slide`를 제시할 뿐 서버는 enum 검증을 하지 않아, 기존 `"prep"`/`"reference"` 값과 MCP의 `ttobak_put_document`가 그대로 동작.
- **위키링크 인덱스 = 문서 아이템의 `links []string` 속성**, 매 생성/수정 시 서버가 정규식으로 파싱(`[[제목]]`/`[[제목|별칭]]`/`[[제목#절]]`, 정규화+중복제거) — 공용 `putDoc`/`updateDoc` 코어를 지나므로 MCP를 통한 저장도 포함. 별도 edge/그래프 아이템은 아직 없음; 향후 그래프 뷰(SP4)는 마이그레이션 없이 이 목록에서 바로 엣지를 만들 수 있음.
- **슬라이드 = `FileKey`가 있는 문서** (S3 키, `Content`는 빈 값). `FileKey`는 `docs/{userId}/` 접두어 강제 — 서버 검증, 클라이언트가 준 S3 키를 신뢰하는 유일한 지점이기 때문. presigned 업로드 후의 `PutDocument`/`UpdateDocument` 호출 자체가 업로드 완료 기록 — 오디오/이미지/파일 업로드와 달리(다운스트림 처리 트리거용 완료 단계 필요) 슬라이드는 `/api/upload/complete` 단계가 없음.
- **PDF 뷰어 = presigned GET URL의 브라우저 네이티브 `<iframe>`.** PDF.js 미도입. PPTX는 다운로드만(뷰어 없음). 알려진 한계: iOS Safari의 PDF iframe은 1페이지만 렌더링 — 항상 노출되는 다운로드 버튼이 이를 커버.
- **Vault export**가 개인 문서 + 모든 계정 멤버십을 훑어, 마크다운 콘텐츠가 있는 문서를 `_Private/Docs/` 또는 `Accounts/{name}/Docs/`에 `ttobak_id` 프론트매터와 함께 배치(미팅과 동일하게 ADR-017 루프 가드를 닫음). 슬라이드는 스킵 — vault export는 마크다운 전용.

## 결과

### 긍정
- 신규 DynamoDB 테이블/GSI 없음; IAM/CORS 변경 없음 — api Lambda의 S3 접근이 prefix별 정책이 아니라 버킷 전체 대상 `bucket.grantReadWrite(apiRole)`(`infra/lib/ai-stack.ts`) 하나이고 CORS도 prefix 무관(`storage-stack.ts`)임을 확인, 신규 `docs/` prefix에 정책 변경 불필요. 단 CloudFront SPA 라우터 함수(`frontend-stack.ts`)는 갱신이 필요했다 — 동적 세그먼트 URL(예: `/docs/{docId}`)을 Next.js가 실제로 빌드한 정적 placeholder 페이지로 재작성하는 함수인데, 이 PR의 신규 라우트를 아직 모르고 있었다.
- MCP `ttobak_put_document`가 웹 UI와 동일한 공용 경로를 지나므로 위키링크 인덱싱을 "공짜로" 획득.
- 슬라이드 업로드가 기존 presigned-URL 패턴(`GeneratePresignedUploadURL`/`GeneratePresignedDownloadURL`)에 카테고리 하나만 추가 — 병렬 업로드 서브시스템 아님.

### 부정
- `links`는 조회 가능한 edge 테이블이 아니라 문서상의 flat 목록 — 향후 그래프 뷰(SP4)가 대규모에서 역방향 조회("X를 링크하는 문서")가 필요하면 자체 인덱스/조회 경로를 구축해야 함.
- 위키링크 유효성 검사 없음 — 이름이 바뀌거나 삭제된 제목으로의 링크는 UI가 렌더링할 때까지 조용히 깨진 상태로 남음(끊긴 링크 표시는 로드맵에 명시된 SP2 자체의 열린 질문으로 후속 처리).

## 위험
- `FileKey` 접두어 확인이 슬라이드 업로드의 유일한 소유권 통제이며, put 시점에 S3에 실제 객체가 존재하는지 검증하지 않음(아직 업로드되지 않았거나 이후 삭제된 키를 참조하는 문서가 가능). 수용 가능: presigned PUT이 이미 키를 호출자로 스코프했고, 누락된 객체는 결국 GET에서 실패할 뿐.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| 문서 아이템의 `links` 속성(채택) | 신규 아이템 없이 기존 put/update 경로로 동작 | 조회 가능한 edge 인덱스 아님 |
| 위키링크별 별도 `LINK#` edge 아이템 | 양방향 조회 가능, 그래프 준비 완료 | 신규 아이템 타입, 아직 없는 기능(SP4)을 위한 쓰기 증폭 |
| PDF.js 뷰어 | 전체 페이지네이션, 브라우저 간 일관된 렌더링 | iframe으로 충분히 커버되는 기능에 신규 의존성 + 번들 크기 추가 |
