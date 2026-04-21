# Ttobak 프로젝트 3-모델 코드/아키텍처 리뷰

> 리뷰 일시: 2026-03-25
> 리뷰어: Claude Opus (Architect), Gemini 2.5 Pro, OpenAI Codex (gpt-5.4)

---

## 요약

| 심각도 | Claude | Gemini | Codex | 공통 |
|--------|--------|--------|-------|------|
| CRITICAL | 2 | 3 | 2 | 1 (race condition) |
| HIGH | 4 | 3 | 6 | 3 (JWT, IAM, N+1) |
| MEDIUM | 8 | 4 | 7 | 4 |
| LOW | 6 | 1 | 2 | — |

> **Codex 업데이트**: 전체 리뷰 결과 반영 (126K 토큰 사용, 30+ 파일 분석)

---

## 1. 공통 발견사항 (3개 모두 동의)

### [CRITICAL] 이미지 처리 Race Condition
- **파일**: `backend/cmd/process-image/main.go`
- **내용**: S3 이벤트가 `process-image` Lambda를 트리거할 때, 프론트엔드의 `upload/complete` API가 아직 DynamoDB에 attachment 레코드를 생성하지 않았을 수 있음. 결과: 이미지 분석 결과 유실.
- **Claude**: CRITICAL — 구현은 되었으나 race condition + S3 클라이언트 누락
- **Gemini**: CRITICAL — EventBridge 커스텀 이벤트로 전환 제안
- **Codex**: 동일 이슈 확인 (upload 권한 검증과 함께 지적)
- **권장 수정**: `upload/complete` API에서 DynamoDB 레코드 생성 후 EventBridge 커스텀 이벤트 발행 → `process-image` 트리거

### [HIGH] JWT 서명 검증 우회 경로
- **파일**: `backend/internal/middleware/auth.go:141-146`
- **내용**: `COGNITO_USER_POOL_ID` 환경변수 미설정 시 `parseUnverifiedJWT` fallback으로 서명 없이 토큰 수락
- **Claude**: Lambda@Edge가 사전 검증하므로 defense-in-depth 문제
- **Gemini**: Lambda@Edge 우회 시 위조 토큰 공격 취약
- **Codex**: 동일 확인 — 백엔드 자체 검증 필수
- **권장 수정**: `parseUnverifiedJWT` 제거, 모든 경로에서 서명 검증 강제

### [HIGH] 단일 IAM 역할 공유 (최소 권한 위반)
- **파일**: `infra/lib/ai-stack.ts:14`
- **내용**: 6개 Lambda 함수가 `ttobak-lambda-role` 하나를 공유 → 불필요한 권한 노출
- **Claude**: `resources: ['*']`도 함께 지적 (Cognito, Bedrock KB, API GW 등)
- **Gemini**: CRITICAL로 평가 — 침해 시 전체 서비스 권한 노출
- **Codex**: IAM 과잉 권한 확인
- **권장 수정**: Lambda별 개별 IAM 역할 생성, 필요한 서비스만 허용

### [HIGH] N+1 쿼리 (BatchGetMeetings)
- **파일**: `backend/internal/repository/dynamodb.go:260-279`
- **내용**: `BatchGetMeetings`가 실제로는 loop에서 `GetMeetingByID` 순차 호출
- **Claude**: LOW (소규모 데이터)로 평가
- **Gemini**: HIGH — `dynamodb.BatchGetItem` 사용 권장
- **Codex**: 성능 저하 후보로 식별
- **권장 수정**: `BatchGetItem` 또는 `errgroup` 병렬화

---

## 2. 두 모델이 동의한 발견

### [MEDIUM] 회의 삭제 원자성 미보장
- **Gemini**: HIGH — `DeleteMeeting`에서 첨부/공유를 순차 삭제, 중간 실패 시 고아 데이터
- **Codex**: 동일 확인 (repository 코드 리뷰에서 발견)
- Claude는 별도 언급 없음
- **권장 수정**: `TransactWriteItems`로 원자적 삭제

### [MEDIUM] CDK 하드코딩된 ARN
- **Gemini**: CRITICAL — `knowledge-stack.ts:107`에서 `Fn.sub`으로 역할 ARN 문자열 하드코딩
- **Claude**: 관련 이슈 간접 언급 (cross-stack 참조)
- **권장 수정**: `ai-stack`에서 역할 객체를 props로 전달

### [MEDIUM] Lambda@Edge 인라인 코드
- **Claude**: LOW — 테스트 어려움, 별도 파일 분리 권장
- **Gemini**: MEDIUM — 린팅/테스트/버전관리 어려움
- **권장 수정**: `Code.fromAsset`으로 외부 파일 참조

---

## 3. 독자적 발견 (한 모델만 발견)

### Gemini 독자 발견

| 심각도 | 이슈 | 파일 |
|--------|------|------|
| MEDIUM | OpenSearch Serverless `AllowFromPublic: true` — 공격 표면 확대 | `knowledge-stack.ts:71` |
| MEDIUM | 토큰을 `localStorage`에 저장 — XSS 취약 | `frontend/src/lib/auth.ts:121` |
| MEDIUM | `UpdateMeeting`이 `PutItem` 사용 — 동시 수정 시 last-write-wins | `repository/dynamodb.go:374` |
| MEDIUM | 미사용 DynamoDB Stream 활성화 — 불필요한 비용 | `storage-stack.ts:16` |

### Claude 독자 발견

| 심각도 | 이슈 | 파일 |
|--------|------|------|
| CRITICAL | `summarize` Lambda의 URL 디코딩이 `+` → space만 처리 (`%XX` 미지원) | `cmd/summarize/main.go:127` |
| HIGH | 에러 핸들링에서 `err.Error() == "forbidden"` 문자열 비교 | `handler/meeting.go:127-136` |
| MEDIUM | Refresh Token 유효기간 3시간 — 너무 짧음 (일반적으로 7-30일) | `auth-stack.ts:85-86` |
| MEDIUM | `checkAccess` 함수가 여러 시나리오에서 동일한 `(nil, "", nil)` 반환 | `service/meeting.go:56-58` |

### Codex 독자 발견

| 심각도 | 이슈 | 파일 |
|--------|------|------|
| CRITICAL | **API Gateway에 JWT authorizer 없음** — CloudFront 우회 시 `execute-api` 직접 호출로 인증 전체 무력화 | `gateway-stack.ts:143` |
| CRITICAL | **QA 엔드포인트 3개 인증 없이 호출 가능** — `/api/qa/ask`, `/api/qa/detect-questions`는 완전 무인증, `/api/qa/meeting/{id}`도 JWT sub만 추출 | `handler.py:47,230,309` |
| HIGH | **업로드 완료 시 소유권 미검증** — `meetingId`와 `key`의 소유자 일치 확인 없이 아무 회의에 파일 첨부/오디오키 덮어쓰기 가능 | `upload.go:64`, `upload.go:82` |
| HIGH | **Notion API 키 평문 저장** — DynamoDB에 암호화 없이 저장, Secrets Manager/KMS 미사용 | `settings.go:76`, `integration.go:7` |
| HIGH | **KB 검색 시 사용자 격리 없음** — 업로드는 `kb/{userId}/`로 분리하지만 retrieval에 metadata filter 없어 타 사용자 문서 노출 | `kb.go:73`, `handler.py:88` |
| HIGH | `kb/main.go`에 TODO 5개 — KB 핵심 기능(sync, query, ingest) 미구현 | `backend/cmd/kb/main.go` |
| MEDIUM | **GET /api/meetings/{id}가 쓰기 부작용** — 조회 API가 상태를 `error`로 갱신하는 side-effect | `handler/meeting.go:139` |
| MEDIUM | **QA 세션이 사용자 스코프 없음** — `SESSION#{sessionId}` 단독 키로 세션 ID 유출 시 타 사용자 대화 접근 | `handler.py:114` |
| MEDIUM | **cold start 최적화 필요** — API Lambda가 모든 AWS 클라이언트를 일괄 초기화 | `cmd/api/main.go:26` |
| MEDIUM | **KB 파일 삭제 비효율** — `ListObjectsV2`로 전체 prefix 스캔 후 단건 삭제 | `kb.go:165` |
| MEDIUM | **회의 상세 조회에서 에러 무시** — 첨부/공유 조회 실패해도 정상 응답 반환 | `service/meeting.go:121` |
| MEDIUM | **CDK 하드코딩 값** — 인증서 ARN, 도메인, 버킷명이 환경별 분리 불가 | `frontend-stack.ts:83` |
| MEDIUM | 테스트 코드 전무 — `infra.test.ts`가 주석 처리됨, backend/frontend 테스트 없음 | 프로젝트 전체 |
| LOW | **README와 구현 불일치** — realtime 경로 삭제됐는데 문서는 이중 STT 기술 | `README.md` |
| LOW | `infra-stack.ts`가 빈 스캐폴드 — 사용하지 않는 스택 | `infra/lib/infra-stack.ts` |

---

## 4. 의견 차이

| 이슈 | Claude | Gemini | Codex |
|------|--------|--------|-------|
| 모놀리식 API Lambda | 언급 없음 | LOW — 단일 장애점 가능성 | 언급 없음 |
| N+1 쿼리 심각도 | LOW | HIGH | MEDIUM |
| 단일 IAM 역할 심각도 | HIGH | CRITICAL | HIGH |
| race condition 해결 방향 | retry + backoff | EventBridge 커스텀 이벤트 | upload 완료 후 트리거 |

---

## 5. 우선순위별 실행 계획

### P0 — 즉시 수정 (데이터 유실/보안) ⚠️

1. **URL 디코딩 수정** (`summarize/main.go:127`)
   - `strings.ReplaceAll` → `url.QueryUnescape` (5분, Claude 발견)

2. **JWT 우회 제거** (`middleware/auth.go`)
   - `parseUnverifiedJWT` 삭제, 모든 환경에서 JWKS 검증 강제 (3개 모두 동의)

3. **Python QA 인증 체계 추가** (`handler.py` 전체)
   - QA 엔드포인트 3개 모두 JWT 검증 필수화 (Codex CRITICAL)
   - API Gateway에 JWT authorizer 추가 (Codex CRITICAL)

4. **업로드 완료 소유권 검증** (`upload.go:64`)
   - `meetingId` + `userId` 소유자 일치 확인 (Codex HIGH)

5. **process-image race condition 수정**
   - `upload/complete` API → DynamoDB 레코드 생성 → EventBridge 커스텀 이벤트 → `process-image` (3개 모두 동의)

### P1 — 높은 우선순위 (성능/보안 강화)

6. **Lambda별 개별 IAM 역할** (`ai-stack.ts`) (3개 모두 동의)
7. **KB 검색 사용자 격리** — retrieval에 metadata filter 추가 (Codex HIGH)
8. **Notion API 키 암호화** — Secrets Manager + KMS 적용 (Codex HIGH)
9. **에러 핸들링 sentinel error 패턴** (`handler/meeting.go`) (Claude HIGH)
10. **BatchGetMeetings → BatchGetItem** (`repository/dynamodb.go`) (Gemini HIGH)
11. **DeleteMeeting 원자적 트랜잭션** (`repository/dynamodb.go`) (Gemini HIGH)

### P2 — 중간 우선순위 (안정성/비용)

12. UpdateMeeting: `PutItem` → `UpdateItem` + 조건부 표현식 (Gemini+Codex)
13. CDK 하드코딩 ARN → props 전달 + 환경별 분리 (Gemini+Codex)
14. Cognito `removalPolicy: DESTROY` → `RETAIN` (프로덕션) (Gemini)
15. Lambda@Edge 코드 외부 파일 분리 (Claude+Gemini)
16. 미사용 DynamoDB Stream 제거 (Gemini)
17. Refresh Token 유효기간 7일로 연장 (Claude)
18. QA 세션에 사용자 스코프 추가 (Codex)
19. GET 조회 API에서 쓰기 부작용 제거 (Codex)

### P3 — 낮은 우선순위 (개선)

20. OpenSearch Serverless VPC 엔드포인트 (Gemini)
21. localStorage → httpOnly cookie (Gemini+Codex)
22. 테스트 코드 추가 — 인증 우회 방지, 업로드 권한, KB tenant isolation, CDK snapshot (Codex)
23. README와 구현 불일치 수정 (Codex)
24. 빈 `infra-stack.ts` 제거 (Codex)
25. KB Lambda TODO 구현 (Codex)

---

## 6. 리뷰어별 평가

| 리뷰어 | 강점 | 약점 |
|--------|------|------|
| **Claude Opus** | 코드 라인 단위 정밀 분석, 이전 CODE-REVIEW.md 대비 변경 추적, 구체적 코드 수정 제안 | 일부 이슈를 보수적으로 평가 (N+1을 LOW), 보안 공격 벡터 분석이 상대적으로 약함 |
| **Gemini 2.5 Pro** | 아키텍처/인프라 관점 우수, CDK 안티패턴 발견, 동시성/원자성 문제 포착 | 코드 수준 세부 사항 (URL 디코딩 등) 놓침, Python QA 핸들러 미검토 |
| **Codex gpt-5.4** | **가장 포괄적 리뷰** — 보안 공격 벡터 분석 탁월(API GW 우회, 업로드 소유권, KB tenant isolation, QA 무인증), 실용적 개선안 제시, 30+ 파일 직접 탐색 | 126K 토큰 사용 (무료 티어 부담), 일부 중복 출력 |

> **결론**: 세 모델의 관점이 상호보완적이며, 특히 Codex가 예상보다 깊은 보안 분석을 제공함.
> - **Claude** → 코드 수준 버그 (URL 디코딩, 에러 문자열 비교)
> - **Gemini** → 인프라/CDK 안티패턴 (ARN 하드코딩, 삭제 원자성, DDB Stream)
> - **Codex** → 보안 공격 벡터 (API GW 우회, 업로드 IDOR, KB tenant leak, QA 무인증)
>
> 3-모델 협업으로 총 25개 고유 이슈 발견 (단일 모델 최대 ~15개). 특히 P0 보안 이슈 5건 중 3건은 단일 모델로는 놓칠 수 있었던 항목.
