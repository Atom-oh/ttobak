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

See [recording and upload workflows](https://www.atomai.click/ttobak/#record) for meeting capture.

Use the [Accounts](https://www.atomai.click/ttobak/#accounts) and [Settings](https://www.atomai.click/ttobak/#settings) chapters for team setup.
