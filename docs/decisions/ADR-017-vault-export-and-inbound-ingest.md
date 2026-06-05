# ADR-017: Obsidian Vault Export and Inbound Document Ingest with Loop-Guard

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — implemented on `feat/account-foundation` (Plan 5 of 6). `VaultService.ExportVault` (`GET /api/vault/export`) and account documents (`PutDocument`/`ListDocuments`/`GetDocument`) are live, plus the `export_vault`/`put_document` MCP tools ([ADR-018](ADR-018-mcp-back-data-tools.md)).

## Context
SAs live in their own knowledge tools (Obsidian, Notion). TTOBAK should not be a walled garden: meeting knowledge must flow **out** as portable markdown, and external notes must flow **in** as account documents. The dangerous case is a **round-trip loop**: TTOBAK exports a meeting → the SA's vault syncs it back in as a "document" → it re-exports → infinite duplication. We need a guard that recognizes TTOBAK-originated content and refuses to re-ingest it.

## Decision
- **Export**: `ExportVault` renders the caller's owned meetings as Obsidian-ready markdown `VaultFile`s. Placement encodes sharing: meetings shared to an account go under `Accounts/{name}/`, the rest under `_Private/Meetings/`. Frontmatter (`account: "[[name]]"`, date, participants, tags, `insights: {...}` counts, `ttobak_id`) drives Obsidian's graph. Listing paginates over all owned meetings.
- **Inbound ingest**: `PutDocument` stores external markdown as an account document (`SK=DOC#{docId}`, inline ≤300 KB), member-gated.
- **Loop-guard**: ingest rejects any markdown carrying the TTOBAK origin marker (the `ttobak_id` frontmatter we emit on export) with the `ErrLoopGuard` sentinel → `400`. The guard strips a leading UTF-8 BOM before checking, so a BOM-prefixed re-import cannot bypass it (the fix in commit `f5ba17c`).

## Consequences

### Positive
- Two-way interoperability with Obsidian without lock-in.
- Loop-guard prevents export→reimport duplication, including the BOM-bypass variant.
- Shared/private placement gives a sensible default vault hierarchy out of the box.

### Negative
- Account MOC (Map of Content) index files are a noted follow-up; only per-meeting notes are emitted today.
- 300 KB inline cap means very large pasted documents are rejected (no S3 overflow path yet, unlike transcripts).

### Risks
- The loop-guard depends on the origin marker surviving the round-trip; a user who strips `ttobak_id` defeats it (acceptable — it's a foot-gun guard, not a security control).

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| Marker-based loop-guard + BOM strip (chosen) | Cheap, robust to BOM bypass | Defeated if user edits out the marker |
| Content-hash dedup of ingested docs | Catches edited re-imports | Cost/complexity; false positives on legitimate edits |
| No guard | Simplest | Export↔sync loops duplicate indefinitely |

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — `feat/account-foundation`에서 구현(Plan 5/6). `VaultService.ExportVault`(`GET /api/vault/export`)와 계정 문서(`PutDocument`/`ListDocuments`/`GetDocument`) 동작 중. `export_vault`/`put_document` MCP 도구 포함([ADR-018](ADR-018-mcp-back-data-tools.md)).

## 맥락
SA는 자신의 지식 도구(Obsidian, Notion)에서 일한다. TTOBAK이 폐쇄형이어선 안 된다: 회의 지식은 휴대 가능한 마크다운으로 **밖으로** 나가고, 외부 노트는 계정 문서로 **안으로** 들어와야 한다. 위험한 경우는 **왕복 루프**다: TTOBAK이 미팅을 내보냄 → SA vault가 이를 "문서"로 다시 동기화 → 재내보내기 → 무한 중복. TTOBAK 발신 콘텐츠를 인식해 재인제스트를 거부하는 가드가 필요하다.

## 결정
- **내보내기**: `ExportVault`가 호출자 소유 미팅을 Obsidian용 마크다운 `VaultFile`로 렌더링. 배치가 공유 상태를 인코딩: Account 공유 미팅은 `Accounts/{name}/`, 나머지는 `_Private/Meetings/`. 프론트매터(`account: "[[name]]"`, 날짜, 참석자, 태그, `insights: {...}` 카운트, `ttobak_id`)가 Obsidian 그래프를 구동. 목록은 소유 미팅 전체를 페이지네이션.
- **인바운드 인제스트**: `PutDocument`가 외부 마크다운을 계정 문서로 저장(`SK=DOC#{docId}`, 인라인 ≤300KB), 멤버 게이트.
- **루프 가드**: 내보낼 때 우리가 넣는 `ttobak_id` 프론트매터(TTOBAK 출처 마커)를 가진 마크다운은 `ErrLoopGuard`로 거부 → `400`. 확인 전 선행 UTF-8 BOM을 제거하므로 BOM 선행 재임포트로 우회 불가(커밋 `f5ba17c` 수정).

## 결과

### 긍정
- 종속 없이 Obsidian과 양방향 상호운용.
- 루프 가드가 내보내기→재임포트 중복(및 BOM 우회 변종)을 방지.
- 공유/비공개 배치로 기본 vault 계층을 즉시 제공.

### 부정
- Account MOC(Map of Content) 인덱스 파일은 후속 과제로 명시; 현재는 미팅별 노트만 생성.
- 300KB 인라인 한도로 매우 큰 붙여넣기 문서는 거부(트랜스크립트와 달리 S3 오버플로 경로 아직 없음).

### 위험
- 루프 가드는 출처 마커가 왕복에서 살아남는 데 의존; `ttobak_id`를 지운 사용자는 우회 가능(수용 — 보안 통제가 아닌 실수 방지 가드).

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| 마커 기반 루프 가드 + BOM 제거(채택) | 저렴, BOM 우회에 견고 | 사용자가 마커 제거 시 무력화 |
| 인제스트 문서 콘텐츠 해시 중복 제거 | 편집된 재임포트도 포착 | 비용/복잡도, 정상 편집에 오탐 |
| 가드 없음 | 가장 단순 | 내보내기↔동기화 루프가 무한 중복 |
