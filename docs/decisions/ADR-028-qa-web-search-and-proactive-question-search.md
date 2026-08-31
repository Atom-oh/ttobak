# ADR-028: QA 웹 검색 도구와 선제 질문 검색 (proactive search)

- **Status**: Accepted
- **Date**: 2026-07-31
- **PR**: #143

## Context

라이브 Q&A(QA Lambda의 에이전틱 루프)는 KB 검색·AWS 문서 검색·트랜스크립트 검색만 가능해, 미팅 중 나오는
최신 정보성 질문(출시 소식, 가격, 경쟁사 동향, AWS 외 일반 주제)에 답하지 못했다. 또한 `detect-questions`가
대화에서 질문을 추출해 추천 칩으로 띄워주긴 하지만, 사용자가 칩을 눌러야만 답변이 시작됐다 — "질문의 맥락이
감지되면 답이 이미 떠 있는" 경험이 목표였다.

한편 크롤러(SP1)와 research-agent는 이미 us-east-1 AgentCore Gateway Web Search 커넥터를 SigV4 크로스리전으로
호출하고 있었다.

## Decision

1. **`search_web` 도구를 QA Lambda에 추가** (`backend/python/qa/web_search.py`): 기존 크롤러와 동일한
   SigV4+MCP 플러밍을 세 번째로 복제한다. 세 소비자(crawler Lambda zip, research-agent 컨테이너, qa Lambda
   zip)는 배포 아티팩트가 전부 달라 공용 패키지의 배포 복잡도가 함수 하나의 중복 비용을 넘어선다고 판단 —
   대신 세 파일 모두에 상호 동기화 주석을 명시한다.
   - 실패(전송/IAM/게이트웨이 오류)와 무결과(genuine zero-hit)를 구분해 모델에 전달한다 — 장애가 "관련 결과
     없음"으로 위장하면 모델이 그 위에서 지어낸다.
   - IAM은 `bedrock-agentcore:InvokeGateway`를 Gateway ARN으로 스코프(`ai-stack.ts`), env는
     `gateway-stack.ts`가 주입(크로스리전 참조 → GatewayStack이 WebSearchGatewayStack에 의존 추가).
2. **`detect-questions`에 `search` 플래그 추가**: 감지된 질문 중 "검색으로 즉시 사실 확인 가능"한 것을
   `proactive` 배열(questions의 부분집합)로 반환한다. 레거시 문자열 배열 응답도 계속 파싱한다.
3. **선제 자동 발화는 기본 꺼짐(opt-in), 단 opt-in의 범위는 '자동 발화'뿐**: 회의 대화에서 파생된 검색어가
   사용자 조작 없이 외부 웹 검색 제공자로 나가는 것은 기존 `search_knowledge_base`(계정 내부)와 다른 신뢰
   경계다. 따라서
   - LiveQAPanel 헤더의 "선제 검색" 토글(localStorage 기반 모듈 스토어 — 두 패널 인스턴스가 동시에
     마운트되므로 `useSyncExternalStore`로 인스턴스 간 동기화, 기본 OFF)을 켠 사용자에게만 자동 발화하고,
   - **수동 질문 경로에서는 토글과 무관하게 모델이 `search_web`을 호출할 수 있다** — 이 경로의 완화책은
     시스템 프롬프트/도구 설명의 쿼리 구성 제약(고객사·참석자 실명, 내부 코드명, 회의 수치 금지 — 일반화
     키워드만)과 트랜스크립트-지시문 무시 인젝션 가드다. 참가자 발화가 system 컨텍스트에 들어가는 구조상
     prompt injection → `search_web` exfil 표면이 남는데, 이는 소프트 완화이지 보안 경계가 아니다
     (FoldLiveSummary의 위협 모델과 같은 등급) — 수용하되 문서화한다.
   - 검색 쿼리 원문은 CloudWatch에 로깅하지 않으며(해시 접두사+길이만 — `web_search.py` 자체 로그와
     에이전틱 루프의 tool-call 로그 양쪽 모두, `redact_tool_input_for_log`),
   - 외부 전송 사실(수동 경로 포함)을 API-SPEC에 명시한다.
4. **자동 발화 가드**: 감지 배치당 1건, 질문당 녹음 세션 내 전 패널 인스턴스에 걸쳐 1회(모듈 레벨 claim
   set — 데스크톱 aside와 모바일 바텀시트가 동시에 마운트되므로 인스턴스 로컬 dedup은 이중 발화; 녹음
   시작 시 `resetProactiveClaims()`로 초기화 — meetingId 네임스페이스는 녹음 중 id가 생기는 순간 키가
   바뀌어 재발화하므로 쓰지 않는다), 실패 시 claim 롤백 + asked 기록을 성공 시점으로 미룸(답 없이 질문이
   영구 소진되는 것 방지 — asked 기록이 detect의 previousQuestions로 전달되므로 선기록하면 롤백이 무력화됨),
   답변 진행 중·사용자 입력 중·패널 비가시(IntersectionObserver로 반응형 추적) 상태에서는 보류. stale 응답
   방지를 위해 detect 경로도 summary와 동일한 generation guard를 쓴다(`useLiveSummary`).

## Consequences

- 미팅 중 최신 정보 질문에 라이브 QA가 답할 수 있고, (opt-in 시) 감지된 사실형 질문은 답이 미리 떠 있다.
- 토큰/호출량: 자동 발화는 배치당 1건 + Bedrock 라운드 제한(MAX_TOOL_ROUNDS)으로 상한이 있고, 서버측으로는
  `check_web_search_limit`이 qa Lambda의 인증 경로에서 사용자당 시간당 `WEB_SEARCH_HOURLY_LIMIT`회(기본 30)를
  강제한다 — 게이트웨이 호출 전에 검사해 초과 호출은 외부 쿼터를 소비하지 않고, DynamoDB 오류 시 fail-open,
  tumbling-hour 경계 버스트는 최대 ~2×까지 허용되는 가용성 우선의 남용 브레이크이지 보안 경계가 아니다.
  crawler/research-agent의 게이트웨이 사용은 시스템 트리거라 의도적으로 무제한이다.
- opt-in 위생: 선제 검색 opt-in은 명시적 로그아웃(`auth.ts` signOut)과 세션 만료 teardown
  (`AuthProvider`의 authFailureCallback) 양쪽에서 OFF 기본값으로 초기화된다 — origin-wide localStorage라
  공용 브라우저에서 이전 사용자의 외부 전송 동의가 다음 사용자에게 승계되면 안 되기 때문.
- SigV4+MCP 플러밍이 3중 복제로 늘었다. 변경 시 세 파일을 함께 고쳐야 한다(각 파일 docstring에 명시).
- `WEB_SEARCH_GATEWAY_URL` 미설정 시 도구가 목록에서 빠지는 게 아니라 호출 시 실패 사유를 반환한다 —
  도구 라운드 1회를 소비하므로 "완전 비활성"은 아니다. 미설정 배포는 초기 셋업 과도기뿐이라 수용.
