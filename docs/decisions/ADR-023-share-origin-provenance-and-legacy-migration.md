# ADR-023: Share.Origin Provenance, Legacy Migration, and the RemoveMember Force Gate

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
- `AccountID` (new field, this round): every `Origin == "account"` share also carries the granting account's ID, and `DeleteShareIfAccountOrigin`'s delete condition is scoped to `origin == "account" AND accountId == :accountID`, not origin alone. Without this, a meeting re-shared from account A to account B in the gap between A's `RemoveMember` cleanup reading the meeting (still showing A at read time) and deleting the share (by then B's fresh `CreateShareIfMember` grant) could have B's valid grant deleted by A's removal — the meeting-level `meeting.AccountID != accountID` guard that read happens with is a separate, non-atomic call and cannot itself close that race; only a condition on the row being deleted can. `BackfillShareOrigin` stamps `AccountID` alongside `Origin` for the same reason: a backfilled row with `Origin=="account"` but no `AccountID` would fail this same condition and become un-revocable all over again.

### 2. RemoveMember's force gate (this round's fix)
Earlier drafts of this PR made `RemoveMember` always delete membership and merely *report* `AmbiguousUntaggedMeetingIDs` in the response — i.e. fail-open: a member holding only ambiguous shares kept transcript/Bedrock access indefinitely, with the failure signal easy to miss (200-with-body looks like success). This ADR changes that to fail-closed by default:
- `RemoveMember` now runs a precheck (`checkNoAmbiguousShares`) BEFORE deleting membership. If the target holds any `Origin != "account"` share on a meeting still linked to this account (`AccountID` matches AND `SharedToAccount` is true -- a Link-only meeting, `AccountID` set but `SharedToAccount` false, was never a team-share grant and must not count), the call returns `ErrAmbiguousShareBlocksRemoval` (HTTP 400) and membership is left untouched.
- The precheck fails CLOSED on infrastructure errors: a `GetMeetingByID`/`GetShare` failure returns an error and preserves membership (retryable), the same treatment `RemoveMember` already gives a `ListMeetingRefsForAccount` failure. This is deliberate -- this precheck IS the security gate that closes RemoveMember's fail-open access-retention gap, so letting a transient DynamoDB blip silently pass it through would reopen the exact gap it exists to close. (An earlier version of this precheck was soft on these errors; review caught that this reintroduced fail-open behavior through the back door.) The post-delete cleanup loop, by contrast, still fails soft on the same errors -- by the time it runs, membership is already gone, so there's no "block removal" option left, only "report the failure."
- Passing `?force=true` (handler) / `force=true` (service) skips the precheck entirely and reproduces the prior behavior exactly: membership is deleted, account-origin shares are revoked, ambiguous shares are left untouched and reported back. This is the owner's explicit "I understand the risk" override — the frontend (`AccountDetailClient.tsx`) catches the 400, shows the reason, and offers a confirm-gated retry with `force=true` rather than leaving the owner stuck with no path forward.

This means: for an account that has already run the backfill CLI (below), removal behaves exactly as before (no ambiguous shares exist, nothing is ever blocked). For an account that hasn't, the owner is forced to make a conscious choice at removal time instead of the ambiguity silently resolving to "access retained forever."

### 3. Legacy migration: `backend/cmd/backfill-share-origin`
A manually-run, one-account-at-a-time CLI that retags a legacy account-share's `Origin` to `"account"` (and stamps `AccountID`) so it becomes revocable again. It is NOT run automatically by any deploy pipeline — this is deliberate; retagging the wrong row (a genuine direct grant, indistinguishable per §1) would let a future `RemoveMember` silently revoke an owner's explicit grant, and that risk should sit with a human decision per account, not a script.

Candidate detection enumerates each account-linked meeting's Share rows directly (`ListSharesForMeeting`, keyed on the meeting, not on current membership) rather than joining through `ListAccountMembers` — this is what lets the CLI find and tag legacy shares belonging to users who have **already been removed** from the account by the time it runs, not just current members (see §4 below; this closes what an earlier draft of this ADR accepted as a permanent Known Limitation).

**Operational procedure (dry-run first, always):**
```
go run ./cmd/backfill-share-origin --account-id <id>                # dry-run: lists candidates, tags nothing
go run ./cmd/backfill-share-origin --account-id <id> --apply        # tags every listed candidate
go run ./cmd/backfill-share-origin --account-id <id> --apply --exclude userId1:meetingId1,userId2:meetingId2
```
- Review the dry-run's CANDIDATE list. Every listed `(sharedTo, meeting)` pair is *ambiguous by construction* — the CLI cannot tell a true legacy account-share from a coincidental direct grant on an account-shared meeting. Anything you don't trust must be named in `--exclude` before `--apply`, or it WILL be tagged (and a future `RemoveMember` will revoke it as if it were a team grant).
- Run one account at a time (`--account-id` is required, not optional) — this is the safer phased rollout the CLI's own design forces.
- **Operational sequencing this ADR still recommends**: run backfill before removing a member from an account that predates this PR, so the force gate never triggers in the first place. This is no longer a hard *requirement* the way it was before §4's fix (a member removed before backfill runs is now still a valid candidate on a later backfill run), but running backfill first remains the friction-free path.
- Rollback: retagging is a two-attribute (`Origin`, `AccountID`) write per row, ConditionExpression-guarded against concurrent changes. To undo a mistaken `--apply`, manually `UpdateItem` the specific `(sharedToID, meetingID)` pair's `origin`/`accountId` attributes back to absent/empty — there is no CLI `--undo` flag; this is intentionally a rare, manual-review operation, not a scripted one.

### 4. Cross-account race + ex-member backfill coverage (second fix this round)
Review found two further gaps in the first draft of this round's fix:
- **Cross-account share race**: `Share` had no field tying an account-origin row to the account that granted it — `DeleteShareIfAccountOrigin`'s delete condition checked `origin == "account"` alone. A meeting re-shared from account A to account B in the (non-atomic) gap between RemoveMember's cleanup reading the meeting and deleting the share could have B's fresh grant deleted by A's removal, because both rows share the same origin value. Fixed by adding `Share.AccountID` (see §1) and scoping the delete condition to it.
- **Ex-member backfill coverage**: the original backfill CLI iterated `ListAccountMembers` (current members only), so a user removed from the account BEFORE backfill ran had no way back into the candidate list — their legacy share stayed `Origin==""` forever. Fixed by switching candidate detection to `ListSharesForMeeting` (§3), which enumerates Share rows by meeting, independent of current membership.

### 5. Orphaned legacy shares on un-shared/re-shared meetings (third fix this round, detect-only)
Review found a further, structurally different gap: §2's precheck/cleanup and §3's backfill CLI all gate on the meeting **currently** being `SharedToAccount` for this exact `accountID`. A legacy `Origin==""` share whose meeting has since been un-shared from the account (or re-shared to a different one) falls outside every one of these predicates -- it is invisible to the precheck (so `RemoveMember` never blocks on it), invisible to cleanup (so it's never revoked or reported as ambiguous), and invisible to backfill (so it can never be tagged and made revocable). `resolveSharedAccess` honors it as an unconditional direct grant forever, with no membership re-verification at all -- worse than the ambiguous-but-tracked case §2 closes, because there is no code path that even notices this share exists.

This is NOT auto-remediated. Tagging it would require the tool to decide which account it now belongs to -- this account (its original grantor, per the stale `MeetingRef`) or the meeting's current `AccountID` (if any, and if that account's membership even includes this user) -- and neither answer is safe to infer automatically; a wrong guess would either fail to fix anything or (worse) let a future `RemoveMember` revoke what might be a genuine direct grant. Instead, `backend/cmd/backfill-share-origin` now REPORTS these as `ORPHANED` (distinct from `CANDIDATE`) whenever it walks a `MeetingRef` whose meeting no longer matches this account's `SharedToAccount`/`AccountID` invariants, still carrying an untagged share. This is a detection tool only: `--apply` never touches an `ORPHANED` row.

**Manual remediation**: an `ORPHANED` line names the `(sharedTo, meeting)` pair and the meeting's current `AccountID`. An operator must contact the meeting's owner to confirm whether the share is a genuine direct grant (keep it) or a stranded legacy account-share (the owner should call `RevokeShare` to remove it, since automated code has no way to make this call safely). There is no scripted remediation for this case, by design -- it is the one situation this ADR's automation deliberately defers entirely to a human.

## Consequences

### Positive
- Every share created after this PR ships has unambiguous provenance (`Origin` + `AccountID`) and is correctly revoked/preserved by `RemoveMember`, including under the cross-account re-share race.
- The force gate makes "this account has an unresolved legacy-share ambiguity" a decision an owner must actively make, not a silent default.
- The backfill CLI's `--exclude` + per-account, dry-run-first design keeps a human in the loop for the one case (coincidental direct-share-on-shared-meeting) that's fundamentally unresolvable by any automated heuristic — and now also covers ex-members, closing what was previously accepted as a permanent residual risk.

### Negative
- The force gate is a new required step in the removal flow for any account that hasn't backfilled — this is deliberate friction, not an oversight, but it does mean "remove this troublesome member right now" can be blocked until an owner either backfills the account or explicitly overrides with `force=true`.
- No automated enforcement of the "backfill before removal" sequencing beyond the force gate itself — an owner who always passes `force=true` reflexively bypasses the protection this ADR adds without ever running backfill. The gate raises the bar from "impossible to notice" to "requires an explicit override," not to "impossible to bypass."
- A meeting re-shared cross-account is still detected by a non-atomic read at the point cleanup decides whether to even attempt the delete (`meeting.AccountID != accountID` skip) — §4's `AccountID` condition is what makes the delete itself safe regardless, but the read-side skip logic remains best-effort, not transactional.
- **Known limitation (§5, detect-only)**: a legacy share whose meeting has since been un-shared from the account, or re-shared to a different account, is un-taggable by any automation and un-revocable except by the owner manually calling `RevokeShare` after a human confirms it's not a genuine direct grant. `backfill-share-origin` surfaces it as `ORPHANED` so it's at least visible, but this ADR does not close the gap itself — it is accepted as a residual, human-remediated risk.

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| Force gate on ambiguous share (chosen) | No code path silently revokes a direct grant OR silently leaves a legacy share un-revocable forever without SOME explicit signal reaching the owner | Adds friction; bypassable via `force=true` |
| Fail-open + report only (rejected, this round's prior state) | No friction for owners | The exact fail-open gap 4 independent AI review models flagged across multiple rounds — a member with only legacy shares is removed and keeps access forever with an easy-to-miss signal |
| Treat `Origin==""` as always-revocable (rejected) | No ambiguity, no gate needed | Would let `RemoveMember` silently revoke a genuine direct grant the owner made explicitly — worse than the fail-open case, since it actively destroys correct state instead of merely failing to fix incorrect state |
| Scope Share deletes to accountId, not origin alone (chosen, §4) | Closes the cross-account re-share race at the row being deleted, not just at an earlier non-atomic read | Requires a new field (`Share.AccountID`) and backfilling it alongside `Origin` for legacy rows |
| Backfill CLI enumerates Share rows by meeting, not by current membership (chosen, §4) | Closes the ex-member Known Limitation this ADR previously accepted as permanent | None significant — `ListSharesForMeeting` already existed (used by `GetMeeting`'s owner-facing share list) and returns exactly the rows needed |
| Report orphaned legacy shares as detect-only, never auto-tag (chosen, §5) | Never guesses the wrong account for a share whose meeting has moved; keeps the human in the loop for the one case with no safe automated answer | Doesn't close the gap -- stays a human-remediated residual risk |
| Auto-tag orphaned shares to their MeetingRef's original account (rejected, §5) | Would close the gap fully automatically | The meeting may have moved to a genuinely different account (or none) since the ref was written -- tagging it under the stale account could let that account's `RemoveMember` revoke a share it no longer has any relationship to, or tag a row nobody should touch at all |

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
- `AccountID`(신규 필드, 이번 라운드): `Origin == "account"`인 모든 share는 이제 발급한 account의 ID도 함께 가지며, `DeleteShareIfAccountOrigin`의 삭제 조건은 origin 단독이 아니라 `origin == "account" AND accountId == :accountID`로 범위가 좁혀진다. 이게 없으면, A 계정에서 B 계정으로 재공유된 미팅이 A의 `RemoveMember` cleanup이 미팅을 읽는 시점(그때는 아직 A로 표시됨)과 share를 삭제하는 시점(그 사이 B의 새 `CreateShareIfMember` grant로 바뀜) 사이의 비원자적 gap에서, B의 유효한 grant가 A의 제거에 의해 삭제될 수 있다 — 이 read가 겪는 미팅 단위 가드(`meeting.AccountID != accountID`)는 별도의 비원자적 호출이라 그 race를 스스로 막을 수 없고, 삭제 대상 row 자체에 대한 조건만이 막을 수 있다. `BackfillShareOrigin`도 `Origin`과 함께 `AccountID`를 태깅한다 — `AccountID` 없이 `Origin=="account"`만 태깅된 row는 이 조건을 통과하지 못해 또다시 회수 불가능해지기 때문이다.

### 2. RemoveMember의 force gate (이번 라운드 수정)
이 PR의 초기 초안은 `RemoveMember`가 항상 멤버십을 삭제하고 응답에 `AmbiguousUntaggedMeetingIDs`만 *보고*했다 — 즉 fail-open: legacy share만 보유한 멤버는 transcript/Bedrock 접근을 영구 보유했고, 그 실패 신호는 놓치기 쉬웠다(200-with-body는 성공처럼 보임). 이 ADR은 기본값을 fail-closed로 바꾼다:
- `RemoveMember`는 멤버십 삭제 **전에** precheck(`checkNoAmbiguousShares`)를 실행한다. 대상이 이 account에 아직 연결된(`AccountID` 일치 **그리고** `SharedToAccount`가 true — `AccountID`만 설정되고 `SharedToAccount`가 false인 Link-only 미팅은 team-share grant가 아니므로 제외) 미팅에 `Origin != "account"` share를 하나라도 보유하면 `ErrAmbiguousShareBlocksRemoval`(HTTP 400)을 반환하고 멤버십은 그대로 유지된다.
- precheck는 인프라 오류에 fail-closed다: `GetMeetingByID`/`GetShare` 실패 시 오류를 반환하고 멤버십을 보존한다(재시도 가능) — `RemoveMember`가 `ListMeetingRefsForAccount` 실패에 이미 적용하는 것과 동일한 처리다. 이는 의도적이다 — 이 precheck 자체가 RemoveMember의 fail-open 접근-잔존 갭을 닫는 보안 게이트이므로, 일시적 DynamoDB 오류를 조용히 통과시키면 그 갭이 뒷문으로 재개방된다(이 precheck의 초기 버전은 이런 오류에 관대했으나, 리뷰에서 이것이 fail-open을 다시 들여온다는 점이 확인되어 수정됨). 반면 삭제 후 실행되는 cleanup 루프는 동일한 오류에 여전히 관대하다 — 이 시점엔 멤버십이 이미 삭제되어 "제거를 막는다"는 선택지가 없고 "실패를 보고한다"만 남기 때문이다.
- `?force=true`(핸들러) / `force=true`(서비스)를 넘기면 precheck를 완전히 건너뛰고 이전 동작을 그대로 재현한다: 멤버십 삭제, account-origin share 회수, ambiguous share는 그대로 두고 응답에 보고. 이는 owner의 명시적 "위험을 이해했다" override다 — frontend(`AccountDetailClient.tsx`)가 400을 잡아 이유를 보여주고 confirm으로 게이트된 `force=true` 재시도를 제공해, owner가 진행할 방법이 없는 상태에 갇히지 않게 한다.

즉: 이미 backfill CLI(아래)를 실행한 account는 제거가 이전과 완전히 동일하게 동작한다(ambiguous share가 없으므로 차단될 일이 없음). 아직 실행하지 않은 account는 owner가 제거 시점에 의식적인 선택을 하도록 강제되며, 모호성이 "접근이 영원히 유지됨"으로 조용히 귀결되지 않는다.

### 3. Legacy migration: `backend/cmd/backfill-share-origin`
legacy account-share의 `Origin`을 `"account"`로 재태깅(및 `AccountID` 태깅)해 다시 회수 가능하게 만드는, 수동 실행·계정 단위 CLI. 어떤 배포 파이프라인도 이를 자동 실행하지 않는다 — 의도적이다; 잘못된 row(§1에서 구분 불가능한 진짜 direct grant)를 재태깅하면 향후 `RemoveMember`가 owner의 명시적 grant를 조용히 회수할 수 있으므로, 이 위험은 스크립트가 아니라 계정별 인간의 판단이 감당해야 한다.

후보 탐지는 현재 멤버십을 거치지 않고 account에 연결된 각 미팅의 Share row를 직접 열거한다(`ListSharesForMeeting`, 미팅 기준 — `ListAccountMembers`를 조인하지 않음). 이 덕분에 CLI 실행 시점에 이미 계정에서 **제거된** 사용자가 보유한 legacy share도 찾아서 태깅할 수 있다(§4 참고 — 이전 초안이 영구적 Known Limitation으로 수용했던 부분을 이번 라운드에서 닫음).

**운영 절차 (항상 dry-run 먼저):**
```
go run ./cmd/backfill-share-origin --account-id <id>                # dry-run: 후보만 나열, 아무것도 태깅 안 함
go run ./cmd/backfill-share-origin --account-id <id> --apply        # 나열된 모든 후보 태깅
go run ./cmd/backfill-share-origin --account-id <id> --apply --exclude userId1:meetingId1,userId2:meetingId2
```
- dry-run의 CANDIDATE 목록을 검토하라. 나열된 모든 `(sharedTo, meeting)` 쌍은 *구조적으로 모호하다* — CLI는 진짜 legacy account-share와 account-shared 미팅에 우연히 존재하는 direct grant를 구분할 수 없다. 믿을 수 없는 항목은 `--apply` 전에 반드시 `--exclude`로 명시해야 한다. 그렇지 않으면 태깅되며(향후 `RemoveMember`가 팀 grant인 것처럼 회수함).
- 한 번에 한 계정씩 실행(`--account-id`는 필수, 선택 아님) — CLI 자체 설계가 강제하는 더 안전한 단계적 rollout이다.
- **이 ADR이 여전히 권장하는 운영 순서**: 이 PR 이전부터 존재한 계정에서 멤버를 제거하기 전에 backfill을 먼저 실행해, force gate가 애초에 걸리지 않게 하라. §4의 수정으로 이는 더 이상 엄격한 *요구사항*은 아니다(backfill 실행 전에 제거된 멤버도 이후 backfill 실행에서 여전히 유효한 후보가 됨) — 다만 backfill을 먼저 하는 쪽이 여전히 마찰 없는 경로다.
- Rollback: 재태깅은 row당 두 속성(`Origin`, `AccountID`) 쓰기이며 ConditionExpression으로 동시 변경을 방어한다. 잘못된 `--apply`를 되돌리려면 해당 `(sharedToID, meetingID)` 쌍의 `origin`/`accountId` 속성을 수동으로 `UpdateItem`해 비어있는 상태로 되돌려야 한다 — CLI에는 `--undo` 플래그가 없다; 이는 의도적으로 드물고 수동 검토가 필요한 작업이지 스크립트화할 작업이 아니다.

### 4. Cross-account race + ex-member backfill 커버리지 (이번 라운드 두 번째 수정)
리뷰에서 이번 라운드 첫 수정의 추가 갭 2건이 발견됨:
- **Cross-account share race**: `Share`에는 account-origin row를 발급한 account와 묶어주는 필드가 없었다 — `DeleteShareIfAccountOrigin`의 삭제 조건은 `origin == "account"`만 확인했다. A 계정에서 B 계정으로 재공유된 미팅이 RemoveMember의 cleanup이 미팅을 읽는 시점과 share를 삭제하는 시점 사이의 (비원자적) gap에서, 두 row가 같은 origin 값을 가진다는 이유로 B의 새 grant가 A의 제거에 의해 삭제될 수 있었다. `Share.AccountID`(§1)를 추가하고 삭제 조건을 이 필드로 좁혀 수정.
- **Ex-member backfill 커버리지**: 원래 backfill CLI는 `ListAccountMembers`(현재 멤버만)를 순회했으므로, backfill 실행 **전에** 계정에서 제거된 사용자는 후보 목록으로 돌아올 방법이 없었고 — 그들의 legacy share는 영원히 `Origin==""`로 남았다. 후보 탐지를 `ListSharesForMeeting`(§3)으로 전환해 수정 — 이는 현재 멤버십과 무관하게 미팅 기준으로 Share row를 열거한다.

### 5. un-share/재공유된 미팅의 고아(orphaned) legacy share (이번 라운드 세 번째 수정, 탐지 전용)
리뷰에서 구조적으로 다른 갭이 하나 더 발견됨: §2의 precheck/cleanup과 §3의 backfill CLI는 모두 미팅이 **현재** 이 정확한 `accountID`에 대해 `SharedToAccount`인지를 조건으로 삼는다. 미팅이 이후 account에서 un-share되거나(또는 다른 account로 재공유되어) 이 조건에서 벗어난 legacy `Origin==""` share는 이 모든 predicate의 사각지대에 놓인다 — precheck에는 보이지 않아 `RemoveMember`가 이를 차단하지 않고, cleanup에도 보이지 않아 회수되거나 ambiguous로 보고되지 않으며, backfill에도 보이지 않아 태깅되어 회수 가능해질 방법도 없다. `resolveSharedAccess`는 이를 영원히 무조건적인 direct grant로 honor하며, 어떤 멤버십 재검증도 적용되지 않는다 — §2가 닫는 ambiguous-하지만-추적되는 케이스보다 더 나쁘다. 이 share가 존재한다는 사실 자체를 알아차리는 코드 경로가 아예 없기 때문이다.

이는 자동으로 복구되지 않는다. 태깅하려면 도구가 이 share가 지금 어느 account에 속하는지 — 이 account(정체된 `MeetingRef` 기준 원래 발급자)인지, 아니면 미팅의 현재 `AccountID`(있다면, 그리고 그 account의 멤버십에 이 사용자가 실제로 포함되는지)인지 — 결정해야 하는데, 어느 쪽도 자동으로 안전하게 추론할 수 없다. 잘못 추측하면 아무것도 고치지 못하거나(최선의 경우), 더 나쁘게는 향후 `RemoveMember`가 실제로는 direct grant일 수 있는 것을 회수하게 만들 수 있다. 대신 `backend/cmd/backfill-share-origin`은 이제 이 account의 `SharedToAccount`/`AccountID` invariant와 더 이상 일치하지 않는 미팅의 `MeetingRef`를 순회하다가 여전히 untagged share를 발견하면 이를 `CANDIDATE`와 구분된 `ORPHANED`로 **보고**한다. 이는 탐지 전용 도구다: `--apply`는 `ORPHANED` row를 절대 건드리지 않는다.

**수동 remediation**: `ORPHANED` 라인은 `(sharedTo, meeting)` 쌍과 미팅의 현재 `AccountID`를 명시한다. 운영자는 미팅 owner에게 연락해 이 share가 진짜 direct grant인지(그대로 유지) 아니면 정체된 legacy account-share인지(owner가 `RevokeShare`를 호출해 제거해야 함 — 자동화된 코드는 이 판단을 안전하게 내릴 방법이 없으므로) 확인해야 한다. 이 케이스에는 스크립트화된 remediation이 의도적으로 없다 — 이 ADR의 자동화가 전적으로 인간에게 위임하는 유일한 상황이다.

## 결과

### 긍정
- 이 PR 배포 이후 생성되는 모든 share는 명확한 provenance(`Origin` + `AccountID`)를 가지며 `RemoveMember`가 cross-account 재공유 race 상황에서도 올바르게 회수/보존한다.
- force gate는 "이 계정에 해결되지 않은 legacy-share 모호성이 있다"를 owner가 능동적으로 결정해야 하는 사안으로 만든다(조용한 기본값이 아니라).
- backfill CLI의 `--exclude` + 계정별·dry-run-우선 설계는 어떤 자동화된 휴리스틱으로도 근본적으로 해결 불가능한 유일한 케이스(공유된 미팅에 우연히 존재하는 direct share)에 인간을 개입시키며, 이제 ex-member도 커버해 이전에 영구적 잔존 위험으로 수용했던 부분을 닫는다.

### 부정
- backfill을 아직 하지 않은 계정에서는 force gate가 제거 흐름에 새로운 필수 단계가 된다 — 의도된 마찰이지만, "지금 당장 이 문제 멤버를 제거"가 owner가 backfill을 하거나 `force=true`로 명시적으로 override할 때까지 막힐 수 있다는 뜻이기도 하다.
- force gate 자체 외에 "제거 전 backfill" 순서를 강제하는 자동화된 장치는 없다 — 반사적으로 항상 `force=true`를 넘기는 owner는 backfill을 한 번도 실행하지 않고도 이 ADR이 추가한 보호를 우회한다. 이 gate는 기준을 "알아차릴 수 없음"에서 "명시적 override가 필요함"으로 올린 것이지, "우회 불가능"으로 만든 것은 아니다.
- cross-account로 재공유된 미팅은 cleanup이 삭제를 시도할지 결정하는 시점에 여전히 비원자적 read로 감지된다(`meeting.AccountID != accountID` skip) — §4의 `AccountID` 조건은 그와 무관하게 삭제 자체를 안전하게 만들지만, read 쪽의 skip 로직 자체는 여전히 best-effort이며 트랜잭션이 아니다.
- **알려진 한계(§5, 탐지 전용)**: 미팅이 그 이후 account에서 un-share되거나 다른 account로 재공유된 legacy share는 어떤 자동화로도 태깅할 수 없고, owner가 직접 `RevokeShare`를 호출하는 것(그 전에 인간이 진짜 direct grant가 아님을 확인) 외에는 회수할 수 없다. `backfill-share-origin`이 이를 `ORPHANED`로 노출해 최소한 눈에 보이게는 하지만, 이 ADR이 그 자체로 갭을 닫는 것은 아니다 — 수용된, 인간이 remediate해야 하는 잔존 위험이다.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|------|------|------|
| ambiguous share에 force gate(채택) | direct grant를 조용히 회수하거나 legacy share를 영원히 회수 불가능하게 방치하는 코드 경로가 owner에게 어떤 명시적 신호도 없이 존재하지 않음 | 마찰 추가; `force=true`로 우회 가능 |
| fail-open + 보고만(기각, 이번 라운드 이전 상태) | owner에게 마찰 없음 | 여러 라운드에 걸쳐 4개 독립 AI 리뷰 모델이 지적한 정확한 fail-open 갭 — legacy share만 있는 멤버가 제거되고도 영구히 접근을 유지하며 신호는 놓치기 쉬움 |
| `Origin==""`를 항상 회수 가능으로 처리(기각) | 모호성 없음, gate 불필요 | `RemoveMember`가 owner가 명시적으로 부여한 진짜 direct grant를 조용히 회수할 수 있게 됨 — 잘못된 상태를 고치지 못하는 것보다 올바른 상태를 능동적으로 파괴하는 게 더 나쁨 |
| Share 삭제를 origin 단독이 아니라 accountId로 범위 좁힘(채택, §4) | cross-account 재공유 race를 더 이른 비원자적 read가 아니라 삭제 대상 row 자체에서 닫음 | 새 필드(`Share.AccountID`) 필요, legacy row에 `Origin`과 함께 소급 태깅 필요 |
| backfill CLI가 현재 멤버십이 아니라 미팅 기준으로 Share row를 열거(채택, §4) | 이 ADR이 이전에 영구적으로 수용했던 ex-member Known Limitation을 닫음 | 큰 단점 없음 — `ListSharesForMeeting`이 이미 존재했고(`GetMeeting`의 owner용 share 목록에서 사용) 필요한 row를 그대로 반환함 |
| 고아 legacy share를 탐지 전용으로 보고, 자동 태깅 안 함(채택, §5) | 미팅이 옮겨간 경우 잘못된 account로 추측하지 않음; 안전한 자동 답이 없는 유일한 케이스에 인간을 개입시킴 | 갭을 닫지 못함 — 인간이 remediate해야 하는 잔존 위험으로 남음 |
| 고아 share를 MeetingRef의 원래 account로 자동 태깅(기각, §5) | 완전히 자동으로 갭을 닫을 수 있음 | ref가 쓰인 이후 미팅이 진짜 다른 account로(또는 어디로도) 옮겨갔을 수 있음 — 정체된 account로 태깅하면 그 account의 `RemoveMember`가 더 이상 아무 관계도 없는 share를 회수하거나, 아무도 손대면 안 될 row를 태깅하게 될 수 있음 |
