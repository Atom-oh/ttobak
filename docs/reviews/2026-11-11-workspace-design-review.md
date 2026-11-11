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

PR review exposed a mobile scrolling defect: the shell's end padding did not
protect descendants overflowing its flex child. The actual Meetings continuation
button ended at y=584 behind navigation starting at y=536 in a 390×600 window.
Moving page scrolling into the bounded content wrapper leaves that button at
y=478–520, with 16px clearance. Final loaded items in Projects and Docs are
also reachable above navigation.

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
| Meetings continuation, 390×600 window | Reviewed intermediate button at y=542–584, behind navigation | Button at y=478–520, above navigation at y=536 | Last-page controls remain reachable after scrolling |

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
| Sidebar / desktop header / mobile navigation | Preserve grouped destinations, guide access, profile/help/notification controls, and search; keep short-window actions reachable. Review inconsistent labels (Assistant/AI and mixed Korean/English) as a follow-up |
| User guide | Keep the existing localized user-guide entry and guide viewer; actual product captures remain the evidence for menu structure |

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
- Initial viewport matrix: 18 routes at 390, 768, 1024, 1440, 1920, 2560,
  and 3840px (126 checks), followed by nine reading-surface checks.
- Final viewport and console/HTTP matrix after the scrolling fix and main
  integration: 21 routes at all seven widths (147 checks), including document,
  news article, and research detail. All loaded without JavaScript/console errors,
  failed HTTP responses, horizontal document overflow, API writes, or external
  requests.
- Final dark/short windows: nine working/reading routes at 390, 1440, and
  3840px and 600px height (27 checks), with no console errors, HTTP failures, or
  horizontal document overflow. Project/document card contrast also passed
  separate computed-color checks.
- Interaction assertions: 15 passed, including 12 UI checks (typed input/focus,
  menu opening, Escape/focus restoration, research navigation, short-window guide
  access, recording upload navigation, adaptive cards, settings width) and three
  error/HTTP/traffic checks. No API writes or external requests were attempted.
- Bottom clearance: five loaded mobile pages checked after scrolling to the end,
  including Meetings with a fictional continuation token, Home, Projects, Docs,
  and Accounts. Each final control/item stayed above navigation in a 390×600
  window; the Meetings button retained 16px clearance.
- Comparison gallery: image loading, slider, surface selection, keyboard input,
  and overflow checked at 390, 768, 1440, and 3840px.
- Final checks: all 780 frontend unit tests after main integration, production build, documentation
  checks, and 23 documentation tests passed. Lint has no errors and 15 existing
  warnings. Publication is gated on independent content quality and latest-HEAD PR review.

Fixtures cover representative loaded, empty, and unavailable states. They do not
prove native macOS capture, real-device keyboard/safe-area behavior, every large
customer dataset, or Billing's separate interface. Functional data/access paths
are unchanged. Screenshots are a dated record, not a promise that future content
has identical geometry.

## Further design recommendations

These are follow-up recommendations, not completed changes:

1. Standardize page headings and secondary metadata contrast. Older pages use
   oversized headings, repeated mobile titles, and smaller low-contrast labels.
   Dark “New Meeting” and localized “New Project” labels measure 3.09:1 (white on
   rgb(139,133,247)), below the 4.5:1 target for their small text. Apply a consistent
   foreground/fill policy to primary controls throughout the product; the measured
   card-text improvement does not fix this separate issue.
2. Group the long Settings page by user goal and add local section navigation.
   The new columns improve space use, but administration and connector settings
   still require substantial scanning.
3. Review dense account/person tables with larger representative datasets and
   keyboard users. Keep table scrolling explicit and preserve source context.
4. Review the independent Billing interface and native Mac windows separately.
   Shell geometry alone cannot establish those products' usability.
5. Add a persistent scroll cue for short desktop sidebars. Some destinations are
   below the visible list at 600px height; scrolling works, but its availability
   is not obvious in a static capture.
6. Standardize navigation label language and naming across the sidebar, header,
   and mobile tabs, including “Assistant” versus “AI” and mixed Korean/English.
