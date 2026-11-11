# Account relationship views design

Date: 2026-11-11. Status: draft for owner review.

## Why

The account relationship graph (`AccountGraph`, `GET /api/accounts/{id}/graph`)
draws every account item, up to 150 nodes and 300 edges, as one mermaid
`flowchart LR` behind the "전체 관계 보기" toggle. The owner found it hard to
read. It answers no specific question. The owner wants the relationships to
answer four questions:

1. Which meetings happened, who attended, and which documents belong to them?
2. Did account spend (COT) change after a meeting?
3. Which news items and SA Activities surround a meeting?
4. Who are the customer's key people, and which of them are practitioners?

## Goals

- Replace the single graph with four focused views, one per question, on one
  "관계" tab of the account detail page. Each view reads at a glance without
  panning or zooming.
- Anchor every view on the meeting timeline, because meetings are the account's
  shared spine.
- Show every connection with its source: a stored link, or a time window.

## Non-goals

- A general graph explorer, force layout or free-form zoom.
- Writing anything to SFDC, or storing SFDC data beyond the current rules.
- Asserting that a meeting caused a spend change.

## Anti-goals (a failure even if it works)

- An edge drawn from model or heuristic inference without a stored link. Time
  proximity is shown as "같은 시기", never as an edge.
- Owner-private data shown to other members. The owner's SA Activities
  (`PERFTASK#` rows in the owner's `USER#` partition, ADR-023) appear only for
  that owner.
- A page that stalls or fails as a whole when one source (billing, news,
  performance) is missing or slow.

## Constraints

- Cloud storage rules from the account ontology roadmap are unchanged: SFDC
  data in the cloud is limited to opportunity ID/name/stage, and the owner's SA
  Task metadata already stored by the performance API.
- Account access rules are unchanged: every account read authorizes through
  `AccountOverviewService.authorizeAccount`; billing reads keep the Billing BFF
  team-access rules (ADR-012). The views never widen who sees an item.
- Bounded responses and pagination, as for the existing overview routes.
- Korean UI copy; English repository text.
- The existing `/graph` route stays for MCP and compatibility. The web stops
  using it once the views ship.

## Layout

The "관계" tab replaces the "전체 관계 보기" toggle in "기회별 흐름". It shows a
segmented control with four views. The selected view persists in the URL
(`?tab=relations&view=meetings`).

### View 1: 미팅 (meetings, attendees, documents)

A reverse-chronological list of meetings published to the account. Each row
shows date, title and project chips. Expanding a row shows three columns:

```
2026-11-03  하나은행 EKS 마이그레이션 리뷰            [프로젝트: EKS 전환]
  ┌ 고객 참석자 ─────────┐ ┌ AWS 참석자 ─────┐ ┌ 문서 ─────────────────┐
  │ 김OO 차장 (발화 14)  │ │ 오OO SA (발화 20)│ │ 첨부: 아키텍처.pdf     │
  │ 이OO 과장 (할일 2)   │ │ 박OO AM          │ │ 노트: 사전 준비 메모   │
  └──────────────────────┘ └──────────────────┘ │ 뉴스: 하나금융 클라우드… │
                                                  └────────────────────────┘
```

- Customer attendees are contacts reached through people edges
  (`LINK#meeting#…#contact#…#{role}`). AWS attendees are account members
  reached the same way. Each person shows the roles they held in that
  meeting: 참석 (participant), 발화 (speaker, with count when available),
  할 일 (assignee), 언급 (mentions). Auto edges render with a dashed underline
  and the existing remove control. Manual edges render solid.
- Documents are only items with a stored link to the meeting:
  - attachments of the meeting (`ATTACH#` rows);
  - account documents whose stored `Links` (normalized wikilinks) resolve to the
    meeting;
  - news linked to the meeting (`LINK#` news→meeting).
  A document with no stored link never appears under a meeting.
- A filter limits the list to meetings with a chosen person or project.

### View 2: 비용 (spend after meetings)

A monthly spend line chart from the account's current COT dataset
(`spend_monthly`: month, spend, bedrock), fetched in the browser through the
existing `fetchAccountDataset` (Billing BFF). Meetings appear as markers on the
x-axis. Selecting a marker shows a card:

```
2026-08-12 미팅 "Bedrock PoC 킥오프"
  미팅 월 $12.4k → +1개월 $13.1k (+6%) → +3개월 $19.8k (+60%)
  같은 기간 증가가 큰 서비스: Amazon Bedrock +$5.9k, EC2 +$0.8k
  ※ 시기만 겹친 것이며 미팅이 원인이라는 뜻은 아닙니다.
```

- Service deltas come from the `services` dataset of the same upload version.
- No COT upload, or no Billing access: the view shows the existing upload
  call-to-action (`billingUploadHref`) and the rest of the tab still works.
- The window lengths are +1 and +3 months. They are fixed in this version.
- No edge is created or stored between a meeting and a spend change.

### View 3: 맥락 (news and SA Activities around meetings)

A vertical timeline. Each meeting is a row. Each row has two lanes:

- **연결됨**: items with a stored link to the meeting (news→meeting links today).
  An auto link shows its evidence on hover, like other auto links.
- **같은 시기**: items within ±7 days of the meeting that have no link:
  - account news (`CurrentNews`);
  - account events;
  - the owner's own SA Activities mapped to this account (`PERFMAP#` mapping,
    `PERFTASK#` rows; type, subject, status, date). They are visible only to
    the owner and carry a "나만 보임" badge.

  Each item has a "연결" action. For news it creates the existing
  news→meeting link. SA Activities cannot be linked in this version; the
  owner-private rows stay in the owner's partition.

### View 4: 사람 (key people and practitioners)

A ranked table of the account's customer contacts:

```
이름     직함/부서        미팅  발화  할 일  최근     구분
김OO     차장/IT인프라팀    9    41     6   11-03   실무 핵심
최OO     상무/디지털본부    3     5     0   09-20   의사결정
이OO     과장/IT인프라팀    6    12     4   11-03   실무
```

- Counts come only from stored people edges of meetings the viewer can read:
  distinct meetings per role, and the latest meeting date.
- 구분 is a derived label with its evidence visible on hover:
  - **의사결정**: title matches a decision-maker list (대표, 사장, 부사장, 전무,
    상무, 이사, 본부장, 실장, CTO, CIO, CDO, CISO, Head, VP, Director).
  - **실무 핵심**: not a decision-maker, and assignee in at least 3 meetings or
    speaker in at least half of the meetings attended (minimum 3 meetings).
  - **실무**: other contacts with at least one participant or speaker edge.
  - **미확인**: contacts with no meeting edges. They are listed last and
    collapsed.
- The label is computed on read and is never stored. A manual override is out
  of scope for this version.
- Selecting a person filters View 1 to their meetings.

## Data contract

One new read route serves Views 1, 3 and 4:

`GET /api/accounts/{accountId}/relations?view=meetings|context|people&cursor=…`

- `meetings`: one page of meetings, newest first, at most 20 per page. Each
  meeting carries its people (contact/member ID, name, title, roles, auto or
  manual origin), its linked documents (kind, ID, title) and linked news.
- `context`: the same meeting page with the two lanes per meeting. The
  `같은 시기` lane includes the caller's own mapped SA Activities only when the
  caller has them. Nothing is read from another user's partition.
- `people`: all contacts with aggregated counts. They are aggregated from the
  account's people edges, bounded by the same read limits as `graphPeople`.
  When a bound is hit the response sets `truncated`.
- A missing source (news or performance) returns that part empty with a
  per-part `unavailable` flag, not a failed response.
- The exact response types, size bounds and error codes are left to the builder.
  They follow `docs/API-SPEC.md` conventions and are documented there.

View 2 makes no new backend route. It combines `relations?view=meetings`
(meeting dates) with the Billing BFF datasets in the browser.

## Delivery order

1. View 4 (사람) and View 1 (미팅): backend aggregation and the tab shell.
2. View 3 (맥락): time-window lane and SA Activity read.
3. View 2 (비용): billing chart and meeting markers.
4. Remove the "전체 관계 보기" toggle from the web. Keep `/graph`.

Each step is its own PR under the 95 KB review cap.

## Calls made without owner input

These are the designer's calls. The owner should check them while reading:

- Four views instead of an improved single graph.
- The ±7 day window for "같은 시기", and the +1 and +3 month spend windows.
- The 의사결정 title list and the 실무 핵심 thresholds.
- SA Activities stay unlinkable and owner-only.
- No manual override of the person label in this version.

## Left to the builder

- Response types, page sizes inside the stated bounds, and error codes.
- Chart library choice inside the existing frontend dependencies, following the
  `dataviz` conventions already used by the billing tab.
- How wikilink targets resolve to a meeting. If no stored resolution exists
  today, the documents column shows attachments and news only, and the gap is
  recorded in the PR.
