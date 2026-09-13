# Infrastructure

Eleven TypeScript CDK stacks are instantiated in `bin/infra.ts`; its dependency
edges are authoritative. `lib/*-stack.ts` owns resources and `test/` contains
active Jest assertions, including security invariants.

```bash
npx cdk synth
npm test
```

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
