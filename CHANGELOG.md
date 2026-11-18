# Changelog

Repository change history, not a live deployment inventory. Notable changes follow
the categories of Keep a Changelog and Semantic Versioning. Each version is an
annotated `vX.Y.Z` Git tag described in the [release runbook](docs/runbooks/releases.md);
**Unreleased** holds changes merged since the latest tag. The app's `/changelog` page
shows the user-facing subset of each release. A tag does not prove deployment.
Older model aliases, workflow counts and infrastructure choices below describe their
original changes; current configuration belongs to the project guide and source
manifests.

## [Unreleased]

### Added

- SA activity page: a create-customer button on unmapped SFDC accounts and a customer onboarding section that invites the SFDC account team's AM, TAM and SA to the mapped Account, at most 20 per click.
- SA dashboard onboarding API: an owner-private `sfdc-account-team` sync source, an onboarding read that joins the SFDC account team with the mapped Account's members, and an invite route that invites selected team members (at most 20 per request) through the existing member-add rules.

### Changed

- MCP sign-in: Cognito now accepts local MCP callbacks on ports 9876-9879, so Claude Code, Codex or Kiro can switch to an alternate callback port when 9876 is busy. All registered callbacks stay on localhost.

### Fixed

- The SA performance report can save small tool results in its output folder again: its write permission is an `Edit(...)` rule, the only form Claude Code applies to file writes.
- The SA performance report runs on the owner's `model` setting, else Fable (it ran on the CLI default Sonnet), and its prompt names the exact script command form, so calendar, interview, Slack thread and PFR/CI counts are no longer skipped.

## [1.1.0] - 2026-11-17

### Added

- Local intake: a `links` connector reads new messages of the Slack channels you chose through your own Slack MCP server (`list_my_channels`, `get_messages` only) and uploads only public https links as customer update sources; message text, author and channel ID stay on the Mac (`ttobak-mcp intake links channels|select`, `intake run --only links`) (#984).
- Admin team sync: a Team column in the admin user list and an admin-only Mac app action that previews and writes each `@amazon.com` user's team from the internal directory into the shared user pool's `custom:department` (#978).
- User guide: a Mac app tab covering install and updates, macOS permissions, System audio recording, meeting detection and mute following, caption windows and interpretation, local intake, and troubleshooting; the Mac app opens the guide on that tab (#967).
- Drag and drop a Remember CSV or .xlsx export onto the contact import fields on the Account page and in Settings, in the browser and the Mac app; the Mac app's other upload drop zones now receive drops too (#959).
- Mac app: choose Slack channels for customer update links in Settings (load, search, label, save); the intake status table shows a Slack links row with channel, link and upload counts, and Slack sources can be deleted in the web settings (#985).
- Personalized Assistant cards and meeting-aware questions; automatic read-only answers to spoken questions, enabled by default with a separate external-search opt-in (#894).
- Customer update drafts API, storage side only with no collector or generator yet:
  followed sources, link upload, owner-private drafts and a per-account opt-out
  (#957).
- Customer update drafts on the web: followed-source settings, an account drafts tab
  and the `/drafts` inbox with copy, done and dismiss actions; lists stay empty
  until the collector and generator ship (#961).
- Customer update feed registry: RSS, site and YouTube sources register a shared feed
  subscription, source lists report a feed error, and the feed collector code is
  added without a schedule yet (#964).
- Mac app: an Interpretation screen (`/interpret`) that hears the microphone and
  system audio and shows live interpretation with no meeting; no audio file,
  transcript or upload is created. Needs Mac app 0.2.0 or later, which carries the
  `start_listening` command (#949, #954).
- Speaker colours on the Mac app's interpret screen: each caption line is coloured by who spoke, with a speaker tag where the speaker changes. The floating caption windows show the same speaker colours from Mac app 0.2.2 (#966, #968).
- Customer update collection: an hourly feed collector and a 10-minute fan-out worker
  deliver new links from followed RSS, site and YouTube sources into each
  subscriber's inbox (#970).
- Customer update agent runtime: an AgentCore container that reads followed links, decides per customer whether an update is worth a draft, and writes sealed run results; not invoked by anything yet (#972, #973, #974, #975).
- Billing ingest writes a per-account `services_in_use` object (service names only, no amounts) and the backend gains Go draft checks that match the agent's shared contract; nothing reads either yet (#976).
- Customer update run storage: per-user run lease, daily draft and research counters, pending marks with a rotation cursor, item holds and sealed decisions; no caller yet (#977).
- Customer update context packs: per-account packs of account name, AWS service names, opportunity names and stages, public news, internal insights and readable meeting-note excerpts, built only for accounts the user can access; no caller yet (#979).
- Customer update run phases in the worker: start and finalize agent runs with bounded retries, item holds, a 3-day backlog window and per-user daily draft and research caps; inactive until the runtime ARN is configured (#980).
- Customer update run infrastructure and records: worker environment and least-privilege grants, the research cap setting (`CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY`, default 30), gate evaluation tools, the runbook and ADR-030; runs stay off until `customerUpdateRuntimeArn` is set (#982).
- Account-shared calendar on the Mac intake and the web: intake matches Outlook events to Accounts by title, location and attendee/organizer domains (own domains excluded) and shares an event with a single matched Account only when the meetings connector reports `isPrivate: false`; otherwise the match is a candidate. Events in the Outlook `Private` category or marked private are never shared, and `Private` beats a manual Account override. Upcoming meetings show shared, candidate and private badges with assign, unshare and reset controls, and direct members see an Account schedule. Not yet validated against a real Outlook/Meetings MCP on a Mac (#842, #934, #843).
- Customer brief: `POST`/`GET /api/accounts/{accountId}/customer-brief` request and read a brief for current direct members (others get 404). The Account overview shows a "one page before the meeting" card (next meeting, situation, since the last meeting, recent news, questions to ask, unfamiliar terms) with source chips; an unfamiliar term can be added to that customer's Account dictionary. Removing a member deletes their brief row (#915, #917, #924).
- Manual news collection: `POST /api/accounts/{accountId}/news-sources/{sourceId}/collect` lets a direct member request one run of a customer-only source at most once an hour, refused with 409 inside the daily window (03:25–04:25 KST) or while another collection runs. A `ttobak-news-manual` starter runs it as an ordinary versioned publication run over up to 20 candidate articles. Insights sources get a collect-now button and an Account without a news source gets a one-click start; the legacy crawler branch now refuses stray events with `MANUAL_NOT_SUPPORTED`. No deployed run has been recorded (#919, #921, #922, #926, #930, #941, #942).
- SFDC contact sync in Mac intake: `intake run` / `intake sfdc` reads SFDC contacts for each mapped Account, matches them on the Mac by existing SFDC link, unique email or unique name, and links or creates up to 200 per Account; emails and phones never leave the Mac. Account contacts show SFDC and "left SFDC" badges and Mac intake settings show an SFDC contacts row. Not yet run against the real SFDC MCP (#910, #918, #923).
- Calendar sync options: `ttobak-mcp intake options` and a sync-options block in Mac intake Settings choose which of your responses sync (default accepted, organizer and tentative; declined always excluded) and switch SFDC contacts on or off. With the default, events you have not answered are removed on the next complete run; a calendar read whose items all lack a response fails with `SCHEMA_CHANGED` and removes nothing (#920, #923).
- One-click Mac intake setup: a skill package API (`/api/skill-packages`; personal upload/delete for everyone, org upload, versions and rollback for admins; zips validated, at most 20 MiB, staged uploads expire after a day), a skill packages section in Settings, `ttobak-mcp setup plan|apply` (installs skills and missing MCP servers; servers new to that Mac need approval) and the Mac app's one-click setup button that runs PATH repair, adapter update, packages, skills, servers and intake setup in order (#931, #933, #935, #936, #937, #939).
- SA report: Slack channels you posted in over the last 90 days can be unchecked under the report dialog's advanced settings, and a report that still names an excluded channel is rejected with `REPORT_EXCLUDED_CHANNEL`. The report tool allowlist adds the read tools and two scripts used by `sa-performance-report` v1.5.0 (#932, #938).
- Recording auto-link: a recording started between 10 minutes before a calendar event and its end, where the event resolves to one Account, links to that Account and fills an empty title, with a chip to change it. It never overrides an Account chosen another way and is off for uploads. Upcoming meetings list customer meetings first and collapse internal and all-day events into one internal-events row (#927).
- Meetings list: your own unassigned meetings get a customer picker that links the meeting to a writable Account (classification only, no team sharing). On phones the picker wraps onto its own line, and a later list refresh no longer drops the new link (#986, #987, #996).
- SpecReq tasks that need an Account or owner offer a not-a-customer-meeting action, which closes the task (#951).
- Mac interpretation screen: the floating caption windows can be used from `/interpret`, which shows one large-text column per caption-window language; the draft-translation toggle is removed and the screen no longer calls `/api/translate` (#953, #955).
- Mac interpretation screen: pick one or two spoken languages (Korean, English, Chinese, Japanese; default Korean and English), remembered per browser. The effect on recognition accuracy is not measured (#958).
- Mac app 0.2.0: floating caption windows have rounded corners and a close button that closes that language's window and unchecks it in the caption panel (#954).

### Changed

- Custom dictionary: a recognized form written in Hangul (how a Korean speaker
  says `Claude Code`, for example) is now also a Transcribe vocabulary row that displays as the
  term, for batch transcription and live captions. It applies to terms added or
  edited by a person and takes effect after the vocabulary rebuilds (#990).
- Live captions and Mac interpretation rewrite a Hangul recognized form that
  Transcribe still spelled out to its dictionary term (for example to
  `plan mode`), using the same recognized forms as the vocabulary (#991).
- Custom dictionary: Korean pronunciations of Latin-named terms in your own and
  your accounts' dictionaries are now generated automatically and used for
  recognition and live captions, so they no longer have to be typed in.
  Controlled by `ttobak:spokenFormsMode` (on); existing terms are filled in
  gradually as meetings are processed and terms are saved (#992).

- Customer update drafts: the worker's run phases are switched on by setting the `ttobakCustomerUpdate` runtime ARN in `infra/cdk.json`; every 10 minutes it builds per-customer context (meeting-note excerpts included by default) and stores owner-private drafts. Nothing is sent automatically (#988).
- Validate private asset paths before signing uploads or downloads, and bind meeting audio and attachments to their owner and meeting while preserving authorized sharing (#963).
- Enable the project REA Claude plugin and permit standard PR merges while denying `--admin` merges; retain the existing review-size and secret-scan hooks (#962).
- The SA performance report accepts Claude Code 2.1.311 or newer (was 2.1.322); 2.1.311 has the same verified sandbox settings schema (#960).
- Mac report: Bedrock can no longer be saved or run without an AWS profile (the default credential chain used to hang until timeout), a Bedrock timeout reports `CLAUDE_CLI_BEDROCK`, the adapter Update shows whether anything changed, and the login PATH is repaired with `~/.toolbox/bin`, `~/.aim/mcp-servers` and `~/.local/bin` when they exist (#931).
- Insights news cards can show the search term that found an article on the versioned publication path (`matchedTerm`); the crawler-stack deploy and a daily run carrying the field are still to be confirmed (#928).
- The edit form for a customer-only news source states that changing its name, search terms or other names permanently hides its earlier articles (#929).
- The contact import file pickers on the Account page and in bulk import are styled as buttons (#950).

### Fixed

- Mac interpretation screen: the speaker who talks most is shown in the default
  text colour and the other speakers in colours, instead of colouring by the
  order the recognizer first heard each voice. The floating caption windows
  follow the same rule with their configured font colour (#993).
- Local intake: the Slack links connector failed with `PARSE_FAILED` against the real Slack MCP server, which puts a text notice before its JSON; tool results now skip a leading notice. Update the adapter (`~/.ttobak/server.mjs`) to use Slack link collection (#989).
- Customer update sources: concurrent adds can no longer exceed the 30-source cap; the cap is enforced atomically with each add and delete, and colliding concurrent writes are retried before returning 503 `SOURCES_BUSY` (#983).
- Mac interpretation: reloading or leaving the page stops the capture instead of
  leaving the app listening, and a failed live-caption configuration load is retried
  by the next start instead of disabling the screen until a reload. A failed
  `/config.json` fetch is no longer cached for the page lifetime (#965).
- Mac app: going to sleep ends live interpretation even when its start is still
  in flight, and the stop is no longer dropped when it outlasts the sleep
  deadline. The notice after a recording start ended interpretation no longer
  claims the recording started (#969).
- Daily news: `ttobak-news-daily` no longer ends `FAILED` every day because child receipts list rejected articles; a source fails only on run faults, and a run that rejects every article for one systemic reason succeeds. Not yet run against AWS (#948).
- Customer-only news sources no longer feed the shared Common dictionary or count toward its source threshold. Common terms created by `system:news` before this change need a manual operator review; nothing was deleted (#925).
- Infrastructure deploys after #930 no longer stop at `TtobakAiStack`: the crawler role export that the deployed crawler stack imports is kept (#943).
- Customer update agent: Sonnet 5.5 writer replies stay under the 1,500-token output cap (`thinking: between_tools`); a reply that still hits the cap gets the one rewrite instead of discarding the pair (#995).
- Mac app: native meeting detection starts only while a meeting app captures input, so a notification or reminder sound no longer shows a false "Zoom meeting detected" (#945).
- Mac app 0.2.1: native capture and interpretation receive audio on a quiet Mac; the aggregate device no longer waits for a tapped process to start playing (#956).
- Mac app 0.2.3: report, intake and one-click setup events go only to the main window, not to the caption windows (#981).
- Web: the document no longer scrolls behind the app shell after scrolling past the end of the sidebar. Not checked inside the Mac app's WebView (#952).

## [1.0.0] - 2026-11-11

First tagged application release. It contains all earlier history; the entries below
summarize the main user-visible changes merged from 2026-11-03 to 2026-11-11 and the
previously unreleased log. Mac app binaries keep separate `mac-v*` releases.

### Added

- Versioned releases: `vX.Y.Z` tags, this changelog's release sections and the in-app
  `/changelog` page, reached from a version link at the bottom of the sidebar and the
  mobile menu sheet that marks a release the browser has not opened yet. Notes live
  in `frontend/src/lib/releaseNotes.ts`; tests keep them aligned with this file, the
  frontend package version and served routes (#903).
- Separate Home and Meetings workspaces (#640); customer and project links while
  recording (#648); internal meeting categories with a list filter (#724).
- English meeting notes with a Korean/English toggle on the AI summary (#802, #807,
  #808, #809).
- Owner-only SA Activity page with scorecard, missed SFDC logging and mapping queue
  (#817), saved SA reports and Mac-app report generation (#882, #883), Mac local intake
  settings and collection (#860, #862).
- Account overview AWS signals, per-opportunity flow and person/project focus graph
  (#718, #719, #877); account people panel and speaker binding (#744); account merge
  (#835, #837) and owner transfer (#819).
- Remember/CSV/Excel contact import with account matching (#677, #858).
- SpecReq task strip on Home and Meetings (#775).
- Fixed SFDC-aligned project stages with history (#708).
- Self-service and admin display-name editing (#880).
- In-app product manual (#811).
- Mac app: caption overlay windows (#788, #803), meeting detection recording prompt
  (#679, #722), Zoom/Meet mute following and mid-recording microphone switch (#621,
  #667, #725), in-app update checks (#764).
- Billing Insight single-use hand-off button: `POST /api/billing-insight/handoff`
  stores a 60-second hashed code and returns the AWS Billing Insight companion
  redemption URL; gated by `ttobak:billingInsightUrl` and runtime `billingInsight`.
- News source management in the Insights news tab: register common or customer-only
  sources, edit customer-only sources and filter news by scope, backed by
  `GET /api/news-sources`, `PUT /api/accounts/{accountId}/news-sources/{sourceId}` and
  `kind`/`account` filters on `GET /api/insights` (#895, #899).
- `POST /api/accounts/{accountId}/contacts/sfdc-sync` for the Mac intake SFDC contact
  sync (#901); customer brief worker, consumer side only with no route or UI yet
  (#908); Korean financial regulator news as operator-seeded global sources (#907).

### Changed

- Account hierarchy tree in Accounts (#852); news origin labels and common/customer
  grouping, with customer-source news limited to direct members (#886, #891);
  recording card layout menu (#804); full-width workspace layouts with mobile
  navigation clearance (#889); meetings list first with a schedule sidebar and
  alphabetical account groups (#893).
- Run the first Whisper worker on On-Demand capacity and activate STT recovery for
  stranded single-file audio (#888).
- Dictionary terms reach more of the pipeline: the owner's READY vocabulary on live
  caption streams (#896), dictionary spellings in high-quality live translation
  (#898), wider term collection and re-extraction of earlier meetings (#897, #902),
  account-term cleanup on meeting relink (#905), and glossary transcript correction,
  the account terms layer and AWS technical document terms turned on (#900, #911,
  #912, #914). Recognition gain is not yet measured.
- The deploy coordinator retries throttled idempotent reads (#913).

### Fixed

- Seek meeting audio from summary timestamps (#598, #610); fall back to the default
  microphone when the saved device is gone (#885); confirm/alert dialogs in the Mac
  app (#619); let Mac caption windows enter other apps' full-screen Spaces (#887, not
  yet observed over a real full-screen slideshow).
- News discovery honors the connector date filter and accepts grounded Korean
  restatements (#906; publication after deployment not yet verified); SFDC departures
  are checked against every present list of a complete snapshot (#909); the Mac
  report dialog names `ADAPTER_OUTDATED` with an update hint (#904).

### Documentation and review

- Consolidate current project guidance, generate one shared review extract and
  embed it in isolated Kiro CI prompts.
- Convert project documentation to concise English; separate current references
  from historical decisions, implementation plans, audits and benchmarks.
- Correct review markers/latest-HEAD completion guidance, Go/Python payload scope,
  test/build instructions, unsafe deployment examples and stale feature claims.
- Add documentation integrity and local review-prompt delivery checks.

## Historical entries retained from the previous Unreleased log

No release/version boundary was recorded for these entries. Do not infer a release
date or that all listed deployment changes are still configured.

### Added

- Editable research titles distinct from immutable prompts; research Markdown/Notion
  export, tab URL synchronization and follow-up research features.
- Admin user lifecycle panel and fail-open last-login tracking (ADR-007).
- Meeting-driven Code Interpreter sizing simulator (ADR-010).
- Pending email shares for invited users, materialized after verified login.
- Mermaid zoom/pan and fullscreen display.
- Multi-model PR review, historically introduced with earlier Opus aliases.
- GPU Spot Whisper with zero scaling, S3 model loading and vocabulary prompt hints.
- AgentCore FastAPI research container, cross-meeting QA and KB recording panel.
- STT benchmark tooling, custom vocabulary, domain allowlist, crawler pipeline and
  browser tab-audio capture.

### Changed

- Split deployment/test/review workflows and move runners to ARC with setup actions.
- Evolve research/summarization model profiles and CUDA/Python image versions;
  inspect present workflow/Dockerfile values before treating an old version as pinned.
- Add Whisper-primary batch processing alongside AWS Transcribe fallback; earlier
  Nova Sonic support remained experimental, not a complete production alternative.
- Load Cognito config from runtime config.json and repair research SPA routing.

### Fixed

- Add mobile waveform/caption manual gesture recovery in addition to automatic
  wake-lock/resume/reconnect watchdog paths; preserve synchronous user activation.
- Repair recovery checkpoint lookup across webm/m4a and preserve exact stable
  checkpoint keys by bypassing timestamp prefixing for allowlisted filenames.
- Synchronize Mermaid/Shiki/table rendering with light/dark theme changes.
- Repair research route rewriting, research read timeouts, non-root AWS CLI installs,
  reserved log-group naming and AgentCore endpoint IAM resources.
- Repair stale frontend chunk/HTML deployment, auth checks, JSON parsing and XML safety.

### Security-related history

- Add PR review and credential-scan hooks, API JWT validation and KMS-backed
  integration-key handling. These additions do not certify complete compliance.
- Add crawler paywall/body-quality filtering. Filtering is not access authorization.
- Public document links later introduced an explicit, narrow unauthenticated route;
  old claims that every API route requires login are superseded by ADR-020.
