# ADR-012: Add entityType Sort Key to GSI3 for Meeting Lookup

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
The `GetMeetingByID` function queries GSI3 (partition key: `meetingId`) to retrieve a meeting when only the meeting ID is known (no user context). This function is called from 15+ locations across the event-driven pipeline (summarize Lambda, transcribe Lambda, upload service, etc.).

GSI3 originally had no sort key, so the query used a `FilterExpression` to match `entityType = MEETING`. DynamoDB's `Query` operation applies `Limit` **before** `FilterExpression`, which caused a critical bug: when `Limit: 1` was set, the first scanned item might not be a MEETING entity, causing the filter to discard it and return an empty result.

The immediate fix (PR #53) removed `Limit: 1`, but this left the query reading all items in the GSI3 partition before filtering -- inefficient and fragile as the data grows.

## Options Considered

### Option 1: Remove Limit and keep FilterExpression (status quo after PR #53)
- **Pros**: Minimal code change, no schema migration
- **Cons**: Reads all items in the partition before filtering, wastes RCU. Cannot safely use `Limit`. Fragile if more entity types share the same `meetingId` partition.

### Option 2: Add entityType as GSI3 sort key
- **Pros**: Key condition replaces FilterExpression, `Limit: 1` becomes safe again. Reads exactly 1 item. Future-proof for multi-entity partitions.
- **Cons**: Requires GSI recreation (CloudFormation deletes and recreates GSI3). Brief unavailability during backfill. All existing items must already have the `entityType` attribute (they do).

### Option 3: Create a new GSI (GSI3v2) alongside the old one
- **Pros**: Zero downtime -- migrate code to new GSI, then remove old one.
- **Cons**: Doubles GSI storage and write costs during migration. More complex rollout with two deployments.

## Decision
Chose **Option 2**: add `entityType` as the sort key for GSI3.

The table has only ~559 items, so GSI recreation completes in seconds. The `entityType` attribute is already populated on all existing items, so no data backfill script is needed. The benefits of a clean key condition query outweigh the brief GSI rebuild time.

The Go query now uses:
```go
keyEx := expression.Key("meetingId").Equal(expression.Value(meetingID)).
    And(expression.Key("entityType").Equal(expression.Value("MEETING")))
```

This eliminates `FilterExpression` entirely and safely restores `Limit: 1`.

## Consequences

### Positive
- Exact 1-item lookup with key condition only -- no FilterExpression overhead
- `Limit: 1` is safe again (applied after key condition evaluation)
- Reduced RCU consumption per query
- Eliminates the class of bugs where Limit + FilterExpression interact incorrectly

### Negative
- GSI3 is briefly unavailable during CloudFormation recreation (seconds for this table size)
- Any future entity types sharing `meetingId` as a GSI3 partition key must have `entityType` populated

## References
- PR #53: Initial fix removing `Limit: 1`
- PR #56: Schema improvement adding sort key
- [DynamoDB Query Limit documentation](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.html#Query.Limit)

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
`GetMeetingByID` 함수는 미팅 ID만 알고 사용자 정보가 없는 상황에서 GSI3(파티션 키: `meetingId`)을 쿼리하여 미팅을 조회합니다. 이 함수는 이벤트 드리븐 파이프라인 전반에서 15곳 이상 호출됩니다 (summarize Lambda, transcribe Lambda, upload service 등).

GSI3에는 원래 sort key가 없었기 때문에, `entityType = MEETING` 조건을 `FilterExpression`으로 처리했습니다. DynamoDB의 `Query` 연산은 `Limit`을 `FilterExpression` **이전에** 적용하므로, `Limit: 1`이 설정된 경우 첫 번째 스캔된 아이템이 MEETING 엔티티가 아니면 필터에 의해 제거되어 빈 결과를 반환하는 버그가 발생했습니다.

PR #53에서 `Limit: 1`을 제거하여 즉시 수정했지만, GSI3 파티션의 모든 아이템을 읽은 후 필터링하는 비효율적이고 취약한 구조가 남아있었습니다.

## 검토한 옵션

### 옵션 1: Limit 제거하고 FilterExpression 유지 (PR #53 이후 현 상태)
- **장점**: 코드 변경 최소화, 스키마 마이그레이션 불필요
- **단점**: 파티션 내 모든 아이템을 읽은 후 필터링하여 RCU 낭비. `Limit` 안전하게 사용 불가. 같은 `meetingId` 파티션에 엔티티 타입이 늘어나면 취약.

### 옵션 2: GSI3에 entityType을 sort key로 추가
- **장점**: FilterExpression 대신 key condition 사용 가능, `Limit: 1` 안전하게 복원. 정확히 1개 아이템만 읽음. 멀티 엔티티 파티션에도 대응 가능.
- **단점**: GSI 재생성 필요 (CloudFormation이 GSI3 삭제 후 재생성). 백필 중 짧은 사용 불가 시간. 기존 모든 아이템에 `entityType` 속성이 있어야 함 (이미 존재).

### 옵션 3: 기존 GSI와 병행하여 새 GSI(GSI3v2) 생성
- **장점**: 다운타임 제로 -- 코드를 새 GSI로 마이그레이션한 후 기존 GSI 제거.
- **단점**: 마이그레이션 기간 동안 GSI 스토리지 및 쓰기 비용 2배. 배포 2회 필요로 롤아웃 복잡도 증가.

## 결정
**옵션 2**를 선택했습니다: GSI3에 `entityType`을 sort key로 추가합니다.

테이블에 약 559개 아이템만 있어 GSI 재생성은 수 초 만에 완료됩니다. `entityType` 속성은 기존 모든 아이템에 이미 존재하므로 별도 데이터 백필 스크립트가 필요하지 않습니다. 깔끔한 key condition 쿼리의 이점이 짧은 GSI 재빌드 시간보다 큽니다.

Go 쿼리는 다음과 같이 변경되었습니다:
```go
keyEx := expression.Key("meetingId").Equal(expression.Value(meetingID)).
    And(expression.Key("entityType").Equal(expression.Value("MEETING")))
```

이로써 `FilterExpression`이 완전히 제거되고 `Limit: 1`이 안전하게 복원되었습니다.

## 영향

### 긍정적
- key condition만으로 정확히 1개 아이템 조회 -- FilterExpression 오버헤드 제거
- `Limit: 1` 안전하게 사용 가능 (key condition 평가 후 적용)
- 쿼리당 RCU 소비 감소
- Limit + FilterExpression 상호작용으로 인한 버그 클래스 제거

### 부정적
- CloudFormation 재생성 중 GSI3 짧은 사용 불가 시간 (이 테이블 크기에서는 수 초)
- 향후 `meetingId`를 GSI3 파티션 키로 공유하는 엔티티 타입은 반드시 `entityType`을 포함해야 함

## 참고 자료
- PR #53: `Limit: 1` 제거 초기 수정
- PR #56: sort key 추가 스키마 개선
- [DynamoDB Query Limit 문서](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.html#Query.Limit)
