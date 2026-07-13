# TTOBAK Infrastructure (CDK TypeScript)

The `cdk.json` file tells the CDK Toolkit how to execute your app.

## Useful commands

* `npm run build`   compile typescript to js
* `npm run watch`   watch for changes and compile
* `npm run test`    perform the jest unit tests
* `npx cdk diff`    compare deployed stack with current state
* `npx cdk synth`   emits the synthesized CloudFormation template

## Deploying

**Never `npx cdk deploy` (or `--all`) bare.** Deploy one stack at a time
with `--exclusively`, e.g. `npx cdk deploy TtobakGatewayStack --exclusively`
— see root `CLAUDE.md`'s Known Issues for why (a bare/`--all` deploy pulls
in `TtobakKnowledgeStack`'s dependency closure, applying a deliberately
undeployed Bedrock KB teardown) and for the full stack list and deploy
order.
