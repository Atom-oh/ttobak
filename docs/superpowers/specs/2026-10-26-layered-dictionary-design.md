# Layered Dictionary (Common · Account · Personal) — Design

> Status: historical design record (implemented; current behavior is ADR-008, amendment 2026-10-26). Was: draft for review. Date: 2026-10-26. Author: Junseok Oh, with Claude.
> Extends ADR-008 (custom dictionary for STT accuracy). Builds on automatic
> personal terms, enabled in production on 2026-10-26 (PR #478).

## 1. Why

Speech recognition rewrites unknown proper nouns into common words that sound
alike. A real case from a customer meeting outside TTOBAK: "K-culture" was
transcribed as "gay culture" — wrong, and harmful if it reaches notes, a summary
or a customer. Such terms are usually already known to TTOBAK before the
meeting: they appear in the Account's research, its KB documents, earlier
meetings, or industry news.

Today only one source feeds the dictionary: LLM extraction from the owner's own
finished meetings, stored per user (`USER#…` `DICTTERM#…`). News crawling, KB
documents and deep research contribute nothing, and nothing is shared between
the members of an Account.

Goal: **a term that TTOBAK already knows for an Account is recognised — or at
least corrected — in that Account's next meeting, for every member.**

## 2. Goals, non-goals, anti-goals

Goals
- Three layers: **Common** (everyone), **Account** (members of one Account),
  **Personal** (one user, as today).
- New sources: news crawling → Common; KB documents and deep research → Account
  (or Personal when not linked to an Account); meetings → Personal (today) and
  Account (when the meeting is linked).
- A meeting uses Personal + its Account + Common, within provider limits.
- A transcript-correction step that uses the same terms, so a wrong
  substitution is fixed even when recognition biasing was not enough.

Non-goals
- Replacing the personal dictionary (ADR-008 rejected an organisation-only
  dictionary as insufficiently personalised; layers add to it).
- Live-stream vocabulary changes beyond what ADR-008 already covers.

Anti-goals
- One Account's terms visible to a non-member (UI, API, MCP) or used to correct
  another Account's meetings.
- Common news terms crowding out Personal/Account terms in the vocabulary.
- Silent failure (the off-by-default flag hid the feature for its whole life).

## 3. Current facts

- Terms: `DICTTERM#{termKey}` under `USER#`; `ACCOUNT#` partition is reserved
  in ADR-008 but unused. Auto terms: confidence ≥ 0.85, 300 auto terms per user,
  500 active manual terms per scope.
- Vocabulary: one personal ko-KR Transcribe vocabulary per user (A/B slots,
  version-guarded state row); 1,500 rows / 48 KiB table limit enforced in
  `vocabulary_table.go`; account-wide Transcribe quota 100 vocabularies (ADR-008
  rollout note). A batch job takes exactly one vocabulary name.
- Whisper: `INITIAL_PROMPT`, now capped at 300 runes (PR #478).
- Meetings carry `accountId`. Research carries `accountIds`. KB objects live
  under `kb/{owner}/`. News documents belong to a crawler source
  (`CRAWLER#{sourceId}`), users subscribe via `USER#… CRAWL_SUB#…`.
- The transcript refine step (`BedrockService.RefineTranscript`) does not use
  the dictionary.

## 4. Design

### 4.1 Storage

| Layer | Partition | Written by | Read by |
|---|---|---|---|
| Common | `DICT#COMMON` | news extraction job; any signed-in user | everyone |
| Account | `ACCOUNT#{id}` | KB/research/meeting extraction for that Account; members (manual) | members of the Account |
| Personal | `USER#{id}` | as today | the user |

Same row shape and `termKey` as ADR-008 (`origin`, `state`, `version`,
tombstones, `userEditedAt`). New field `sources` (bounded list of
`{kind: meeting|kb|research|news, ref, at}`) replaces meeting-only
observations, so every term shows where it came from. Caps per layer:
Common 300 auto, Account 300 auto + 500 manual, Personal unchanged.

Because every user may edit and delete Common terms (L2), each Common write
keeps `updatedBy`/`updatedAt`, deletes stay tombstones, and the last 20 changes
per term are kept in an audit list so a bad edit can be traced and restored.

Writes to `ACCOUNT#` require membership (ConditionCheck on the membership
item), as ADR-008 anticipated. Automatic writes into an Account come only from
content already linked to that Account.

### 4.2 Sources and jobs

All sources reuse the existing extraction prompt, deterministic filters and
merge rules; only the input text and target layer differ.

| Source | Trigger | Input | Target |
|---|---|---|---|
| Meeting (today) | summary done | notes + transcript | Personal; also Account when `accountId` set |
| KB document | KB ingestion success | extracted text (first N KB) | the Account chosen at upload (L4), else Personal of the owner |
| Deep research | research completed | final report | each Account in `accountIds`, else Personal of the owner |
| News | daily after crawl | titles + summaries of the day's documents | Common, only terms seen in ≥ 2 documents or ≥ 2 days |

Grounding rule (as today): a term is kept only if it occurs verbatim in the
input. News terms additionally need the repeat threshold so one-off names do
not enter Common.

### 4.3 Recognition (Transcribe and Whisper)

One vocabulary per user, as today, built from a merged plan:

1. Personal manual, 2. Account terms of the user's Accounts ordered by the
user's most recent meeting per Account, 3. Personal auto, 4. Common — cut at
the 1,500-row / 48 KiB limit with whole terms. One vocabulary per user keeps
quota usage unchanged (≤ 3 names per user).

Whisper prompt per meeting: Personal manual, then the meeting's Account terms,
then Common, within the 300-rune cap.

Trade-off: a user in many Accounts gets other Accounts' terms in the same
vocabulary. That only biases recognition (it never exposes text), and the
correction step below is scoped to the meeting's own Account.

### 4.4 Correction step (the K-culture case)

`RefineTranscript` receives a glossary: the meeting's Account terms + Personal
terms + Common terms with their `heardAs` aliases. The prompt asks it to fix
only clear mis-hearings of glossary terms (sound-alike, in context), never to
paraphrase. Fixes are applied silently (L3): the transcript shows the corrected
text with no highlight. Each fix is still recorded on the meeting (`from`, `to`,
position, term) for audit and debugging, not shown in the UI. Repeated
fixes of the same `from` add it to that term's `heardAs`, which also feeds
Transcribe as an alias.

Safety: the step never introduces glossary terms where no similar-sounding
span exists; corrections are capped per meeting; an Account glossary is only
used for that Account's meetings.

### 4.5 UI

Custom Dictionary (Knowledge menu) gets layer tabs — Personal / Account
(selector of the user's Accounts) / Common — with source badges (meeting, KB,
research, news), the "auto/suggested" filters that exist today, and promote /
demote between Personal and Account for members. Common is editable by every
signed-in user (L2), with its change history visible per term.

KB upload asks which Account the document belongs to (L4): a picker of the
user's Accounts plus "none (personal)"; the choice is stored on the KB object
and decides the target layer.

### 4.6 Observability

Log every enqueue outcome per source (including DISABLED); counters per layer
(created, suggested, merged, evicted); a daily metric of vocabulary rows by
layer and of correction counts; alarm when a source produced zero runs for 7
days while its trigger ran.

## 5. Rollout

1. Storage + membership-guarded Account writes; UI tabs (read-only).
2. Meeting → Account layer; merged vocabulary plan; Whisper per-meeting prompt.
3. Correction step with glossary (flag `ttobak:transcriptCorrection`), canary.
4. KB and research sources.
5. News → Common with the repeat threshold.

Each step behind its own flag, logged when off.

## 6. Decisions (owner, 2026-10-26)

- **L1** Account terms from meetings are **activated automatically** (still
  suggested-only for tainted meetings, as for Personal today).
- **L2** **Every signed-in user** can edit and delete Common terms (audit list
  and tombstones above).
- **L3** Corrections are **applied silently**; records kept on the meeting only.
- **L4** KB upload **asks for the Account** (or none = Personal).
