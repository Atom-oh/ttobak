# ADR-040: Guarded summary publication

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — 2026-09-12.

## Context
Summary generation can overlap human edits. New meetings omit optional text
attributes, so comparing every absent field to an empty string rejects the first
summary. A failed document must not discard otherwise valid meeting notes.

## Options considered
1. Save unconditionally: simple, but overwrites concurrent edits.
2. Treat absent and empty as equivalent globally: permits first saves, but
   weakens unrelated writers and loses observed attribute presence.
3. Capture exact source attributes and publish conditionally: retain source
   fidelity at the cost of explicit conflicts. Select this option.

## Decision
Capture a strong canonical read before hydration/model invocation. Preserve each
attribute's presence and exact stored value in `SummaryCheck`; use it for the
batch-summary write. Keep general conditional-write semantics unchanged.

Treat verified document text as separate DOCUMENT evidence. Omit unavailable
documents with a coverage notice. Drop an entire paragraph containing an invalid
document citation, not just its marker. Reject notice-only results and incomplete
model responses. Do not claim omitted document revisions were included.

Separate saved-summary orchestration uses `MEETING#id / ANALYSIS#summary`, a run,
source hash and lease, and atomic content/coverage/success publication. Guard
current edit grants and sources. Preserve prior content on failure. Delete source,
action analysis, sim and summary state together as four initial transaction
entries; subsequent attachment/share pairs stay within 100-item boundaries.

## Consequences
Protect human edits and permit first summaries without weakening CAS. Document
failures can yield useful notes with explicit omissions. Canonical source changes
still reject publication. Immutable spill ambiguity retains objects as specified
by ADR-037. The existing batch summarizer changes when deployed; this foundation
is not dormant. Release API producers only after their consumers are deployed.

## References
- [Immutable spills](ADR-037-immutable-spills-for-conditional-transcript-writes.md)
- [Document extraction](ADR-039-meeting-document-extraction.md)
- [Release and rollback](../runbooks/meeting-document-release.md)

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — 2026-09-12.

## 배경
요약 생성과 사용자 편집이 겹칠 수 있습니다. 새 회의의 선택적 텍스트 속성은
생략되므로 빈 문자열과 비교하면 첫 요약이 실패합니다. 문서 하나의 실패로
정상적인 회의록까지 버려서는 안 됩니다.

## 검토한 옵션
1. 무조건 저장: 간단하지만 동시 편집을 덮어씁니다.
2. 전역에서 부재와 빈 문자열을 동일시: 첫 저장은 되지만 다른 쓰기의 조건을
   약화하고 관측한 속성 존재 여부를 잃습니다.
3. 정확한 소스 캡처 후 조건부 저장: 충돌을 명시하면서 소스를 보존하므로 선택합니다.

## 결정
원문 로딩과 모델 호출 전에 canonical 행을 강하게 읽습니다. 각 속성의 존재
여부와 저장값을 `SummaryCheck`에 보존해 배치 요약 저장 조건으로 사용합니다.
일반 조건부 쓰기의 의미는 바꾸지 않습니다.

검증된 문서는 별도 DOCUMENT 근거로 다룹니다. 읽을 수 없는 문서는 범위 안내와
함께 제외합니다. 잘못된 문서 인용은 표식만 지우지 않고 문단 전체를 제외합니다.
안내문만 남은 결과와 미완료 모델 응답은 거부하며, 제외한 문서가 반영됐다고
기록하지 않습니다.

별도 재요약 처리는 `MEETING#id / ANALYSIS#summary`, run·소스 해시·lease를 사용하고
본문·반영 범위·성공 상태를 원자적으로 저장합니다. 현재 편집 권한과 소스를
조건으로 확인하고 실패 시 이전 본문을 유지합니다. 회의·액션 분석·sim·요약 상태를
선두 네 항목에서 함께 삭제해 이후 첨부/공유 쌍이 100개 경계를 넘지 않게 합니다.

## 영향
CAS를 약화하지 않고 사용자 편집과 첫 요약을 보호합니다. 문서 실패가 있어도
제외 범위를 밝힌 유용한 회의록을 만들 수 있지만 canonical 소스 변경은 저장을
거부합니다. 불확실한 spill 결과는 ADR-037에 따라 객체를 보존합니다. 이 기반은
배포 시 기존 배치 요약 동작을 바꾸며 휴면 코드가 아닙니다. 소비자 배포 후에만
API 생산자를 배포합니다.

## 참고 자료
- [불변 spill](ADR-037-immutable-spills-for-conditional-transcript-writes.md)
- [문서 추출](ADR-039-meeting-document-extraction.md)
- [배포와 롤백](../runbooks/meeting-document-release.md)
