# Account Insight Substrate and MCP Back-Data Design

> Historical design record. Original date: 2026-05-30. Original status: Draft.
> Author: Junseok Oh, brainstormed with Claude. Proposed tools, extraction paths,
> and synchronization features are not current completion claims.

## Product goal and responsibility boundary

Organize durable customer knowledge around an Account rather than a single meeting.
The substrate would accumulate typed, dated, source-linked facts for an SA's meeting
preparation and external agents. Internal SFDC activity, SIFT, 2by2, and Player Card
templates would remain assembled by the user's local/internal tools; their exact
internal semantics were deliberately not inferred here.

That boundary did not reduce TTOBAK to passive storage. Search, question suggestions,
research, and later customer-facing deliverable preparation remained part of the
assistant vision. The current proposal focused on long-term account knowledge.

## Proposed foundations

- Shared Account metadata, aliases, domains, industry, and member roles in the
  existing single table; reverse membership lookup and ambiguity-safe alias resolution.
- Personally owned meetings private by default. Classification with `AccountID`
  differed from explicit team publication with `SharedToAccount`.
- Account `MEETINGREF#`, `INSIGHT#`, and `DOC#` items for shared meetings, field
  insights, and inbound notes. Occurrence time, not ingestion time, assigned an
  insight to the correct reporting period.
- Eight insight types: `trend`, `need`, `competitive`, `risk`, `opportunity`,
  `tech`, `stakeholder`, and `action`. Source identity and optional transcript
  timestamps enabled evidence review.
- A separate extraction call after eligible meeting/news/document events, intended
  to replace prior results idempotently without breaking the primary save when
  extraction failed. Failure visibility and privacy boundaries still mattered.
- Bidirectional MCP reads/writes for briefs, insights, documents, and meeting export.
  Original proposed names, filters, statistics, and incremental `updatedAfter`
  support were not a shipped tool registry.

## Vault and user experience

The proposed vault placed explicitly published meetings under `Accounts/{name}/`
and private ones under `_Private/Meetings/`. Account MOC indexes and incremental
sync were intended extensions. A `ttobak_id` provenance marker made exported notes
read-only mirrors for accidental-loop prevention; locally authored notes could be
imported, including meeting-preparation documents with `docType: prep`.

Directory-level OneDrive sharing was a user-managed companion workflow, not a
server-enforced reflection of Account membership. Revoking application access does
not recall an exported file. Minimal UI covered account registration/members,
classification/publication, and account meetings, insights, and documents.

## Tradeoffs, exclusions, and open choices

Reuse of the single table and Account membership avoided separate group infrastructure.
The costs were coarse roles, denormalized refs, partial side effects, alias conflicts,
PII retention, export-size bounds, and differences between local and server copies.
The marker was never a security or semantic-deduplication control.

Internal performance-document assembly, a separate personal contribution ledger,
standalone groups, and live translation/Q&A improvements were excluded. A later
no-Mac deliverable workflow could consume this substrate, but was not implemented
by this spec. Vocabulary/model choice, export limits, and exact vault conventions
were open. The original assertion that existing account PII already had the required
KMS coverage was not supported by the table configuration.

## Current evidence and successors

[ADR-015](../../decisions/ADR-015-account-first-class-shared-entity.md) through
[ADR-018](../../decisions/ADR-018-mcp-back-data-tools.md) cover implemented foundations;
[ADR-034](../../decisions/ADR-034-account-member-permission-democratization.md) changes
member-add/role permissions and [ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md)
adds hierarchy without inherited access. Current
[account](../../../backend/internal/service/account.go),
[meeting](../../../backend/internal/service/meeting.go), and
[vault](../../../backend/internal/service/vault.go) services define the actual gates,
insight projection, and bounded export. The
[MCP registry](../../../mcp-server/src/index.ts) defines current tools; not all
proposed extraction sources or sync options above exist.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
and [project security guidance](../../../CLAUDE.md).
