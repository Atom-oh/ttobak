# Public user guide

The user explicitly requested this Korean public-facing guide on 2026-09-17.
The language exception is limited to the HTML guide; repository engineering
references remain English. Examples are fictional and contain no meeting data.

The user requested the expanded enterprise product guide on 2026-11-10. The
public Korean language exception remains limited to the product HTML guide.
Source inspection used main revision `caba6da9` (UI walkthrough initially used `fec64b30`); source configuration is not
proof of production activation or acceptance.

The standalone site has 28 chapters, full body/keyword search, keyboard contents,
current-chapter links, mobile navigation, light/dark themes, copyable question
examples and complete print/PDF output. No JavaScript is required to read the
manual or follow its anchors. Search leaves all printable chapters intact.

Preview from the repository root:

```bash
python3 -m http.server 8080 --bind 127.0.0.1 --directory docs/user-guide
```

Only files explicitly named in `scripts/docs/build_user_guide.py` enter the
public artifact: HTML, CSS, JavaScript, a local SVG favicon and eight reviewed
SVG menu illustrations. README, plans, source inventories and review reports are never
copied. A local-only CSP disables API connections and remote scripts/assets.
The illustrations are simplified, text-reviewable menu diagrams derived from
local UI inspection. Captions explicitly distinguish them from screenshots.
Local raster walkthrough captures remain internal evidence. No production
account, customer records, credentials or private meeting data were accessed.

The repository Pages API reports `https://www.atomai.click/ttobak/` as its publication URL (verified 2026-11-10).

The Pages workflow validates local assets and anchors, then copies only the
explicit public asset allowlist into its artifact. It never publishes the repository's internal
reference archive. Enable GitHub Pages with the Actions build source. Main-branch
pushes deploy automatically after merge, including unrelated changes so that
superseded builds always have a replacement run;
pull requests validate without deployment permissions. Manual deployment is
restricted to main. No application API, credentials, or AWS deployment is used.
The complete workflow uses GitHub's FIFO `queue: max` concurrency mode, so an
older run cannot replace the newest pending run. A main-SHA check in the build
job skips superseded builds and old manual reruns before the deployment job
enters the Pages environment. Official actions are pinned to immutable commits.
Every deployment attempt also rechecks main, including failed-job-only reruns
that reuse old build outputs. A stale deployment retry fails before publication.
If main advances after that check, the active deployment may briefly publish its
reviewed revision; the newer queued run follows. This is sequential publication,
not an atomic transaction with future pushes.
GitHub allows up to 100 waiting runs; after a queue overflow or failed build,
dispatch a fresh workflow on main to publish the current guide.
See GitHub's [workflow concurrency documentation](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency).
Verified on 2026-09-17: the official documentation includes `queue: max`, its
100-run capacity, and its incompatibility with `cancel-in-progress: true`.
[PR validation run 35199025849](https://github.com/Atom-oh/ttobak/actions/runs/35199025849)
accepted this concurrency mapping and passed build and publication-boundary tests
at revision `3209055132b01fa1fba57d4ed84f5988a930c0dc`. Offline recollections that
concurrency only supports `group` and `cancel-in-progress` predate this feature.

Verify content against these sources when updating:

- `frontend/src/components/layout/Sidebar.tsx` and `frontend/src/app/`
- `frontend/src/components/meeting/SimCard.tsx`, `backend/internal/service/sim.go`,
  and `backend/python/sim/pricing.py`
- `frontend/src/components/record/`, `frontend/src/components/AudioUploader.tsx`
- `frontend/src/components/ShareButton.tsx`, `frontend/src/components/DocDetailClient.tsx`,
  `frontend/src/components/AccountsClient.tsx`, `frontend/src/components/ProjectDetailClient.tsx`, and `frontend/src/components/ResearchChat.tsx`

Do not imply universal pricing coverage, arbitrary manual requirement entry,
editor access to owner-only simulation, automatic sharing through classification,
or that upload/preview/indexing alone proves AI source acceptance.

## Verified menu and behavior inventory (2026-11-10)

The primary labels/URLs come from `frontend/src/lib/navigation.ts`; conditional
Billing entries come from `frontend/src/components/layout/BillingInsightButton.tsx` and runtime configuration.
Submenus were cross-checked against their components, not inferred from names.

| Guide chapter | UI / flow covered | Consequential implementation evidence |
|---|---|---|
| 1. Start | Invite, first login, object boundaries | `frontend/src/components/auth/AuthProvider.tsx`; `infra/lib/auth-stack.ts` |
| 2. Home | Recent records, calendar, account selection, independent retry | `frontend/src/components/workspace/HomeDashboard.tsx` |
| 3. Chats | Assistant, meeting QA, live QA, research chat, external MCP | `frontend/src/app/chat/page.tsx`; `frontend/src/components/QAPanel.tsx`; `frontend/src/components/LiveQAPanel.tsx`; `frontend/src/components/ResearchChat.tsx`; `backend/python/qa/handler.py` |
| 4. Models | Source-configured defaults, STT vs notes, retrieval, stale-source checks | `infra/lib/model-config.ts`; `infra/lib/gateway-stack.ts`; `backend/internal/service/bedrock.go`; `backend/python/research-agent/agent.py`; `backend/python/sim/handler.py` |
| 5. Recording | Preparation, live captions, notes, opt-in, final processing, backup | `frontend/src/app/record/`; `frontend/src/components/record/`; `frontend/src/hooks/useRecordingSession.ts` |
| 6. Upload | Immediate audio-only new meeting; existing meeting queue/completion/500MB check; extraction/preview, crop | `frontend/src/components/AudioUploader.tsx`; `frontend/src/lib/upload.ts`; `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx` |
| 7. Meetings | Listing, summary/notes, speakers, saved-source resummary, translation | `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`; `frontend/src/components/meeting/MeetingNotesEditor.tsx`; `frontend/src/components/meeting/MeetingTranslation.tsx`; `backend/internal/service/resummary.go`; `backend/internal/service/meeting_translation.go` |
| 8. Calendar | Personal Outlook intake, source status, stable identities, cancellation | `frontend/src/components/workspace/UpcomingMeetings.tsx`; `backend/internal/service/calendar.go`; `docs/runbooks/local-outlook-calendar-sync.md`; `docs/runbooks/local-mcp-intake.md`; `mcp-server/src/intake/` |
| 9. Follow-up | Meeting preparation, action item run state, human checkbox retention | `frontend/src/hooks/useActionItems.ts`; `backend/internal/service/action_analysis.go`; `backend/internal/service/action_analysis_events.go`; `docs/features/sa-meeting-workflow.md` |
| 10. Simulation | Owner controls, confirmed numeric inputs, estimates, limited sandbox | `frontend/src/components/meeting/SimCard.tsx`; `backend/internal/service/sim.go`; `backend/python/sim/handler.py`; `backend/python/sim/pricing.py` |
| 11. Accounts | Creation, initial owner, members, hierarchy, every tab, events | `frontend/src/components/AccountsClient.tsx`; `frontend/src/components/AccountDetailClient.tsx`; `frontend/src/components/AccountEvents.tsx`; `frontend/src/lib/accountTabs.ts`; `frontend/src/lib/accountAccess.ts`; `backend/internal/service/account.go` |
| 12. Contacts | Individual contacts, CSV/XLSX mapping, assignment, duplicates, row results | `frontend/src/components/AccountContacts.tsx`; `frontend/src/components/ContactImport.tsx`; `frontend/src/components/BulkContactImport.tsx`; `frontend/src/lib/contactFile.ts`; `frontend/src/lib/contactExcel.ts`; `backend/internal/service/account_contacts.go` |
| 13. People | Confirmed vs unresolved mentions, manual resolution, signals and flow | `frontend/src/components/account/AccountPeople.tsx`; `frontend/src/components/account/AccountSignals.tsx`; `frontend/src/components/account/AccountFlow.tsx`; `backend/internal/service/account_people_resolve.go`; `docs/decisions/ADR-065-account-people-edges.md` |
| 14. Projects | Stage, direct members, linked accounts, meetings, research, delete guard | `frontend/src/components/ProjectDetailClient.tsx`; `backend/internal/service/project.go`; `backend/internal/service/project_read_access.go` |
| 15. Sharing | Meeting roles, classification vs sharing, account copy vs email read, public links | `frontend/src/components/ShareButton.tsx`; `frontend/src/components/DocDetailClient.tsx`; `backend/internal/service/account.go`; `docs/decisions/ADR-022-slide-preview-conversion-and-public-share-links.md` |
| 16. Docs | Notes, slides, save state, previews, account copies | `frontend/src/components/DocsClient.tsx`; `frontend/src/components/DocDetailClient.tsx`; `backend/internal/service/account.go`; `backend/cmd/convert-doc/` |
| 17. Library | Direct upload vs meeting attachments, extraction, indexing, current source | `frontend/src/app/kb/page.tsx`; `frontend/src/components/KBFileList.tsx`; `frontend/src/components/MeetingAttachmentList.tsx`; `frontend/src/components/IndexStatus.tsx`; `backend/internal/service/attachment_text_document.go`; `backend/python/qa/SOURCE_CONTRACT.md` |
| 18. Research | Quick/Standard/Deep, plan approval, reporting, all-mode follow-up, exports | `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx`; `frontend/src/components/ResearchChat.tsx`; `backend/python/research-agent/agent.py` |
| 19. News | Filters, collected articles, sources, summary vs original | `frontend/src/app/insights/`; `frontend/src/components/CrawlerSettings.tsx`; `backend/python/crawler/news_crawler.py` |
| 20. Dictionary | Personal/account/common, display/heard forms, suggestions, history, build state | `frontend/src/components/CustomDictionary.tsx`; `frontend/src/components/DictionaryLayerTabs.tsx`; `frontend/src/lib/dictionary.ts`; `backend/internal/service/dictionary_layers.go` |
| 21. Billing | Runtime-gated link/handoff, dataset boundaries, account tab | `frontend/src/components/layout/BillingInsightButton.tsx`; `frontend/src/lib/billingInsight.ts`; `frontend/src/lib/billingApi.ts`; `frontend/src/components/AccountBillingTab.tsx`; `services/billing/page/` |
| 22. Settings | Profile/theme, Notion, import, dictionary, Mac-only detection, crawler, connectors, MCP | `frontend/src/app/settings/page.tsx`; `frontend/src/components/IntegrationSettings.tsx`; `frontend/src/components/MeetingDetectionSettings.tsx`; `frontend/src/components/ConnectorSettings.tsx`; `frontend/src/components/McpGuide.tsx` |
| 23. Admin | Invite, disable/delete boundaries, no automatic data erasure, role separation | `backend/internal/handler/user_admin.go`; `backend/internal/service/user_admin.go`; `docs/decisions/ADR-032-admin-user-management-panel.md` |
| 24. Devices | Web/mobile/Mac, profile menu, leftover WAV account caveat | `frontend/src/app/profile/page.tsx`; `frontend/src/lib/tauri.ts`; `mac-app/README.md`; `mac-app/src-tauri/src/leftover.rs` |
| 25. Security | Current-source authorization, external search, public link, issued JWT limits | `backend/python/qa/SOURCE_CONTRACT.md`; `backend/internal/service/summary_privacy.go`; `backend/internal/service/account.go`; `CLAUDE.md` |
| 26–28. Operations | Team workflows, failure recovery, terms, version/activation limits | Cross-checks against the flows above; no invented deployment or compliance claims |

Decision filenames record intent. Runtime statements were checked against
implementation, not inferred from those decisions alone.

## Model mapping evidence

- Final meeting notes: `global.openai.gpt-6.1-sol` by default; the preset/context/
  environment precedence in `model-config.ts` may select a different supported
  model. The manual does not promise a universal fixed model.
- The paginated notes/transcript translation flow: `global.openai.gpt-6-luna`.
  This describes `MeetingTranslation` / `MeetingTranslationService`; it is not a
  claim that every older or unrelated translation path uses that model. A page
  that fails the structure-preservation check can fall back to Amazon Translate.
- Go refinement and auxiliary steps: Sonnet 5.5 / Opus 5.5; lightweight steps use Haiku 5.5 configuration.
- QA/Assistant: Sonnet 5.5, with Haiku 5.5 detection configuration.
- Research: Opus 5.5 defaults for all three modes and chat; modes change workflow
  depth, not necessarily the model family.
- Images: Opus 4.8. Simulation and news summaries: Sonnet 5.5 configuration.
- Live STT and final batch transcription remain separate from note generation.
- External MCP clients choose their own conversation model.

## Walkthrough and validation evidence

A local Next dev process used the existing `NEXT_PUBLIC_DEV_AUTH` seam, a
synthetic local token and intercepted fictional API responses. All non-loopback
HTTP requests and external WebSocket construction were blocked. This was not a
production login, not an authentication test and not a live connector acceptance
test. Screens were captured with Noto Sans CJK KR for legible system typography.

The browser walkthrough opened 19 primary/detail routes and seven additional
Account tabs (26 routes total), including the conditionally enabled local Billing
entry. Its generated Billing bundle used only checked-in page sources and local
vendor packages. Empty costs and unavailable conditional data are not a claim
that a production dataset was connected. Mac-only controls were source-inspected;
no macOS capture or Outlook provider acceptance was performed on this Linux host.

Publication-boundary tests cover the reviewed illustrations, rejection of an
unreviewed image even if present in the source directory, symlink rejection, CSP,
remote/path references, missing/duplicate anchors and preservation of existing
output directories. Browser checks cover 1920/3840 desktop, tablet, 390/360 mobile,
body search, empty results, inert search input text, keyboard shortcut/drawer,
active chapter, theme persistence, no-JavaScript reading and full A4 printing, including no-JavaScript readers with a dark OS preference.

Re-run repository checks:

```bash
python3 scripts/docs/build_user_guide.py --output /tmp/ttobak-guide-review-new
python3 -m unittest discover -s scripts/docs -p 'test_*.py' -v
python3 scripts/docs/check_docs.py
node --check docs/user-guide/guide.js
```

Use a new output directory: the packager deliberately refuses overwriting.
The content rubric review is recorded in
`results/TTOBAK_User_Guide_Review_Report.md` and excluded from Pages publication.
