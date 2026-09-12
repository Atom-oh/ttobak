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
Capture exact presence in `SummaryCheck` before hydration/model invocation.
Pin actual inputs (transcript candidates/selection, notes, captured live summary),
protected content/coverage and execution state; exclude unrelated metadata.
Keep general CAS semantics unchanged. On conflict discard output and persist a
retry marker, releasing only the observed claim. Lambda redelivery claims and
regenerates from fresh sources without STT/refinement; never refresh expected
values to reuse old output. Success clears the marker; failed writes surface.

Treat verified document text as separate DOCUMENT evidence. Omit unavailable
documents with a coverage notice. Drop the affected paragraph or list item, including its continuations, not only
the citation marker; retain valid sibling items. Reject heading/notice-only and
incomplete results. Keep done-file links and input coverage independent from
citation rejection. Only verified documents enable DOC instructions; exclude
bucket/key/user/ETag values from model input.

The follow-up saved-summary orchestration uses `MEETING#id / ANALYSIS#summary`, a run,
source hash and lease, and atomic content/coverage/success publication. Guard
current edit grants and sources. Preserve prior content on failure. Delete source,
action analysis, sim and summary state together as four initial transaction
entries; subsequent attachment/share pairs stay within 100-item boundaries.

## Consequences
Protect human edits and permit first summaries without weakening CAS. Document
failures can yield useful notes with explicit omissions. Canonical source changes
still reject publication. Immutable spill ambiguity retains objects as specified
by ADR-037. CAS, conflict recovery and output filtering change the existing batch path on
merge/deployment. DOCUMENT fetching remains dormant until caller injection in
the wiring release. Lambda retry limits are finite; pending markers support
later redelivery/manual recovery. Release producers after deployed consumers.

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
모델 호출 전에 속성 존재 여부를 `SummaryCheck`에 캡처합니다. 실제 입력인
전사문 후보/선택·메모·캡처한 live summary와 보호할 본문/반영 범위 및 실행 상태만
조건으로 확인하고 관련 없는 메타데이터는 제외합니다. 일반 CAS는 바꾸지 않습니다.
충돌 시 출력을 버리고 재시도 표식을 저장하며 관측한 claim만 해제합니다.
Lambda 재전달은 새 소스로 모델을 다시 호출하고 전사/정제를 반복하지 않습니다.
이전 출력의 expected 값만 바꿔 저장하지 않습니다. 성공 시 표식을 지우고 저장
실패는 오류로 반환합니다.

검증된 문서는 별도 DOCUMENT 근거로 다루고 읽지 못한 근거는 안내와 함께
제외합니다. 잘못된 인용은 해당 문단 또는 연속 내용을 포함한 목록 항목을
제외하되 정상 형제 항목을 유지합니다. 제목/안내문만 남거나 미완료인 결과는
거부합니다. 다운로드 링크·입력 범위·인용 거절은 구분합니다. 실제 검증된 문서가
있을 때만 DOC 지시문을 넣고 bucket/key/user/ETag는 모델 입력에서 제외합니다.

후속 재요약 저장소·처리는 `MEETING#id / ANALYSIS#summary`, run·소스 해시·lease를 사용하고
본문·반영 범위·성공 상태를 원자적으로 저장합니다. 현재 편집 권한과 소스를
조건으로 확인하고 실패 시 이전 본문을 유지합니다. 회의·액션 분석·sim·요약 상태를
선두 네 항목에서 함께 삭제해 이후 첨부/공유 쌍이 100개 경계를 넘지 않게 합니다.

## 영향
CAS를 약화하지 않고 사용자 편집과 첫 요약을 보호합니다. 문서 실패가 있어도
제외 범위를 밝힌 유용한 회의록을 만들 수 있지만 canonical 소스 변경은 저장을
거부합니다. 불확실한 spill은 ADR-037에 따라 보존합니다. 머지/배포는 기존 배치의
CAS·충돌 복구·출력 필터를 바꿉니다. DOCUMENT 조회는 wiring 릴리스의 주입 전까지
휴면입니다. Lambda 자동 재시도는 유한하며 남은 표식은 이후 재전달/수동 복구에
사용합니다. 소비자 배포 후 API 생산자를 배포합니다.

## 참고 자료
- [불변 spill](ADR-037-immutable-spills-for-conditional-transcript-writes.md)
- [문서 추출](ADR-039-meeting-document-extraction.md)
- [배포와 롤백](../runbooks/meeting-document-release.md)
