# ADR-040: Guarded summary publication

Accepted / 승인됨 — 2026-09-12.

Capture actual source presence/bytes before generation. CAS protects human text
and supplied attachment rows, excluding unrelated metadata. Never rebind old
output after edits: discard it and regenerate with at most two durable retries.
Release failed-read/run claims; exhaustion is visible error. Limits/read failures
are not source conflicts. Final S3 HEAD and DynamoDB cannot be atomic.

Preserve valid claims, markdown and file links; distinguish omitted/excerpted
input from rejected citations. Reject incomplete or insubstantial output.
Batch guards are active; document collection and saved-summary APIs follow.
Retain ambiguous spills (ADR-037). These guarantees cost extra reads/conflicts.

입력과 사용자 본문을 조건으로 보호하고 충돌 시 새로 생성합니다. 실패 claim을
해제하고 재시도 소진은 오류로 끝냅니다. S3와 DynamoDB는 원자적이지 않습니다.
배치 보호는 활성 코드이며 문서 수집·재요약은 후속입니다.

[Release/recovery](../runbooks/meeting-document-release.md)
