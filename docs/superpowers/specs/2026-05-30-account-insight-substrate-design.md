# Account 중심 Insight Substrate & MCP Back-Data 설계

> Status: Draft · Date: 2026-05-30 · Author: Junseok Oh (brainstormed with Claude)

## 1. Overview (목적)

TTOBAK은 현재 **"미팅 1건"** 단위 도구다(record → STT → 구조화 요약 → KB → Q&A). 그러나 사용자(AWS SA/Account 담당)의 실제 업무는 **고객사(Account)** 를 중심으로 누적되는 산출물 — SFDC Activity Logging, SIFT(월간 Field Insight), 2by2(월간 Risk/Opportunity), Player Card(분기/반기) — 로 흘러간다.

이 산출물 문서들은 **개인 맥북에서 사내 Internal MCP/에이전트로 조립**된다. 따라서 TTOBAK의 역할은 그 문서들을 **흉내 내거나 조립하는 것이 아니라**, 그것들을 채울 **원재료(raw material)를 Account 단위로 누적하고 MCP로 떠먹여 주는 것**이다.

> **한 줄 정의:** TTOBAK은 Account 단위 *필드 인사이트 적립 + 미팅 코퍼스 검색* 층이 되고, 정형 사내 산출물 조립은 개인 맥북 에이전트(예: `playercard-agent`)에게 맡긴다.

### 제품 비전 (중요)
TTOBAK은 *단순한 데이터 공급 도구*가 아니라 **Solutions Architect의 전용 비서**다. 따라서 "원재료만 만든다"는 경계는 **외부 정형 사내 산출물(SFDC·SIFT·2by2·Player Card)** 에만 적용된다 — 그것들은 사내 템플릿/기밀이고 개인 맥북에서 조립한다. 그 외 영역에서 TTOBAK은 SA를 돕는 **능동적 비서**로서 검색·질문추천·리서치·(향후) 즉석 deliverable 생성까지 한다. 이번 스펙은 그 비서의 **장기 기억(Account 단위 데이터 계층)** 을 까는 기반 작업이다.

## 2. Goals / Non-Goals

### In (이번 스펙)
1. **Account** = 1급 엔티티 (명시 등록 + 기존 태그 매핑 + 팀 공유)
2. **Insight Substrate** = 미팅·뉴스·인제스트 문서에서 추출한 *유형·시간·출처가 붙은* 인사이트 레코드를 Account 단위 누적
3. **MCP back-data 도구** (양방향): Account 인사이트/미팅 코퍼스를 `account + 기간 + 유형 + scope`로 서빙(아웃바운드) + 로컬 vault 문서·**고객 미팅 준비 자료(`docType:"prep"`)** 를 TTOBAK으로 인제스트(인바운드)
4. **Obsidian Vault 미러링**: 미팅 코퍼스를 디렉터리 단위 공유가 가능한 vault 구조로 export
5. **최소 프런트엔드**: Account 등록/멤버 초대, 미팅↔Account 연결·공유, Account 상세(인사이트·공유 미팅·문서 열람)

### Out (명시적 제외 — 단, 일부는 후속 스펙으로 명문화)
- ❌ SFDC / SIFT / 2by2 / **Player Card 문서 조립** — 개인 맥북 에이전트의 몫(영구 외부)
- ⏭️ **챗 에이전트 deliverable 출력(PPT/Notion)** — *바로 다음 스펙*. 이 기반(Account+substrate+MCP+KB) 위에서 동작. no-Mac 시나리오용(§14 참조)
- ⏭️ **Self 축**(Player Card 기여 원장: STAR + LP 매핑 + 정량지표) — 후속 하위 시스템
- ❌ 실시간 통역 강화, 미팅 중 라이브 Q&A 강화 — 별도 하위 시스템
- ❌ 독립(standalone) 그룹 엔티티 — 1차에선 Account 멤버십을 그룹으로 사용

## 3. Background (현재 상태와 재사용 가능한 자산)

| 자산 | 현재 | 이번 설계에서의 역할 |
|---|---|---|
| 태그 (`하나은행`) | 고객사 구분의 유일한 수단 (느슨) | Account 별칭(alias)으로 매핑 |
| `CRAWLER#{sourceId}` (ADR-004) | 고객사별 뉴스 크롤 + `subscribers[]` | Account에 링크 → 뉴스 인사이트 공급 |
| `Share` 엔티티 (`meeting.go:70`) | 미팅 read/edit 권한, 특정 사용자 대상 | 미팅 공유 통제(개인/Account 그룹) |
| `kb_export.GenerateMeetingDocument` | 미팅→완전한 Markdown 문서 | Obsidian 노트 본문 생성에 재사용 |
| KB 업로드 경로 (`kb.go`) + `KBFileList.tsx` | 태그·docType 문서 저장·열람 | 인바운드 인제스트 저장소 + 열람 화면 |
| MCP 서버 (ADR-003) | `ttobak_ask`/`get_meeting`/`list_meetings` | Account 스코프 도구 추가 |
| `ExtractActionItems`/`ExtractTags`/`ExtractSentiment` (`bedrock.go`) | 미팅 요약 시 Bedrock 추출 | `ExtractInsights` 동일 패턴으로 추가 |

**핵심 부재:** Account/Company를 1급 객체로 두고 미팅·뉴스·이해관계자·문서를 그 아래로 "롤업"하는 구조가 전혀 없음. 모든 데이터가 `PK: USER#{userId}` 개인 파티션에 갇혀 있음.

## 4. 소비처와 주기 (Consumers & Cadence)

| 소비처 | 주기 | 성격 | 만드는 곳 |
|---|---|---|---|
| SFDC Activity Logging | 미팅마다 | 활동 기록 | 개인 맥북(사내 MCP) |
| SIFT (Field Insight) | 월간(마감일) | 고객 트렌드·니즈·경쟁 인사이트 | 개인 맥북 |
| 2by2 (Risk/Opportunity) | 월간 | 리스크·기회 분류 매트릭스 | 개인 맥북 |
| Player Card | 분기/반기 | SA 본인 성과 포트폴리오(STAR+LP) | 개인 맥북(`playercard-agent`) |

> **2by2 주의:** 팀에서 쓰는 "2by2"의 정확한 축(Well-Architected Operating Model 2x2 vs 개인 Risk/Opportunity 사분면)은 사내 맥락이라 단정하지 않는다. 접근법(아래 §5.4) 덕분에 TTOBAK은 `risk`/`opportunity` 인사이트를 공급할 뿐 사분면 배치는 개인 맥북에서 결정하므로 무관해진다.

## 5. 핵심 설계 결정 (Key Decisions)

### 5.1 소유/공유 모델 — "공유 Account, 내 미팅은 내 것"
- **Account 자체** = 팀(AM·TAM·SSA) 공유 파티션.
- **각자의 미팅 노트** = 기본 비공개(개인 파티션). 명시적으로 Account에 공유해야 팀 롤업/열람에 포함.

### 5.2 Account 식별 — 명시 등록 + 태그 매핑
- 고객사를 1급 객체로 명시 등록(이름·별칭·도메인·산업).
- 기존 태그(`하나은행`)를 Account 별칭(`aliases`)으로 매핑. 미팅 작성/공유 시 Account 선택.

### 5.3 그룹 — Account 멤버십 = 그룹 (옵션 A)
- 별도 그룹 엔티티 없이, Account의 `MEMBER#{userId}` 집합을 공유 그룹으로 사용.
- 미팅 공유 대상으로 *개인* 또는 *Account 그룹* 선택(기존 `Share` 확장).

### 5.4 데이터 가공 — Insight Substrate (접근법 A)
- TTOBAK은 **유형·시간·출처가 붙은 인사이트 레코드**를 누적할 뿐, SFDC/SIFT/2by2/Player Card 템플릿을 **복제하지 않는다**.
- 사내 템플릿이 바뀌거나 새 소비처가 생겨도 TTOBAK은 영향받지 않음. 책임 경계 = "구조화된 사실의 적립과 검색".

### 5.5 양방향 싱크 — 출처(origin)가 방향을 결정 (루프 차단)
| 문서 출처 | 진실의 주인 | 방향 | 식별 |
|---|---|---|---|
| TTOBAK 발 (미팅·추출 인사이트) | TTOBAK | TTOBAK → vault (읽기전용 미러) | frontmatter `ttobak_id` 있음 |
| 로컬 발 (email·calendar 노트) | 로컬(vault) | vault → TTOBAK (인제스트) | `ttobak_id` 없음 |

→ TTOBAK이 팀이 보는 단일 진실 공급원(canonical surface), Obsidian은 개인 편집 도구.

## 6. 데이터 모델 (DynamoDB `ttobak-main`, single-table)

### 6.1 신규 아이템 (모두 `ACCOUNT#{accountId}` 공유 파티션)

```
ACCOUNT#{accountId} | META
  { accountId, name:"하나은행",
    aliases:["하나은행","Hana Bank","하나"],   # 태그 매핑
    domains:["hanafn.com"], industry:"Financial",
    crawlerSourceId?:"hanabank",               # CRAWLER# 링크 (뉴스 인사이트)
    ownerUserId, createdAt, updatedAt }

ACCOUNT#{accountId} | MEMBER#{userId}
  { userId, role:"AM|TAM|SSA|owner", addedAt }

ACCOUNT#{accountId} | INSIGHT#{occurredAt}#{insightId}     # ★ substrate
  { insightId, type, text,
    sourceType:"meeting|news|ingest", sourceId, sourceUserId,
    occurredAt,                # 미팅/뉴스 발생 시각 (ISO8601) = SK 정렬키
    tsMarker?:"[TS:412]",      # 트랜스크립트 딥링크 (ADR-013)
    entities?:["ROSA","Azure","신임 CTO"],
    createdAt }

ACCOUNT#{accountId} | MEETINGREF#{occurredAt}#{meetingId}  # 공유 미팅 메타
  { meetingId, ownerUserId, title, date }

ACCOUNT#{accountId} | DOC#{docId}                          # 인바운드 인제스트 문서
  { docId, title, docType, sourceUserId, ttobakOrigin:false, createdAt }
```

### 6.2 기존 아이템 변경

```
USER#{userId} | MEETING#{meetingId}   (비공개 유지)
  + AccountID?: string         # 어느 Account 소속
  + SharedToAccount: bool      # 팀 롤업/열람에 공개했나 (기본 false)

# "Account에 공유" 단일 액션은 다음을 한 번에 수행:
#   1) SharedToAccount=true 설정 → 인사이트 롤업 + MEETINGREF 적립 트리거
#   2) 현 Account 멤버 전원에게 기존 Share(read) 부여 → 전체 본문 열람
# AccountID만 설정하고 SharedToAccount=false면: 분류만 되고 비공개 유지(_Private/).

CRAWLER#{sourceId} | CONFIG    (ADR-004, 기존)
  + AccountID?: string         # 어느 Account에 뉴스 인사이트를 공급하나
```

### 6.3 GSI
- **"내 Account 목록"**: `MEMBER#{userId}` 아이템에 GSI (`GSIpk=USER#{userId}`, `GSIsk=ACCOUNT#{accountId}`).
- **인사이트 기간 조회**: SK가 `INSIGHT#{occurredAt}#...` → 단일 파티션 + SK `between` 한 방. 유형 필터는 1차에선 결과 필터(볼륨 작음), 확장 시 `ACCOUNT#{id}#TYPE#{type}` GSI 추가.

> **설계 노트:** SK 정렬키로 `createdAt`이 아니라 `occurredAt`(발생 시각)을 쓴다. 지난 미팅을 늦게 공유해도 올바른 달(SIFT/2by2 기간)에 꽂혀야 하기 때문.

## 7. 인사이트 유형 (Taxonomy)

| 유형 | 먹이는 곳 | 예 |
|---|---|---|
| `trend` | SIFT | "그룹사 전체 클라우드 전환 가속" |
| `need` | SIFT, SFDC | "DR 센터 금융보안 컴플라이언스 요구" |
| `competitive` | SIFT, 2by2 | "Azure 견적 받는 중" |
| `risk` | 2by2 | "PoC 일정 2개월 지연 가능성" |
| `opportunity` | 2by2, SFDC | "차세대 시스템 ROSA 확대 여지" |
| `tech` | SIFT, Player Card | "EKS, PrivateLink, ROSA" |
| `stakeholder` | SFDC, Player Card | "신임 CTO 부임, 클라우드 우호적" |
| `action` | SFDC | "다음주 아키텍처 리뷰 세션" |

## 8. 추출 파이프라인 (데이터 IN)

```
[트리거] 미팅 공유(→Account) | 요약 완료(AccountID 有) | 크롤러 Account-매칭 문서 | put_document(인제스트)
        ↓
ExtractInsights (Bedrock Haiku/Sonnet, 단일 책임 프롬프트)
   입력: refined transcript + 구조화 요약 + [TS:NNN] 마커 (뉴스/문서는 본문)
   출력: [{type, text, tsMarker?, entities?}]   # 8개 유형으로만 분류
        ↓
ACCOUNT#{accountId} 파티션에 INSIGHT# 적립 (occurredAt = 출처 발생 시각)
```

원칙:
- **Best-effort 분리**: 추출 실패해도 미팅 요약·저장은 안 깨짐(기존 extract들과 동일 decouple).
- **멱등**: 같은 `sourceId` 재처리 시 기존 인사이트 교체(중복 방지).
- **역추적**: 인사이트는 `tsMarker`로 원문 순간에 연결(작성자에게만 열림).

> `ExtractInsights`를 기존 요약 프롬프트에 합치지 않고 별도 Bedrock 호출로 두는 이유: 단일 책임 프롬프트가 분류 정확도·JSON 파싱 안정성 모두에서 유리. `ExtractActionItems` 코드 모양을 그대로 복제.

## 9. MCP 도구 (데이터 OUT/IN) — 출력은 전부 구조화된 원재료, 문서 조립 안 함

| 도구 | 반환 | 용도 |
|---|---|---|
| `ttobak_list_accounts` | 내 소속 Account + 통계 | 진입점 |
| `ttobak_get_account` | Account 메타·멤버·태그 | 컨텍스트 |
| `ttobak_list_meetings(account?, from, to, scope:own\|shared\|all, updatedAfter?)` | 미팅 메타 목록 — Account별·시간대별·증분 | Vault 동기화 진입점 |
| `ttobak_get_meeting(meetingId, format:json\|markdown)` | 세부 전체(트랜스크립트·요약·노트·액션·첨부) / Obsidian Markdown | Vault 노트 본문 |
| `ttobak_export_vault(account?, from, to, updatedAfter?)` | `[{path, markdown}]` 일괄(미팅 노트 + Account MOC) | Obsidian Vault 미러링 |
| `ttobak_get_insights(account, from, to, types[])` | 유형·기간별 인사이트 배열 | SIFT·2by2 |
| `ttobak_get_account_activity(account, from, to)` | 공유 미팅 메타+요약+action 인사이트 | SFDC Activity |
| `ttobak_get_account_brief(account, period)` | 위를 묶은 단일 원재료 페이로드 | 개인 에이전트 일괄 소비 |
| `ttobak_list_documents(account, docType?)` | Account KB 문서 목록(인제스트·준비자료·뉴스) | 다음 스펙(B) deliverable 소스 |
| `ttobak_get_document(docId)` | 문서 본문 | 다음 스펙(B) deliverable 소스 |
| `ttobak_put_document(account, path, markdown, docType, shared)` | 인제스트 결과 (`docType:"prep"` = 미팅 준비 자료) | 로컬 vault → TTOBAK (인바운드) |

소비 예:
```
"하나은행 5월 SIFT 써야 해"
 → ttobak_get_insights("하나은행","2026-05-01","2026-05-31",["trend","need","competitive"])
 → 개인 에이전트가 SIFT 양식으로 조립
```

## 10. Obsidian Vault 레이아웃 + OneDrive 디렉터리 공유

단일 vault 유지(그래프·검색·Dataview 전체 동작) + **공유 경계 = 디렉터리 경계**로 배치하여 OneDrive 폴더 단위 공유를 가능케 한다.

```
Vault/
  Accounts/
    하나은행/        ◀── OneDrive로 하나은행 팀(AM·TAM·SSA)에게 폴더 공유
      _하나은행.md                  # MOC: 메타 + [[미팅]] 백링크 + 인사이트 롤업
      2026-05-12 ROSA PoC 리뷰.md   # 공유된 미팅(전체 본문)
    삼성전자/        ◀── 다른 멤버 집합에게 폴더 공유
  _Private/
    Meetings/        ◀── 공유 안 한 내 미팅 (어느 공유 폴더에도 안 들어감)
```

미팅 노트 frontmatter (Dataview 쿼리 가능):
```yaml
---
account: "[[하나은행]]"
date: 2026-05-12
participants: [김팀장, 이수석]
tags: [meeting, 하나은행, ROSA]
insights: { risk: 1, opportunity: 2, competitive: 1 }
ttobak_id: <meetingId>
---
```

규칙:
- 미팅이 Account에 **공유됨** → `Accounts/{account}/`에 전체 본문.
- Account 태그만 달고 **비공개** → `_Private/Meetings/`.
- 각 `Accounts/{account}/`를 OneDrive에서 해당 Account 멤버에게만 공유(멤버 집합 = TTOBAK Account 멤버십).
- (운영) 받는 사람은 OneDrive "내 파일에 바로가기 추가"를 최초 1회 수행.

폴더명 관례(`Accounts/`, `_Private/`)는 사용자 기존 vault에 맞춰 변경 가능 — **구현 시 확정**.

## 11. 프런트엔드 표면 (최소)

| 화면 | 내용 | 재사용 |
|---|---|---|
| Account 등록/관리 | 이름·별칭(태그 매핑)·도메인·산업, 멤버 초대(email+역할) | 신규(소형) |
| 미팅↔Account 연결 | Account 선택 + "이 Account에 공유" 토글 | `MeetingDetailClient` 확장 |
| Account 상세 | 멤버·공유 미팅·인사이트(유형/기간 필터)·인제스트/뉴스 문서 | `KBFileList`·`AISummaryCard` 재사용 |

→ 비-Obsidian 팀원은 Account 상세 화면만으로 공유 미팅·인사이트·email/calendar 문서를 모두 열람(= "TTOBAK만 보고 공유").

## 12. 권한 · 보안 · 에러 · 엣지케이스

**권한 (서버측 강제):**
- Account 풀·공유 미팅 읽기 = 멤버십 검사. `ErrForbidden`/`ErrNotFound` 센티넬(기존 패턴). 클라이언트 신뢰 안 함.
- MCP는 기존 `ttobak_login` 세션 인증 위에서 동작. **신규 공개 엔드포인트 없음**(CLAUDE.md 보안 규칙 — 전부 CloudFront/기존 경로, IAM 최소권한).
- 고객 이해관계자·계정 정보는 PII → 기존 `ttobak-main` KMS 암호화 범위 내.

**싱크 안정성:**
- **루프 차단**: `put_document`는 `ttobak_id`가 이미 있는 문서(TTOBAK 발)는 거부.
- **증분**: `updatedAfter` 커서로 변경분만.
- **멱등 추출**: 같은 `sourceId` 재처리 시 인사이트 교체.

**엣지케이스:**
- 별칭이 두 Account에 매핑 → 에러로 표면화, 자동 선택 금지.
- 인사이트 추출 실패 → 미팅 요약/저장 안 깨짐.
- Account 삭제/멤버 탈퇴 시 공유 미팅 ref·인사이트 가시성 정리(권한 재평가).

## 13. 테스트 전략
- **Go 단위**: Account repo(멤버십·권한), Insight repo(기간 range 쿼리), 태그 매핑 충돌, 미팅↔Account 링크.
- **추출**: `ExtractInsights` 프롬프트 + JSON 파싱 fixture 테스트(8개 유형 분류).
- **MCP**: 도구 11종 통합 테스트(권한 경계 포함).
- **Vault export**: Markdown/frontmatter 스냅샷 테스트, 공유/비공개 폴더 분기.
- **인바운드**: `put_document` 루프 차단(ttobak_id 거부), Account 스코프 저장.
- **CDK jest**: 신규 GSI / IAM 최소권한.

## 14. Future / 후속 하위 시스템

### 14.1 챗 에이전트 deliverable 출력 (바로 다음 스펙) — no-Mac 시나리오
**동기:** 맥을 켜기 어려운 현장에서, TTOBAK 챗 에이전트(전용 비서) 안에서 고객 미팅 자료를 즉석 생성.
- 기존 Q&A 에이전트(`backend/python/qa/`)의 agentic tool-use 루프에 **deliverable 생성 도구** 추가.
- 소스 = 이 기반이 제공하는 **Account KB(`prep`/뉴스/인제스트) + 인사이트**(`list_documents`/`get_document`/`get_insights`).
- 산출: 발표용 Markdown/HTML 덱 + Notion 페이지(Notion MCP / 기존 `ExportMenu` 재사용). 실제 `.pptx` 렌더링은 별도 난이도 → 단계적.
- **의존성:** 본 스펙(Account + substrate + MCP + KB 인제스트)이 선행되어야 함. 본 스펙은 (B)가 소비할 데이터 훅(`list_documents`/`get_document`)을 미리 보장한다.

### 14.2 기타
- **Self 축 (Player Card)**: 기여 원장(STAR 후보 + 정량지표 + LP 매핑) 누적 + `playercard-agent`의 `00_Raw_Data` 공급. 발표·CSAT·ARR 등 외부 증빙 입력 경로 필요.
- **독립 그룹 엔티티**: Account와 무관한 재사용 공유 그룹.
- **인사이트 유형 GSI**: 볼륨 증가 시 `ACCOUNT#{id}#TYPE#{type}` GSI.
- **실시간 통역/라이브 Q&A 강화**: 별도 스펙.

## 15. 미해결/구현 시 확정
- Obsidian vault 폴더명 관례(`Accounts/`, `_Private/`) — 사용자 기존 vault에 맞춤.
- `ExtractInsights` 모델 선택(Haiku vs Sonnet) — 분류 품질 vs 비용 벤치 후 결정.
- `export_vault` 페이로드 크기 한계 — 대량 미팅 시 페이지네이션/청크 전략.
