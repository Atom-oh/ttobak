# Personalized Assistant Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan inline. Preserve unrelated working-tree edits.

**Status:** Historical implementation record, completed and locally verified on 2026-11-11. This is not evidence of deployment or live-model acceptance.

**Goal:** Personalize Assistant cards and answer questions spoken during a meeting automatically.

**Architecture:** Build bounded suggestions from authenticated recent meeting metadata. Detect spoken questions independently of summary thresholds, using the meeting title, background and latest transcript. Automatic answers use the existing asynchronous QA transport with enforced read-only tools and external search opt-in.

**Tech Stack:** Existing Next.js/React/TypeScript and Python QA Lambda; no new dependencies or infrastructure.

**Spec:** The user's requests in this session: personalize AI Assistant cards; tailor the in-meeting Assistant to the meeting and automatically answer questions appearing in its content.

## Constraints and decisions

- Automatic answers default on for an authenticated user, with a separate per-user off preference.
- External web search remains a separate per-user opt-in, default off. Automatic answers cannot create research or other mutations.
- Only explicitly spoken, answerable questions qualify for automatic answers. Suggested follow-ups remain clickable.
- Keep one automatic answer per detection generation, shared claims across desktop/mobile panels, and bounded retries.
- Detect at most once every 10 seconds, coalesce final transcript updates in a 1.5-second window, and retain the latest input while detection is running. Abort a stalled detection after 30 seconds and cancel it on stop/reset.
- Bound metadata, request text, question lists and model output. Treat meeting content as data.
- Preserve current-source validation, notes, recording behavior and existing API/WS manual answers.
- Show Q&A by default when entering the recording workspace; retain visible-panel guards for automatic requests.
- Preserve unrelated working-tree edits. The user's follow-up requests PR publication; merge and deployment are outside this change.

## Review focus

- Short spoken questions must trigger detection before the summary threshold.
- No automatic answer to an invented suggestion, resolved question, opinion or decision request.
- Reset/unmount and user changes must reject stale detection/metadata results.
- Default automatic answering must not send queries to external search or execute mutations.
- Desktop/mobile instances must not submit the same automatic question twice.

## Tasks

1. [x] Add executable tests for personalized cards, detection scheduling/reset, per-user automatic preference and backend question/policy enforcement; observe failing tests.
2. [x] Implement personalized cards, meeting-aware suggestions and independent detection; wire title/background to both panel instances.
3. [x] Implement automatic answer preference and read-only HTTP QA policy with external search opt-in.
4. [x] Run frontend lint/unit/build and QA tests; review changes and record validation.

## Validation record

- Frontend: `npm run lint` exited successfully with existing warnings; `npm run test:unit` passed all 135 tests; `npm run build` passed, including the bundled-icon check and TypeScript/static export.
- QA: `python3 -m unittest test_handler test_live_assistant test_async_jobs test_async_runtime test_tool_context -v` passed all 451 tests.
- Headless Chromium with synthetic authenticated metadata, speech and QA responses verified personalized cards, mobile dark layout, a spoken question triggering one automatic HTTP job before any summary request, external search disabled, an independently working off switch, and zero page/React errors.
- Added regressions for strict spoken/answerable flags, legacy model output, read-only tool dispatch, history-policy isolation, both HTTP transports, stalled detection cancellation, and ARIA roles being mistaken for bundled icons.
- Reviewed inline without starting subagents: this host does not expose the `close_agent` lifecycle tool required by the user's instructions.
- No new dependency, route or AWS resource. Deploy the QA Lambda before the frontend; the updated detector's `automatic` field is required to arm automatic answers. Actual model semantics and production speech capture still require runtime verification.

## PR validation

- Ported the scoped changes onto remote `main` at `621cc99fbfbe`, retaining current caption integration, viewport layout, icon discovery exclusions and model configuration. The original user workspace was preserved.
- Preserved Markdown code-fence parsing and the detector's omitted temperature setting, with regression coverage.
- Latest-base frontend lint passed with existing warnings and all 786 unit tests passed. The QA suite above passed all 494 tests. Production build and synthetic browser verification passed on the latest base.
- PR publication does not merge or deploy this change.
