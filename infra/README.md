# TTOBAK infrastructure

Eleven CDK stacks, defined and connected in `bin/infra.ts`.

```bash
npm ci
npx cdk synth
npm test
```

Read [module guidance](CLAUDE.md) and [infrastructure reference](../docs/INFRA-SPEC.md).
Deploy only explicitly selected changed stacks with `--exclusively`; never use
`--all` or implicit dependencies. KnowledgeStack has a staged, undeployed teardown.
Deployment is separate from synthesis and needs an authorized target environment.
