# TTOBAK Project Issues

## How to Add Issues

각 이터레이션 후 아래 형식으로 이슈를 추가하세요.
스킬이 이 문서를 파싱하여 Open 이슈를 자동으로 처리합니다.

### Issue Format

```markdown
### ISSUE-NNN: [제목]
- **Category**: frontend | backend | transcript | recording | infra | qa
- **Severity**: critical | major | minor
- **Affected**: `path/to/file` (line ~N)
- **Description**: 문제 설명
- **Root Cause**: 근본 원인 분석
- **Expected**: 기대 동작
- **Fix Direction**: 수정 방향
- **Screenshot**: (optional) 스크린샷 파일명
```

### Categories

| Category | Description | Target Files |
|----------|-------------|-------------|
| frontend | UI/UX, 컴포넌트 렌더링, 네비게이션 | `frontend/src/components/`, `frontend/src/app/` |
| backend | API 핸들러, 서비스 로직, Lambda | `backend/internal/`, `backend/cmd/` |
| transcript | 음성 인식, STT, 텍스트 처리 | `frontend/src/lib/speechRecognition.ts`, `backend/cmd/transcribe/` |
| recording | 오디오 녹음, MediaRecorder, AudioContext | `frontend/src/components/RecordButton.tsx`, `frontend/src/hooks/` |
| infra | CDK, Lambda 설정, EventBridge, S3 트리거 | `infra/lib/` |
| qa | Live Q&A, KB RAG, 질문 감지 | `backend/python/qa/`, `frontend/src/components/LiveQAPanel.tsx` |

---

## OPEN

(없음)

---

## In Progress

(없음)

---

## Resolved

### 1.1 Q&A 질문 인식 지연 (Resolved)
- **문제**: 미팅중 질문을 했는데 Q&A가 바로 인식하지 못함
- **해결**: Q&A 질문 감지 로직 개선, interim 텍스트도 실시간 분석에 포함

### 1.2 화면 구성 -- Q&A 패널 (Resolved)
- **문제**: Q&A가 별도 탭이라 미팅로그를 보면서 질문할 수 없음, 질문하면 화면이 날아감
- **해결**: 데스크탑: Q&A를 상시 사이드 패널로 변경. 모바일: 플로팅 버튼 + 바텀시트로 변경

### 1.3 음성 텍스트가 어지럽게 붙음 (Resolved)
- **문제**: 인식 재시작 시 interim->final 승격으로 중복 텍스트 발생, 시각적 구분 부족
- **해결**: speechRecognition.ts에 중복 감지 로직 추가 (80% 이상 겹침 시 승격 스킵), LiveTranscript.tsx에 타임스탬프 표시 + entry 간 구분선 + interim 타이핑 인디케이터 추가

### 1.4 5분 후 음성 인식 중단 (Resolved)
- **문제**: Chrome Web Speech API가 ~5분 후 onresult 중단, 탭 전환 시 복귀 안 됨
- **해결**: 30초 watchdog 타이머로 자동 재시작, visibilitychange 리스너로 탭 복귀 시 재시작, recognition-stalled 에러 시 사용자 알림 + 수동 재시작 버튼

### 2. 미팅 완료 후 프로그레싱 (Resolved)
- **문제**: 미팅 완료 후 blocking overlay로 아무 동작 불가
- **해결**: 블로킹 오버레이를 상단 토스트 배너로 변경, 백그라운드 네비게이션 가능

### ISSUE-001: Live Q&A 500 Internal Server Error (Resolved)
- **문제**: 미팅 중 Live Q&A에서 질문 시 500 Internal Server Error 발생
- **해결**: `backend/python/qa/handler.py`의 `call_bedrock()` 함수에 try/except 추가하여 Bedrock API 에러를 graceful하게 처리. 에러 발생 시 사용자에게 명확한 에러 메시지 반환.

### ISSUE-004: Q&A 질문 감지 지연 (Resolved)
- **문제**: 질문 감지가 200자 이상의 새 transcript가 쌓여야 트리거됨. 짧은 질문 누락.
- **해결**: 감지 threshold를 200자 → 100자로 낮춤, 시간 기반 fallback 추가 (15초 경과 시 threshold 무시), debounce를 1000ms → 500ms로 단축.

### ISSUE-005: 공유 미팅 N+1 쿼리 성능 문제 (Resolved)
- **문제**: 공유받은 미팅 목록 조회 시 각 미팅마다 별도 `GetMeetingByID` 호출로 응답 지연
- **해결**: `BatchGetMeetings` 메서드 추가하여 단일 DynamoDB Scan으로 여러 미팅 일괄 조회. N+1 쿼리를 1회 쿼리로 최적화.

### ISSUE-008: S3 키 URL 디코딩 불완전 (Resolved)
- **문제**: S3 이벤트의 key에서 `+` → space 변환만 처리하고, `%XX` 퍼센트 인코딩 미처리
- **해결**: `transcribe/main.go`, `process-image/main.go`에서 `url.QueryUnescape(key)` 사용으로 변경. 한글, 특수문자 파일명 정상 처리.

### ISSUE-002: updateAttachmentByKey 미구현 (Resolved — 이전 수정 확인)
- **문제**: 이미지 처리 결과가 DynamoDB에 저장되지 않음
- **해결**: `process-image/main.go:105-122`에 이미 구현되어 있음을 확인. attachment 목록 조회 후 originalKey 매칭으로 업데이트.

### ISSUE-003: Summarize Lambda 트리거 불일치 (Resolved — 이전 수정 확인)
- **문제**: CDK 트리거와 Go 코드의 이벤트 형식 불일치
- **해결**: CDK(`gateway-stack.ts:304-320`)와 Go(`summarize/main.go:70-88`) 모두 EventBridge S3 이벤트 기반으로 일치함을 확인. 불일치 없음.

### ISSUE-006: AudioContext 미해제 (Resolved — 이전 수정 확인)
- **문제**: 녹음 중지 시 AudioContext가 close() 되지 않아 메모리 누수
- **해결**: `RecordButton.tsx:73`에서 `audioContext.close()` 호출이 이미 구현되어 있음을 확인.

### ISSUE-009: 마이크 선택 시 음성 레벨 미표시 (Resolved)
- **Category**: recording
- **Severity**: major
- **Affected**: `frontend/src/app/record/page.tsx`
- **Description**: MicSelector에 14-segment 레벨 미터가 구현되어 있지만, AnalyserNode가 녹음 시작 시에만 생성되어 마이크 선택 단계에서 레벨이 보이지 않음
- **Root Cause**: `analyserNode`가 RecordButton의 `onAnalyserReady` 콜백에서만 설정됨
- **Fix**: `selectedDeviceId` 변경 시 preview용 AudioContext + AnalyserNode 생성. 녹음 중이면 RecordButton analyser, 아니면 preview analyser를 MicSelector에 전달. 녹음 시작/언마운트 시 cleanup.

### ISSUE-010: 녹음 중 화면 캡처 첨부 반응 없음 (Resolved)
- **Category**: frontend
- **Severity**: major
- **Affected**: `frontend/src/app/record/page.tsx`, `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`
- **Description**: 화면 캡처 S3 업로드 후 `notifyComplete()` 미호출로 백엔드 process-image Lambda 미트리거. MeetingDetailClient에서 upload 완료 시 attachments 미갱신.
- **Root Cause**: `handleCaptureImage()`에서 S3 PUT만 하고 백엔드 알림 누락. FileUploader의 `onUploadComplete`에서 meeting refetch 없음.
- **Fix**: `handleCaptureImage()`: S3 PUT 후 `uploadsApi.notifyComplete()` 추가. `MeetingDetailClient`: upload 완료 시 `meetingsApi.get()`으로 meeting refetch.

### ISSUE-011: Share 버튼 user list 미표시 (Resolved)
- **Category**: frontend
- **Severity**: major
- **Affected**: `frontend/src/components/ShareButton.tsx`, `backend/internal/handler/share.go`
- **Description**: Share 버튼 눌러도 user list가 나오지 않음
- **Root Cause**: 백엔드 라우트(`/api/users/search`)는 정상 등록됨. 검색어 2글자 미만일 때 안내 없어 사용자가 동작 여부 판단 불가.
- **Fix**: 검색어 1글자일 때 "2글자 이상 입력해주세요" 힌트 텍스트 추가. 빈 결과 시 "No users found" 안내는 기존 구현 확인.

### ISSUE-012: Live Q&A 500 에러 재발 (Resolved)
- **Category**: qa
- **Severity**: critical
- **Affected**: `backend/python/qa/handler.py`
- **Description**: `agentic_converse()` 내 Bedrock 호출과 `execute_tool()` 호출에 try/except 없어 에러 시 500 반환
- **Root Cause**: agentic 버전 업데이트 시 에러 핸들링 누락
- **Fix**: `bedrock_runtime.converse()` 호출에 try/except 추가 (한국어 fallback 메시지 반환). `execute_tool()` 호출에 try/except 추가 (도구 에러 메시지를 toolResult로 반환하여 모델이 graceful하게 처리).

### ISSUE-013: 요약이 너무 김 (Resolved)
- **Category**: backend
- **Severity**: major
- **Affected**: `backend/internal/service/bedrock.go`
- **Description**: 요약 프롬프트에 길이 제한 없어 과도하게 긴 요약 생성
- **Root Cause**: 시스템 프롬프트가 "Be concise but thorough"로 모호. maxTokens=4096으로 과다 설정.
- **Fix**: 시스템 프롬프트에 "200단어 이내" 명시적 지시 추가. maxTokens 4096→1024 축소. 섹션 간소화 (개요+주요 논의+결정+액션 아이템, "다음 단계" 섹션 제거).

### ISSUE-007: JWT 서명 미검증 (Resolved)
- **문제**: Backend에서 JWT payload만 decode하고 서명을 검증하지 않음
- **해결**: `golang-jwt/jwt/v5` 라이브러리 도입. Cognito JWKS 공개키 fetch + 1시간 TTL 메모리 캐시. `parseJWT()`에서 RSA 서명, `exp`, `iss` 클레임 검증. `COGNITO_USER_POOL_ID` 미설정 시 기존 방식 fallback.

### ISSUE-014: 녹음 후 Uploading & preparing 블로킹 + 오래된 미팅 영구 업로딩 상태 (Resolved)
- **Category**: recording / frontend
- **Severity**: critical
- **Affected**: `frontend/src/app/record/page.tsx`, `frontend/src/components/RecordButton.tsx`, `backend/internal/service/upload.go`
- **Description**: (1) 녹음 종료 후 "Uploading & preparing..." 중 아무 동작 불가. (2) 오래된 미팅이 영구적으로 업로딩 상태.
- **Root Cause**: presigned URL 생성 시 random UUID를 meetingId로 사용하여 실제 미팅 ID와 불일치. S3 경로의 meetingId가 서버 meetingId와 달라 transcribe Lambda가 올바른 미팅을 업데이트하지 못함.
- **Fix**: (1) RecordButton에 `onBlobReady` 콜백 추가하여 부모가 업로드 흐름 제어. (2) record/page.tsx에서 녹음 종료 후: 미팅 생성 → 서버 meetingId로 presigned URL 요청 → S3 업로드 → notifyComplete 호출 → 미팅 상세로 리다이렉트. (3) upload.go에서 `req.MeetingID`가 있으면 그대로 사용.

### ISSUE-015: Live QA 항상 500 에러 (Resolved)
- **Category**: qa / infra
- **Severity**: critical
- **Affected**: `backend/python/qa/handler.py`, `infra/lib/gateway-stack.ts`, `infra/lib/ai-stack.ts`
- **Description**: QA Lambda 호출 시 Bedrock Converse API에서 항상 에러 발생
- **Root Cause**: (1) `anthropic.claude-opus-4-6-v1` 모델 ID를 직접 호출 불가 — inference profile 필요. (2) `qwen.qwen3-32b-v1:0` 모델이 ap-northeast-2에 존재하지 않음. (3) IAM 정책에 inference profile 리소스 ARN 미포함. (4) Global inference profile 사용 시 foundation-model ARN에 리전이 비어있어 리전 지정 IAM 정책 매칭 실패.
- **Fix**: (1) 모델 ID를 `global.anthropic.claude-opus-4-6-v1` inference profile로 변경. (2) 질문 감지 모델을 `global.anthropic.claude-haiku-4-5-20251001-v1:0`로 변경. (3) IAM 정책에 `inference-profile/*` 리소스 추가. (4) foundation-model ARN 리전을 `*` 와일드카드로 변경.

### ISSUE-016: 외부 마이크 동작 안함 (Resolved)
- **Category**: recording
- **Severity**: major
- **Affected**: `frontend/src/components/RecordButton.tsx`
- **Description**: 외부 마이크 선택해도 내장 마이크로 녹음됨
- **Root Cause**: `getUserMedia` 호출 시 `deviceId: { ideal: deviceId }` 사용 — ideal은 해당 장치가 없으면 다른 장치로 fallback.
- **Fix**: `deviceId: { exact: deviceId }`로 변경하여 선택한 장치만 사용.

### ISSUE-017: Uploading 블로킹 제거 — 실시간 STT를 primary transcript로 사용 (Resolved)
- **Category**: recording / frontend
- **Severity**: critical
- **Affected**: `frontend/src/app/record/page.tsx`
- **Description**: 녹음 종료 후 "Uploading & preparing..." 단계가 S3 업로드 + 재전사(AWS Transcribe)를 기다리며 사용자를 블로킹. 실시간 STT로 이미 transcript가 있는데 중복 전사.
- **Root Cause**: 실시간 STT(Web Speech API/ECS faster-whisper)와 후처리 STT(S3 → Transcribe Lambda)가 이중으로 동작. S3 업로드가 메인 흐름을 블로킹.
- **Fix**: `handleBlobReady` 재구성 — 미팅 생성 → 라이브 transcript/summary를 바로 저장 → 즉시 리다이렉트. 오디오는 백그라운드에서 비동기 업로드(아카이브용). "Uploading" 단계 제거, "Saving transcript..." 으로 교체.

### ISSUE-018: ECS 자동 스케일링 실패 — Lambda 타임아웃 (Resolved)
- **Category**: infra / backend
- **Severity**: major
- **Affected**: `backend/internal/handler/realtime.go`, `backend/internal/service/realtime.go`, `frontend/src/lib/sttOrchestrator.ts`
- **Description**: `/api/realtime/start` 호출 시 ECS task가 뜨지 않음. ECS Fargate 콜드 스타트(30-90초) 중 API Lambda(30초 타임아웃)가 먼저 타임아웃.
- **Root Cause**: `StartRealtime()`이 120초 폴링하지만 Lambda 타임아웃 30초에 걸림. 동기 블로킹 API 설계.
- **Fix**: 비동기 패턴으로 분리 — (1) `StartRealtimeAsync()` 메서드: desiredCount=1만 설정 후 즉시 반환. (2) `GET /api/realtime/status` 엔드포인트 신규 추가. (3) 프론트엔드 `SttOrchestrator`에서 start 후 5초 간격 status 폴링 (최대 120초). ECS 준비 시 WebSocket 연결, 미준비 시 Web Speech API fallback 유지.

### ISSUE-019: 녹음 3초 후 음성 인식 멈춤 — restart 연쇄 충돌 (Resolved)
- **Category**: transcript
- **Severity**: critical
- **Affected**: `frontend/src/lib/speechRecognition.ts`
- **Description**: 미팅 시작 후 마이크 녹음이 3초 정도만 진행되고 transcribe가 멈춤. 입력 레벨 그래프는 동작하지만 텍스트 생성 안 됨.
- **Root Cause**: `hasKoreanSentenceEnding()` 감지 → `flushTimer`(300ms) → `restartRecognition()` 호출 시 old recognition abort → `onend` 100ms 후 또 `restartRecognition()` 호출 → 방금 생성된 fresh recognition이 abort됨 → 연쇄 재시작 → Chrome 쓰로틀링 → 인식 완전 중단.
- **Fix**: `isRestarting` guard 플래그 추가. `restartRecognition()`이 이미 진행 중이면 early return. `onend` 핸들러에서 `!this.isRestarting` 조건 추가. 500ms 후 guard 해제로 정상 재시작 허용.

### ISSUE-020: 미팅 종료 후 Processing 화면 영구 표시 (Resolved)
- **Category**: frontend
- **Severity**: major
- **Affected**: `frontend/src/app/record/page.tsx`
- **Description**: 미팅 종료 후 "Creating meeting..." / "Saving transcript..." 토스트가 지속 표시되어 다른 화면으로 이동 불가.
- **Root Cause**: `handleBlobReady`의 API 호출(`meetingsApi.create`, `meetingsApi.update`)이 hang되거나 응답 지연 시 `postRecordingStep`이 영원히 'creating'/'saving' 상태. dismiss 버튼 없음.
- **Fix**: (1) API 호출에 15초 `withTimeout` 래퍼 추가 — 타임아웃 시 에러 상태로 전환. (2) 진행 중 토스트에 X(dismiss) 버튼 추가 — 클릭 시 홈으로 이동하여 언제든 탈출 가능.
