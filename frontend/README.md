# TTOBAK frontend

Next.js 16/React 19 application with Tailwind 4 and TipTap. Production uses static
export; development uses the Next.js dev server.

```bash
npm ci
npm run dev
npm run lint
npm run build
```

There is no component test framework. `npm run test:unit` runs the node:test
suites in `scripts/*.test.mjs` (also in CI); use lint/build and targeted browser
checks for interaction changes. `/config.json` supplies runtime Cognito/API configuration.
See [module guidance](CLAUDE.md), [UI reference](../docs/DESIGN-SPEC.md), and the
[deployment runbook](../docs/runbooks/deployment.md).
