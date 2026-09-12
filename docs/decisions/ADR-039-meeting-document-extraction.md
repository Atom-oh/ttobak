# ADR-039: Meeting Document Text Extraction

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

<a id="english"></a>

## English

### Status

Accepted — 2026-09-12. This PR implements only the bounded parser. The asynchronous worker and API/summary/Q&A/UI wiring below are planned rollout steps; deploy and verify the worker before enabling producers.

### Context and options

Meeting attachments previously contributed filenames only. Calling a parser
synchronously from the API would spend its request budget on untrusted files.
Use an asynchronous Lambda with the bounded native PDF/PPTX/DOCX/Markdown parser.
OCR and legacy Office conversion are outside this parser's declared scope.

The parser uses a 20 MiB input limit, 512 MiB child address-space limit, 8/9
second soft/hard CPU limits and a 12-second wall deadline. Limits can be lowered,
not raised by document input. Complete means native text in the declared scope,
not OCR or rendered appearance. Encrypted/OLE/macro content, strict OOXML and
embedded packages are rejected; ordinary hyperlinks remain inert. This also
excludes chart decks with embedded workbooks until separately supported.

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

- [Parser scope and limits](../../backend/python/document-extract/README.md)

<a id="korean"></a>

## 한국어

### 상태

승인 — 2026-09-12. 현재 PR에는 제한된 파서만 구현합니다. 아래 비동기 워커와 API·요약·Q&A·화면 연결은 후속 단계이며, 요청을 활성화하기 전에 워커를 배포해 검증해야 합니다.

### 배경과 대안

기존 회의 첨부 문서는 파일명만 활용했습니다. API 안에서 파일을 동기 파싱하면
요청 시간 제한을 소모하므로, 제한된 PDF/PPTX/DOCX/Markdown 파서를
비동기 Lambda에서 실행합니다. OCR과 구형 Office 변환은 이 파서의 범위가 아닙니다.

입력은 20MiB, 자식 주소 공간은 512MiB, CPU는 soft/hard 8/9초, 실제 경과
시간은 12초로 제한합니다. 문서 입력으로 한도를 늘릴 수 없습니다. 완료는 선언된
범위의 원래 텍스트를 뜻하며 OCR·전체 시각 표현을 보장하지 않습니다.
암호화·OLE·매크로·strict OOXML·내장 패키지는 거부하고 일반 하이퍼링크는
실행하지 않습니다. 내장 워크북을 포함한 차트 덱은 별도 지원 전까지 제외됩니다.

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

- [파서 범위와 제한](../../backend/python/document-extract/README.md)
