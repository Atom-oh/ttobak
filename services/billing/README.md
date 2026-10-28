# Billing service

Billing Insight BFF, served under `/api/billing` and run as a Lambda Web Function
(entry point and auth arrive in later tasks of the Phase 1 plan).

## Provenance

`bff/` was imported from the companion Billing Insight repository, commit
`dd540f51e0b5a67dff7389e401e65fc479cd77a7`
(`dashboard-app/app/server.js`, `dashboard-app/app/lib/*`, `tests/dom/*`).

Changes against the source in this import: pages, cookies, OAuth hand-off and the
Lambda client are removed; every route moves under `/api/billing`; `createApp`
takes an injected `authenticate` middleware. `lib/access.js` is still the
imported hierarchy version and is rewritten later in Phase 1.

## Tests

```
cd services/billing/bff
npm ci
npm test
```

Requires Node.js 24. The suites use fake S3 and DynamoDB clients and synthetic data only.

## Dashboard pages

`page/` holds the sources of the dashboard and upload pages (`body.html`,
`upload.html`, `dash-shim.js`, `tokens.css`, from `dashboard-app/app/page/` and
`dashboard-app/app/public/` of the same companion commit). They are served as
same-origin static assets at `/billing-app/` and run inside an iframe of the TTOBAK
`/billing` page, with no cookies and no server shell. `dash-shim.js` takes the token
from `window.parent.ttobakBilling.token()`, sends `Authorization: Bearer`, refreshes
once on a 401 and calls `signedOut()` on a second one. It loads
`/api/billing/accounts` before the page scripts run (`dash.ready`). The upload page
loads it with `data-auth-only` for the same authenticated fetch; upload part PUTs go
to the presigned URLs without the header.

The output is not committed. The frontend `prebuild`
(`frontend/scripts/build-billing-app.mjs`) generates it into
`frontend/public/billing-app/` (gitignored), so `next build` exports it to
`out/billing-app/`. The script runs offline but fails if the vendor sources are
missing, so install them first:

```
cd services/billing/page
npm ci
python3 -m unittest test_build_page -v
python3 -I build_page.py            # optional; frontend prebuild does the same
```

The tests build twice into temp dirs and require byte-identical output. CI
installs these dependencies before every frontend build (`deploy-frontend.yml`)
and before the infra Jest suite, whose `billing-frontend.test.ts` generates the
pages into a temp dir. Output files:

| File | Kind |
|---|---|
| `index.html`, `upload.html` | Generated from `body.html` / `upload.html` plus a shell; the first line is the GENERATED comment |
| `dash-shim.js`, `tokens.css` | Copied from `page/` with a GENERATED header comment |
| `vendor/d3.min.js`, `vendor/d3-LICENSE.txt` | Copied unchanged from `d3` 7.9.0 |
| `vendor/pretendard/pretendardvariable-dynamic-subset.css`, `vendor/pretendard/woff2-dynamic-subset/*.woff2`, `vendor/pretendard/LICENSE.txt` | Copied unchanged from `pretendard` 1.3.9 (only the fonts the CSS references) |

The shim tests are `bff/test/shim.test.js` and run with the BFF suite.
