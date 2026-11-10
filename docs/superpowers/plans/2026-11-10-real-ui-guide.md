# Real UI Guide Implementation Plan

Status: historical implementation record (2026-11-10); not a current reference.

> **For agentic workers:** Use superpowers:executing-plans inline; this host
> cannot close spawned agents as required by the user's lifecycle rule.

**Goal:** Publish actual TTOBAK UI screenshots throughout the enterprise guide.

**Architecture:** Capture the unchanged local frontend with fictional read-only
fixtures. Store lossless image bytes in a reviewed documentation Release archive;
package only pinned, verified media into the existing Pages site.

**Tech Stack:** Python stdlib, Playwright, Pillow, semantic HTML/CSS.

**Spec:** `docs/superpowers/specs/2026-11-10-real-ui-guide-design.md`

## Global constraints

- Preserve the original dirty checkout and application/runtime/workflow code.
- Do not expose production data, tokens, external API requests or microphone data.
- Keep the self-only CSP and all existing source-review/CI gates.
- Publish no internal manifests or capture reports in the Pages artifact.

## Review focus

- Real UI is unchanged; no redrawn menu, DOM rewrite or substitute app CSS.
- All primary menus and meaningful Account/settings/mobile states are included.
- Wrong hashes, oversized archives, duplicate/unexpected entries fail closed.
- Broken images, collapsed galleries and PDF omission are checked in a browser.
- Source fixtures are distinguishable from production and native acceptance.

## Tasks

- [x] Capture actual screens with `scripts/docs/capture_user_guide.py`;
  inspect every capture and bind it to source revision and pixel identity.
- [x] Write failing media-boundary tests, then implement the pinned screenshot
  archive in `scripts/docs/build_user_guide.py` and its explicit allowlist.
- [x] Replace SVG figures and add visible related-tab screenshots.
- [x] Verify packaging, docs checks, images, navigation and complete printing;
  perform the mandatory content rubric review.
- [ ] Obtain latest-head required CI and independent review, merge, deploy,
  and verify the ordinary public URL and screenshot hashes.
