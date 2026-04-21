# ADR-005: Multi-Language Auto-Detection for Speech-to-Text

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
Ttobak is used in Korean enterprise environments where meetings frequently switch between Korean and English. Technical discussions involve English terms (AWS services, architecture patterns, code references), and meetings with international participants are conducted in mixed languages.

The previous implementation hardcoded `LanguageCode: ko-KR` across all STT paths:
- Web Speech API (live): `lang = 'ko-KR'` in constructor
- AWS Transcribe Streaming (live): `LanguageCode: 'ko-KR'` passed by caller
- AWS Transcribe Batch (post-recording): `types.LanguageCodeKoKr` in `StartTranscriptionJob`

English words spoken during meetings were force-interpreted as Korean phonemes, producing garbled text in the final transcript.

## Options Considered

### Option 1: Keep single-language mode with manual language selector
- **Pros**: Simple implementation; no API behavior change; user has explicit control
- **Cons**: Users must know the meeting language in advance; cannot handle mid-meeting language switches; extra UI complexity for a poor experience

### Option 2: Use AWS Transcribe `IdentifyMultipleLanguages` with `LanguageOptions`
- **Pros**: Automatic detection of language switches within a single recording; no user input required; `LanguageOptions` narrows detection to expected languages (ko-KR, en-US) for accuracy; supported in both batch and streaming APIs
- **Cons**: Slightly higher latency for initial language identification; `LanguageCode` and `IdentifyMultipleLanguages` are mutually exclusive (requires code change); Web Speech API does not support this feature

### Option 3: Run parallel single-language transcription jobs and merge results
- **Pros**: Could produce highest accuracy per language segment
- **Cons**: Double the cost; complex merge logic; no clear way to determine which segment belongs to which language without detection

## Decision
Use Option 2: AWS Transcribe `IdentifyMultipleLanguages` with `LanguageOptions: [ko-KR, en-US]` for batch transcription. This replaces the hardcoded `LanguageCode` parameter.

Implementation scope:
- **Batch STT (post-recording)**: `IdentifyMultipleLanguages: true` + `LanguageOptions` on both standard and Nova Sonic transcription paths in `backend/internal/service/transcribe.go`
- **Live STT (Transcribe Streaming)**: Planned for future iteration; currently still `ko-KR`
- **Web Speech API (browser fallback)**: No change possible due to API limitation; users needing multi-language should use AWS Transcribe Streaming

## Consequences

### Positive
- Korean+English mixed meetings produce accurate transcripts without user configuration
- Existing Korean-only meetings are unaffected (Korean is still detected automatically)
- No UI changes required; detection is fully automatic
- Foundation for adding more languages (Japanese, Chinese) by extending `LanguageOptions`

### Negative
- Web Speech API (browser fallback) remains Korean-only; users must select AWS Transcribe Streaming for multi-language live STT
- Initial language identification may add 1-2 seconds of latency to the first transcript segment
- If unexpected languages appear (e.g., Japanese), they may be misidentified as Korean or English

## References
- Commit: `ad5905b feat: enable multi-language transcription (Korean + English)`
- AWS Docs: [Transcribing multi-language audio](https://docs.aws.amazon.com/transcribe/latest/dg/lang-id-batch.html)
- File: `backend/internal/service/transcribe.go` lines 52-65, 96-109

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
Ttobak은 한국 기업 환경에서 사용되며, 미팅 중 한국어와 영어 간 전환이 빈번합니다. 기술 논의에서 영어 용어(AWS 서비스, 아키텍처 패턴, 코드 참조)가 사용되고, 국제 참석자가 있는 미팅은 혼합 언어로 진행됩니다.

이전 구현은 모든 STT 경로에서 `LanguageCode: ko-KR`을 하드코딩했습니다:
- Web Speech API (실시간): 생성자에서 `lang = 'ko-KR'`
- AWS Transcribe Streaming (실시간): 호출자가 `LanguageCode: 'ko-KR'` 전달
- AWS Transcribe Batch (녹음 후): `StartTranscriptionJob`에서 `types.LanguageCodeKoKr`

미팅 중 발화된 영어 단어가 한국어 음소로 강제 해석되어 최종 트랜스크립트에 왜곡된 텍스트가 생성되었습니다.

## 검토한 옵션

### 옵션 1: 수동 언어 선택기와 단일 언어 모드 유지
- **장점**: 구현이 간단함; API 동작 변경 없음; 사용자가 명시적으로 제어 가능
- **단점**: 사용자가 미팅 언어를 사전에 알아야 함; 미팅 중 언어 전환 처리 불가; UX가 좋지 않은 추가 UI

### 옵션 2: AWS Transcribe `IdentifyMultipleLanguages` + `LanguageOptions` 사용
- **장점**: 단일 녹음 내 언어 전환 자동 감지; 사용자 입력 불필요; `LanguageOptions`가 예상 언어(ko-KR, en-US)로 감지 범위를 좁혀 정확도 향상; 배치 및 스트리밍 API 모두 지원
- **단점**: 초기 언어 식별에 약간의 지연; `LanguageCode`와 `IdentifyMultipleLanguages`는 상호 배타적(코드 변경 필요); Web Speech API는 이 기능 미지원

### 옵션 3: 병렬 단일 언어 전사 작업 실행 후 결과 병합
- **장점**: 언어별 세그먼트에서 가장 높은 정확도 가능
- **단점**: 비용 2배; 복잡한 병합 로직; 감지 없이는 어떤 세그먼트가 어떤 언어인지 판별 어려움

## 결정
옵션 2를 사용합니다: 배치 전사에 AWS Transcribe `IdentifyMultipleLanguages` + `LanguageOptions: [ko-KR, en-US]`를 적용합니다. 기존 하드코딩된 `LanguageCode` 파라미터를 대체합니다.

구현 범위:
- **배치 STT (녹음 후)**: `backend/internal/service/transcribe.go`의 표준 및 Nova Sonic 전사 경로 모두에 `IdentifyMultipleLanguages: true` + `LanguageOptions` 적용
- **실시간 STT (Transcribe Streaming)**: 향후 반복에서 계획; 현재는 `ko-KR` 유지
- **Web Speech API (브라우저 폴백)**: API 제한으로 변경 불가; 다국어가 필요한 사용자는 AWS Transcribe Streaming 사용 권장

## 영향

### 긍정적
- 한국어+영어 혼합 미팅에서 사용자 설정 없이 정확한 트랜스크립트 생성
- 기존 한국어 전용 미팅은 영향 없음 (한국어가 자동 감지됨)
- UI 변경 불필요; 감지가 완전 자동화
- `LanguageOptions` 확장으로 일본어, 중국어 등 추가 언어 지원 기반 마련

### 부정적
- Web Speech API (브라우저 폴백)는 한국어 전용 유지; 다국어 실시간 STT는 AWS Transcribe Streaming 선택 필요
- 초기 언어 식별이 첫 번째 전사 세그먼트에 1-2초 지연 추가 가능
- 예상치 못한 언어(예: 일본어)가 나타나면 한국어 또는 영어로 잘못 식별될 수 있음

## 참고 자료
- 커밋: `ad5905b feat: enable multi-language transcription (Korean + English)`
- AWS 문서: [다국어 오디오 전사](https://docs.aws.amazon.com/transcribe/latest/dg/lang-id-batch.html)
- 파일: `backend/internal/service/transcribe.go` 52-65행, 96-109행
