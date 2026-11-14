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

- User guide: a Mac app tab covering install and updates, macOS permissions, System audio recording, meeting detection and mute following, caption windows and interpretation, local intake, and troubleshooting; the Mac app opens the guide on that tab.
- Drag and drop a Remember CSV or .xlsx export onto the contact import fields on the Account page and in Settings, in the browser and the Mac app; the Mac app's other upload drop zones now receive drops too.
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
  transcript or upload is created. Needs a Mac app build with the
  `start_listening` command (#949).
- Speaker colours on the Mac app's interpret screen: each caption line is coloured by who spoke, with a speaker tag where the speaker changes. The floating caption windows show the same speaker colours from Mac app 0.2.2.
- Customer update collection: an hourly feed collector and a 10-minute fan-out worker
  deliver new links from followed RSS, site and YouTube sources into each
  subscriber's inbox (#970).
- Customer update agent runtime: an AgentCore container that reads followed links, decides per customer whether an update is worth a draft, and writes sealed run results; not invoked by anything yet (#972, #973, #974, #975).
- Billing ingest writes a per-account `services_in_use` object (service names only, no amounts) and the backend gains Go draft checks that match the agent's shared contract; nothing reads either yet (#976).

### Changed

- Validate private asset paths before signing uploads or downloads, and bind meeting audio and attachments to their owner and meeting while preserving authorized sharing.
- Enable the project REA Claude plugin and permit standard PR merges while denying `--admin` merges; retain the existing review-size and secret-scan hooks.
- The SA performance report accepts Claude Code 2.1.311 or newer (was 2.1.322); 2.1.311 has the same verified sandbox settings schema.

### Fixed

- Mac interpretation: reloading or leaving the page stops the capture instead of
  leaving the app listening, and a failed live-caption configuration load is retried
  by the next start instead of disabling the screen until a reload. A failed
  `/config.json` fetch is no longer cached for the page lifetime.
- Mac app: going to sleep ends live interpretation even when its start is still
  in flight, and the stop is no longer dropped when it outlasts the sleep
  deadline. The notice after a recording start ended interpretation no longer
  claims the recording started.

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
