# Work Assistant Expansion Roadmap

> Historical roadmap. Original date: 2026-07-09. Original status: Draft;
> brainstormed with Claude. The sequence and future interfaces below are planning
> context, not a present-day task list or proof of delivery.

## Vision and responsibility boundaries

Extend meeting capture and Account knowledge into an SA workspace for research,
notes/blogs/slides, linked knowledge, customer sharing, opportunity context, and RAG.
Reuse Account membership and existing document/editor infrastructure. Real-time
coediting, an in-app slide authoring system, and server-side Salesforce access were
excluded. The intended Salesforce source was the user's local/internal tooling,
which would push authorized data to TTOBAK.

## Original decomposition

| Project | Intended scope and limits |
| --- | --- |
| SP1: News search | Gateway Web Search for news and research; retain source attribution and snippet-based briefings. Recorded shipped 2026-07-13, PR #111. |
| SP2: Document Hub | Personal/account notes, blogs, uploaded slides, Markdown links, vault export. No slide authoring/coediting. Recorded shipped 2026-07-15, PR #113. |
| SP3: Public pages | Secret bearer-token URLs, explicit revocation, no public index/search/comments. Generic document/meeting pages were an early proposal. |
| SP4: Graph view | Visualize explicit wikilinks; no inferred entity edges, 3D view, or graph editing. |
| SP5: Opportunity intake | Local/internal Salesforce reader pushes opportunity metadata; no reverse Salesforce writes or direct server connector. |
| SP6: KB integration | Add approved new content sources to retrieval; avoid a wholesale vector-store replacement. |

SP1 and SP5 were independent; SP3/SP4 depended on document foundations, and SP6
followed the content-producing projects. The suggested order was SP1 through SP6,
with detailed design and validation required for each rather than one large change.

## Decisions and tradeoffs

Uploaded PPTX/PDF rather than slide creation reduced scope. Native browser PDF
viewing avoided another viewer dependency but retained mobile viewing limitations.
Explicit wikilinks preserved a predictable graph model but did not provide
relationship authorization. Secret URLs avoided public listing but still required
safe token lifecycle, minimal disclosure, and current policy approval for any route.
The roadmap did not authorize copying account membership into external file ACLs.

## Current mapping and supersession

- [ADR-020](../../decisions/ADR-020-doc-hub-v2-personal-docs-wikilinks-slides.md) and
  [ADR-022](../../decisions/ADR-022-slide-preview-conversion-and-public-share-links.md)
  describe later document work. PPT conversion now exists; the current public
  exception is file-backed personal-document access through
  `GET /api/public/docs/{token}`, not generic public meeting/note pages.
- [ADR-025](../../decisions/ADR-025-project-entity-sfdc-oppty.md) and the
  [Project service](../../../backend/internal/service/project.go) define Project
  metadata and links, not the proposed `OPPTY#` intake API or a Salesforce sync.
- [AccountDocument](../../../backend/internal/model/account.go) stores parsed
  wikilinks; that alone does not establish SP4's graph endpoint/visualization.
- [KB entry point](../../../backend/cmd/kb/main.go) still exists. The old instruction
  to delete it as a stub is historical and requires a current dependency review.

The broad public-page, PDF-only sharing, and future API sketches are superseded or
unimplemented as described above. Current requirements and accepted exceptions live
in the [documentation map](../../README.md), [API](../../API-SPEC.md),
[architecture](../../architecture.md), and [project guide](../../../CLAUDE.md).
