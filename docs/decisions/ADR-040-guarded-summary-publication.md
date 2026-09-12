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
무조건 저장과 이전 출력의 조건 재바인딩을 거부합니다. 생성 전에 정확한 존재 여부와
실제 입력을 캡처하고 관련 없는 메타데이터는 제외합니다. S3 바이트와 첨부 행을
검증하되 최종 HEAD와 DynamoDB의 원자성 한계를 인정합니다. 충돌 시 본문을 보존하고
최대 두 번 새 입력으로 재생성합니다. 읽기 실패 claim을 해제하고 소진은 즉시 오류로
종결하며 한도 초과는 충돌로 재시도하지 않습니다. 근거 미제공·발췌·인용 거절을 구분하고
파일 링크와 정상 목록 항목을 유지합니다. 실질 본문 없는 결과와 미완료 응답은 거부하며
모델에 저장소/사용자 식별자를 보내지 않습니다. 추가 읽기/충돌 비용을 수용합니다.
배치 복구는 활성 코드이며 문서 수집·재요약 API는 후속입니다. 불확실한 spill은 보존합니다.

[Release/recovery](../runbooks/meeting-document-release.md)
