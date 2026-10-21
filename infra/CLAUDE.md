# Infrastructure

Eleven TypeScript CDK stacks are instantiated in `bin/infra.ts`; its dependency
edges are authoritative. `lib/*-stack.ts` owns resources and `test/` contains
active Jest assertions, including security invariants.

```bash
(cd ../mcp-server && npm ci && npm run bundle:lambda)  # ttobak-mcp asset, gitignored
npx cdk synth
npm test
```

Synth and connector gateway tests bundle the pinned ARM64/Python 3.12 crypto
layer. The runner needs Python 3.12 with pip (`python3.12` or `python3`), or a
Docker daemon that can run the Lambda Python 3.12 ARM64 build image using volume
copy. The selected bundler needs access to the package index for the three
hash-pinned wheels in `assets/connector-crypto/requirements.txt`; local pip may
instead use a populated wheel directory via `PIP_FIND_LINKS` and `PIP_NO_INDEX=1`.
An available Python 3.12 whose pip install fails stops synth without Docker
fallback. Both PR tests and the deployment's `infra-tests` job rely on this
runner prerequisite; Python setup in the later deploy job does not satisfy it.

Deploy each changed stack individually with `--exclusively`, in dependency order.
Never run `cdk deploy --all` or implicit dependency deployment: KnowledgeStack has
a staged, undeployed teardown. Synth is not deployment evidence.

Use `Fn.split`/`Fn.select` for unresolved tokens, explicit resource scopes, and
root security requirements. Cross-region references need the producer/consumer
settings in `bin/infra.ts`; EdgeAuth and WebSearchGateway are in us-east-1.

Preserve Whisper image-owned bundle/entrypoint pins and the OAC policy-tightening
custom resource's changing Timestamp. New asset prefixes need OAC permission;
new static pages need the SPA router allowlist. Go and Python API payload versions
are intentionally different. See `docs/INFRA-SPEC.md` for current stack roles.
