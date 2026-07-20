# ADR-022: Share.Origin Provenance, Legacy Migration, and the RemoveMember Force Gate

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — implemented on `feat/account-team-members`. `Share.Origin`, `CreateShareIfMember`, `RemoveMember`'s cleanup + force gate, and `backend/cmd/backfill-share-origin` are live.

## Context
[ADR-016](ADR-016-meeting-account-linking-and-sharing.md) introduced account-team sharing (`ShareMeetingToAccount`) but recorded no provenance on the `Share` row it wrote — every share, whether created by an owner's explicit `ShareMeetingByEmail` (a "direct" grant) or by team publication, looked identical in DynamoDB. This made two things impossible: (1) `RemoveMember` could not tell "revoke this, it's a team grant" apart from "never touch this, the owner gave it explicitly" and so never revoked anything on removal; (2) there was no way to retroactively distinguish the two for shares written before this gap was noticed.

This PR adds `Share.Origin` (`"account"` for team-published shares, `""` for direct grants) going forward, closing the ambiguity for every NEW share. But `Origin==""` is the *default* value for a `string` field — it is indistinguishable from "not set because this predates the field" and "not set because this genuinely is a direct grant." Every share written before this PR shipped is `Origin==""` and stays ambiguous forever unless explicitly re-tagged.

## Decision

### 1. Origin semantics
- `Origin == "account"`: written exclusively by `CreateShareIfMember` (a single `TransactWriteItems` call that ConditionChecks live `AccountMember` existence and Puts both Share rows atomically — closes the TOCTOU window a plain `GetMember`-then-`CreateShare` pair would leave between `AddMember`/`ShareMeetingToAccount`). Safe for `RemoveMember`'s cleanup to unilaterally revoke.
- `Origin == ""`: either (a) a direct grant from `ShareMeetingByEmail`/`RevokeShare`'s counterpart, which must NEVER be touched by account-membership cleanup, or (b) a legacy account-share written by `ShareMeetingToAccount` before this PR, which SHOULD be revoked on removal but isn't tagged as such. **These two cases are structurally identical rows and cannot be distinguished by any field this system persists.** This is a permanent property of the migration gap, not a bug to fix later — see "Alternatives Considered."

### 2. RemoveMember's force gate (this round's fix)
Earlier drafts of this PR made `RemoveMember` always delete membership and merely *report* `AmbiguousUntaggedMeetingIDs` in the response — i.e. fail-open: a member holding only ambiguous shares kept transcript/Bedrock access indefinitely, with the failure signal easy to miss (200-with-body looks like success). This ADR changes that to fail-closed by default:
- `RemoveMember` now runs a precheck (`checkNoAmbiguousShares`) BEFORE deleting membership. If the target holds any `Origin != "account"` share on a meeting still linked to this account, the call returns `ErrAmbiguousShareBlocksRemoval` (HTTP 400) and membership is left untouched.
- The precheck is soft on infrastructure errors (a transient `GetShare` failure logs and continues, deferring to the post-delete cleanup loop's own failure reporting) — it only ever blocks on a share it can positively confirm is ambiguous. A DynamoDB blip must not become a new way to fail a routine removal.
- Passing `?force=true` (handler) / `force=true` (service) skips the precheck entirely and reproduces the prior behavior exactly: membership is deleted, account-origin shares are revoked, ambiguous shares are left untouched and reported back. This is the owner's explicit "I understand the risk" override — the frontend (`AccountDetailClient.tsx`) catches the 400, shows the reason, and offers a confirm-gated retry with `force=true` rather than leaving the owner stuck with no path forward.

This means: for an account that has already run the backfill CLI (below), removal behaves exactly as before (no ambiguous shares exist, nothing is ever blocked). For an account that hasn't, the owner is forced to make a conscious choice at removal time instead of the ambiguity silently resolving to "access retained forever."

### 3. Legacy migration: `backend/cmd/backfill-share-origin`
A manually-run, one-account-at-a-time CLI that retags a legacy account-share's `Origin` to `"account"` so it becomes revocable again. It is NOT run automatically by any deploy pipeline — this is deliberate; retagging the wrong row (a genuine direct grant, indistinguishable per §1) would let a future `RemoveMember` silently revoke an owner's explicit grant, and that risk should sit with a human decision per account, not a script.

**Operational procedure (dry-run first, always):**
```
go run ./cmd/backfill-share-origin --account-id <id>                # dry-run: lists candidates, tags nothing
go run ./cmd/backfill-share-origin --account-id <id> --apply        # tags every listed candidate
go run ./cmd/backfill-share-origin --account-id <id> --apply --exclude userId1:meetingId1,userId2:meetingId2
```
- Review the dry-run's CANDIDATE list. Every listed `(member, meeting)` pair is *ambiguous by construction* — the CLI cannot tell a true legacy account-share from a coincidental direct grant on an account-shared meeting. Anything you don't trust must be named in `--exclude` before `--apply`, or it WILL be tagged (and a future `RemoveMember` will revoke it as if it were a team grant).
- Run one account at a time (`--account-id` is required, not optional) — this is the safer phased rollout the CLI's own design forces.
- The CLI only iterates the account's *current* members (`ListAccountMembers`). A member already removed before backfill runs has no candidate path back — see Known Limitations.
- **Operational sequencing this ADR requires**: run backfill BEFORE removing any member from an account that predates this PR. Once a member is removed, their legacy shares (if any) become permanently un-revocable by any means short of manual DynamoDB surgery (`DeleteShare` by hand, with the same false-positive risk the CLI itself carries).
- Rollback: retagging is a single-attribute `Origin` write per row, ConditionExpression-guarded against concurrent changes. To undo a mistaken `--apply`, manually `UpdateItem` the specific `(sharedToID, meetingID)` pair's `origin` attribute back to absent/empty — there is no CLI `--undo` flag; this is intentionally a rare, manual-review operation, not a scripted one.

## Consequences

### Positive
- Every share created after this PR ships has unambiguous provenance and is correctly revoked/preserved by `RemoveMember`.
- The force gate makes "this account has an unresolved legacy-share ambiguity" a decision an owner must actively make, not a silent default.
- The backfill CLI's `--exclude` + per-account, dry-run-first design keeps a human in the loop for the one case (coincidental direct-share-on-shared-meeting) that's fundamentally unresolvable by any automated heuristic.

### Negative
- **Known limitation**: a member removed BEFORE their account runs backfill has no remediation path — the CLI's candidate detection depends on `ListAccountMembers`, which no longer includes them. Their legacy shares (if any existed) stay `Origin==""` forever, silently collapsed with "direct grant" and therefore un-revocable by any means this system provides going forward, short of manual DynamoDB access. This is the residual risk this ADR accepts rather than closes — extending the CLI to also enumerate ex-members (e.g. by scanning `Share` rows directly rather than joining through `AccountMember`) is a legitimate follow-up but out of scope for this round.
- The force gate is a new required step in the removal flow for any account that hasn't backfilled — this is deliberate friction, not an oversight, but it does mean "remove this troublesome member right now" can be blocked until an owner either backfills the account or explicitly overrides with `force=true`.
- No automated enforcement of the "backfill before removal" sequencing beyond the force gate itself — an owner who always passes `force=true` reflexively bypasses the protection this ADR adds without ever running backfill. The gate raises the bar from "impossible to notice" to "requires an explicit override," not to "impossible to bypass."

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| Force gate on ambiguous share (chosen) | No code path silently revokes a direct grant OR silently leaves a legacy share un-revocable forever without SOME explicit signal reaching the owner | Adds friction; bypassable via `force=true` |
| Fail-open + report only (rejected, this round's prior state) | No friction for owners | The exact fail-open gap 4 independent AI review models flagged across multiple rounds — a member with only legacy shares is removed and keeps access forever with an easy-to-miss signal |
| Treat `Origin==""` as always-revocable (rejected) | No ambiguity, no gate needed | Would let `RemoveMember` silently revoke a genuine direct grant the owner made explicitly — worse than the fail-open case, since it actively destroys correct state instead of merely failing to fix incorrect state |
| Extend backfill CLI to cover ex-members now (deferred) | Closes the Known Limitation above | Requires a new read path (scan `Share` rows directly, since `ListAccountMembers` excludes ex-members by definition) and a new ambiguity-review UX for candidates with no current membership context to help a human judge them — sized as a follow-up, not blocking this round |

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — `feat/account-team-members`에서 구현. `Share.Origin`, `CreateShareIfMember`, `RemoveMember`의 cleanup + force gate, `backend/cmd/backfill-share-origin`이 모두 반영됨.

## 맥락
[ADR-016](ADR-016-meeting-account-linking-and-sharing.md)이 account 팀 공유(`ShareMeetingToAccount`)를 도입했지만 그때 작성한 `Share` row에는 provenance(출처) 정보가 없었다 — owner의 명시적 `ShareMeetingByEmail`(direct grant)이든 팀 게시로 생긴 공유든 DynamoDB상 완전히 동일한 모양이었다. 그 결과 두 가지가 불가능했다: (1) `RemoveMember`가 "이건 팀 grant니 회수" 대 "이건 owner가 명시적으로 준 것이니 절대 손대지 말 것"을 구분할 수 없어 제거 시 아무것도 회수하지 않았다; (2) 이 갭을 알아차리기 전에 쓰인 share들을 사후에 구분할 방법이 없었다.

이 PR은 앞으로의 모든 신규 share에 대해 `Share.Origin`(팀 게시는 `"account"`, direct grant는 `""`)을 추가해 이 모호성을 닫는다. 그러나 `Origin==""`는 `string` 필드의 *기본값*이다 — "이 필드가 생기기 전에 써서 값이 없는 것"과 "진짜로 direct grant라서 값이 없는 것"을 구분할 수 없다. 이 PR 배포 이전에 쓰인 모든 share는 `Origin==""`이며, 명시적으로 재태깅하지 않는 한 영원히 모호하다.

## 결정

### 1. Origin 의미론
- `Origin == "account"`: `CreateShareIfMember`(단일 `TransactWriteItems` 호출로 살아있는 `AccountMember` 존재를 ConditionCheck하고 두 Share row를 원자적으로 Put — `AddMember`/`ShareMeetingToAccount` 사이 평범한 `GetMember`-then-`CreateShare` 쌍이 남기는 TOCTOU 창을 닫음)에 의해서만 작성됨. `RemoveMember`의 cleanup이 일방적으로 회수해도 안전.
- `Origin == ""`: (a) `ShareMeetingByEmail`/`RevokeShare`의 짝인 direct grant — account membership cleanup이 절대 손대면 안 됨, 또는 (b) 이 PR 이전에 `ShareMeetingToAccount`가 쓴 legacy account-share — 제거 시 회수돼야 하지만 그렇게 태깅돼 있지 않음. **이 두 경우는 구조적으로 동일한 row이며 이 시스템이 저장하는 어떤 필드로도 구분할 수 없다.** 이는 나중에 고칠 버그가 아니라 이 migration 갭의 영구적 속성이다 — "검토한 대안" 참고.

### 2. RemoveMember의 force gate (이번 라운드 수정)
이 PR의 초기 초안은 `RemoveMember`가 항상 멤버십을 삭제하고 응답에 `AmbiguousUntaggedMeetingIDs`만 *보고*했다 — 즉 fail-open: legacy share만 보유한 멤버는 transcript/Bedrock 접근을 영구 보유했고, 그 실패 신호는 놓치기 쉬웠다(200-with-body는 성공처럼 보임). 이 ADR은 기본값을 fail-closed로 바꾼다:
- `RemoveMember`는 멤버십 삭제 **전에** precheck(`checkNoAmbiguousShares`)를 실행한다. 대상이 이 account에 아직 연결된 미팅에 `Origin != "account"` share를 하나라도 보유하면 `ErrAmbiguousShareBlocksRemoval`(HTTP 400)을 반환하고 멤버십은 그대로 유지된다.
- precheck는 인프라 오류에는 관대하다(일시적 `GetShare` 실패는 로그만 남기고 계속 진행 — 삭제 후 cleanup 루프 자신의 실패 보고에 위임) — 명확히 모호하다고 확인된 share에만 차단한다. DynamoDB 일시 오류가 평범한 제거를 실패시키는 새로운 방식이 되면 안 된다.
- `?force=true`(핸들러) / `force=true`(서비스)를 넘기면 precheck를 완전히 건너뛰고 이전 동작을 그대로 재현한다: 멤버십 삭제, account-origin share 회수, ambiguous share는 그대로 두고 응답에 보고. 이는 owner의 명시적 "위험을 이해했다" override다 — frontend(`AccountDetailClient.tsx`)가 400을 잡아 이유를 보여주고 confirm으로 게이트된 `force=true` 재시도를 제공해, owner가 진행할 방법이 없는 상태에 갇히지 않게 한다.

즉: 이미 backfill CLI(아래)를 실행한 account는 제거가 이전과 완전히 동일하게 동작한다(ambiguous share가 없으므로 차단될 일이 없음). 아직 실행하지 않은 account는 owner가 제거 시점에 의식적인 선택을 하도록 강제되며, 모호성이 "접근이 영원히 유지됨"으로 조용히 귀결되지 않는다.

### 3. Legacy migration: `backend/cmd/backfill-share-origin`
legacy account-share의 `Origin`을 `"account"`로 재태깅해 다시 회수 가능하게 만드는, 수동 실행·계정 단위 CLI. 어떤 배포 파이프라인도 이를 자동 실행하지 않는다 — 의도적이다; 잘못된 row(§1에서 구분 불가능한 진짜 direct grant)를 재태깅하면 향후 `RemoveMember`가 owner의 명시적 grant를 조용히 회수할 수 있으므로, 이 위험은 스크립트가 아니라 계정별 인간의 판단이 감당해야 한다.

**운영 절차 (항상 dry-run 먼저):**
```
go run ./cmd/backfill-share-origin --account-id <id>                # dry-run: 후보만 나열, 아무것도 태깅 안 함
go run ./cmd/backfill-share-origin --account-id <id> --apply        # 나열된 모든 후보 태깅
go run ./cmd/backfill-share-origin --account-id <id> --apply --exclude userId1:meetingId1,userId2:meetingId2
```
- dry-run의 CANDIDATE 목록을 검토하라. 나열된 모든 `(member, meeting)` 쌍은 *구조적으로 모호하다* — CLI는 진짜 legacy account-share와 account-shared 미팅에 우연히 존재하는 direct grant를 구분할 수 없다. 믿을 수 없는 항목은 `--apply` 전에 반드시 `--exclude`로 명시해야 한다. 그렇지 않으면 태깅되며(향후 `RemoveMember`가 팀 grant인 것처럼 회수함).
- 한 번에 한 계정씩 실행(`--account-id`는 필수, 선택 아님) — CLI 자체 설계가 강제하는 더 안전한 단계적 rollout이다.
- CLI는 계정의 *현재* 멤버만 순회한다(`ListAccountMembers`). backfill 실행 전에 이미 제거된 멤버는 복구 경로가 없다 — Known Limitations 참고.
- **이 ADR이 요구하는 운영 순서**: 이 PR 이전부터 존재한 계정에서 멤버를 제거하기 전에 반드시 backfill을 먼저 실행하라. 멤버가 제거되고 나면 그들의 legacy share(있다면)는 수동 DynamoDB 수술(CLI 자체와 동일한 오탐 위험을 감수한 손수 `DeleteShare`) 외에는 영구적으로 회수 불가능해진다.
- Rollback: 재태깅은 row당 단일 속성(`Origin`) 쓰기이며 ConditionExpression으로 동시 변경을 방어한다. 잘못된 `--apply`를 되돌리려면 해당 `(sharedToID, meetingID)` 쌍의 `origin` 속성을 수동으로 `UpdateItem`해 비어있는 상태로 되돌려야 한다 — CLI에는 `--undo` 플래그가 없다; 이는 의도적으로 드물고 수동 검토가 필요한 작업이지 스크립트화할 작업이 아니다.

## 결과

### 긍정
- 이 PR 배포 이후 생성되는 모든 share는 명확한 provenance를 가지며 `RemoveMember`가 올바르게 회수/보존한다.
- force gate는 "이 계정에 해결되지 않은 legacy-share 모호성이 있다"를 owner가 능동적으로 결정해야 하는 사안으로 만든다(조용한 기본값이 아니라).
- backfill CLI의 `--exclude` + 계정별·dry-run-우선 설계는 어떤 자동화된 휴리스틱으로도 근본적으로 해결 불가능한 유일한 케이스(공유된 미팅에 우연히 존재하는 direct share)에 인간을 개입시킨다.

### 부정
- **알려진 한계**: 계정이 backfill을 실행하기 **전에** 제거된 멤버는 구제 경로가 없다 — CLI의 후보 탐지는 `ListAccountMembers`에 의존하는데, 제거된 멤버는 더 이상 여기 포함되지 않는다. 그들의 legacy share(있었다면)는 영원히 `Origin==""`로 남아 "direct grant"와 조용히 겹쳐지며, 수동 DynamoDB 접근 외에는 이 시스템이 제공하는 어떤 수단으로도 앞으로 회수할 수 없다. 이는 이 ADR이 닫는 것이 아니라 수용하는 잔존 위험이다 — CLI가 ex-member도 열거하도록 확장하는 것(예: `AccountMember`를 거치지 않고 `Share` row를 직접 스캔)은 타당한 후속 작업이지만 이번 라운드 범위 밖이다.
- backfill을 아직 하지 않은 계정에서는 force gate가 제거 흐름에 새로운 필수 단계가 된다 — 의도된 마찰이지만, "지금 당장 이 문제 멤버를 제거"가 owner가 backfill을 하거나 `force=true`로 명시적으로 override할 때까지 막힐 수 있다는 뜻이기도 하다.
- force gate 자체 외에 "제거 전 backfill" 순서를 강제하는 자동화된 장치는 없다 — 반사적으로 항상 `force=true`를 넘기는 owner는 backfill을 한 번도 실행하지 않고도 이 ADR이 추가한 보호를 우회한다. 이 gate는 기준을 "알아차릴 수 없음"에서 "명시적 override가 필요함"으로 올린 것이지, "우회 불가능"으로 만든 것은 아니다.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| ambiguous share에 force gate(채택) | direct grant를 조용히 회수하거나 legacy share를 영원히 회수 불가능하게 방치하는 코드 경로가 owner에게 어떤 명시적 신호도 없이 존재하지 않음 | 마찰 추가; `force=true`로 우회 가능 |
| fail-open + 보고만(기각, 이번 라운드 이전 상태) | owner에게 마찰 없음 | 여러 라운드에 걸쳐 4개 독립 AI 리뷰 모델이 지적한 정확한 fail-open 갭 — legacy share만 있는 멤버가 제거되고도 영구히 접근을 유지하며 신호는 놓치기 쉬움 |
| `Origin==""`를 항상 회수 가능으로 처리(기각) | 모호성 없음, gate 불필요 | `RemoveMember`가 owner가 명시적으로 부여한 진짜 direct grant를 조용히 회수할 수 있게 됨 — 잘못된 상태를 고치지 못하는 것보다 올바른 상태를 능동적으로 파괴하는 게 더 나쁨 |
| backfill CLI를 지금 ex-member까지 확장(연기) | 위 Known Limitation을 닫음 | 새로운 read 경로(`ListAccountMembers`가 정의상 ex-member를 제외하므로 `Share` row를 직접 스캔해야 함)와, 현재 membership 컨텍스트가 없는 후보를 인간이 판단하도록 돕는 새로운 모호성-검토 UX가 필요 — 이번 라운드를 막지 않는 후속 작업으로 분류 |
