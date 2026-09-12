# ADR-038: Canonical Note Indexing

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

<a id="english"></a>

## English

**Status:** Accepted — 2026-09-12. Worker implementation is staged; automatic
delivery and current-source retrieval must be deployed before activation.

### Context and options

Manual exports miss edits and deletion. Direct ingestion would introduce a
second writer beside existing S3 full synchronizations. We retain the S3 data
source and coalesce full ingestion jobs.

### Decision

Canonical sources are owner-partition meetings and personal/account documents.
`KBINDEX#JOBS` records desired revisions, leases and immutable projection keys;
`KBINDEX#CONTROL/STATE` freezes each export/sync generation. Revisions bind raw
saved fields and current S3 ETag/version/size. Persist a client token before
submitting ingestion; acceptance alone never means indexed.

The worker rereads current sources, conditionally commits state, removes obsolete
canonical/legacy meeting exports, and paginates reconciliation to recover missed
events and existing records. Same-revision requests preserve active preparation;
source edits invalidate stale completion. Retain uncertain writes for recovery.

Retrieval must authorize current canonical records and reject stale file
revisions. Index metadata is provenance, not a grant. Converter previews must
bind to the current original. Deploy these readers before enabling streams and
the scheduled worker. Preserve both during rollback.

### Consequences

Search becomes eventually consistent with saved sources and deletion, with
explicit pending/failure states. Full syncs, source checks and reconciliation
cost requests and time; immutable projections require cleanup. Cross-service
S3/DynamoDB checks are not atomic, so retrieval revalidation remains necessary.

Implementation and cross-language revision vectors:
[worker contract](../superpowers/plans/2026-09-12-automatic-note-indexing-backend.md).

<a id="korean"></a>

## 한국어

**상태:** 승인 — 2026-09-12. 워커 코드는 준비 단계이며, 자동 실행 전에
이벤트 전달과 현재 원본을 검증하는 검색 경로를 배포해야 합니다.

수동 내보내기는 수정·삭제를 놓칩니다. 기존 S3 전체 동기화와 별도로 직접
색인 쓰기를 추가하지 않고, 같은 데이터 소스의 동기화 요청을 묶습니다.

소유자 파티션의 회의와 개인·어카운트 문서를 원본으로 삼습니다.
`KBINDEX#JOBS`에는 버전·임대·불변 객체 목록을,
`KBINDEX#CONTROL/STATE`에는 내보내기와 동기화의 고정된 세대를 저장합니다.
원본 필드와 S3 ETag·버전·크기를 묶어 변경을 확인하고, 요청 토큰을 먼저
저장합니다. 동기화 요청이 접수됐다는 이유만으로 색인 완료로 표시하지 않습니다.

중복 요청은 같은 버전의 실행을 유지하고, 실제 수정은 오래된 완료 쓰기를
막습니다. 페이지를 순회하는 복구 작업이 누락 이벤트·기존 자료·삭제를
처리하며, 결과가 불명확한 쓰기는 복구할 수 있게 보존합니다.

검색은 현재 원본의 권한과 파일 버전을 다시 확인해야 합니다. 색인 메타데이터는
접근 권한이 아닙니다. 미리보기도 현재 원본과 연결돼야 합니다. 검증하는
읽기 경로를 먼저 배포한 뒤 자동 실행을 켜고, 롤백에도 이 경계를 유지합니다.

저장과 검색은 지연을 두고 일치하며 대기·실패 상태를 드러냅니다. 전체 동기화와
복구에는 시간·요청 비용이 들고 불변 객체 정리가 필요합니다. S3와 DynamoDB
검사는 원자적이지 않으므로 검색 시 재검증을 생략할 수 없습니다.
