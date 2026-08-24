# ADR-031: 요약 파이프라인 타임아웃 복원력 — refine 병렬화 + 정체 시도 재처리

- Status: 승인됨 (Accepted)
- Date: 2026-08-19

## Context

사용자 신고로 미팅 `11809272-ec6c-48fc-b43b-91c8532f9294`를 조사하던 중 —
해당 미팅 자체는 Whisper GPU Spot 콜드스타트로 지연됐을 뿐 정상 완료됐다 — 전체
`ttobak-main` 테이블을 스캔해 **다른 두 미팅이 `summarizing` 상태로 영구 정체**된
것을 발견했다:

| 미팅 | 세그먼트 | refine 소요 | 결과 |
|---|---|---|---|
| `9c7563b1` (8/19) | 1,241 | 8m56s | `ttobak-summarize` `Status: timeout` @ 600,000ms |
| `296908eb` (8/10) | 1,042 | 9m15s | 동일, 9일째 정체 |

### 원인 1 — 순차/병렬 분기가 거꾸로 걸린다

`BedrockService.RefineTranscript`(`backend/internal/service/bedrock.go`)는 Whisper
세그먼트를 300개씩 청크로 나눈 뒤, 청크 수가 5 이하면 화자 라벨 연속성을 위해
**순차** 처리(앞 청크 꼬리를 다음 청크 프롬프트에 넘김), 그 이상이면 **배치 4의
병렬** 처리로 나뉜다. 청크당 Sonnet 호출이 ~2분이므로:

- 1,042 세그먼트 → 4청크 → 순차 8분
- 1,241 세그먼트 → 5청크 → 순차 9분

둘 다 `<=5` 경계에 걸려 순차 경로로 갔고, (당시) Lambda 예산 10분이 refine
단계에서 소진돼 **Opus 5 최종 노트 생성은 시작조차 못 했다.** 역설적으로
1,500 세그먼트를 넘는 더 긴 회의는 6청크 이상이 되어 병렬 경로로 빠지므로 오히려
빠르게 끝난다 — 짧은 회의와 긴 회의 사이, 대략 50~80분 분량(~1,000~1,500
세그먼트)만 골라서 타임아웃하는 사각지대였다.

### 원인 2 — 재시도 가드가 죽은 시도와 진행 중인 시도를 구분하지 못한다

`cmd/summarize/main.go`의 `handleSingleTranscript`/`handleAllPartsTranscribed`는
상태가 정확히 `transcribing`일 때만 처리하는 화이트리스트 가드를 쓴다. 목적은
EventBridge의 at-least-once 재전달이 이미 처리된(또는 처리 중인) 미팅에 Bedrock
요약·KB 익스포트·DynamoDB 쓰기를 중복 실행하지 않게 막는 것이다.

문제는 refine 성공 직후 상태가 `summarizing`으로 넘어가고 그 다음 요약 단계에서
타임아웃하면, 이후 재전달이 이미 `summarizing`인 상태를 보고 매번 스킵한다는
점이다. 로그에 그대로 남아 있다:

```
Skipping transcript for meeting 9c7563b1... (status=summarizing, expected=transcribing)
```

가드 자체는 정당하지만 "동시에 진행 중"과 "이전 시도가 죽고 아무도 재개하지
않음"을 같은 값(`summarizing`)으로 취급해 후자를 영구 정체로 만든다.

## Decision

### 1. 순차/병렬 분기 임계값을 5 → 2로 낮춘다

`bedrock.go`의 `if len(chunks) <= 5` → `<= 2`. 4~5청크는 이제 병렬 경로(배치 4,
2웨이브 ~4분)로 가서 요약 단계에 6분 이상을 남긴다. 화자 라벨 연속성이 중요한
짧은 회의(≤2청크, ≤600세그먼트)만 순차로 남긴다.

### 2. `ttobak-summarize` 타임아웃을 10분 → 15분(Lambda 상한)으로

`gateway-stack.ts`의 `SummarizeFunction` 타임아웃을 Lambda가 허용하는 최댓값으로
올려 안전 마진을 둔다. 메모리(512MB)는 그대로 유지 — 실측 `Max Memory Used: 43 MB`로
메모리는 병목이 아니라 Bedrock 응답 대기 시간이 병목이었다.

### 3. 재시도 가드를 "진행 중" vs "정체됨"으로 분리한다

`GetMeeting`의 자동 만료 판정(`MeetingService.isStuck`, "상태가 임계값을 넘겨
정체됨")과 같은 로직을 `cmd/summarize`에서도 쓸 수 있게 `service.IsStuck`으로
승격해 공유한다. 가드를:

- `transcribing` → 처리 (기존과 동일)
- `summarizing` + `IsStuck` (정체) → 처리 (신규 복구 경로)
- `summarizing` + 정체 아님 → 스킵 (동시 중복 실행 방지, 기존 의도 유지)
- `done` / `error` → 스킵 (기존과 동일)

으로 넓힌다. 별도의 새 임계값을 만들지 않고 기존 `stuckTranscribingThreshold`를
재사용해 "얼마나 오래 정체돼야 죽은 것으로 볼지"의 판단 기준을 한 곳에만 둔다.

### 4. `stuckTranscribingThreshold`를 30분 → 60분으로

이 값은 결정 3에서 재시도를 허용하는 문턱이자 `GetMeeting`이 `error`로 오인
마킹하는 문턱이기도 하다. 별건으로 조사한 Whisper GPU Spot 콜드스타트(단일 AZ
Spot 용량 부족으로 최대 ~16분 관측)에 마진을 두기 위해 함께 올린다 — 30분은 이미
그 사례에서 3분 차이로 오인 마킹을 겨우 면한 값이었다.

## Considered Alternatives

- **재시도 가드를 아예 없애고 매번 처리**: EventBridge가 정상적으로 짧은 간격으로
  재전달할 때(초 단위) 진행 중인 Bedrock 호출과 경쟁해 중복 요약·중복 KB 익스포트를
  일으킨다. 기각.
- **`summarizing` 진입 시 타임스탬프를 별도 필드에 남기고 그 필드만 확인**:
  `isStuck`이 이미 `updatedAt` 기준으로 같은 판정을 하고 있어 별도 필드는 상태
  전이마다 하나 더 관리해야 할 부담만 늘린다. 기각, 기존 필드 재사용.
- **Lambda 타임아웃을 올리지 않고 refine만 병렬화**: 5청크 이상에서도 아직
  드물게 Bedrock 응답 지연이 겹치면 10분을 넘길 수 있다. 두 변경을 함께 적용해
  단일 원인 수정에 의존하지 않게 한다.

## Consequences

- 4~5청크(50~80분 분량) 회의의 refine이 9분 → ~4분으로 줄어 타임아웃 위험이
  사라진다.
- 죽은 요약 시도가 최대 `stuckTranscribingThreshold`(60분) 뒤 자동으로 재시도돼
  수동 개입(S3 `CopyObject`로 EventBridge 재발화 등) 없이도 복구된다.
- `stuckTranscribingThreshold`를 60분으로 올린 대가로, 실제로 죽은 미팅이
  `error`로 마킹되기까지의 사용자 대기 시간도 30분에서 60분으로 늘어난다.
  콜드스타트/요약 지연을 오인 마킹하지 않는 이득이 이 지연을 상회한다고 판단했다.
- 정지됐던 두 미팅(`9c7563b1`, `296908eb`)은 배포 후 전사 S3 객체를 자기 자신에게
  `CopyObject`해 EventBridge를 재발화시켜 복구한다(`RediarizeMeeting`이 오디오에
  쓰는 것과 같은 수법) — 이제 정체 가드가 재처리를 허용하므로 별도 상태 수정이
  필요 없다.
