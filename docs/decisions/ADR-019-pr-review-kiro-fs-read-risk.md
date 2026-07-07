# ADR-019: PR-Review Kiro `fs_read` — Residual Exfiltration Risk (Accepted, Mitigated)

## Status

Accepted (2026-07-06)

## Context

The PR-review CI panel (lens×model matrix, `scripts/pr-review/*`) moved Kiro's diff
delivery from an invalid `--trust-tools=read,grep` to the real read-only tool name
`--trust-tools=fs_read`. `fs_read` is read-capable for absolute paths, and it runs against
an **untrusted PR diff** injected as the review prompt — a `pull_request_target` job with
self-hosted-runner secrets in scope. A diff-borne prompt injection could instruct Kiro to
`fs_read` a fixed credential path (e.g. `.git/config`'s persisted `GITHUB_TOKEN`, or an EKS
Pod Identity token file) and have the value appear in its response, which Claude then
synthesizes — and Kiro itself is an **external service**, so the read value would leave the
region/account before any scrub runs.

## Mitigations Already In Place

- Isolated `$HOME`/cwd for the Kiro subprocess (`KIRO_CWD`) — closes the `~`-relative
  path vector (real credentials aren't under the fake `$HOME`).
- `env -i` allowlist — only `KIRO_API_KEY` and the minimum needed vars reach Kiro;
  `AWS_*`/`GH_TOKEN` are never passed via environment.
- `scrub_secrets()` — a last-line-of-defense regex pass over every cell's output before
  it reaches the chair or a public PR comment.

## Residual Risk (Accepted)

None of the above stops an **absolute-path** `fs_read` from succeeding — `fs_read` is
read-capable by design and the isolation only affects env vars and `~`-relative lookups.
The concrete residual path: `actions/checkout`'s default `persist-credentials: true` writes
`GITHUB_TOKEN` into `.git/config`; a diff-borne injection reading that absolute path would
have `scrub_secrets()` catch a well-formed GitHub token pattern, but a differently-shaped or
partial secret could still slip through as the *first* line of defense, not a guarantee.

## Decision

1. **`persist-credentials: false`** on this workflow's `actions/checkout@v4` step — removes
   the one concrete, known-shape credential this design would otherwise leave on disk.
   This is a direct fix, not just documentation.
2. **Accept as residual risk, not blocking**: the general "absolute-path `fs_read` can read
   anything the runner's OS user can read" capability has no full mitigation without
   dropping `fs_read` entirely (which would break Kiro's diff delivery — Kiro's `chat`
   ignores stdin). `scrub_secrets()` remains the last line of defense for whatever *does*
   get read. Accepted per the same reasoning already applied in sibling repos that adopted
   `fs_read` first (see References) — this is not a new class of risk unique to ttobak, and
   the alternative (no Kiro panel member) loses a full vendor's cross-check.
3. Scope: **CI pr-review only**. This does not change co-agent's own Kiro fan-out, which
   uses the same tool but is interactive/on-demand rather than a `pull_request_target` gate
   over untrusted PR content.

## Consequences

- The one concrete, addressable leak (persisted `GITHUB_TOKEN`) is closed.
- The general absolute-path read capability remains an accepted, documented residual risk
  — any future hardening (e.g. a dedicated low-privilege OS user for the Kiro subprocess)
  should reference this ADR rather than re-litigating the same trade-off.

## References

- `scripts/pr-review/run-panel.sh` (Kiro cell: `kiro_env`, `KIRO_CWD`, `--trust-tools=fs_read`)
- `scripts/pr-review/lib.sh` (`scrub_secrets`)
- `.github/workflows/pr-review.yml` (`persist-credentials: false`)
- Same trade-off documented in sibling repos' own ADRs for the ported lens×model design
  (e.g. cc-on-bedrock ADR-011) and in `plugins/co-agent/skills/co-agent/scripts/consensus_hooks.py`'s
  `_sanitized_env` for the equivalent interactive-panel threat model.
