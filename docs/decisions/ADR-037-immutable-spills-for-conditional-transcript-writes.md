# ADR-037: Immutable Spills for Conditional Transcript Writes

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status

Accepted — 2026-09-12

## Context

Speaker renaming derives new text from a meeting snapshot. Matching the original
`updatedAt` prevents a stale database update, but the previous spill operation
overwrote `{field}.txt` before that check. A rejected rename could therefore
replace the transcript still referenced by newer data.

## Options Considered

| Option | Benefit | Limitation |
| --- | --- | --- |
| Check the database before overwriting the fixed key | Small change | Another write can still occur between the check and the S3 overwrite |
| Reject all changes requiring a spill | Avoid that overwrite | Prevent speaker renaming on large meetings |
| Upload a unique object and conditionally publish its reference | Preserve existing objects on conflict | Require compatible readers and retain additional objects |

## Decision

Use `transcripts/{meetingId}/{field}.{32-lowercase-hex}.txt` for spills made by
`UpdateMeetingFieldsIfMatch`. Generate a new UUID suffix for each upload, preserve
the caller's input map, and publish references only through the conditional
database update. Keep the existing fixed-key behavior for unconditional writes.

Disable SDK retries for a database write carrying new spills. A definite
condition rejection can then attempt cleanup of its uncommitted objects.
Retain objects after an ambiguous database response: deleting them could break
a write that actually committed. Surface cleanup failures. Retain previously
committed objects so an in-flight reader can finish.

Deploy the Go and Q&A readers before enabling the writer. Both formats remain
bound to the configured bucket, authorized meeting ID, and allowed field.
This does not require S3 bucket versioning or new IAM grants. Keep compatible
readers when rolling back the writer.

## Consequences

### Positive

- Preserve referenced transcript bytes when a stale rename is rejected.
- Keep database fields, analysis results, and completion checks consistent.
- Verify definite rejection, successful commit, and ambiguous response through SDK HTTP tests.

### Negative

- Retain older committed versions and possibly orphaned uploads after uncertain outcomes.
- Require a separate reclamation policy for those objects; this change adds no garbage collector.
- Return transient write errors without an automatic SDK retry when spills are involved.

## References

- [Reader rollout](../runbooks/qa-transcript-read-rollout.md)
- [PR195](https://github.com/Atom-oh/ttobak/pull/195)
- [PR196](https://github.com/Atom-oh/ttobak/pull/196)

---

<a id="korean"></a>

# 한국어

## 상태

승인됨 — 2026-09-12

## 배경

화자 이름 변경은 읽어 둔 미팅으로부터 새 텍스트를 만듭니다. 원래
`updatedAt`을 비교하면 오래된 DB 갱신을 막을 수 있지만, 기존 spill은
이 검사 전에 `{field}.txt`를 덮어썼습니다. 따라서 거절된 이름 변경이
더 최신 데이터에서 참조하는 전사문을 바꿀 수 있었습니다.

## 검토한 옵션

| 옵션 | 장점 | 한계 |
| --- | --- | --- |
| 고정 키를 덮어쓰기 전에 DB 확인 | 변경량이 작습니다 | 확인과 S3 덮어쓰기 사이에 다른 쓰기가 발생할 수 있습니다 |
| spill이 필요한 변경을 모두 거절 | 해당 덮어쓰기를 막습니다 | 큰 미팅의 화자 이름 변경이 불가능해집니다 |
| 고유 객체를 업로드하고 참조를 조건부 반영 | 충돌 시 기존 객체를 보존합니다 | 호환 가능한 reader와 추가 객체 보관이 필요합니다 |

## 결정

`UpdateMeetingFieldsIfMatch`의 spill에는
`transcripts/{meetingId}/{field}.{32-lowercase-hex}.txt`를 사용합니다.
업로드마다 새 UUID 접미사를 만들고 호출자의 입력 map을 보존하며,
조건부 DB 갱신으로만 참조를 반영합니다. 무조건 갱신 경로는 기존 고정 키
동작을 유지합니다.

새 spill을 포함한 DB 쓰기에서는 SDK 재시도를 비활성화합니다. 그러면
조건 거절이 확실한 경우 미반영 객체의 정리를 시도할 수 있습니다.
DB 응답이 불확실하면 실제 반영됐을 수 있으므로 객체를 보존합니다.
정리 실패는 오류로 드러냅니다. 이미 반영된 이전 객체는 진행 중인 읽기를
위해 보존합니다.

쓰기 기능을 활성화하기 전에 Go와 Q&A reader를 배포합니다. 두 형식 모두
설정된 버킷, 인가된 미팅 ID, 허용된 필드에 고정합니다. S3 버킷 버전 관리나
새 IAM 권한은 필요하지 않습니다. writer를 롤백해도 호환 reader는 유지합니다.

## 영향

### 긍정적

- 오래된 이름 변경이 거절돼도 참조 중인 전사문 바이트를 보존합니다.
- DB 필드, 분석 결과, 완료 체크의 정합성을 유지합니다.
- SDK HTTP 테스트로 확실한 거절, 성공한 반영, 불확실한 응답을 검증합니다.

### 부정적

- 이전에 반영된 버전과 결과가 불확실한 업로드의 고아 객체가 남을 수 있습니다.
- 별도 객체 회수 정책이 필요하며, 이번 변경은 자동 정리기를 추가하지 않습니다.
- spill이 있으면 일시적인 쓰기 오류를 SDK 자동 재시도 없이 반환합니다.

## 참고 자료

- [Reader 배포 순서](../runbooks/qa-transcript-read-rollout.md)
- [PR195](https://github.com/Atom-oh/ttobak/pull/195)
- [PR196](https://github.com/Atom-oh/ttobak/pull/196)
