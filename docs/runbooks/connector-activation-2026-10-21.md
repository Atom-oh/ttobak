# Connector activation acceptance — 2026-10-21

**Bounded production acceptance completed on 2026-10-21 at 21:43:58 UTC. Approved synthetic fixture cleanup completed on 2026-10-22 at 01:11:58 UTC.** The actual results below bind the deployed connector source and artifacts. Native-host migration and final composed-tree/main verification retain the separate limits described here.

The repaired deployment completed at 18:17:20 UTC from source
`bf98ac72871664f2255085b483a882c594f2eb24`. Its receipt binds all 13 Lambda
functions, including the converter image, and API `live` version 234. The
separate integration candidate is `4c061548bf16af6abc844518c26546b21373b47d`;
its source review does not establish deployment of that complete tree. Final
composed-source verification remains separate from this acceptance run.

The [disabled-staging record](connector-staging-2026-10-21.md) remains the
historical prerequisite. An earlier dedicated authorization request failed
before credentials or code exchange because its custom-scope resource server
did not match the requested resource. The repaired issuance uses the existing
MCP URL resource server, with `https://ttobak.atomai.click/api/mcp/read` and
`https://ttobak.atomai.click/api/mcp/write`. Resource audience and scope checks
remain enforced. Failed attempts and successor receipts remain in the private
operations archive.

## Verified acceptance

| Check | Observed result |
|---|---|
| Deployment and preflight | Repaired artifacts and runtime prerequisites verified. Summary privacy remains writer/reader `1/1`, with the existing `2026-10-21T14:28:12Z` cutoff preserved. |
| Provider authorization | Four bounded provider flows completed, covering first-party, dedicated read, dedicated read/write and the deliberately unbound control. Signed claims and expected audience/scope boundaries were checked. |
| HTTP reads and token rejection | Both dedicated profiles read the authorized current source. First-party HTTP MCP was rejected while first-party API access remained accepted. The private predecessor and five negative token cases were rejected. |
| Private QA | One bounded, tool-free model turn used and cited the current saved source through the private service IAM/KMS path. Private predecessor material was absent. |
| WebSocket and identity-pool isolation | First-party WebSocket access succeeded. Four dedicated WebSocket attempts and two dedicated identity-pool attempts were denied; no identity-pool credentials were requested. |
| Personal-note lifecycle | Creation, same-key replay, changed-body conflict, human-edit preservation, stale-continuation rejection, deletion and rejected deleted-note replay passed at 18:30:46 UTC. The idempotency receipt was retained. |
| Natural expiry | Both original, previously accepted dedicated tokens were rejected after natural expiry. No clock/token edits, refresh attempts or model turns were used. |
| Installed stdio | The actual downloaded canonical bundle performed one bounded source read and a real provider refresh in a unique synthetic profile. The refresh was triggered by an intentionally stale synthetic cache timestamp. |
| Stdio cleanup | The synthetic profile was removed, original profiles remained unchanged, and the installed artifact was retained as evidence. All six remaining-acceptance stages completed. |
| Grant lifecycle and restoration | The reviewed fresh-token continuation passed at 18:48:17 UTC. Revocation, preserved blocking and policy disable checks passed; both policies ended disabled and the synthetic write approval inactive, with no pending effects. This was the historical grant-test restoration; the later final enable is recorded below. |
| Static guide delivery | All 563 published files were verified at 18:46:25 UTC, with the intermediate runtime configuration and backend preserved. |
| Published guide without private configuration | At 18:46:50 UTC, actual published assets and runtime configuration rendered no HTTP copy instructions and retained first-party stdio copy behavior. The browser used a synthetic SDK cache and intercepted application APIs; this is UI evidence, not live host authentication. |
| Private flag off | Deployment completed at 19:13:47 UTC and consumer acceptance at 19:15:18 UTC. Fresh first-party HTTP initialize/list and the bounded current-source read succeeded; both dedicated profiles were rejected. Baseline discovery/challenge and the installed stdio flow passed, with OAuth still enabled. |
| Private flag restored | Deployment completed at 19:30:05 UTC and consumer acceptance at 19:33:34 UTC. Private discovery/challenge returned, first-party HTTP was rejected, and both dedicated clients initialized/listed tools. Policies were still disabled during this check. |
| Final policy enable | At 19:34:19 UTC, a complete bounded inventory found no active write approvals before enabling the two policies. The synthetic approval remained inactive, no other approvals were changed, and one bounded current-source read per dedicated client succeeded. No QA turn was added. |
| Historical retained synthetic fixtures | At 21:37:31 UTC, a new bounded read-only observation matched the earlier 21:08:17 metadata: two retained meetings, one account and two synthetic users; five owned QA sessions were absent. Pilot write approvals remained inactive, the connector note remained absent, and audit/idempotency evidence remained retained. This was the historical retained disposition; refresh-token revocation is not claimed. |
| Authorized synthetic fixture cleanup | Completed on 2026-10-22 at 01:11:58 UTC: two meetings, one account and two temporary identities removed; linked content/index absence and protected state verified. Deleted-user profile tombstones and audit evidence remain retained. |
| Final private configuration and guide | After configuration deployment, the 21:42:34 UTC read-only check confirmed unchanged retained boundaries and backend functions/alias. At 21:43:58 UTC, the actual published guide passed five dedicated-read HTTP and four unchanged first-party stdio views, 43 clipboard checks and explicit per-user write-approval notices. Application APIs were intercepted; this does not claim native-host login or migration. |

The note stage first stopped on a read-only origin failure before any note
mutation. A separately reviewed continuation preserved that stop and completed
the lifecycle checks without another QA turn. Its original stop and reconciliation
receipts remain in the private archive.

The grant stage later stopped on the token verifier's local freshness margin,
with two expected-denial write observations still requiring retained-receipt
identity reconciliation. The attempted restoration stopped on the pending-effect
guard before policy writes. The separately reviewed recovery resolved exactly
those two effect identities and restored disabled policies at 18:43:54 UTC.
Fresh dedicated credentials were then obtained separately; the original tokens
and natural-expiry evidence were preserved. The remaining grant checks and final
restoration passed at 18:48:17 UTC. The blocked condition was rebuilt before its
previously unexecuted read check, and the successful original prefix was retained.
Both policies ended disabled, the synthetic approval was inactive, the owned note
remained absent, and audit/idempotency records were retained. The full run used
86 application HTTP calls including its diagnostic, within the 90-call bound.
The continuation added no QA turn, note key or write approval. Both original
stops and the historical initial recovery remain preserved; the separate final
finish receipt supplies the final restoration proof for the grant test. The later
flag-only rollback and final policy enable preserve that historical evidence.

The first cleanup inventory stopped on a collection-type error with no pending
effects. Its stop remains preserved. A separately reviewed successor completed
normal login, exact inventory and removal of five owned QA sessions. The later
read-only disposition verified the exact remaining fixture identities and
boundaries without credentials, model calls or mutations. Those historical
guide/configuration receipts retain `cleanupComplete: false` and the retained
disposition; they are not rewritten by a later cleanup.

The user subsequently approved deletion of the exact two synthetic meetings,
one account and two temporary users with their linked content/index state.
Six preflights stopped before deletion: missing adapter inputs, main advancing
twice, an omitted read-only CloudFormation method, and an inline-code expression
treated as a string, and an AWS read error that was not reproduced. Their
original records remain preserved. The corrected
full read-only runtime check passed at 23:45:58 UTC on 2026-10-21; main
`396518f1` was its initial observation, not a constant revision throughout
that run. API `live` 234, all 13 Gateway and nine Crawler functions, templates,
parameters, packages and retained fixture boundaries were checked. A separate
203-read diagnostic passed at 00:15:15 UTC on 2026-10-22; it does not identify
the original AWS error cause or establish cleanup completion.

The cleanup successor admits only the finite reviewed complete source trees,
checks nondecreasing revisions before and after each stage, and separately
verifies the deployed runtime and disabled deployment workflows. Its eight
actual stages completed on 2026-10-22 at 01:11:58 UTC. The final check confirmed
the approved fixture and linked content/index absence, four inactive pilot
write approvals, preserved protected users and audit evidence. The five
previously removed sessions remained absent; no session was deleted again.
Deleted-user profiles retain tombstones with email lookup entries detached.
Two index observation windows expired after the meeting/account deletions.
The final continuation retained both stops and the five completed stages,
required a successful read-only readiness check, then revalidated the index
and completed users and verify.
The scheduled indexer progressed normally. Current COMPLETE ingestions had
unrelated document failures; exact current-revision DELETED jobs, empty key
sets, absent S3 objects and exhaustive provider NOT_FOUND responses establish
these fixtures' removal without claiming global ingestion success.
No runtime deployment, QA/model turn or immediate issued-token revocation
is claimed by this cleanup. Final composed-main attestation remains separate.

A separate original-day news replay failed when the scheduler received the
literal string `$` instead of the parent JSON object. Read-only history
confirmed zero child tasks. The first repair deployment then stopped during
postcheck because its asset manifest still selected the prior template,
leaving the live state machine unchanged. Both failed/no-op attempts remain
preserved and are not treated as successful deployments.

The separately reviewed coherent template-asset deployment completed at
21:29:55 UTC from `b176ca2a15289aec865f51057d9cc7462c270631`, with all nine
Crawler functions unchanged. Its actual original-day replay passed at
21:31:11 UTC: 72 history events showed one scheduler invocation with object
input equal to the parent, five duplicate admissions and zero new child
executions or provider work. All five original child outputs and source
partitions remained unchanged. These receipts resolve the earlier replay
prerequisite. The separately bound final guide results appear above.

## Receipt binding

Receipts remain in the private operations archive. These hashes identify the
reviewable evidence without publishing identities, source text, tokens or login
material. The private receipt map supplies the exact archive paths.

| Receipt | SHA-256 |
|---|---|
| Repaired deployment | `115f8e30f3f9a373772e5377ca398cbf9107033eac51d788260192fb3fb9e08f` |
| Runtime preflight | `d021b77c957d6c138060f6a9215dfadcfce06465459318d46d2ca32849588552` |
| Provider authorization | `f203c788c4511eb93080a1900d8587edf5e30b9876cea0c4cc824c3a1871b357` |
| Current-source reads | `190df95af75057e85b7ab53e07396fb5d693721049c44bb741569fa7d6fbf5fa` |
| Private QA | `cef1a53c761d3e08b931214079d594cc922a8e1c3300cd049bea77e4dcd571c6` |
| Client isolation | `5c2f554bad42fbfc7f48e122f8a7536e23e6a5c0238ae7f83cddd747ab32f734` |
| Note-stage reconciliation | `2a516db7ed883602585a4a84b52afe3efdbdeb132b0fc70f1d09cd330bfca727` |
| Note lifecycle | `269f0eec6c093e32f86cafc5db2d801bb160fd205cd9517c2a4d775376aa016b` |
| Natural expiry | `4c959f5e93b3b7d52a1fe1dc9e0f969c7a0123d1252f7d3768cb3d1dfa862839` |
| Installed stdio refresh | `cd41355b9c9b8f9736a658f354eabf302a7058ad8203e6281c9865c7b3c5b9f5` |
| Synthetic stdio cache cleanup | `adf27b3e17705de0ba214ef2f08acf25acb134e5107622c807f7c822fecfc190` |
| Static preflight | `99944ee6d22833b83002ce2a4a4cb43c7b0500603ea699180d1839534e84ff7d` |
| Initial grant recovery (historical) | `fafd4277aee156c3e6f6d5272f374fecdbf3d4718ce936eacc9de4b4273bf704` |
| Fresh grant authorization | `971732153a085e490c0b531cbfdf0a1f9b028d2d610d8eb3c2976d9b6ce4834f` |
| Grant checks and final restoration | `bdb487f28bfcade53b0e4b20b5a415f238744741b346de71c9f0910901dbf5a9` |
| Reconciled grant completion | `14daf087a45e89d5d807692c2d9b73c60ace4c9a3284b9019e2ccc943d0d9ed4` |
| Static artifact delivery | `8e3d6b7f384e06c2f57c501c42a65abba41619d6d7691a0d47bb69ab2a552124` |
| Published missing-configuration guide | `50a60f42f93b5fae51b4e50d53f5f947e113545f42c45b3783d027a5a9888290` |
| Flag-off deployment | `0c26737e85a53d49877003d40294d67382ec4aaa12ccbd238fec232f9c026d70` |
| Flag-off consumer acceptance | `9cfc77fe1da6230ba36f08804ae016cc9b360c2b22abacf4b160c66c1d1ba1eb` |
| Restored deployment | `be779e2902a0149ccd7f6343b3c0cee25d510cd362f5b8f8d8bde62c9e45e873` |
| Restored consumer acceptance | `35433e4d71446f41b49f4e2609d897f6a9979bca70820a26416e17e8ac63fdc5` |
| Final policy enable | `c21de45a4cda7f11be3a31adee775a6320391fffc8771e5810328f47cf6a4176` |
| Cleanup successor login | `effe098b42839f15452e674298f082bb6034991dadac10f1605b632fa4d0113d` |
| Cleanup successor inventory | `009fb596eee439f88335e5a3122c39b56ae309d3c2c64be85f961409a2b59e6c` |
| Five synthetic sessions removed | `2eea7783e2721db70116286632fffd8e6c58df2f67dcb06b29a73c5b997a70ca` |
| Original cleanup inventory stop | `d7a8ff3b91aa93438e867e34ac01553ba01d2cc9f700a3923e22c00139e9a640` |
| Preserved six-preflight cleanup binding | `6d7f2f86843ccfd0ba179e8b946de1b26031d81da46734bc8e3c2311da7ad112` |
| Index continuation and completed-prefix binding | `c19f8cdbefcbf36d4423f94810149cfc8a4b18f454d82dc2cfa4e8bfae3352f9` |
| Full read-only runtime preflight review | `49349acaaabfedcfe466e26148f789e53370e131916b3c72d63bab349444ef4d` |
| Actual full read-only runtime preflight | `7286beddec6e749d29dc1fe1f3eff48ff9bc8c0be9dd13f3f8abe31dd30cf1e6` |
| Actual eight-stage cleanup completion | `3f6710d556e7a7733fe1eda1b4896fe6ff796725f4077848f0a9a84e8da98d59` |
| Retained fixture read-only observation | `fb23aeb4e3239ddcc09829e2f2ac3701da4350a2adfa20d16bddeb17c457c7c8` |
| Fresh retained fixture re-observation | `403ee158c0f4f2eaf58063ff9c49f53bc46fbf6d2c35e7e4b8148c0c01805112` |
| Failed original-day replay diagnosis | `12fd2773e5ebf51441474ac4ab2b1f228bb159a3298ac724d2e33b34e40ff1c9` |
| Preserved failed replay ledger | `0204832dce7e3c3686ceb9be4679f525854172ebf67b743e02e585495190df01` |
| Coherent Crawler template deployment | `b1eeea1092f4141f4af5e92c97426357d15275ddafa1654f44589bc4713f8975` |
| Original-day object-input replay | `937bf831628e11135bf383ac2f72ffb0b709bca42ffdfbcf7951279e83e99b6d` |
| Retained boundaries before config | `244fcae243e058ecfaaca1c242aba575499e03ac8d3e12ee2d0eeaef3a6d97ec` |
| Retained boundaries after config | `f8af6783309890ecb01e9ac50f83f369025b322c81e6e889378bebbdd71d1720` |
| Final private configuration | `46005238d694aaaf919d67161012da3e727c86cb8b8bb0423d541e4c1ff2b6d0` |
| Published private guide | `814900e9de533cddc0e2ac4479b7167c049578367a00cb1de29027ee4e53d97c` |
| Deployed-source/integration equivalence | `dccf2f7f3b42b77e80d6d8b677e9cede95fa9ecaff250439806c5458d9606993` |

The final runtime configuration is bound by SHA-256
`804fb249bae96d23616cceafa029ce60466d10ab35507bb0312e860cae42f23c`. The reviewed assembly snapshot is
`452973245a8871d64cd1193475751c51cc88d0b74cafd7725f1a10ccf267dca3`;
the published static snapshot is
`3c4bba7efe4741fe3cbd35159a0acadae4d33d9393be45ec56acbe615689d1fb`.
The configuration-only Frontend update preserved all 13 backend functions and
API `live` version 234 without weighted routing, with summary privacy still
writer/reader `1/1` and cutoff `2026-10-21T14:28:12Z`. The config receipt's
`at` field binds its initial intent; the later retained check and browser times
above establish the postdeployment observations.

The source-equivalence receipt binds deployed `bf98ac72`, dormant core
`c4c74b74` and activation `4c061548`. Connector implementation and relevant
runtime/build boundaries match; the false-to-true composition wrapper,
separate news changes and packaged-test difference are explicitly inventoried.
This does not claim that the complete C1/C2 tree was deployed, that differing
ZIP/container contexts are byte-identical, or that artifacts were built from
those integration commits. Final main composition requires a separate rebind
and attestation. Original PR432/native-four history and prior review gates remain
preserved; this evidence does not reset its review-iteration history.

## Documentation commit rebind

The independently reviewed documentation commit
`1e2c626e5667ac217718959340b667f37bc7a7f2` has sole parent
`4c061548bf16af6abc844518c26546b21373b47d`. It changes exactly these six
reviewed documentation files:

- `docs/API-SPEC.md`
- `docs/README.md`
- `docs/decisions/ADR-053-connector-capability-private-delegation.md`
- `docs/runbooks/connector-activation-2026-10-21.md`
- `docs/runbooks/connector-release.md`
- `mcp-server/README.md`

Every other tracked entry is identical: **1,388 paths, Git modes, object types
and object IDs**. The source projection sorts these entries by path and hashes
a UTF-8 JSON array of objects with keys `mode`, `objectId`, `path`, `type`,
sorted object keys, compact separators, no ASCII escaping (`ensure_ascii=False`)
and no trailing newline. Its SHA-256 is
`ed30aa05bd7b4ab66a91eae055f74db815d9dd6f97c77c49d67e4835d0a04ef9`.
The six committed document files also match the reviewed documentation
projection byte for byte.

| Rebind evidence | SHA-256 |
|---|---|
| Exact committed six-document source proof | `4ed1791e2266f44fc7a132db3a3f8cea36b8aa739304ff9471bae69d6027d154` |
| Independent committed-source review, 100/100, no Critical/Major | `ecebf8e0f3a9536c673f8def9176102d538501d583dfd81b7539f4de2c59a7e2` |
| Independent current-candidate source projection attestation | `726b025c2090a29a2c6c6039d16ebaa6bd9ca5a15c37803585f9448ee63413d7` |
| Deployed-source-to-C1/C2 equivalence, including 19 runtime/build boundaries | `dccf2f7f3b42b77e80d6d8b677e9cede95fa9ecaff250439806c5458d9606993` |

This rebind carries the recorded bounded connector acceptance from `4c061548`
to the unchanged implementation in `1e2c626e`; it does not claim that artifacts
were built from the documentation commit or that the complete integration tree
was deployed. The separately inventoried news, packaged-test and composition
limits above remain in force.

A subsequent correction confined to the named documentation files retains this
implementation identity only when an independent comparison of its actual
commit reproduces the exact source-projection digest and checks every changed
path. That documentation revision still requires independent content review
and all applicable current-head CI, including complete required reviewer
coverage. This is not a waiver of ADR-053's integration and activation order or a reset of
review iterations. Changes outside the named files require a new equivalence
assessment; a document need not contain its own future commit hash. Final
composed-main verification remains a separate obligation.

The 1,388-entry digest is the historical `1e2c626e`–`4c061548` comparison,
not a fingerprint for every later main composition. The separately reviewed
composition to assess uses the exact news tree
`b176ca2a15289aec865f51057d9cc7462c270631`, the six independently reviewed
documentation paths above at their final recorded revision, and exactly these
six CI-control paths from `33ac6b6029ed88fb753419f39a9478cd981ff3f0`:

- `.github/workflows/test-backend.yml`
- `scripts/pr-review/role_review.py`
- `scripts/pr-review/run_role.py`
- `scripts/pr-review/test_e2e_roles.py`
- `scripts/pr-review/test_role_review.py`
- `scripts/pr-review/test_run_role.py`

The composition also includes exactly these three reviewed paths from
`c426a17dd160cb6fcf32ae10be370c0d98f37c2f`, whose sole parent is
`a85b8ec1c21aa62b804543f1a031559f0c631575`:

- `scripts/operations/http_origin_release.py`
- `scripts/operations/test_http_origin_release.py`
- `docs/runbooks/http-origin-secret-migration.md`

This coordinator correction accepts the identity-free QA probe's 403 only with
origin-protocol marker `1`, alongside the legacy marked 405. Missing and retired
proofs must still return an unmarked 403; API and MCP predicates are unchanged.
Independent review `8112d64f5c55420abd55959f3e671d903d79314507803af3221302a0252a88f9`
scored 100/100 with no Critical/Major findings. Its 52 tests, 12 real-handler
compositions and 364 response-predicate cases passed; the committed three blobs
match that reviewed source. This is coordinator source validation, not an actual
production secret rotation or a new runtime acceptance. The exact candidate's
assessment must include these three path/mode/object bindings explicitly; they
are not added to the six-document historical exclusion set.

The news source includes the separately accepted settlement/scheduler repairs;
its actual deployment and replay receipts are recorded above. These named inputs
are an assessment contract, not blanket exclusions or a claim that a future
merge is already verified. Under ADR-053's integration and activation order,
before any deployment from a main commit carrying the enabled flags, and before
re-enabling either push-triggered deploy workflow, the independent
assessment must bind that exact commit and every path/mode/object to that
composition, reproduce the 19 connector runtime/build boundary comparisons to
accepted `bf98ac72` under the inventoried exceptions, and preserve the separate
news, CI and coordinator source/review bindings. Any additional difference needs explicit
assessment. That exact-commit attestation and current checks precede the
deployment and are recorded as ADR-053 requires; naming the component revisions alone does not
satisfy those gates or turn unchanged compiled inputs into whole-package or
new-build equivalence.

## Host migration and rollback limits

HTTP hosts previously using the first-party client need a dedicated read or
read/write connection and fresh PKCE login, as described in the
[release runbook](connector-release.md). Personal-note writes additionally
require the current user's explicit approval; OAuth write scope alone is
insufficient. Installed stdio retains its first-party client and
`deployment-auth.ts` deployment/profile-bound cache. Never convert an existing
first-party cache into dedicated credentials.

The completed provider/HTTP checks and standalone installed stdio test do not prove
native OAuth migration in every host. Each host still needs its own supported
client/callback configuration and actual login, refresh and representative read
verification. Record host-specific results only when evidence exists; retain the
[host guidance](../../mcp-server/README.md) and its stated limits.

Flag rollback retains the UUID-based API and its idempotency receipts. Do not
restore deterministic note IDs or delete receipts to bypass a conflict. Separate
Edge and Gateway updates can briefly disagree about accepted clients/scopes;
end the maintenance window only after both stacks and the rendered guide agree.
