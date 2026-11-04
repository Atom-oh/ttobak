# Billing release

The BFF, static pages and processing worker are separate delivery units.
TTOBAK CDK owns `TtobakBillingStack`, including the data plane (data bucket,
platform table, worker, collector and FSI search; INFRA-SPEC "Billing data
plane"). The retiring `TossBillingDashboard` companion stack still holds the
data the SSM parameters name until the one-time cutover below. Changing Python
source alone does not update production processing.

## Gates and artifacts

Use the reviewed, merged revision. Run BFF tests/package, worker tests, frontend
lint/unit/build, applicable infra tests/synth and documentation checks. Verify
all required native PR checks and complete code/security coverage for that exact
revision. The content review must score at least 85 before deployment and bind
the generated frontend, BFF zip and worker image to the reviewed sources.
Keep private exports and generated analysis outside Git and logs.

The COT core consists of `Current_State_Template.xlsx`, `COH_Template.xlsx` and
`COT360.html` with the export's `COT_…_` naming. Existing exports can also include
`Template.xlsx` and `PPT_Companion.pptx`. When present, the combined Template
supplies the dataset sheets; otherwise both core workbooks supply them.

## Worker

The worker image is a `TtobakBillingStack` asset built from
`services/billing/worker/Dockerfile.ingest`, so a worker release is a reviewed
`TtobakBillingStack` deployment (below). The `JobCreated` rule starts a task only
for a `jobs/` object written with `PutObject` (how the BFF writes jobs) in the
data-plane bucket, so copied objects start nothing, and before the cutover the
companion worker alone consumes the jobs the BFF writes to the old bucket.

A worker rollback must target a reviewed revision that already contains the
data plane. Redeploying the revision before it removes the rule, cluster, task
definition and roles, which after the cutover leaves no consumer.

## One-time cutover from the companion stack

Run these in order with the reviewed revision; record each output in a private
receipt. Customer data stays in the old bucket and table until step 6 passes.

1. Deploy `TtobakBillingStack` with `--exclusively` and record its
   `DataBucketName` and `PlatformTableName` outputs. The BFF still reads the old
   resources, so user behavior does not change.
2. Copy the data: server-side copy every object of the old bucket **except
   `jobs/`** to the new one (keys and object tags unchanged), then copy every
   platform table item. Compare object counts, sizes and item counts.
3. Freeze. Announce a short window, then confirm that no platform table `UP#`
   row is `uploading`, `queued` or `running`, that the companion cluster has no
   running worker task, and that there is no incomplete multipart upload under
   `accounts/` in the old bucket. Multipart upload IDs are bucket-bound, so an
   upload in flight across the switch fails and must be uploaded again; repeat
   this check right before step 4.
4. Point `/ttobak/billing/data-bucket-name` and
   `/ttobak/billing/platform-table-name` at the new resources. Deploy
   `TtobakBillingStack`, then `TtobakFrontendStack`, each with `--exclusively
   --force`. Both read the parameters only during a stack update, and an
   unchanged template is otherwise skipped. Verify the active Billing revision's
   `DATA_BUCKET` and `PLATFORM_TABLE` and the distribution's `/billing-upload/*`
   origin domain name the new resources.
5. Copy any non-`jobs/` object or item the old resources received after step 2,
   repeating the comparison. The freeze keeps this empty unless a write slipped
   through.
6. Accept: the dashboard lists every account and its current versions; a new
   COT upload runs on the new worker (its job lands in the new bucket, its task
   in the new cluster) and publishes; the old bucket receives no new object.
7. Before deleting the companion stack, set `DeletionPolicy: Retain` on its
   three `AWS::EC2::VPCEndpoint` resources (CloudWatch Logs, Lambda, Secrets
   Manager) with an update that changes nothing else. They are the shared VPC's
   only endpoints for those services, and other workloads use them. Notify the
   VPC owner that they are no longer managed by a stack.
8. Delete `TossBillingDashboard`. Its old dashboard, distribution, ALB, ECS
   service, OAuth function and alias records are removed; its retained bucket and
   table remain. Delete those only after a separately recorded backup or explicit
   user approval.

Rollback before step 4: nothing to undo; the BFF still uses the old resources.
After step 4, writes land only in the new resources. Freeze again, copy every
object and item created or changed since step 4 back to the old resources, then
point the parameters back and redeploy both stacks with `--force`. A rollback
that only repoints the parameters loses every upload and publication accepted
after step 4.

## BFF and frontend

Package the BFF, synthesize and freeze the assembly. Review the
`TtobakBillingStack` diff, preserving both data SSM parameters and CloudFront
ingress. Deploy only that stack with `--exclusively`; do not deploy Gateway,
Knowledge or implicit dependencies. Verify the endpoint routes 100% to an
active `IamAuth` revision and its actual pinned S3 zip matches the reviewed code.

Publish `frontend/out` with `scripts/publish-frontend.sh`, supplying `S3_BUCKET`
and `CF_DISTRIBUTION`. Preserve runtime `config.json`, publish assets before
HTML, and verify invalidation completion and public file hashes. An anonymous
Billing API request must remain unauthorized.

## Acceptance and rollback

Check direct and ancestor team discovery/upload access and denied unrelated
access. Upload a three-file COT with COT selected; check extraction, dataset
checks and final processing state. Local staging/dataset success and a matching
deployed image do not prove an authenticated production upload completed.

Before reverting the worker, restore the prior frontend and drain queued/active
three-file COT jobs. Then redeploy `TtobakBillingStack` from the prior reviewed
revision (its worker image asset). Re-enable three-file support only after
verifying a compatible worker task.
Restore the prior BFF for faults in that unit. Retain customer data and versions.
