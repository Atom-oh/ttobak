# Commercial readiness remediation

Historical execution plan initiated on 2026-10-12 from `640d297`, following the
[baseline review](../../research/reviews/2026-10-12-commercial-readiness.md).
The requested delivery includes fixes, tests, push, PR creation, review feedback
and merge only after the current HEAD has complete required review coverage,
passing checks and no unresolved Critical/Major findings.

The working interpretation is remediation of the report's product defects as
well as factual corrections to the report. A scope clarification was offered.
Existing accepted limitations and unverified deployment facts remain visible;
they are not silently converted into fixed defects.

## Work and acceptance

| Finding | Required implementation evidence | Delivery grouping |
|---|---|---|
| R1 | Source/provenance and authorization conditions cover linked-context publication, all grant paths, later sharing and stale derivative writers; concurrency tests exercise each boundary. | Dedicated summary/sharing change and decision. |
| R2 | ARN-only verifiers and dynamic edge reference; coherent bootstrap/migration/rotation with no secret output, bounded overlap and direct-origin negative acceptance. Removing the tracked value alone is insufficient. | Coordinated HTTP-origin migration. |
| R3 | Route-template logging and cleanup errors contain no token, query or uncontrolled error content; canary tests cover successful and unmatched requests. Existing logs/links need operator disposition. | Privacy logging. |
| R4 | Refinement parse/provider/panic failures log fixed categories and chunk metadata; canary values never appear. | Privacy logging. |
| R5 | Complete model responses, validated timestamps/schema/source coverage, raw fallback and explicit degraded quality; adversarial and multipart tests. | Refinement validation. |
| R6 | Evaluator resolves the effective final-note model and exercises the actual completion/continuation path with hash-bound evidence. | Quality evaluation, with independent workflow review. |
| R7 | Loopback-only callback, state-first handling, safe HTML, bounded/validated token exchange, single-flight and logout race safety. | Local MCP auth. |
| R8 | Verified deployment-specific token cache, safe atomic permissions, no global-cache reuse and verified status identity. | Local MCP auth. |
| R9 | Server-owned, authorized, revision-bound document continuation restores large documents within encoded response budgets; relevant collection/brief/vault reads remain bounded. | Document/MCP reading. |
| R10 | Dedicated connector clients and capability enforcement in MCP, ordinary API and QA paths; direct API and ID-token alternatives cannot bypass a read-only grant. Structured tool responses remain within the existing result cap. | Connector capabilities and contracts. |
| R11 | Core delivery DLQs, execution failure destinations, observed alarms and safe replay; templates and failure/replay tests distinguish delivery from execution failures. | Pipeline recovery. |
| R12 | Existing user-pool logical ID retained; deletion/replacement retention, deletion protection and stack termination protection verified without replacement. MFA requires its own compatible flow. | Identity deletion protection. |
| R13 | The export selected by the user actually downloads a valid, readable artifact with the advertised format, including Korean text and long content. | Export. |
| Additional confirmed gaps | Browser dependency advisory triage, HTML publication/cache consistency and compatible application security headers; relevant build, audit and deployment checks. | Separate dependency/frontend releases. |

## Delivery rules

Keep each PR within the repository's complete-review limits. Preserve the
trusted review workflow and its required coverage. For each correction, verify
the review against the exact current HEAD and collect paginated inline/human
feedback; apply substantive findings and rerun the necessary checks. Five
corrective rounds are the autofix bound, not permission to waive a remaining
blocker.

Code acceptance and runtime acceptance are separate. AWS credentials were absent
during the baseline review. Identity protection, secret rotation, alarms,
authenticated MCP clients, devices and real-audio/load quality require their
own operational evidence. Do not claim them complete from source or synth alone.
No incidental KnowledgeStack deployment, automatic recording purge, table TTL
attribute switch or unsupported MFA cutover is part of a routine fix.
