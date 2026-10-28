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
