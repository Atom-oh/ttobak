# Billing release

The BFF, static pages and processing worker are separate delivery units.
TTOBAK CDK owns `TtobakBillingStack`; the existing `TossBillingDashboard`
companion stack owns the worker. Both use the data bucket and platform table
resolved by the Billing SSM parameters. Changing Python source alone does not
update production processing.

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

## Existing companion worker: deploy first

Complete and verify step 4 before publishing BFF/frontend three-file support.

1. Capture the original `TossBillingDashboard` template, current task definition,
   EventBridge target, image digest and stack parameters in a private release
   receipt. Compare the deployed image's `/app` sources with the proposed image;
   include any previously undelivered main changes in operational review.
2. Build the ARM64 image from `services/billing/worker/Dockerfile.ingest`.
   Verify the image's code hashes, non-root user, entry point and pipeline
   smoke test without credentials or networking. Publish it under a new revision
   tag in the existing asset repository; resolve and pin its ECR digest.
3. Produce a copy of the captured template changing **only**
   `Resources.WorkerTaskCBB1FEE3.Properties.ContainerDefinitions[0].Image`.
   Assert exact equality after restoring that single property. Validate schema
   and review the security boundary, then create an UPDATE change set with every
   existing parameter set to `UsePreviousValue: true`. Inspect pre-deployment
   validation results. Reject changes outside the worker task definition, its
   referencing EventBridge target and the existing EventBridge invoke policy's
   task-ARN reference update. The policy statement itself must remain unchanged.
4. Execute the reviewed change set. Wait for `UPDATE_COMPLETE`, then verify the
   live task definition's image digest and the `JobCreated` rule's target refer
   to the new task revision. Preserve the prior image and template for rollback.
   New jobs use the updated target; existing tasks keep their current image.

No new origin, service, role, network rule or data migration is part of this
procedure. A later companion CDK deployment must retain the released image
digest, or it can revert this image-only update. Move worker ownership into
TTOBAK CDK separately rather than creating two consumers for the same jobs.

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
three-file COT jobs. Then restore the captured image with an image-only change
set. Re-enable three-file support only after verifying a compatible worker target.
Restore the prior BFF for faults in that unit. Retain customer data and versions.
