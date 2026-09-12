# ADR-040: Guarded summary publication

Accepted / 승인됨 — 2026-09-12.

## Decision
Reject unconditional saves and rebinding old output. Capture exact presence and
actual inputs before generation; exclude unrelated metadata. Verify selected S3
bytes with IfMatch/ETag/version, and atomically guard supplied attachment rows.
S3's final HEAD and DynamoDB cannot be atomic. Preserve human content on conflict.
Two durable retry claims regenerate from fresh inputs without STT/refinement;
release failed-read claims and terminate exhaustion visibly. Limits are not conflicts.

Unavailable evidence, input excerpts and rejected claims are separate. Keep
uploaded-file links; remove unsupported claim units, not valid sibling items.
Reject heading/notice-only or incomplete output. Only verified documents enable
DOC prompts; exclude storage/user identity. Extra reads/conflicts are the cost.
Batch guards/recovery are active; document collection and saved-summary APIs ship
in follow-ups. Preserve ambiguous immutable spills (ADR-037).

## 결정
생성 전 입력의 존재 여부·바이트·첨부 행을 고정합니다. 충돌 시 본문을 보존하고
최대 두 번 새 입력으로 재생성하며 소진은 오류로 종결합니다. 일반 읽기 오류와
한도 초과는 충돌이 아닙니다. 근거 누락·발췌·인용 거절을 구분하고 파일 링크와
정상 주장을 유지합니다. 배치 복구는 활성 코드이며 문서 수집·재요약은 후속입니다.

[Release/recovery](../runbooks/meeting-document-release.md)
