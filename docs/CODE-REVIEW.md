# Ttobak - Code Review Report

> 2026-03-05 전체 코드 리뷰 결과

## Review Summary

| 영역 | 파일 수 | 심각도 High | Medium | Low |
|------|---------|------------|--------|-----|
| Infrastructure (CDK) | 6 | 0 | 1 | 2 |
| Backend (Go) | 16 | 2 | 4 | 3 |
| Frontend (Next.js) | 19 | 0 | 3 | 4 |
| **Total** | **41** | **2** | **8** | **9** |

---

## 1. Infrastructure (CDK)

### [LOW] infra.ts:18 - Origin verify secret 하드코딩
```typescript
const originVerifySecret = process.env.ORIGIN_VERIFY_SECRET || 'ttobak-origin-secret-2024';
```
- **문제**: 기본값이 예측 가능한 문자열. 프로덕션에서 환경변수 미설정 시 보안 취약
- **권장**: cdk.SecretValue 또는 SSM SecureString 사용, 기본값 제거하고 필수로 변경

### [MED] api-stack.ts - ALB Listener HTTP only
- **문제**: ALB listener가 HTTP:80만 사용. CloudFront→ALB 구간이 평문 통신
- **권장**: 당장은 괜찮지만 (CloudFront가 HTTPS 종단), 프로덕션에서는 ACM 인증서 + HTTPS listener 고려

### [LOW] storage-stack.ts:49 - CORS allowedOrigins ['*']
- **문제**: 모든 origin 허용
- **권장**: CloudFront 도메인 + localhost:3000만 허용 (TODO 주석은 있음)

---

## 2. Backend (Go)

### [HIGH] process-image/main.go:109-132 - updateAttachmentByKey 미구현
```go
func updateAttachmentByKey(ctx context.Context, originalKey, attachType, processedContent string) {
    log.Printf("Would update attachment with originalKey=%s, type=%s", originalKey, attachType)
    // ... placeholder only
}
```
- **문제**: S3 이벤트로 트리거된 이미지 처리 결과가 DynamoDB에 저장되지 않음. 핵심 기능 누락
- **권장**: S3 key 경로에 meetingId 포함하도록 변경 (`images/{userId}/{meetingId}/{filename}`)하고, meetingId 파싱 후 attachment 업데이트 구현

### [HIGH] summarize/main.go:95-161 - S3 트리거 vs DynamoDB Stream 혼재
```go
// Handler processes S3 events for completed transcriptions
func Handler(ctx context.Context, s3Event events.S3Event) error {
```
- **문제**: CDK에서는 DynamoDB Stream으로 summarize Lambda를 트리거하지만, 코드는 S3 이벤트 핸들러로 구현됨. 트리거 방식 불일치
- **권장**: 둘 중 하나로 통일:
  - (A) S3 트리거 유지: CDK에서 transcripts/ prefix S3 이벤트 → summarize Lambda
  - (B) DynamoDB Stream 유지: Lambda 핸들러를 DynamoDBEvent로 변경
  - **추천**: (A) S3 트리거가 더 단순. CDK의 DynamoDB Stream 트리거를 S3 트리거로 변경

### [MED] middleware/auth.go - JWT 서명 검증 없음
```go
func parseALBJWT(token string) (*ALBOIDCClaims, error) {
    parts := strings.Split(token, ".")
    // Decode payload (second part) - no signature verification
    payload, err := base64URLDecode(parts[1])
```
- **문제**: ALB가 전달한 JWT의 서명을 검증하지 않음. ALB 뒤에서만 사용되므로 즉각적 위험은 낮지만, 방어적 코딩 관점에서 부족
- **참고**: ALB OIDC JWT는 ALB가 서명하므로, 프로덕션에서는 ALB의 공개 키로 검증 필요
  - https://docs.aws.amazon.com/elasticloadbalancing/latest/application/listener-authenticate-users.html

### [MED] handler/meeting.go - writeError 함수 중복 정의 가능성
- **문제**: `writeError`와 `writeJSON`가 meeting.go에 정의되어 있고, share.go와 upload.go에서도 사용. Go에서 같은 패키지 내 다른 파일에서 사용 가능하지만, 유틸리티 파일로 분리하는 것이 관리 편의성에 좋음
- **권장**: `handler/response.go`로 분리

### [MED] handler/meeting.go - ListMeetings의 ListShares 호출에서 missing import 확인 필요
- **문제**: 수정된 ListMeetings에서 `repository.ListMeetingsParams` 사용하는데, 이 struct가 repository에 정의되어 있는지 확인 필요
- **확인**: `go build ./...` 통과했으므로 존재는 하지만, 페이지네이션 구현의 정확성 검증 필요

### [MED] service/meeting.go:85-92 - N+1 쿼리 문제
```go
for _, share := range result.Shares {
    meeting, err := s.repo.GetMeetingByID(ctx, share.MeetingID)
```
- **문제**: 공유받은 회의 N개에 대해 각각 GetMeetingByID 호출 → N+1 쿼리
- **권장**: BatchGetItem으로 일괄 조회하거나, 공유 레코드에 미팅 제목/요약을 비정규화

### [LOW] transcribe/main.go:57 - URL 디코딩 불완전
```go
key = strings.ReplaceAll(key, "+", " ")
```
- **문제**: 전체 URL 디코딩이 아닌 `+` → 공백만 처리. `%20`, `%2F` 등 미처리
- **권장**: `url.QueryUnescape(key)` 사용

### [LOW] cmd/api/main.go:37,41 - 기본 테이블/버킷명 불일치
```go
tableName = "ttobak-meetings"  // CDK는 "ttobak-main"
bucketName = "ttobak-assets"   // CDK는 "ttobak-storage-{account}-{region}"
```
- **문제**: 환경변수가 없을 때의 fallback 값이 CDK와 다름
- **권장**: CDK가 env var를 주입하므로 실제 문제는 없지만, 로컬 개발 시 혼동 가능. 주석 추가

### [LOW] service/bedrock.go:234 - 분류 실패 시 에러 반환하면서 기본값도 반환
```go
return model.AttachTypePhoto, err  // 에러와 값 동시 반환
```
- **문제**: 에러가 nil이 아닌데 기본값도 반환. 호출자가 에러를 무시하면 잘못된 분류 사용
- **권장**: 에러 시 `"", err` 반환

---

## 3. Frontend (Next.js)

### [MED] components/auth/AuthProvider.tsx - Cognito 토큰 localStorage 저장
- **문제**: JWT를 localStorage에 저장하면 XSS 취약 시 토큰 탈취 가능
- **권장**: ALB Cognito Action이 세션 쿠키를 관리하므로, 프론트엔드에서 직접 토큰 관리가 필요한지 재검토. ALB가 인증을 처리하면 프론트엔드는 별도 토큰 불필요할 수 있음

### [MED] lib/auth.ts - Cognito 직접 구현 vs ALB 인증
- **문제**: ALB Cognito Action이 인증을 처리하는데, 프론트엔드에서도 amazon-cognito-identity-js로 독립적 인증 구현. 이중 인증 경로가 혼란을 줄 수 있음
- **권장**: 인증 전략 하나로 통일:
  - (A) ALB가 인증 → 프론트엔드는 인증 UI 불필요, ALB가 로그인 리다이렉트
  - (B) 프론트엔드가 인증 → ALB Cognito Action 제거, JWT를 직접 전달
  - **현재 상태**: 둘 다 구현되어 있어 결정 필요

### [MED] app/page.tsx - 목데이터 하드코딩
```typescript
const mockMeetings: Meeting[] = [...]
```
- **문제**: API 연동이 안 되어 있고 목데이터 사용 중. `useEffect`에서 `setMeetings(mockMeetings)` 하드코딩
- **권장**: 배포 전에 `meetingsApi.list()` 호출로 교체 필요

### [LOW] components/MeetingEditor.tsx - Tiptap 패키지 설치 확인
- **확인 필요**: `@tiptap/react`, `@tiptap/starter-kit` 등이 package.json에 있는지 확인
- Tiptap 관련 코드가 있지만 실제 빌드에서 사용되지 않을 수 있음 (dynamic import 등)

### [LOW] types/meeting.ts - DynamoDB PK/SK가 프론트엔드 타입에 포함
```typescript
interface Meeting {
  PK: string;
  SK: string;
```
- **문제**: 내부 DB 스키마가 프론트엔드에 노출됨
- **권장**: API 응답 타입과 DB 모델 타입 분리. 프론트엔드는 `MeetingListItem`, `MeetingDetailResponse` 타입만 사용

### [LOW] lib/upload.ts - presigned URL 만료 처리 없음
- **문제**: presigned URL을 받은 후 업로드 실패 시 재시도 로직이나 URL 만료 처리 없음
- **권장**: 업로드 실패 시 새 presigned URL을 재발급받는 retry 로직

### [LOW] components/RecordButton.tsx - AudioContext 미해제
```typescript
const audioContext = new AudioContext();
```
- **문제**: 녹음 종료 시 `audioContext.close()` 호출 없음. 메모리 누수 가능
- **권장**: `mediaRecorder.onstop`에서 `audioContext.close()` 추가

---

## 4. Cross-Cutting Issues

### [DECISION NEEDED] 인증 전략 통일
- **현재**: ALB Cognito Action + 프론트엔드 Cognito 직접 인증 이중 구현
- **옵션 A** (추천): ALB 인증만 사용
  - 장점: 단순, 보안 강화 (토큰이 서버 사이드에만 존재)
  - 단점: 프론트엔드 로그인 UI가 Cognito Hosted UI로 리다이렉트
- **옵션 B**: 프론트엔드 인증만 사용
  - 장점: 커스텀 로그인 UI, 더 나은 UX
  - 단점: ALB Cognito Action 제거 필요, 보안 레이어 하나 감소

### [DECISION NEEDED] Summarize Lambda 트리거 방식
- **현재**: CDK는 DynamoDB Stream 트리거, 코드는 S3 이벤트 핸들러
- **추천**: S3 이벤트 트리거로 통일 (transcripts/ prefix)

---

## 5. 전체 평가

### 잘된 점
- DynamoDB Single Table Design이 잘 구현됨 (PK/SK 패턴, GSI 활용)
- 보안 3중 잠금 (SG Prefix List + WAF + Cognito) 올바르게 구현
- Bedrock Claude Vision 이미지 분류/분석 파이프라인 구조가 좋음
- 프론트엔드 반응형 디자인이 디자인 spec과 잘 맞음
- Go 코드의 레이어 분리 (handler → service → repository)가 깔끔

### 개선이 필요한 점
- process-image Lambda의 attachment 업데이트 미구현 (HIGH)
- summarize Lambda 트리거 방식 불일치 (HIGH)
- 인증 전략 이중 구현으로 인한 혼란 (DECISION NEEDED)
- N+1 쿼리 문제 (공유 회의 목록)
- 프론트엔드 API 연동 미완성 (목데이터)

### 배포 전 필수 수정 (Blockers)
1. process-image의 `updateAttachmentByKey` 구현
2. summarize Lambda 트리거 방식 통일
3. 인증 전략 결정 및 통일
