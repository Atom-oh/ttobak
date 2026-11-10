# TTOBAK frontend

Next.js 16/React 19 application with Tailwind 4 and TipTap. Production uses static
export; development uses the Next.js dev server.

```bash
npm ci --prefix ../services/billing/page   # Billing page vendor sources, needed by prebuild
npm ci
npm run dev
npm run lint
npm run build
```

There is no component test framework. `npm run test:unit` runs the node:test
suites in `scripts/*.test.mjs` (also in CI); use lint/build and targeted browser
checks for interaction changes. `npm run build` first generates the gitignored
`public/billing-app/` from `services/billing/page` (`npm run billing:build`). `/config.json` supplies runtime Cognito/API configuration.
See [module guidance](CLAUDE.md), [UI reference](../docs/DESIGN-SPEC.md), and the
[deployment runbook](../docs/runbooks/deployment.md).

The [product guide](https://www.atomai.click/ttobak/) is published from
`docs/user-guide`; see its [authoring and packaging notes](../docs/user-guide/README.md).
The guide includes actual frontend screenshots of the menus, Account tabs,
sharing controls, settings and mobile layouts, using fictional example data.
The sidebar, header help action, mobile menu and profile open this manual in an
in-page dialog without leaving the current task. `npm run guide:build` packages
the same verified public assets under `public/user-guide/`; `predev` and
`prebuild` run it automatically. The build downloads the pinned screenshot
archive; for offline builds set `TTOBAK_GUIDE_SCREENSHOT_ARCHIVE` to a local
copy, which must pass the same archive and image hash checks.
Deploy FrontendStack's `/user-guide/*` framing behavior before publishing
the embedded reader; the default application headers intentionally reject frames.
The shared publisher requires the infrastructure-owned `userGuideRevision:
same-origin-framing-v1` marker before any upload of an export containing the
manual. If infrastructure has not finished, publication fails without changes;
retry the frontend workflow after the marker is deployed.
Conditional Billing/admin controls depend on configuration and permissions;
these screenshots do not establish production or native macOS acceptance.

See [recording and upload workflows](https://www.atomai.click/ttobak/#record) for meeting capture.

Use the [Accounts](https://www.atomai.click/ttobak/#accounts) and [Settings](https://www.atomai.click/ttobak/#settings) chapters for team setup.

For rollout, read the guide’s [data boundaries](https://www.atomai.click/ttobak/#security) and [troubleshooting](https://www.atomai.click/ttobak/#troubleshooting) chapters.
