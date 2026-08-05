# Insights Readability Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Insights, Account, Project, and Research workspaces use the available width and render AI insights as readable, structured, attributable content.

**Architecture:** Extend the existing meeting insight JSON additively, keeping `text` as the required backwards-compatible headline while adding optional evidence, implication, and next-action fields. Reuse one frontend `FieldInsightsSection` for Account and Project, and keep crawler storage compatible while requiring summaries to contain stable Markdown sections.

**Tech Stack:** Go, AWS Bedrock, Python unittest, Next.js 16, React 19, TypeScript, Tailwind CSS v4

---

### Task 1: Structured Meeting Insight Contract

**Files:**
- Modify: `backend/internal/model/account.go`
- Modify: `backend/internal/service/bedrock.go`
- Test: `backend/internal/service/bedrock_test.go`

- [ ] **Step 1: Write a failing parser test**

Add a structured insight fixture containing `evidence`, `implication`, and `nextAction`, then assert all fields survive `parseMeetingInsights`.

- [ ] **Step 2: Run the focused test**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service -run TestParseMeetingInsights -count=1`

Expected: FAIL because the optional fields do not exist on `model.MeetingInsight`.

- [ ] **Step 3: Extend the additive model and prompt**

Add optional JSON fields to `MeetingInsight` and update `ExtractInsights` to request:

```json
{
  "type": "risk",
  "text": "PoC 일정 지연 가능",
  "evidence": "보안 검토 일정이 아직 확정되지 않음",
  "implication": "목표 오픈 일정에 영향 가능",
  "nextAction": "보안 담당자와 검토 일정을 확정",
  "entities": ["PoC"]
}
```

Keep `text` mandatory so existing one-line records remain valid.

- [ ] **Step 4: Re-run the focused test**

Expected: PASS.

### Task 2: Preserve Structured Fields Through Account and Project Reads

**Files:**
- Modify: `backend/internal/model/account.go`
- Modify: `backend/internal/model/project.go`
- Modify: `backend/internal/service/meeting.go`
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/project.go`
- Test: `backend/internal/service/meeting_test.go`
- Test: `backend/internal/service/account_test.go`
- Test: `backend/internal/service/project_test.go`

- [ ] **Step 1: Add failing propagation assertions**

Assert the three optional fields survive meeting-to-account fan-out, account DTO mapping, and project aggregation.

- [ ] **Step 2: Run the focused service tests**

Run: `cd backend && /usr/local/go/bin/go test ./internal/service -run 'TestBuildAccountInsights|TestListAccountInsights|TestGetProjectInsights' -count=1`

Expected: FAIL until every copy path includes the fields.

- [ ] **Step 3: Add fields to persisted and response models**

Add `Evidence`, `Implication`, and `NextAction` with `omitempty` tags to account persistence and both account/project DTOs, then copy each field explicitly in all three service paths.

- [ ] **Step 4: Re-run focused tests**

Expected: PASS.

### Task 3: Shared Field Insights UI

**Files:**
- Create: `frontend/src/components/FieldInsightsSection.tsx`
- Modify: `frontend/src/types/meeting.ts`
- Modify: `frontend/src/components/AccountDetailClient.tsx`
- Modify: `frontend/src/components/ProjectDetailClient.tsx`

- [ ] **Step 1: Extend frontend insight types**

Add optional `evidence`, `implication`, and `nextAction` properties to Account and Project insights.

- [ ] **Step 2: Build the shared section**

Implement localized labels, semantic icons/colors, per-type counts, responsive horizontal filters, readable cards, entity chips, and a source link to `/meeting/{sourceId}`. Hide absent structured blocks so legacy `text`-only records render cleanly.

- [ ] **Step 3: Replace duplicated account/project markup**

Use `FieldInsightsSection` in both clients and pass the Project fetch error as a visible section error.

- [ ] **Step 4: Widen workspaces**

Change Account and Project workspace caps from `max-w-4xl` to centered `max-w-7xl`.

### Task 4: Insights and Research Layout

**Files:**
- Modify: `frontend/src/app/insights/page.tsx`
- Modify: `frontend/src/components/InsightsList.tsx`
- Modify: `frontend/src/components/InsightsTableView.tsx`
- Modify: `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx`
- Modify: `frontend/src/components/ResearchChat.tsx`

- [ ] **Step 1: Widen the list workspace**

Use a centered `max-w-7xl` container and make card/table content wrap or scroll without clipping.

- [ ] **Step 2: Improve icon control accessibility**

Add `aria-label`, pressed state where applicable, and consistent `focus-visible` rings to view, delete, back, chat, and close controls.

- [ ] **Step 3: Fix research columns**

Use one 400px desktop chat width, cap the report reading column around `76ch`, and render the TOC only when chat is closed.

- [ ] **Step 4: Verify responsive TypeScript output**

Run: `cd frontend && npx tsc --noEmit`

Expected: PASS.

### Task 5: Structured Crawler Briefings

**Files:**
- Modify: `backend/python/crawler/news_crawler.py`
- Modify: `backend/python/crawler/tech_crawler.py`
- Test: `backend/python/crawler/test_crawlers.py`

- [ ] **Step 1: Add prompt contract tests**

Assert both summarization prompts request Markdown sections for 핵심 요약, 왜 중요한가/비즈니스 시사점, and SA 다음 액션/AWS 적용 포인트.

- [ ] **Step 2: Update prompt schemas**

Keep the stored `summary` field for compatibility but require a multiline Markdown briefing value rather than one compressed paragraph.

- [ ] **Step 3: Run crawler tests**

Run: `cd backend/python/crawler && python3 -m unittest test_crawlers -v`

Expected: PASS.

### Task 6: Verification

**Files:**
- Inspect: all modified files

- [ ] **Step 1: Format backend code**

Run: `cd backend && /usr/local/go/bin/gofmt -w internal/model/account.go internal/model/project.go internal/service/bedrock.go internal/service/meeting.go internal/service/account.go internal/service/project.go internal/service/*_test.go`

- [ ] **Step 2: Run backend tests**

Run: `cd backend && /usr/local/go/bin/go test ./internal/...`

Expected: PASS.

- [ ] **Step 3: Run frontend checks**

Run: `cd frontend && npx tsc --noEmit`

Run targeted ESLint on the touched frontend files and distinguish pre-existing failures from regressions.

- [ ] **Step 4: Build and visually inspect**

Run `npm run build`; if the sandbox blocks font downloads, report that exact blocker. Attempt desktop/mobile browser screenshots when Chromium and local port binding are available.

- [ ] **Step 5: Review the diff**

Run `git diff --check` and inspect only task-related hunks, preserving all pre-existing user changes.
