# ADR-039: Meeting Document Text Extraction

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

<a id="english"></a>

## English

### Status

Accepted — 2026-09-12. Deploy the worker before enabling upload/retry producers.
API, summary, Q&A and UI integration remain separate rollout steps.

### Context and options

Meeting attachments previously contributed filenames only. Calling a parser
synchronously from the API would spend its request budget on untrusted files.
Use an asynchronous Lambda with the bounded native PDF/PPTX/DOCX/Markdown parser.
OCR and legacy Office conversion are outside this parser's declared scope. Internal XLSX chart workbooks remain opaque and are not parsed/executed; their omission produces partial extraction while preserving slide text. OLE, macros and other embedded packages remain rejected.

The parser ceilings remain 20 MiB input, 512 MiB child address space, 8/9 seconds
CPU and 12 seconds wall time. Completeness covers native text, not rendered
appearance or OCR. Ordinary hyperlinks remain inert; chart workbooks are the
explicit opaque-package exception above.

### Decision

The API creates a canonical `ATTACH#` row and a separate `ATTEXT#` queued run
before publishing `ttobak.upload / DocumentUploadCompleted`. The worker rereads
the meeting, attachment and run, then conditionally claims an active lease.
Event-supplied keys do not authorize reads.

Pin the stored source ETag, limit input to 20 MiB, run the parser in a child
with resource limits, and write an immutable result under
`files/{uploader}/{meeting}/text/{attachment}/{run}.json`. Results include source
identity and page/slide/paragraph locations. Publish completion only through a
transaction checking the same parent, attachment, run and lease. Partial text is
explicit; empty/scanned/unsupported input cannot become an empty success.
Failure preserves prior result metadata. Readers must verify current source
bindings; S3 HEAD and DynamoDB completion are not one atomic operation.

The Lambda uses isolated subnets and HTTPS egress only to existing S3/DynamoDB
gateway endpoint prefix lists. Its role reads `files/*`, writes only result
JSON keys, and accesses the required table records. The child receives no cloud
credentials; process limits/audit hooks do not claim an RCE-proof sandbox.

### Consequences

The worker adds asynchronous delay and explicit failure/retry handling. Source
and result limits bound work but exclude OCR/image-only documents and very large
files. Immutable orphan results may remain after uncertain commits/deletion.
The execution role remains shared across tenants within its documented prefixes;
canonical validation is mandatory and network isolation is defense in depth.

### References

- [Worker contract and verification](../../backend/python/document-extract/LAMBDA.md)
- [Parser scope and limits](../../backend/python/document-extract/README.md)

<a id="korean"></a>

## 한국어

### 상태

승인 — 2026-09-12. 업로드·재시도 요청을 활성화하기 전에 워커를 배포합니다.
API·요약·Q&A·화면 연결은 별도 배포 단계입니다.

### 배경과 대안

기존 회의 첨부 문서는 파일명만 활용했습니다. API 안에서 파일을 동기 파싱하면
요청 시간 제한을 소모하므로, 제한된 PDF/PPTX/DOCX/Markdown 파서를
비동기 Lambda에서 실행합니다. OCR과 구형 Office 변환은 이 파서의 범위가 아닙니다. 차트의 내부 XLSX 워크북은 읽거나 실행하지 않고 누락을 부분 추출로 표시하며 슬라이드 텍스트를 보존합니다. OLE·매크로·다른 내장 패키지는 거부합니다.

파서 한도는 입력 20MiB, 주소 공간 512MiB, CPU 8/9초, 경과 시간 12초입니다.
완료는 원래 텍스트 범위이며 OCR·전체 시각 표현을 보장하지 않습니다.
일반 하이퍼링크는 실행하지 않고 차트 워크북만 불투명 패키지 예외로 둡니다.

### 결정

API가 `ATTACH#` 원본과 `ATTEXT#` 실행 상태를 먼저 저장한 뒤 이벤트를 보냅니다.
워커는 회의·첨부·실행 상태를 다시 읽고 임대를 조건부로 확보합니다.
이벤트에 적힌 키만으로 파일 접근을 허용하지 않습니다.

원본 ETag와 20MiB 입력 한도를 확인하고 자식 프로세스에서 파싱합니다.
불변 결과 JSON에 원본 식별자와 페이지·슬라이드·문단 위치를 담습니다.
부모·첨부·실행 ID·임대가 여전히 같을 때만 트랜잭션으로 완료를 반영합니다.
부분 추출과 실패를 명시하며 실패 시 이전 결과를 보존합니다. 읽는 쪽도 현재
원본을 재검증해야 합니다. S3 HEAD와 DynamoDB 완료는 원자적이지 않습니다.

격리 서브넷에서 기존 S3·DynamoDB 게이트웨이 엔드포인트로만 HTTPS 통신합니다.
권한은 `files/*` 읽기, 결과 JSON 쓰기, 필요한 테이블 접근으로 제한합니다.
자식 프로세스에 자격증명을 전달하지 않지만 이를 완전한 RCE 방어로 주장하지 않습니다.

### 결과

비동기 대기·실패·재시도 처리가 필요합니다. OCR 문서와 큰 파일은 제한되며,
불명확한 쓰기 결과나 삭제 후 불변 객체가 남을 수 있습니다. 실행 역할의
권한은 정해진 경로 안에서 여러 사용자를 포괄하므로 원본 검증을 생략할 수 없습니다.

### 참고

- [워커 계약과 검증](../../backend/python/document-extract/LAMBDA.md)
- [파서 범위와 제한](../../backend/python/document-extract/README.md)

### Result retention / 결과 보존

Result JSON uses the existing private, versioned assets bucket with S3-managed
server-side encryption and Block Public Access. No body text or parser stderr
is logged. Code/dependencies are packaged together read-only; fixtures/tests are
excluded. Environment stripping does not prevent a same-UID exploit from reading
parent credentials, so it is not an isolation boundary.

There is currently no expiration rule for `files/` results, matching retained
original files. Attachment/meeting deletion removes canonical access and ATTEXT
rows; it does not physically erase result object versions. Unreferenced-object
cleanup remains a recorded retention gap. Active results must not be expired
blindly, and ambiguous commits retain their objects to avoid breaking readers.

결과는 비공개·버전 관리·S3 서버 측 암호화·공개 접근 차단이 적용된 기존
assets 버킷에 저장합니다. 본문과 파서 stderr를 로그에 남기지 않습니다.
현재 `files/` 결과의 자동 만료는 없으며, 원본 파일처럼 보존됩니다.
회의·첨부 삭제는 접근 권한과 ATTEXT 행을 제거하지만 객체 버전의 물리 삭제는
보장하지 않습니다. 참조 없는 객체 정리는 남은 보존 과제로 기록합니다.
환경변수 제거는 같은 UID의 침해에서 부모 자격증명 접근을 막는 경계가 아닙니다.
