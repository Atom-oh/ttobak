# Responsive workspace density

Status: historical design for the 2026-11-11 implementation; source and the
current UI reference own ongoing behavior.

## User requirement

Use the available window like `claude-code-usage-dashboard`, reduce wasted
space, and review the whole product's design using the actual rendered UI.
The reference is the local repository of that name. Its main area fills the
window beside a 256px sidebar. Keep TTOBAK's existing brand, navigation, and
feature behavior.

## Design

- Give workspace pages the full available width. Use one 16px mobile / 24px
  desktop gutter instead of page-specific 32–64px padding and 768–1280px caps.
- Let project cards add columns as space becomes available; do not stretch two
  small cards across an ultrawide screen.
- Keep long article bodies and document prose bounded for reading. Authentication,
  profile forms, empty-state explanations, and upload targets remain focused.
- Use the dynamic viewport height for the shell and reserve space for the mobile
  navigation in the shell, including the device safe area. Independently scrolling
  chat and detail panes must retain their scroll behavior.
- Keep the desktop sidebar's action area visible and its menu independently
  scrollable on short windows. Reduce group spacing, preserve touch/focus targets,
  and allow long header breadcrumbs to shrink without displacing search/actions.
- Use independent columns for settings sections when enough space exists, retaining
  full width for the administration table. Keep order and anchor links stable.

## Acceptance

Review Home, Assistant, Meetings, Accounts and account workspace, Projects and
project detail, Docs, Library, Deep Research, News, Dictionary, SA Activity,
Settings, Recording, and the mobile Profile navigation. Include document,
meeting, news, and research reading surfaces where fixtures are available.

Measure 390, 768, 1024, 1440, 1920, 2560, and 3840px windows, a short desktop
window, and both themes. Main pages must use their available width without
horizontal document overflow; chat input and navigation must not overlap.
Verify real interactive menus, keyboard focus, page scrolling, and the sidebar
action area. Screenshots use fictional local read-only API data, never production
meeting content or synthetic redrawn UI.

Run frontend lint, unit tests, production build, and documentation checks. An
independent content-review-agent must score at least 85 before deployment.
Complete the repository's latest-HEAD PR review and required CI gates.
