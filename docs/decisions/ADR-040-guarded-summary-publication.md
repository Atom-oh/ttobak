# ADR-040: Guarded summary publication

<a href="#english">English</a> · <a href="#korean">한국어</a>

<a id="english"></a>
## English

**Accepted — 2026-09-12.** Generation races with edits and nullable attributes.
Unconditional saves lose edits; globally equating absent/empty weakens CAS.
Choose exact observed source presence and guarded publication instead.

Capture before hydration. Pin effective transcript/selection, consumed segments,
notes/live-summary, protected content and execution ownership, not unrelated
metadata. Read legacy S3 references with IfMatch and pin ETag/version; recheck
bytes before publication. Bind supplied attachments and extraction state, then
publish their conditions and the meeting update in one transaction. S3 HEAD and
DynamoDB cannot be atomic; keep this final check immediately before the write.

Conflicts discard output and record a retry marker, releasing only the observed
claim. Lambda redelivery claims and generates fresh output without STT/refinement.
Never refresh expected sources to reuse old output. Retries are finite; marker
write errors surface. Success clears the marker; prior human content survives.

Separate unavailable evidence, budget excerpts and rejected citations. Keep
completed-upload links. Drop unsupported claim units, not markers or valid sibling
list items. Reject heading/notice-only and incomplete output. Only verified docs
enable DOC instructions; model input excludes storage/user identity.

Batch CAS/retry/filtering changes on deployment. DOCUMENT collection needs caller
injection in the wiring release. Saved-summary run/lease/ACL storage ships with
its follow-up; four initial deletes preserve source/state and later pair atomicity.
These safeguards add conflicts/reads and can omit evidence. Ambiguous writes retain
immutable spills ([ADR-037](ADR-037-immutable-spills-for-conditional-transcript-writes.md)).
See [rollout](../runbooks/meeting-document-release.md).

<a id="korean"></a>
## 한국어

**승인됨 — 2026-09-12.** 생성 중 편집과 선택적 속성 부재가 발생합니다. 무조건
저장은 편집을 잃고 부재/빈 값의 전역 동일시는 CAS를 약화하므로 정확한 관측값을
조건으로 저장합니다.

로딩 전에 캡처하고 실제 전사문/선택·사용 구간·메모/live-summary·보호 본문·실행
소유권만 고정합니다. Legacy S3는 IfMatch로 읽고 ETag/version을 저장 직전에
재확인합니다. 제공된 첨부와 추출 상태 조건을 본문과 한 트랜잭션으로 저장합니다.
S3 HEAD와 DynamoDB는 원자적일 수 없으므로 최종 확인을 쓰기 직전에 둡니다.

충돌은 출력을 버리고 관측한 claim만 해제한 뒤 재시도 표식을 저장합니다. Lambda
재전달은 새 입력으로 모델을 다시 호출하며 STT/정제를 반복하지 않습니다. 이전
출력의 expected만 바꾸지 않습니다. 재시도는 유한하고 표식 저장 실패는 노출합니다.
성공은 표식을 지우며 기존 사용자 본문을 보존합니다.

근거 미제공·예산 발췌·인용 거절을 구분하고 완료 파일 링크를 유지합니다. 잘못된
주장 단위만 제외하고 정상 목록 항목을 보존합니다. 제목/안내문만 남거나 미완료인
출력은 거부합니다. 검증 문서가 있을 때만 DOC 지시문을 넣고 저장소/사용자 식별자를
모델에 보내지 않습니다.

배포는 배치 CAS/복구/필터를 바꾸며 문서 조회는 wiring 주입 후 활성화합니다.
재요약 run/lease/ACL 저장소는 후속으로 나누며 선두 네 삭제로 source/state와 이후
쌍의 원자성을 유지합니다. 추가 충돌/읽기·근거 제외 비용이 있으며 불확실한 쓰기의
불변 spill은 ADR-037에 따라 보존합니다. [배포 절차](../runbooks/meeting-document-release.md)를 따릅니다.
