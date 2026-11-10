# Real UI Guide Screenshots

Status: historical implementation record (2026-11-10); not a current reference.

The user rejected the simplified SVG menu diagrams because they do not show
the actual TTOBAK UI. Replace them with unmodified captures of the real
frontend and add coverage for primary menus, Account tabs, settings and mobile.

Run the current frontend locally with its existing development auth. Intercept
API reads with explicitly fictional fixtures and block external services and
API mutations. Preserve application DOM, CSS, icons and layout; exclude only
the Next.js development tooling from screenshots. Encode the captured pixels
as lossless WebP without resizing or drawing replacement interface elements.
Clearly label captures as actual application UI with example data and the
source date. Do not claim production/provider/native capture acceptance.

Keep binary media in a public, documentation-only GitHub Release artifact,
separate from application releases, rather than modifying the source-review
pipeline's binary-input prohibition. The PR must expose the archive URL and
per-image hashes and contain a reproducible capture script and visual review.
The existing Pages packager fetches only that exact reviewed archive, checks
its SHA-256 and bounded size, and publishes only the explicit image allowlist.
Reject unexpected, duplicate, invalid or changed images before output exists.
Keep the site's self-only CSP and existing CI/review gates unchanged.

Use full-width figures for contextual screenshots and visible related-tab
figures. Each image has useful alt text, captions and a full-size link;
all screenshots remain accessible without JavaScript and print in the manual.
