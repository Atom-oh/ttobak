# ADR-042: Current-source Q&A and conversation history

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status

Accepted. Reader foundations are implemented; the complete runtime and
read-only tool continuity are staged until migration and acceptance finish.
ADR-040 and ADR-041 are reserved for parallel summary/bootstrap work.

## Context

Index chunks and conversation history can outlive edits, deletion or access
revocation. Discarding every history after a normal list tool avoids stale
data but breaks follow-up questions. Replacing a matching legacy chunk with
the file introduction also loses valid facts later in a document.

## Options Considered

### Option 1: Reuse index chunks and history until a TTL expires

- **Pros**: Lower read cost and simple continuity.
- **Cons**: Stale or revoked source text and assistant paraphrases remain usable.

### Option 2: Revalidate source-backed evidence and read-tool results

- **Pros**: Current access and byte bindings govern answers; stable lists keep
  follow-up references usable.
- **Cons**: Additional reads and explicit resets when evidence changes.

## Decision

Choose option 2. Use the index for discovery, authorize canonical sources
again, and hydrate current saved text. Accept binary excerpts only from
matching immutable snapshots. Verify legacy text excerpts against current
bytes and provide bounded, revision-bound continuation.

Store source dependencies beside model messages. Recheck them before replay
and model use. Discard the entire dependent history after a source changes,
disappears or becomes inaccessible; removing tool blocks alone does not
remove private assistant paraphrases. Reject unverifiable legacy histories.

Preserve ordinary read-only list/account tool continuity using bounded
fingerprints of freshly authorized results. Revalidation may call only
explicitly allowlisted read-only functions with the current user. Never
repeat mutations such as research creation merely to validate history.
Treat their historical receipts separately from current source evidence.

Expose additive `sourceDetails` alongside existing `sources` strings.
Construct public details from explicit fields, not arbitrary index metadata.
Keep partial/error states visible and cite file locations without audio
timestamps.

Deploy source permissions and manual/private/shared snapshot bootstrap
first. Verify snapshots before enabling the complete strict consumer.
Enable canonical backfill and legacy meeting export retirement afterward.
Merged code, unit tests and deployed acceptance are separate milestones.

## Consequences

### Positive

- Edited or revoked sources cannot survive through cached chunks or paraphrases.
- Matching facts later in long files remain reachable.
- Stable read-only tool results support normal follow-up questions.

### Negative

- Current reads cost more than trusting caches.
- Source changes or unverifiable old sessions can reset continuity.
- Migration must preserve existing binary answerability before strict cutover.

## References

- [Canonical indexing](ADR-038-canonical-note-indexing.md)
- [Source contract](../../backend/python/qa/SOURCE_CONTRACT.md)
- [Current-source rollout](../runbooks/qa-current-source-rollout.md)

---

<a id="korean"></a>

# 한국어

## 상태

승인됨. 원본 읽기 기반은 구현됐으며, 전체 런타임과 읽기 전용 도구의 대화
연속성은 이관·검증 완료 전까지 단계적으로 준비합니다.
ADR-040과 ADR-041은 병렬 요약·부트스트랩 작업에 예약된 번호입니다.

## 배경

색인 발췌와 대화 이력이 수정·삭제·권한 회수 이후에도 남을 수 있습니다.
일반 목록 도구를 호출할 때마다 이력을 버리면 오래된 데이터는 차단하지만
후속 질문이 끊깁니다. 검색에 맞는 발췌를 파일 도입부로 대체해도 문서 후반의
유효한 사실을 잃습니다.

## 검토한 옵션

### 옵션 1: TTL 만료 전까지 색인 발췌와 이력을 재사용

- **장점**: 조회 비용이 낮고 대화 연결이 단순합니다.
- **단점**: 오래됐거나 권한이 회수된 원문과 답변의 재서술이 남습니다.

### 옵션 2: 원본 근거와 읽기 도구 결과를 다시 검증

- **장점**: 현재 권한과 바이트 일치 여부로 답변을 제어하며, 변하지 않은
  목록의 후속 참조를 유지합니다.
- **단점**: 추가 조회와 근거 변경 시 명시적인 이력 초기화가 필요합니다.

## 결정

옵션 2를 선택합니다. 색인은 후보 탐색에 사용하고 정본 원본을 다시 인가한
뒤 현재 저장 텍스트를 읽습니다. 바이너리 발췌는 일치하는 불변 스냅샷에서만
허용합니다. 기존 텍스트 발췌도 현재 바이트와 대조하고 버전에 묶인 제한된
이어읽기를 제공합니다.

원본 의존성을 모델 메시지 옆에 저장하고 재사용·모델 호출 전에 확인합니다.
원본 변경·삭제·접근 상실 시 해당 이력 전체를 폐기합니다. 도구 블록만 지워도
답변에 재서술된 비공개 사실은 남으므로 충분하지 않습니다. 검증할 수 없는
기존 이력도 재사용하지 않습니다.

읽기 전용 목록·어카운트 도구는 현재 권한으로 조회한 결과의 제한된
지문을 기록해 일반적인 후속 질문을 유지합니다. 재검증은 명시적으로 허용한
읽기 전용 함수만 현재 사용자로 호출합니다. 이력을 검증하려고 리서치 생성
같은 변경 작업을 다시 실행하지 않습니다. 변경 작업의 과거 실행 결과는
현재 원본 근거와 구분합니다.

기존 `sources` 문자열과 함께 `sourceDetails`를 추가합니다. 임의 색인
메타데이터를 그대로 넘기지 않고 명시한 필드로 공개 출처를 구성합니다.
부분·오류 상태를 드러내고 파일은 오디오 시각 대신 문서 위치로 인용합니다.

원본 읽기 권한과 개인·공유 파일 스냅샷 부트스트랩을 먼저 배포합니다.
스냅샷 검증 후 전체 strict consumer를 켜고, 이후 정본 backfill과 기존
미팅 export 정리를 활성화합니다. 코드 머지, 단위 테스트, 배포 검증은
서로 다른 완료 단계로 취급합니다.

## 영향

### 긍정적

- 수정·권한 회수된 원본이 캐시나 답변 재서술로 계속 사용되지 않습니다.
- 긴 파일 후반의 검색 근거도 확인할 수 있습니다.
- 변하지 않은 읽기 전용 결과는 정상적인 후속 질문을 지원합니다.

### 부정적

- 캐시 신뢰 방식보다 현재 원본 조회 비용이 늘어납니다.
- 원본 변경이나 검증 불가능한 기존 세션은 대화 연결을 초기화할 수 있습니다.
- strict 전환 전에 기존 바이너리 파일의 답변 가능성을 보존해야 합니다.

## 참고 자료

- [정본 색인](ADR-038-canonical-note-indexing.md)
- [원본 계약](../../backend/python/qa/SOURCE_CONTRACT.md)
- [현재 원본 배포 순서](../runbooks/qa-current-source-rollout.md)
