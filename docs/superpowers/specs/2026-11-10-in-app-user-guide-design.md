# In-app user guide access

Status: historical design for the 2026-11-10 implementation.

The user wants to read the existing manual from both the web app and Mac app.
Expose a **User guide** action in the desktop sidebar, desktop help action, mobile menu
sheet, and profile page. Open the guide in a large accessible in-page dialog,
keeping the current route and workspace mounted. Closing restores focus.

Bundle the reviewed public guide under `/user-guide/` using the existing
allowlisted packager and its exact screenshot archive and image hash checks.
Publish all 40 verified assets, including the 36 actual UI captures. Preserve
the guide's search, navigation, theme, and browser printing. Embed it in an
opaque-origin sandbox with scripts and printing enabled; add no native capability.
The `/user-guide/*` CloudFront behavior uses the existing private S3 origin
and permits same-site framing through a dedicated response-header policy.
All other application pages retain their existing framing restrictions.
Deploy this behavior before publishing the frontend that embeds the guide.
No new AWS origin, native permission, or API is needed.
Embedded screenshot links open an original-size image dialog inside the reader,
so the WebView does not depend on browser popups. Standalone links stay unchanged.

The build validates a staged package before replacing generated output.
Development and production build hooks generate ignored assets automatically.
An optional offline archive must pass the same identity checks.

Acceptance: real browser checks cover all entry points, responsive layout,
focus return, Escape, readable content, image loading, search and theme.
Render the dialog through a body portal so hiding its responsive opener does
not hide the active reader. Browser checks must include the deployed framing
headers; a static server without CloudFront policies is insufficient.
Frontend unit tests, lint, production export, packaging boundaries, independent
latest-head PR review, and deployment verification must pass. Native macOS
capture and system printing cannot be validated on this Linux host.
