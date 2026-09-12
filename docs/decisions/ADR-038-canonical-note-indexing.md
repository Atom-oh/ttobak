# ADR-038: Canonical Note Indexing

## English

### Status

Accepted — 2026-09-12. Worker code is staged; automatic triggers remain disabled.

### Context and options

Manual exports miss edits/deletion. Direct ingestion would add a second writer
beside existing S3 synchronizations. Retain the S3 data source and coalesce full
syncs instead.

### Decision

Use canonical meetings and personal/account documents, durable revision/lease
jobs, immutable projections and coalesced full sync. Bind exact saved fields and
S3 object versions; require successful ingestion plus current-source checks.
Recover missed events/deletion with paginated reconciliation. Isolate changing
sources and back off failures without holding other jobs.

### Consequences

Search has eventual consistency, request cost and cleanup overhead. Retrieval
must authorize current records and reject stale bindings despite stored success.

Deploy/verify canonical QA first, configure the worker second, enable delivery
last. Old QA filters recognize only legacy `meetings/{owner}/` URIs. Cleanup
removes those exports, so reversing the order loses recall. Rollback to old QA
requires re-export; stopping the worker does not restore deleted exports.

### References

[Exact schema, revision vectors and rollout](../superpowers/plans/2026-09-12-automatic-note-indexing-backend.md)

## 한국어

### 상태

승인 — 2026-09-12. 워커 코드는 준비 단계이며 자동 실행은 아직 비활성입니다.

### 배경과 대안

수동 내보내기는 수정·삭제를 놓칩니다. 기존 S3 동기화와 별도로 직접 색인을
추가하지 않고 같은 데이터 소스의 전체 동기화 요청을 묶습니다.

### 결정

회의·개인·어카운트 문서를 원본으로, 버전·임대·불변 객체와 동기화 세대를
영속화합니다. 접수만으로 완료 처리하지 않고 현재 원본을 다시 확인합니다.
변경 중인 원본은 분리하고 실패는 재시도 간격을 늘립니다.

### 결과

검색 반영에는 지연과 요청 비용이 있으며 불변 객체 정리가 필요합니다. 검색은
현재 권한과 파일 버전을 재검증해야 합니다. 새 검색을 먼저 검증·배포한 뒤
워커 설정과 자동 실행을 적용합니다. 기존 QA는 `meetings/{owner}/`만 검색하므로
레거시 삭제를 먼저 하면 검색이 끊깁니다. 기존 QA로 롤백하려면 재내보내기가
필요하며 워커 중지만으로 복원되지 않습니다.

### 참고

[스키마·검증 벡터·배포 순서](../superpowers/plans/2026-09-12-automatic-note-indexing-backend.md)
