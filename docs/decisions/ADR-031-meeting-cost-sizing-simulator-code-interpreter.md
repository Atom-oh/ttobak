# ADR-031: 미팅 기반 비용·사이징 시뮬레이터 — AgentCore Code Interpreter

- Status: 승인됨 (Accepted)
- Date: 2026-08-18

## Context

또박의 미팅 산출물(`Meeting.Content`, `ActionItems`, `MeetingInsight`)은 전부 서술형
텍스트다. SA가 미팅에서 들은 "사용자 10만 명, 피크 500 TPS, 로그 하루 200GB" 같은
정량 요구사항은 회의록 문장으로만 남고, 아키텍처 대안의 비용 비교는 여전히 사람이
미팅 후 별도로 계산한다. "미팅 → 아키텍처 다이어그램"은 이미 구현돼 있다
(`SummarizeTranscript`가 mermaid를 생성하고 `MermaidBlock`이 렌더링) — 이번에 새로
메우는 격차는 다이어그램이 아니라 **정량 시뮬레이션**이다.

AgentCore Code Interpreter는 다른 방법으로 얻을 수 없는 것을 준다: LLM 추정치가
아니라 **실제로 실행된 파이썬 계산**과, 그 코드 자체가 감사 근거로 남는다는 것.
`aws.codeinterpreter.v1`은 `ap-northeast-2`에서 READY로 확인되어(Web Search
Gateway와 달리) 크로스리전 SigV4 배관이 필요 없다.

세 가지를 결정해야 했다: (a) 이 새로운 능력을 어떤 신뢰 경계 안에 둘 것인가 —
미팅 트랜스크립트는 공격자 영향권이고, 그 끝에서 실행되는 것은 임의의
LLM 생성 코드다. (b) 산출물(차트·리포트·실행 코드)을 기존 첨부 파일
파이프라인에 태울 것인가, 새 엔티티를 만들 것인가. (c) AWS 실제 단가를
어디서 조달하고, Code Interpreter에 어떤 권한을 줄 것인가.

## Decision

### 1. 소유·트리거·입력 게이트

**Meeting이 소유, 사용자 버튼이 트리거, 추출→확인/보정→실행** 3단 게이트.
자동 실행하면 정량 입력이 없는 대부분의 미팅에서 헛돈·환각 수치를 만든다.
추출(Haiku)은 근거 인용(`transcript://{segmentId}`, ADR-013 앵커 재사용)과
함께 초안을 반환하고, 사용자가 폼에서 확인·보정한 값만 실행된다. 필수값이
미확정이면 실행 자체를 서버가 거부한다.

### 2. 신뢰 경계 — 두 LLM 호출은 프롬프트를 공유하지 않는다

경로는 `transcript → 추출 LLM(Go, Haiku) → 검증된 요구사항 JSON → 코드 생성
LLM(Python, Sonnet) → 실행되는 파이썬`이다. **transcript는 코드 생성 프롬프트에
절대 도달하지 않는다** — `ttobak-sim` Lambda는 transcript를 아예 받지 않고,
Go api Lambda가 넘기는 것은 서버 허용목록(`AllowedSimRequirementKeys`)을 통과한
숫자/enum 값뿐이다. 세 겹의 방어:

1. **스키마 제약 핸드오프**: `validateSimRequirements`(Go, 순수함수)가 실제
   경계다 — 키는 고정 허용목록, 숫자는 키별 범위, enum은 키별 허용값, 자유
   텍스트(Label/옵션명)는 길이·문자셋 캡. 확인 폼은 UX 게이트일 뿐이고, 위반은
   서버가 드롭하거나 400을 낸다.
2. **무력한 샌드박스**: Code Interpreter 세션은 `SANDBOX` 네트워크 모드 +
   **빈 실행역할**(정책 미부착)로 돈다. CDK의 `CodeInterpreterCustom` L2는
   실행역할을 생략해도 자동으로 하나를 만들어주므로 "실행역할 없음"이 아니라
   "빈 실행역할"이 정확한 표현이다 — **AWS API 접근을 막는 것은 이 빈 역할이고,
   SANDBOX 네트워크 모드는 공용 인터넷만 제거한다** (AWS 문서: SANDBOX도 S3 등
   AWS 서비스로의 제한적 접근은 열려 있음). 최악의 경우가 런마다 폐기되는
   샌드박스의 낭비된 CPU다.
3. **금지 임포트 스캔**(`boto3`/`socket`/`subprocess`/`requests`/`urllib`/
   `os.system`)은 심층 방어이자 로그 신호일 뿐, 경계가 아니다. 스캔이 있다고
   샌드박스를 완화해서는 안 된다.

### 3. 단가 조달 — 서버가 조회해 주입, 모델은 발명 불가

Python Lambda가 AWS Price List Query API(`us-east-1` — 서울 리전에 이 API의
엔드포인트가 없음, `aws pricing describe-services --region ap-northeast-2`가
엔드포인트 연결 실패로 확인됨. SDK 리전 인자만 다르고 새 인프라는 없음)를 조회해
JSON 스냅샷을 CI 세션에 `writeFiles`로 주입한다. **v1 서비스 범위는 또박 자신의
스택**(Lambda, API Gateway, DynamoDB, S3, CloudFront)으로 의도적으로 좁힌다 —
EC2/RDS/ALB 같은 전통 서비스는 인스턴스 패밀리·테넌시·예약 조합 때문에 SKU
매칭이 훨씬 지저분하고, 범위 밖 요청은 추측하지 말고 명시적으로 실패시킨다.
`pricing:GetProducts`/`DescribeServices`는 리소스 수준 권한이 없는 API라
`Resource: "*"`가 불가피하다 — 기존 `CognitoListUsers`/`BedrockKBRetrieve`
와일드카드와 나란히 두는 **의도적 예외**다.

### 4. 데이터 모델 — 새 아이템 1종, `Attachment` 비재사용

`SimRun`을 미팅당 싱글턴(PK `MEETING#{id}` / SK `SIMRUN`)으로 새로 둔다.
**`Attachment`를 재사용하지 않은 이유**: `Attachment.Status`는 error 슬롯이
없는 3값 enum이고, `GetMeetingDetail`이 모든 `ATTACH#` 행을 첨부 갤러리로
매핑하므로 `queued`/`error` 런이 깨진 갤러리 타일로 노출된다. 게다가
`SummarizeTranscript`의 부록 루프는 `ProcessedContent`가 비어있지 않은 모든
done 첨부에 `![](attachment://id)`를 뱉으므로, 리포트 마크다운이 첨부로
들어가면 회의록에 깨진 이미지 링크로 나타난다. 런은 파일이 아니라 잡이다.
결과: **`Attachment.ProcessedKey`는 계속 미사용**으로 남긴다 — 비어 있다는
이유만으로 필드를 전용하면 모호함을 코드에 새기는 것이다.

### 5. 진행 UX — 폴링, WebSocket 아님

기존 WebSocket 메시지 타입(`answer_start`/`answer_delta`…)은 QA 채팅 토큰
스트림 전용이다. 시뮬레이션은 1~3분짜리 **잡**이고, `ResearchDetailClient`가
정확히 같은 형태의 잡을 10초 폴링으로 이미 처리한다. 새 채널·Go websocket
Lambda 라우팅·프론트 소켓 상태를 추가하는 대신 5초 폴링을 재사용한다.
`Meeting.Status`는 절대 건드리지 않는다 — 그 필드는 요약-vs-스피너 게이트와
30분 `isStuck` 자동 error를 구동하므로, `SimRun.Status`는 완전히 별도
생명주기다. 20분 넘은 `queued`/`running`은 `ReconcileStuckSimRun`이 조회
시점에 error로 보고한다(영속화 없음, `isStuck`과 동일 패턴).

### 6. S3 배치 — 기존 프리픽스 재사용, ADR-027 변경 없음

`images/{userId}/{meetingId}/sim/{simRunId}/chart_N.png`,
`files/{userId}/{meetingId}/sim/{simRunId}/{report.md,generated.py,prices.json}`.
`images/*`와 `files/*`는 이미 OAC 허용목록에 있어 StorageStack 배포와 배포판 ID
래칫 재실행이 불필요하다. 새 `sim/*` 최상위 프리픽스를 만들면 CLAUDE.md가 경고하는
그 함정("업로드는 성공, `/media/*`가 403")에 빠진다. 부가 확인: `images/`의
EventBridge 규칙은 S3 프리픽스가 아니라 커스텀 `ImageUploadCompleted` 이벤트에
반응하므로, `images/sim/` 아래 PNG를 써도 `ttobak-process-image`가 호출되지
않는다.

## Considered Alternatives

- **transcript를 코드 생성 프롬프트에 직접 넘기기** — 컨텍스트가 풍부해
  코드 품질이 나아질 수 있으나, 인젝션 폭발 반경이 사실상 무한대가 된다.
  기각.
- **Code Interpreter에 실행역할·네트워크를 부여해 스스로 Pricing API를
  호출** — 유연하지만 생성 코드에 AWS 자격증명 접근을 허용하는 것과 같다.
  기각 — 단가는 서버가 조회해 주입하는 게 신뢰 경계를 단순하게 만든다.
- **`Attachment`를 확장(새 `AttachType`, `ProcessedKey` 활용)** — 배관이
  가장 적어 보였지만 위 4번 이유로 기각.
- **Step Functions로 오케스트레이션** — `researchSfn`은 단일 태스크 상태
  머신이라 이 경우엔 순수 오버헤드. Lambda 하나가 CI 세션+재시도 루프를
  다 감당하는 게 더 단순하다. 기각.
- **WebSocket 스트리밍으로 코드 실행 과정 노출** — 데모 화려함은 있지만
  새 ws 메시지 타입·Go 라우팅·프론트 소켓 상태가 다 추가된다. 기각 — 잡에는
  폴링, 토큰 스트림에는 소켓.
- **EC2/RDS/ALB까지 포함하는 넓은 서비스 범위** — 고객 미팅에서 가장 흔한
  질문("서버리스로 갈까 EC2로 갈까")이라 유혹적이지만 SKU 필터 난이도가
  훨씬 높아 v1 완성도를 해친다. 다음 사이클로 미룸.

## Consequences

**좋은 점**
- LLM 추정치가 아니라 실제 실행된 코드와 그 코드 자체가 산출물로 남는다
  (`generated.py`) — 감사·재현 가능.
- 신뢰 경계가 계층적이다: 스키마 검증(1차) → 빈 실행역할+SANDBOX(2차,
  실질적 경계) → 임포트 스캔(3차, 로그 신호). 하나가 뚫려도 다음이 막는다.
- 인프라 변경면이 작다: 새 S3 프리픽스·CloudFront 비헤이비어·StorageStack
  배포가 전혀 없다.
- `SimRun` 싱글턴 설계라 `DeleteMeeting`의 트랜잭션 아이템이 재실행 횟수와
  무관하게 정확히 +1만 늘어난다.

**나쁜 점 / 남는 리스크**
- **LLM이 쓴 TCO 모델은 자신 있게 틀릴 수 있고**, matplotlib 차트는 고객에게
  권위 있게 보인다. 리포트 상단에 입력 표·단가 스냅샷 시각·"추정치 — 검증
  필요" 배너를 강제하지만, 완전히 없어지는 리스크는 아니다.
- **비결정성**: 같은 미팅을 두 번 돌리면 두 답이 나올 수 있다.
  temperature 0 + 코드 보관이 완화하지만 제거하지 못한다.
- **Price List 필터 매칭은 까다롭고** 에러가 아니라 조용히 틀린 SKU를
  반환할 수 있다(테넌시·구매옵션·리전 불일치). 리포트에 해결된 SKU 설명을
  노출해 사람이 불일치를 잡게 하는 것으로 완화.
- **잔여 인젝션 표면**: transcript가 *코드*를 못 바꾸지만 *추출된 값*은
  흔들 수 있다(예: 워크로드를 부풀리는 발언). 확인 폼이 유일한 방어이고
  사용자는 폼을 넘길 수 있다. 수용.
- **`pricing:GetProducts`/`DescribeServices`의 `Resource:"*"`** — API 자체의
  구조적 한계이지 완화 여지가 없는 새 와일드카드다. 문서화된 예외.
- v1 서비스 범위가 또박 자신의 스택으로 좁아 EC2/RDS 비교 요청은 아직
  지원하지 않는다 — 명시적으로 실패하며, 조용히 틀린 답을 주지 않는다.
