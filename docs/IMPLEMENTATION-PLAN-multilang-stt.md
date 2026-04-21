# 구현 계획: 다국어 음성 자동 인식 (Multi-Language STT)

> ADR-005 기반. 한국어+영어 혼합 미팅에서 언어를 자동 감지하여 정확한 전사를 생성한다.

## 현재 상태 (As-Is)

| STT 경로 | 언어 설정 | 다국어 | 상태 |
|---------|---------|-------|------|
| Batch Transcribe (녹음 후) | `IdentifyMultipleLanguages + [ko-KR, en-US]` | O | **완료** |
| Nova Sonic Batch (녹음 후) | `IdentifyMultipleLanguages + [ko-KR, en-US]` | O | **완료** |
| Transcribe Streaming (실시간) | `LanguageCode: ko-KR` 하드코딩 | X | 미구현 |
| Web Speech API (실시간) | `lang: ko-KR` 하드코딩 | X | API 제약 |
| 번역 소스 언어 | `'ko'` 하드코딩 | X | 미구현 |

## 목표 (To-Be)

| STT 경로 | 변경 | 다국어 |
|---------|------|-------|
| Batch Transcribe | 유지 | O |
| Transcribe Streaming | `IdentifyMultipleLanguages` 적용 | O |
| Web Speech API | `ko-KR` 유지 (제약), 다국어 미팅 시 Transcribe Streaming 자동 전환 | 부분 |
| 번역 소스 언어 | 감지된 언어 기반 동적 설정 | O |

---

## Phase 1: 실시간 Transcribe Streaming 다국어 (핵심)

### 1-1. `transcribeStreamingClient.ts` 수정

**파일**: `frontend/src/lib/transcribeStreamingClient.ts:127`

현재:
```typescript
LanguageCode: this.config.languageCode
```

변경:
```typescript
// LanguageCode 제거, IdentifyMultipleLanguages 사용
IdentifyMultipleLanguages: true,
LanguageOptions: 'ko-KR,en-US',
PreferredLanguage: 'ko-KR',
```

> AWS Transcribe Streaming에서 `IdentifyMultipleLanguages`와 `LanguageCode`는 상호 배타적. `PreferredLanguage`는 초기 감지 힌트로 사용.

**주의**: `@aws-sdk/client-transcribe-streaming`의 `StartStreamTranscriptionCommandInput` 타입에서 `IdentifyMultipleLanguages` 파라미터 지원 여부 확인 필요. SDK 버전에 따라 다를 수 있음.

### 1-2. `sttManager.ts` 수정

**파일**: `frontend/src/lib/sttManager.ts`

변경 포인트:
- Line 52: `sourceLang = 'ko-KR'` → 제거하거나 `languageMode: 'auto' | 'ko-KR' | 'en-US'`로 변경
- Line 79: Transcribe Streaming 시작 시 `languageCode` 대신 다국어 설정 전달
- Line 175: resume 시 `'ko-KR'` 하드코딩 제거

### 1-3. `useRecordingSession.ts` 수정

**파일**: `frontend/src/hooks/useRecordingSession.ts:180`

현재:
```typescript
manager.start(stream, preferredProvider, 'ko-KR');
```

변경:
```typescript
manager.start(stream, preferredProvider); // 언어 파라미터 제거, 내부에서 auto 사용
```

### 1-4. `speechRecognition.ts` — Web Speech API 폴백

**파일**: `frontend/src/lib/speechRecognition.ts:93`

Web Speech API는 단일 언어만 지원하므로 `ko-KR` 유지. 대신 다국어 미팅에서는 Transcribe Streaming을 기본 엔진으로 자동 선택하는 로직 추가.

**변경**: `sttManager.ts`에서 다국어 모드일 때 Web Speech API 대신 Transcribe Streaming을 강제 선택.

---

## Phase 2: 번역 소스 언어 동적 감지

### 2-1. Transcribe Streaming 언어 감지 결과 활용

AWS Transcribe Streaming의 `IdentifyMultipleLanguages` 응답에는 각 세그먼트의 감지 언어가 포함됨. 이 정보를 활용하여 번역 소스 언어를 동적으로 설정.

**파일**: `frontend/src/lib/sttManager.ts:129`

현재:
```typescript
translateApi.translate(combined, 'ko', this.config.targetLang)
```

변경:
```typescript
// 감지된 언어가 target과 같으면 번역 스킵, 다르면 소스 언어로 사용
const detectedLang = segment.languageCode?.substring(0, 2) || 'ko';
if (detectedLang !== this.config.targetLang) {
  translateApi.translate(combined, detectedLang, this.config.targetLang)
}
```

### 2-2. Batch 전사 결과의 언어 태그 파싱

Batch Transcribe의 `IdentifyMultipleLanguages` 출력 JSON에는 세그먼트별 `language_code` 필드가 포함됨. `ttobak-summarize` Lambda에서 이를 파싱하여 미팅의 주요 언어를 DynamoDB에 저장할 수 있음.

**파일**: `backend/cmd/summarize/main.go` — `downloadAndParseTranscript` 함수

변경: 트랜스크립트 파싱 시 `language_code` 필드도 추출하여 `transcriptSegments`에 포함.

---

## Phase 3: UI — 언어 모드 선택기 (선택적)

### 3-1. RecordingConfig에 언어 모드 추가

**파일**: `frontend/src/components/record/RecordingConfig.tsx`

새로운 선택기:
```
[ 자동 감지 (KR+EN) ] [ 한국어만 ] [ English Only ]
```

기본값: `자동 감지`. 대부분의 사용자는 변경할 필요 없음.

**효과**:
- `자동 감지`: Transcribe Streaming + `IdentifyMultipleLanguages`
- `한국어만`: Web Speech API 또는 Transcribe Streaming + `ko-KR`
- `English Only`: Transcribe Streaming + `en-US`

### 3-2. 미팅 상세에 언어 태그 표시

미팅 카드/상세 페이지에 감지된 언어 뱃지 표시 (예: `KR` `EN`).

---

## 파일 변경 요약

| Phase | 파일 | 변경 |
|-------|------|------|
| 1-1 | `frontend/src/lib/transcribeStreamingClient.ts` | `LanguageCode` → `IdentifyMultipleLanguages` |
| 1-2 | `frontend/src/lib/sttManager.ts` | 언어 파라미터 제거, auto 모드 기본 |
| 1-3 | `frontend/src/hooks/useRecordingSession.ts` | `'ko-KR'` 하드코딩 제거 |
| 1-4 | `frontend/src/lib/speechRecognition.ts` | 변경 없음 (ko-KR 유지) |
| 2-1 | `frontend/src/lib/sttManager.ts` | 번역 소스 언어 동적 설정 |
| 2-2 | `backend/cmd/summarize/main.go` | 언어 태그 파싱 (선택적) |
| 3-1 | `frontend/src/components/record/RecordingConfig.tsx` | 언어 모드 UI (선택적) |
| 3-2 | `frontend/src/components/MeetingList.tsx` | 언어 뱃지 (선택적) |

## 우선순위

```
Phase 1 (필수) ──→ Phase 2 (권장) ──→ Phase 3 (선택적)
실시간 다국어 STT    번역 소스 동적       UI 언어 선택기
```

## 검증 방법

1. **Phase 1 테스트**: 한국어+영어 혼합으로 실시간 녹음 → 라이브 트랜스크립트에서 영어 구간이 정확히 표시되는지 확인
2. **Batch 테스트** (이미 완료): 녹음 후 미팅 상세 → 전사 결과에서 영어 구간 확인
3. **번역 테스트**: 한국어→영어 번역 중 영어 구간은 번역 없이 통과하는지 확인

## 리스크

| 리스크 | 영향 | 완화 |
|--------|------|------|
| Transcribe Streaming SDK가 `IdentifyMultipleLanguages` 미지원 | Phase 1 불가 | SDK 버전 확인, 미지원 시 HTTP/2 직접 호출 검토 |
| 실시간 언어 감지 지연 | 첫 1-2초 텍스트 누락 | `PreferredLanguage: ko-KR`로 초기 힌트 제공 |
| Cognito Identity Pool 제거됨 | Transcribe Streaming 브라우저 직접 호출 불가 | WebSocket Lambda 경유 또는 Identity Pool 재추가 |

## 비용 영향

- Transcribe Streaming 다국어: 단일 언어 대비 추가 비용 없음 (동일 분당 과금)
- Batch 다국어: 이미 적용 완료, 추가 비용 없음
- 전체적으로 비용 중립
