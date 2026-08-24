# ADR-031: 요약 파이프라인 타임아웃 복원력 — 타임아웃 상향 + 정체 시도 재처리

- Status: 승인됨 (Accepted)
- Date: 2026-08-19

## Context

사용자 신고로 미팅 `11809272-...`(뒤 생략)를 조사하던 중 — 해당 미팅 자체는
Whisper GPU Spot 콜드스타트로 지연됐을 뿐 정상 완료됐다 — 전체 `ttobak-main`
테이블을 스캔해 **다른 두 미팅이 `summarizing` 상태로 영구 정체**된 것을
발견했다:

| 미팅(ID 앞 8자) | 오디오 길이 | 세그먼트 | refine 소요 | 결과 |
|---|---|---|---|---|
| `9c7563b1...` (8/19) | ~210분 | 1,241 | 8m56s | `ttobak-summarize` `Status: timeout` @ 600,000ms |
| `296908eb...` (8/10) | ~65분 | 1,042 | 9m15s | 동일, 9일째 정체 |

### 원인 1 — Lambda 타임아웃 예산이 refine + 요약을 합쳐 감당하지 못한다

`BedrockService.RefineTranscript`(`backend/internal/service/bedrock.go`)의
`chunkWhisperSegments(segments, 300)`는 세그먼트 개수가 아니라 **오디오
300초(5분) 단위 시간**으로 청크를 나눈다. 청크 수가 5 이하(≈25분 이하 회의)면
화자 라벨 연속성을 위해 **순차** 처리, 그 이상이면 **배치 4의 병렬** 처리로
나뉜다.

두 정지 미팅은 각각 210분(42청크)·65분(14청크)으로, **처음부터 병렬 경로였다**
— 순차/병렬 분기 임계값과는 무관했다. 42청크를 배치 4로 처리하면
1(동기 선두 청크)+⌈41/4⌉=11웨이브, 14청크는 1+⌈13/4⌉=4웨이브이고, 실측 8~9분은
이 웨이브 수와 청크당 Sonnet 호출 시간(대략 45초~2분)으로 그대로 설명된다.
refine 자체가 딱히 느린 게 아니라, **refine이 끝나는 순간 (당시) 10분
Lambda 예산이 거의 소진돼 그 뒤에 오는 Opus 5 최종 노트 생성 호출이 들어갈
자리가 없었던 것**이 원인이다.

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
않음"을 같은 값(`summarizing`)으로 취급해 후자를 영구 정체로 만든다. 다만 이
가드는 **새로운 재시도를 만들어내지 않는다** — EventBridge/Lambda의 at-least-once
재전달이 (대개 수 분 내에) 소진되기 전에 도착하거나, 운영자가 수동으로 트랜스크립트
S3 객체를 재발화(self-`CopyObject`)시켜야 실제로 통과한다. 이 ADR이 고치는 것은
"재전달이 왔을 때 죽은 시도로 인식하고 재처리하는가"이지, "재전달을 자동으로
만들어내는가"가 아니다.

## Decision

### 1. `ttobak-summarize` 타임아웃을 10분 → 15분(Lambda 상한)으로

`gateway-stack.ts`의 `SummarizeFunction` 타임아웃을 Lambda가 허용하는 최댓값으로
올린다. 메모리(512MB)는 그대로 유지 — 실측 `Max Memory Used: 43 MB`로 메모리는
병목이 아니라 Bedrock 응답 대기 시간이 병목이었다. 순차/병렬 분기 임계값(`<=5`
청크, `bedrock.go`)은 **건드리지 않는다** — 두 정지 미팅 모두 이미 병렬
경로였으므로 이 값을 낮춰도 그 사례들에는 영향이 없고, 낮추면 15~25분대
회의(3~5청크)가 화자 연속성이 더 좋은 순차 경로에서 불필요하게 병렬로 밀려나는
손해만 생긴다.

### 2. 재시도 가드를 "진행 중" vs "정체됨"으로 분리하고, 재처리를 원자적으로 클레임한다

`GetMeeting`의 자동 만료 판정(`isStuck`, 패키지 내부에만 쓰이므로 export하지
않는다)과 같은 계열이지만, `cmd/summarize`의 재시도 자격 판정에는 **별도의 공개
함수와 더 짧은 임계값**(`service.IsSummarizeRetryEligible`,
`summarizeRetryEligibleThreshold` 20분)을 새로 둔다. `isStuck`이 쓰는
`stuckTranscribingThreshold`(60분)를 그대로 재사용하지 않는 이유: 그 값은
`GetMeeting`이 정체된 미팅을 `error`로 오인 마킹하는 문턱과 동일하다. 재시도
자격 문턱을 여기 맞추면, 사용자가 60분 이후 미팅을 한 번이라도 열어 `error`로
마킹된 순간 — 화이트리스트 밖이라 — 수동 재발화조차 거부되어 복구 가능 구간이
"60분 경과 후 & 아무도 안 열어본 사이"로 좁아진다. 재시도 문턱(20분)을 만료
문턱(60분)보다 짧게 둬 그 사이에 실제 복구 창을 확보한다.

가드를 다음과 같이 넓힌다:

- `transcribing` → 처리 (기존과 동일)
- `summarizing` + `IsSummarizeRetryEligible`(20분 이상 정체) → **원자적 클레임 후**
  처리 (신규 복구 경로)
- `summarizing` + 20분 미만 → 스킵 (동시 중복 실행 방지, 기존 의도 유지)
- `done` / `error` → 스킵 (기존과 동일)

`GetMeetingByID` → 자격 판정 → 처리 사이에는 경쟁 구간이 있다 — 수동
`CopyObject` 재발화가 만들 수 있는 중복 S3 이벤트나, 진짜 재전달과 수동
재발화가 겹치는 경우 둘 다 같은 자격 판정을 통과할 수 있다. `repository.
ClaimSummarizeRetry`가 `summarizeRetryClaimedAt` 필드에 대한 조건부
`UpdateItem`(`attribute_not_exists(...) OR ... < staleBefore`, TTL 16분 —
`ClaimAllPartsEmit`과 같은 패턴)으로 원자적 단일 승자만 재처리를 진행하게
한다. 나머지는 스킵한다.

### 3. `stuckTranscribingThreshold`를 30분 → 60분으로

이 값은 `GetMeeting`이 `error`로 오인 마킹하는 문턱이다(결정 2의 재시도 문턱과는
이제 분리됨). 별건으로 조사한 Whisper GPU Spot 콜드스타트(단일 AZ Spot 용량
부족으로 최대 ~16분 관측)에 마진을 두기 위해 올린다 — 30분은 이미 그 사례에서
3분 차이로 오인 마킹을 겨우 면한 값이었다.

## Considered Alternatives

- **재시도 가드를 아예 없애고 매번 처리**: EventBridge가 정상적으로 짧은 간격으로
  재전달할 때(초 단위) 진행 중인 Bedrock 호출과 경쟁해 중복 요약·중복 KB 익스포트를
  일으킨다. 기각.
- **순차/병렬 분기 임계값을 낮춰 refine을 더 빨리 끝낸다**: 실측 데이터로 두
  정지 사례 모두 이미 병렬 경로였음을 확인한 뒤 기각 — 이 사례들에 효과가
  없고, 15~25분대 회의의 화자 연속성만 깎는다.
- **재시도 자격과 만료를 같은 상수로 둔다**: 처음엔 그렇게 했으나, 사용자가
  만료 직전 미팅을 열면 복구 창이 사실상 사라지는 문제가 있어 분리했다(결정 2).
- **원자적 클레임 없이 자격 판정만으로 재처리**: 재전달/수동 재발화가 겹치는
  드문 경우 중복 Bedrock 요약 + 중복 KB export + 상호 덮어쓰기 write가 발생할 수
  있어, 이 가드가 원래 막으려던 문제를 다른 경로로 재현한다. `ClaimSummarizeRetry`로
  막는다.

## Consequences

- 50~80분대(그리고 그보다 긴) 회의의 refine+요약 전체 파이프라인이 15분 예산
  안에서 완료된다 — 문제는 refine의 웨이브 수 자체가 아니라 refine 이후
  Opus 5 호출이 들어갈 여유였으므로, 타임아웃 상향이 실효 수정이다.
- 죽은 요약 시도는 **재전달 또는 수동 재발화가 실제로 도착했을 때**, 정체
  20분을 넘겼다면 재처리된다. 60분(또는 그 이상) 방치해도 자동으로 새 이벤트가
  발화되지는 않는다 — 그런 자동 재발화(sweeper)는 이 ADR의 범위 밖이다.
- `stuckTranscribingThreshold`를 60분으로 올린 대가로, 실제로 죽은 미팅이
  `error`로 마킹되기까지의 사용자 대기 시간도 30분에서 60분으로 늘어난다.
  콜드스타트/요약 지연을 오인 마킹하지 않는 이득이 이 지연을 상회한다고 판단했다.
- 정지됐던 두 미팅은 배포 후 전사 S3 객체를 자기 자신에게 `CopyObject`해
  EventBridge를 재발화시켜 복구했다(`RediarizeMeeting`이 오디오에 쓰는 것과
  같은 수법) — 정체 가드가 재처리를 허용하고 원자적 클레임이 중복을 막으므로
  별도 상태 수정이 필요 없었다.

## Follow-up (범위 밖)

- **자동 재발화(sweeper)**: 정말 "N분 뒤 자동 복구"를 원하면 EventBridge
  Scheduler 기반 sweeper를 추가해 정체된 `summarizing` 미팅에 대해 주기적으로
  트랜스크립트 재발화 이벤트를 발생시켜야 한다. 지금은 재전달/수동 트리거에
  의존한다.
- **stale 재처리 시 refine 재사용**: 현재 재처리는 refine을 처음부터 다시
  실행한다 — 이미 저장된 refined transcript가 있다면 재사용해 Bedrock 호출을
  아낄 수 있다.
- **acoustic 라벨 없는 fallback 경로**의 청크 간 화자 연속성은 여전히 순차/병렬
  분기(`<=5`청크)에 좌우된다 — preserve mode가 기본 경로라 영향 범위는 좁다.
