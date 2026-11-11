# Workspace design review — 2026-11-11

## Finding and outcome

The main usability issue was inconsistent use of the window. Workspace pages
used several unrelated width caps and 32–64px gutters, so large windows added
blank margins instead of useful working space. Mobile Assistant also placed its
composer beneath the fixed navigation.

The implementation gives working pages full width, shares 16px mobile / 24px
desktop gutters, and reserves the mobile navigation's height and safe area in the
shell. Project cards add columns automatically. Settings sections use independent
columns, with the administration table spanning them. Long document/news/research
prose retains a 76ch reading width; Assistant turns retain a 90ch content region.
The desktop sidebar remains 256px wide, as in the reference.

Visual inspection also found light text on white cards in dark mode. Registering
the legacy `glass-panel` surface as a Tailwind utility now generates its `dark:`
variant. Actual project/document card text has a measured 15.4:1 contrast ratio
against the restored dark surface; this is not a whole-product contrast audit.

Reference: the local `claude-code-usage-dashboard` repository, specifically
`dashboard/web/src/App.jsx`, `components/Sidebar.jsx`, and `index.css`. Its
window-filling main column and separate scrolling navigation informed these
changes. TTOBAK keeps its own brand and existing menu structure.

## Measurements

These are Chromium CSS-pixel measurements with fictional local data. At a
3840px window, the main area beside the sidebar is 3584px.

| Surface | Before | After | Effect |
|---|---|---|---|
| Projects, 1440px window | 768px list; two columns | 1136px usable list; three columns | Less wasted horizontal space and fewer rows |
| Projects, 3840px window | 768px list | 3536px usable list | 98.7% of the main width is available for cards |
| Home, 3840px window | 1216px usable content inside a 1280px wrapper | 3536px usable content | Dashboard panels grow with the window |
| Settings desktop gutter | 64px | 24px | More room for settings, with aligned page/header edges |
| Assistant, 390px window | Input at y=786–828, overlaid by navigation | Input at y=722–764; navigation space reserved | Composer remains visible and focusable |
| Short desktop sidebar | Menu competes with the fixed action area | Scrollable menu and nonshrinking actions | Guide and recording actions remain reachable |

## Menu review

| Menu / surface | Design decision |
|---|---|
| Home | Full-width dashboard; retain separate schedule and customer-context panels |
| Assistant | Full-width working area and composer; bounded conversation text; accessible names for icon-only actions |
| Meetings | Full-width filters and loaded rows; preserve search and continuation state |
| Accounts | Consistent gutter around the customer selector and detail workspace; preserve account tabs |
| Projects / project detail | Adaptive cards and full-width detail; preserve member and relationship controls |
| Docs / document detail | Full-width list and title/actions; wrapping list toolbar; bounded prose editor |
| Library | Remove outer width cap and excessive gutter; preserve upload/source tabs |
| Deep Research / research detail | Full-width workspace and metadata; keep prose readable inside it |
| News / article detail | Full-width list and detail header; preserve the article reading width and outline |
| Custom Dictionary | Consistent full-width wrapper; preserve vocabulary controls and language sections |
| SA Activity | Full-width report/dashboard area; keep both tabs mounted and private-state behavior |
| Settings | Adaptive section columns; wide administration table; existing anchors retained |
| Recording | Shared gutter and usable height; retain recording, upload, recovery, and panel logic |
| Mobile Profile | Keep the focused profile form; navigation/menu interaction remains separate |
| Files | Existing redirect to Library; no separate obsolete layout is introduced |
| Billing | The conditional companion link/iframe keeps its existing boundary; its independent product UI is outside this change |
| Sign-in / public documents | Keep their focused forms and reading layout; they are not workspace dashboards |

## Actual UI evidence

All images are captures of the real React application on loopback, using
read-only fictional API fixtures. The displayed development identity is the
application's test identity, not a customer. No production records, credentials,
or microphone input were used. Only the Next.js development overlay was hidden;
menus and application content were not redrawn.

Open the [interactive comparison](workspace-density-comparison.html) to inspect
the same-size before/after captures.

| Capture | Purpose |
|---|---|
| [Projects before](assets/workspace-density/projects-before.png) / [after](assets/workspace-density/projects-after.png) | Actual desktop width and column change |
| [Mobile Assistant before](assets/workspace-density/chat-mobile-before.png) / [after](assets/workspace-density/chat-mobile-after.png) | Composer and navigation separation |
| [Settings](assets/workspace-density/settings-after.png) | Independent sections and aligned gutter |
| [Home](assets/workspace-density/home-after.png) | Full working area with existing dashboard panels |
| [Article](assets/workspace-density/article-after.png) | Full header with a bounded reading surface |
| [Dark, short desktop window](assets/workspace-density/projects-dark-short.png) | Existing theme and scrollable navigation |

## Validation and limits

- Baseline: all 771 frontend unit tests passed.
- Viewport matrix: 18 routes at 390, 768, 1024, 1440, 1920, 2560, and 3840px
  (126 checks). All loaded without JavaScript exceptions or horizontal document
  overflow.
- Reading surfaces: document, news article, and research detail at 390, 1440,
  and 3840px (9 checks), with no console errors or failed HTTP responses.
- Final dark/short windows: nine working/reading routes at 390, 1440, and
  3840px and 600px height (27 checks), with no console errors, HTTP failures, or
  horizontal document overflow. Project/document card contrast also passed
  separate computed-color checks.
- Interaction checks: 15 passed, including typed input/focus, mobile menu opening,
  Escape/focus restoration, query-preserving research navigation, short-window
  guide access, recording upload navigation, adaptive cards, and settings table
  width. No API writes or external requests were attempted.
- Final console/HTTP sweep: 21 routes at 390 and 1440px (42 checks), including the
  three reading surfaces. No console errors, failed HTTP responses, API writes,
  external requests, or horizontal document overflow.
- Comparison gallery: image loading, slider, surface selection, keyboard input,
  and overflow checked at 390, 768, 1440, and 3840px.
- Final checks: all 776 frontend unit tests after main integration, production build, documentation
  checks, and 23 documentation tests passed. Lint has no errors and 15 existing
  warnings. Independent content quality and latest-HEAD PR review gate publication.

Fixtures cover representative loaded, empty, and unavailable states. They do not
prove native macOS capture, real-device keyboard/safe-area behavior, every large
customer dataset, or Billing's separate interface. Functional data/access paths
are unchanged. Screenshots are a dated record, not a promise that future content
has identical geometry.

## Further design recommendations

These are follow-up recommendations, not completed changes:

1. Standardize page headings and secondary metadata contrast. Older pages use
   oversized headings, repeated mobile titles, and smaller low-contrast labels.
2. Group the long Settings page by user goal and add local section navigation.
   The new columns improve space use, but administration and connector settings
   still require substantial scanning.
3. Review dense account/person tables with larger representative datasets and
   keyboard users. Keep table scrolling explicit and preserve source context.
4. Review the independent Billing interface and native Mac windows separately.
   Shell geometry alone cannot establish those products' usability.
