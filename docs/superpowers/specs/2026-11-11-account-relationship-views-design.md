# Account relationship graph: person and project focus

- Status: draft design record; revised after owner feedback on 2026-11-11, not
  yet approved.
- Replaces: the whole-account view of `AccountGraph`
  (`frontend/src/components/account/AccountGraph.tsx`,
  `GET /api/accounts/{accountId}/graph`).

## Why

The current graph draws every account item, up to 150 nodes and 300 edges, as one
mermaid `flowchart LR`. It is hard to read because it has no center. The owner
wants to start from one person or one project. From there, the graph should show
which meetings connect to the focus, and which technologies those meetings cover,
such as EKS, Bedrock, AgentCore or Oracle.

Example reading: "Kim (customer, infrastructure team) attended 9 meetings. Six of
them covered EKS and two covered Oracle migration. Lee attended four of the same
EKS meetings."

## Goals

- A focused graph with one center, chosen from a person (customer contact or
  account member) or a project.
- Three fixed columns, read left to right: focus, meetings, technologies.
- Technologies come from a small curated catalog, so "eks", "kubernetes" and
  "k8s" become one EKS node.
- Selecting a node narrows the graph: a technology highlights its meetings, and a
  meeting opens a side panel with its attendees, documents and linked news.
- People in the side panel can be clicked to re-center the graph on them.

## Non-goals

- The whole-account graph. The old view stays only behind the existing API for
  MCP and compatibility.
- Free-form force layout, zoom or panning.
- Spend (COT) correlation and SA Activity lanes in this version. They are
  possible later side-panel sections.

## Anti-goals

- An edge without stored data behind it. Edges come from stored people links
  (`LINK#meeting#...`), `meeting_project` relations, and stored meeting `tags`.
  Nothing is inferred at read time.
- A technology node presented as certain when it came from model-extracted tags.
  Auto-derived technology edges are drawn dashed, like other auto links.
- Showing a person, meeting or document the viewer cannot already read through
  `AccountOverviewService.authorizeAccount` and the existing meeting rules.

## Layout

```
 FOCUS                MEETINGS (newest first)          TECHNOLOGIES
 +--------------+     +---------------------------+     +-----------+
 | Kim, manager |-----| 11-03 EKS migration review |-----| EKS    (6)|
 | infra team   |-----| 10-21 Bedrock PoC kickoff  |-----| Bedrock(3)|
 | 9 meetings   |-----| 10-02 Oracle DMS check     |- - -| Oracle (2)|
 +--------------+     | ...  (+4 more)             |- - -| AgentCore |
                      +---------------------------+     +-----------+
```

- Column 1 is the focus card: name, title and department for a contact, role
  for a member, stage for a project, and the meeting count.
- Column 2 lists the focus's meetings, newest first, at most 12, with a "more"
  control that pages. For a person, these are meetings with a people edge to
  them (participant, speaker, assignee or mention). For a project, these are
  meetings linked to it (`meeting_project`).
- Column 3 lists technologies covered by the listed meetings. Node size or the
  count badge is the number of the focus's meetings that cover it. Technologies
  are sorted by count.
- Edges between columns are straight connectors drawn in SVG by a deterministic
  layout. No mermaid.
- Selecting a technology dims meetings without it. Selecting a meeting opens the
  side panel:
  - customer attendees and AWS attendees, with their roles in that meeting;
  - documents with a stored link: meeting attachments (`ATTACH#`) and account
    documents whose stored `Links` resolve to the meeting;
  - news linked to the meeting.
- The focus picker is a search box over account contacts, members and projects.
  It shows a short list of suggestions: the three contacts and the project with
  the most meetings. With no focus chosen, the first suggestion is used.
- The focus is kept in the URL (`?focus=contact:{id}` or `project:{id}`), so a
  focused view can be shared with team members who can read the account.

## Technology catalog

- A checked-in catalog maps aliases to a canonical label and a group, for
  example:
  - EKS: `eks`, `kubernetes`, `k8s`;
  - Bedrock: `bedrock`;
  - AgentCore: `agentcore`;
  - Oracle: `oracle`, `oracle-db`, `exadata`.
- The builder seeds the catalog from current meeting tags across accounts and
  the AWS service names already used in `CrawlerSource.AWSServices`.
- Tags that match no catalog entry are counted under "other". That group is
  hidden by default and can be expanded, so the catalog's gaps stay visible.
- Matching is exact after lowercasing and trimming, never fuzzy.
- Source of technology edges: the meeting's stored `tags`. The summarize Lambda
  extracts them (`extractTags` in `backend/internal/service/bedrock.go`, at most
  five per meeting), so these edges are `auto` and drawn dashed.
- A manual correction path (adding or removing a technology on a meeting) is a
  follow-up. It needs its own stored link kind and is out of scope here.

## Data contract

New read route:

`GET /api/accounts/{accountId}/graph/focus?focus={kind}:{id}&cursor=...`

- `kind` is `contact`, `member` or `project`. Any other value returns 400.
- The response has the focus card, one page of meetings (at most 12) and the
  technologies of that page. Each meeting carries its date, title, the focus
  person's roles, and its technologies with an `origin`.
- The side panel uses a second call per meeting, or the builder may embed it if
  the result stays inside the existing overview response bounds.
- A focus the caller cannot see, or an ID that is not in the account, returns
  404. Meetings are filtered by the same publication and access checks as
  `graphMeeting`.
- Exact types, byte bounds and error codes are left to the builder, following
  `docs/API-SPEC.md` conventions.

## Delivery order

1. Technology catalog and the focus route with tests.
2. The focused graph component, focus picker and side panel; replace the
   "show full relationship" toggle with a link to the new view.
3. Docs: `docs/API-SPEC.md`, `docs/DESIGN-SPEC.md` and the user guide.

Each step is its own PR under the 95 KB review cap.

## Designer's calls for owner review

- Three fixed columns: focus, meetings, technologies.
- Twelve meetings per page.
- Default focus: the contact with the most meetings.
- Technology edges from stored model-extracted tags, dashed, with "other" hidden.
- Spend and SA Activity are left out of this version.

## Left to the builder

- Response types and bounds inside the stated limits.
- The SVG layout code and its responsive behavior on narrow screens; on mobile
  the columns may stack vertically.
- How wikilinks resolve to a meeting. If no stored resolution exists, the
  documents section shows attachments and news only, and the PR records the gap.
