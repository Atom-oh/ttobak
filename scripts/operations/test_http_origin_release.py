import copy
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import stat
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock
import zlib

from http_origin_release import CACHE_WAIT, RETIRED_STAGE, SECRET_NAME, STACKS, Cdk, Release, ReleaseError, checkout_revision, main

ACCOUNT, REGION = "111111111111", "ap-northeast-2"
OLD = "synthetic-old-origin-proof"
NEW = "synthetic-new-origin-proof"
ARN = f"arn:aws:secretsmanager:{REGION}:{ACCOUNT}:secret:{SECRET_NAME}-ABC123"


class Missing(Exception):
    pass


class World:
    """Modeled control plane and stale consumer cache; no AWS clients or data."""
    def __init__(self):
        self.now = 1800000000
        self.versions, self.current = {}, None
        self.labels = {}
        self.proof = OLD
        self.functions = {}
        self.alias_version, self.alias_revision = "1", "alias-1"
        self.sequence = 1
        self.history = []
        self.fail_frontend = False
        self.lose_promotion_reply = False
        self.foundation = True
        self.qa_status = 405  # Legacy handler; configured identity ingress rejects this probe with 403.
        self.mutate_on_sleep = False
        self.marker_missing_for = ""
        self.templates_ready = True
        self.fail_pending_write = False
        self.fail_active_consumers = False
        self.mutate_final_public = False
        self.secret_changes = 0
        for name in ("ttobak-api", "ttobak-api:live", "ttobak-qa", "ttobak-mcp"):
            self.functions[name] = {
                "FunctionName": name.split(":")[0],
                "FunctionArn": f"arn:aws:lambda:{REGION}:{ACCOUNT}:function:{name}",
                "Version": "1" if name.endswith(":live") else "$LATEST",
                "RevisionId": "revision-1", "CodeSha256": "synthetic-reviewed-code",
                "State": "Active", "LastUpdateStatus": "Successful",
                "Environment": {"Variables": {"ORIGIN_VERIFY_SECRET": OLD}},
            }
        self.ready_at = 0
        self.previous_document = {"current": OLD, "previous": "", "previousExpiresAt": 0}

    def doc(self):
        return self.versions[self.current] if self.current else None

    def accepts(self, name, proof):
        if proof is None:
            return False
        env = self.functions[name]["Environment"]["Variables"]
        if "ORIGIN_VERIFY_SECRET" in env:
            return proof == env["ORIGIN_VERIFY_SECRET"]
        document = self.versions[env["ORIGIN_VERIFY_SECRET_VERSION_ID"]]
        if self.now < self.ready_at:
            document = self.previous_document
        return proof == document["current"] or (
            proof == document.get("previous") and self.now < document.get("previousExpiresAt", 0))

    def sleep(self, seconds):
        self.history.append(("sleep", seconds))
        self.now += seconds
        if self.mutate_on_sleep:
            self.functions["ttobak-qa"]["RevisionId"] = "unexpected-revision"
            self.mutate_on_sleep = False

    def get_function_configuration(self, FunctionName):
        if FunctionName == "ttobak-summarize":
            return {"Environment": {"Variables": {"BEDROCK_SUMMARY_MODEL_ID": "global.openai.gpt-6.1-sol"}}}
        return copy.deepcopy(self.functions[FunctionName])

    def get_alias(self, **kwargs):
        return {"FunctionVersion": self.alias_version, "RevisionId": self.alias_revision}

    def invoke(self, FunctionName, InvocationType, Payload):
        event = json.loads(Payload)
        self.history.append(("invoke", FunctionName))
        self.assert_probe(event, FunctionName)
        accepted = self.accepts(FunctionName, event["headers"].get("x-origin-verify"))
        # Model consumer behavior independently of the coordinator acceptance predicate.
        status = (404 if FunctionName.startswith("ttobak-api") else
                  self.qa_status if FunctionName == "ttobak-qa" else 200) if accepted else 403
        marker = accepted and self.foundation and self.marker_missing_for != FunctionName
        if FunctionName.startswith("ttobak-api"):
            response = {"statusCode": status, "headers": None,
                        "multiValueHeaders": {"X-Ttobak-Origin-Protocol": ["1"]} if marker else {},
                        "body": "synthetic empty probe"}
        else:
            response = {"statusCode": status, "headers": {"X-Ttobak-Origin-Protocol": "1"} if marker else {},
                        "body": "synthetic empty probe"}
        return {"StatusCode": 200, "ExecutedVersion": self.functions[FunctionName]["Version"],
                "Payload": io.BytesIO(json.dumps(response).encode())}

    @staticmethod
    def assert_probe(event, name):
        assert event.get("body") is None
        assert "authorization" not in event["headers"]
        if name.startswith("ttobak-api"):
            assert event["httpMethod"] == "GET" and "__origin_migration_probe__" in event["path"]
        elif name == "ttobak-qa":
            assert event["requestContext"]["http"]["method"] == "GET" and event["rawPath"] == "/api/qa/origin-probe"
        else:
            assert event["rawPath"] == "/.well-known/oauth-protected-resource"

    def describe_stacks(self, StackName):
        values = {"HttpApiUrl": f"https://api.execute-api.{REGION}.amazonaws.com"} if "Gateway" in StackName else {
            "DistributionId": "distribution", "DistributionDomainName": "synthetic.cloudfront.net"}
        return {"Stacks": [{"StackStatus": "UPDATE_COMPLETE", "Outputs": [
            {"OutputKey": key, "OutputValue": value} for key, value in values.items()]}]}

    def get_distribution(self, **kwargs):
        return {"Distribution": {"Status": "Deployed", "DomainName": "synthetic.cloudfront.net",
                                 "DistributionConfig": {"Origins": {"Items": [{
                                     "DomainName": f"api.execute-api.{REGION}.amazonaws.com",
                                     "CustomHeaders": {"Items": [{"HeaderName": "X-Origin-Verify", "HeaderValue": self.proof}]},
                                 }]}}}}

    def get_paginator(self, operation):
        routes = [
            ("ANY /api/{proxy+}", "api", "JWT"), ("GET /api/public/docs/{token}", "api", "NONE"),
            ("POST /api/qa/ask", "qa", "JWT"), ("ANY /api/mcp", "mcp", "JWT"),
            ("GET /.well-known/oauth-protected-resource", "mcp", "NONE"),
            ("GET /.well-known/oauth-protected-resource/api/mcp", "mcp", "NONE"),
        ]
        if operation == "get_routes":
            items = [{"RouteKey": key, "Target": "integrations/" + target, "AuthorizationType": auth}
                     for key, target, auth in routes]
        else:
            items = [{"IntegrationId": key,
                      "IntegrationUri": f"arn:aws:lambda:{REGION}:{ACCOUNT}:function:{name}",
                      "PayloadFormatVersion": "1.0" if key == "api" else "2.0"}
                     for key, name in (("api", "ttobak-api:live"), ("qa", "ttobak-qa"), ("mcp", "ttobak-mcp"))]
        return SimpleNamespace(paginate=lambda **kwargs: iter([{"Items": items}]))

    def get_role(self, **kwargs):
        return {}

    exceptions = SimpleNamespace(ResourceNotFoundException=Missing, NoSuchEntityException=Missing)

    def get_secret_value(self, SecretId, VersionId=None):
        version = VersionId or self.current
        if version is None or version not in self.versions:
            raise Missing()
        return {"ARN": ARN, "VersionId": version, "SecretString": json.dumps(self.versions[version]),
                "VersionStages": [stage for stage, identity in self.labels.items() if identity == version]}

    def create_secret(self, **kwargs):
        self.secret_changes += 1
        self.history.append(("create_secret",))
        self.current = kwargs["ClientRequestToken"]
        self.labels["AWSCURRENT"] = self.current
        self.versions[self.current] = json.loads(kwargs["SecretString"])
        return {"ARN": ARN, "VersionId": self.current}

    def put_secret_value(self, **kwargs):
        self.secret_changes += 1
        self.history.append(("put_secret",))
        assert kwargs["VersionStages"] == ["AWSPENDING"]
        self.versions[kwargs["ClientRequestToken"]] = json.loads(kwargs["SecretString"])
        self.labels["AWSPENDING"] = kwargs["ClientRequestToken"]
        if self.fail_pending_write:
            self.fail_pending_write = False
            raise RuntimeError("synthetic private pending-write response")

    def update_secret_version_stage(self, **kwargs):
        if kwargs["VersionStage"] == RETIRED_STAGE:
            assert self.labels.get(RETIRED_STAGE) in (None, kwargs["MoveToVersionId"])
            self.labels[RETIRED_STAGE] = kwargs["MoveToVersionId"]
            self.history.append(("retain_retired",))
            return
        assert kwargs["RemoveFromVersionId"] == self.current
        self.secret_changes += 1
        self.labels["AWSPREVIOUS"] = self.current
        self.current = kwargs["MoveToVersionId"]
        self.labels["AWSCURRENT"] = self.current
        self.history.append(("promote", self.doc()["phase"]))
        if self.lose_promotion_reply:
            self.lose_promotion_reply = False
            raise RuntimeError("synthetic private lost response")

    def diff(self):
        self.history.append(("diff",))
        if not self.templates_ready:
            raise ReleaseError("ACTIVATION_TEMPLATE_NOT_READY")

    def validate_model_cutover(self, configurations, now):
        self.history.append(("model_cutover_preflight",))
        if not self.templates_ready:
            raise ReleaseError("MODEL_TRANSITION_GRANT_MISSING")

    def deploy(self, stack, parameters=None):
        self.history.append(("deploy", stack))
        if stack == "TtobakGatewayStack":
            assert parameters == {"HttpOriginSecretArn": ARN, "HttpOriginSecretVersionId": self.current}
            if self.fail_active_consumers and self.doc()["phase"] == "active":
                self.fail_active_consumers = False
                # Model a failed update followed by stable rollback: the dual
                # version still accepts the producer's new current proof.
                raise ReleaseError("CDK_COMMAND_FAILED")
            prior_env = self.functions["ttobak-qa"]["Environment"]["Variables"]
            if prior_env.get("ORIGIN_VERIFY_SECRET_VERSION_ID") != self.current:
                self.previous_document = (
                    self.versions[prior_env["ORIGIN_VERIFY_SECRET_VERSION_ID"]]
                    if "ORIGIN_VERIFY_SECRET_VERSION_ID" in prior_env else {"current": OLD})
                self.ready_at = self.now + 63
                self.sequence += 1
                self.alias_version = str(self.sequence)
                self.alias_revision = f"alias-{self.sequence}"
                for name, config in self.functions.items():
                    config["Environment"]["Variables"] = {"ORIGIN_VERIFY_SECRET_ARN": ARN,
                                                          "ORIGIN_VERIFY_SECRET_VERSION_ID": self.current}
                    config["RevisionId"] = f"revision-{self.sequence}"
                    if name.endswith(":live"):
                        config["Version"] = self.alias_version
        if stack == "TtobakFrontendStack":
            version = parameters["HttpOriginSecretVersionId"]
            upcoming = self.versions[version]["current"]
            if self.fail_frontend and self.versions[version]["phase"] == "rotating":
                self.fail_frontend = False
                raise ReleaseError("CDK_COMMAND_FAILED")
            assert all(self.accepts(name, upcoming) for name in self.functions), "active-old-cache cutover gap"
            self.proof = upcoming

    def public_status(self, url):
        if "execute-api" in url:
            return 403
        if self.mutate_final_public and self.doc()["phase"] == "active":
            self.functions["ttobak-qa"]["RevisionId"] = "changed-before-receipt"
            self.mutate_final_public = False
        return 200 if self.accepts("ttobak-mcp", self.proof) else 403

    def release(self):
        clients = {name: self for name in ("lambda", "secrets", "cloudfront", "cfn", "gateway", "iam")}
        return Release(clients, self, account=ACCOUNT, region=REGION, revision="a" * 40,
                       now=lambda: self.now, sleep=self.sleep, probe=self.public_status)


class ReleaseTests(unittest.TestCase):
    def quiet_run(self, release):
        output = io.StringIO()
        with redirect_stdout(output), mock.patch("http_origin_release.secrets.token_urlsafe", return_value=NEW):
            result = release.run()
        self.assertNotIn(OLD, output.getvalue())
        self.assertNotIn(NEW, output.getvalue())
        return result

    def test_complete_migration_preserves_proof_until_pinned_consumers_drain(self):
        world = World()
        result = self.quiet_run(world.release())
        self.assertEqual(result["status"], "complete")
        self.assertEqual(world.doc()["phase"], "active")
        self.assertEqual(world.doc()["previous"], "")
        self.assertEqual(world.proof, NEW)
        retired = world.doc()["retiredVersionId"]
        self.assertEqual(world.labels[RETIRED_STAGE], retired)
        self.assertNotEqual(world.labels["AWSPREVIOUS"], retired)
        self.assertTrue(all(world.accepts(name, NEW) and not world.accepts(name, OLD) for name in world.functions))
        base_deploys = [entry[1] for entry in world.history if entry[0] == "deploy"][:len(STACKS)]
        self.assertEqual(base_deploys, list(STACKS))
        self.assertNotIn("TtobakKnowledgeStack", base_deploys)
        self.assertGreaterEqual(sum(entry[1] for entry in world.history if entry[0] == "sleep"), 3 * CACHE_WAIT)

    def test_model_preflight_failure_precedes_secret_and_stack_writes(self):
        world = World()
        with mock.patch.object(world, "validate_model_cutover",
                               side_effect=ReleaseError("MODEL_TRANSITION_GRANT_MISSING")):
            with self.assertRaisesRegex(ReleaseError, "MODEL_TRANSITION_GRANT_MISSING"):
                self.quiet_run(world.release())
        self.assertEqual(world.secret_changes, 0)
        self.assertFalse(any(entry[0] == "deploy" for entry in world.history))

    def test_api_model_cleanup_waits_for_stable_alias_before_mutations(self):
        world = World()
        with mock.patch.object(world, "validate_model_cutover", return_value=90):
            self.quiet_run(world.release())
        sleep_index = world.history.index(("sleep", 90))
        write_index = next(i for i, item in enumerate(world.history) if item[0] in ("create_secret", "deploy"))
        self.assertLess(sleep_index, write_index)

    def test_alias_change_during_model_cleanup_stops_before_mutations(self):
        world = World()
        world.mutate_on_sleep = True
        with mock.patch.object(world, "validate_model_cutover", return_value=90):
            with self.assertRaisesRegex(ReleaseError, "MODEL_API_DRAIN_CHANGED"):
                self.quiet_run(world.release())
        self.assertEqual(world.secret_changes, 0)
        self.assertFalse(any(item[0] == "deploy" for item in world.history))

    def test_rotation_accepts_qa_identity_denial_and_legacy_consumer_transition(self):
        for initial_status, deployed_status in ((403, 403), (405, 403), (405, 405)):
            with self.subTest(initial=initial_status, deployed=deployed_status):
                world = World()
                world.qa_status = initial_status
                deploy = world.deploy

                def consumer_transition(stack, parameters=None):
                    deploy(stack, parameters)
                    if stack == "TtobakGatewayStack":
                        world.qa_status = deployed_status

                with mock.patch.object(world, "deploy", side_effect=consumer_transition):
                    result = self.quiet_run(world.release())
                self.assertEqual(result["status"], "complete")
                self.assertEqual(world.proof, NEW)
                self.assertEqual(world.doc()["previous"], "")
                self.assertTrue(all(not world.accepts(name, OLD) for name in world.functions))

    def probe_phase(self, phase):
        world = World()
        if phase == "readers":
            self.quiet_run(world.release())
        release = world.release()
        release.inspect()
        if phase == "readers":
            with redirect_stdout(io.StringIO()):
                release.verify_foundation()
            return world, release, lambda: release.verify_readers(retired=OLD)
        return world, release, release.verify_foundation

    def test_both_phases_reject_unproven_or_wrong_consumer_responses(self):
        cases = (
            ("ttobak-qa", 403, {}),
            ("ttobak-qa", 403, {"headers": {"x-ttobak-origin-protocol": "2"}}),
            ("ttobak-qa", 403, {"headers": {"x-ttobak-origin-protocol": "1"},
                                 "multiValueHeaders": {"X-Ttobak-Origin-Protocol": ["2"]}}),
            ("ttobak-qa", 200, {"headers": {"x-ttobak-origin-protocol": "1"}}),
            ("ttobak-qa", 500, {"headers": {"x-ttobak-origin-protocol": "1"}}),
            ("ttobak-api", 403, {"headers": {"x-ttobak-origin-protocol": "1"}}),
            ("ttobak-api:live", 403, {"headers": {"x-ttobak-origin-protocol": "1"}}),
            ("ttobak-mcp", 403, {"headers": {"x-ttobak-origin-protocol": "1"}}),
        )
        for phase in ("foundation", "readers"):
            for name, status, headers in cases:
                with self.subTest(phase=phase, consumer=name, status=status, headers=headers):
                    world, release, verify = self.probe_phase(phase)
                    invoke = world.invoke
                    changes = world.secret_changes
                    deployments = sum(row[0] == "deploy" for row in world.history)

                    def response(**kwargs):
                        result = invoke(**kwargs)
                        event = json.loads(kwargs["Payload"])
                        if kwargs["FunctionName"] == name and world.accepts(name, event["headers"].get("x-origin-verify")):
                            result["Payload"].close()
                            result["Payload"] = io.BytesIO(json.dumps({"statusCode": status, **headers}).encode())
                        return result

                    code = "READER_FOUNDATION_NOT_DEPLOYED" if phase == "foundation" else "CURRENT_PROOF_NOT_ACCEPTED"
                    with mock.patch.object(world, "invoke", side_effect=response), redirect_stdout(io.StringIO()):
                        with self.assertRaisesRegex(ReleaseError, "^" + code + "$"):
                            verify()
                    self.assertEqual(world.secret_changes, changes)
                    self.assertEqual(sum(row[0] == "deploy" for row in world.history), deployments)

    def test_qa_marked_denial_never_satisfies_missing_or_retired_proof_checks(self):
        for phase, rejected_proof, code in (
            ("foundation", None, "FOUNDATION_MISSING_PROOF_NOT_REJECTED"),
            ("readers", None, "MISSING_PROOF_NOT_REJECTED"),
            ("readers", OLD, "RETIRED_PROOF_STILL_ACCEPTED"),
        ):
            with self.subTest(phase=phase, rejected=rejected_proof is None):
                world, release, verify = self.probe_phase(phase)
                invoke = world.invoke

                def response(**kwargs):
                    result = invoke(**kwargs)
                    event = json.loads(kwargs["Payload"])
                    if kwargs["FunctionName"] == "ttobak-qa" and event["headers"].get("x-origin-verify") == rejected_proof:
                        result["Payload"].close()
                        result["Payload"] = io.BytesIO(json.dumps({"statusCode": 403,
                            "headers": {"x-ttobak-origin-protocol": "1"}}).encode())
                    return result

                with mock.patch.object(world, "invoke", side_effect=response), redirect_stdout(io.StringIO()):
                    with self.assertRaisesRegex(ReleaseError, "^" + code + "$"):
                        verify()

    def test_missing_foundation_or_live_proof_disagreement_prevents_all_mutations(self):
        for mode in ("marker", "proof"):
            world = World()
            if mode == "marker":
                world.marker_missing_for = "ttobak-qa"
            else:
                world.proof = "different-live-origin-proof"
            with self.assertRaises(ReleaseError):
                self.quiet_run(world.release())
            self.assertEqual(world.secret_changes, 0)
            self.assertFalse(any(entry[0] == "deploy" for entry in world.history))

    def test_prepared_tool_cannot_activate_without_paired_templates(self):
        world = World()
        world.templates_ready = False
        with self.assertRaisesRegex(ReleaseError, "ACTIVATION_TEMPLATE_NOT_READY"):
            self.quiet_run(world.release())
        self.assertEqual(world.secret_changes, 0)
        self.assertFalse(any(entry[0] == "deploy" for entry in world.history))

    @staticmethod
    def activation_templates():
        environment = {"ORIGIN_VERIFY_SECRET_ARN": {"Ref": "HttpOriginSecretArn"},
                       "ORIGIN_VERIFY_SECRET_VERSION_ID": {"Ref": "HttpOriginSecretVersionId"}}
        gateway = {"Parameters": {"HttpOriginSecretArn": {}, "HttpOriginSecretVersionId": {}}, "Resources": {}}
        for name in ("ttobak-api", "ttobak-qa", "ttobak-mcp"):
            role_id = name + "Role"
            gateway["Resources"][role_id] = {"Type": "AWS::IAM::Role"}
            role = {"Fn::GetAtt": [role_id, "Arn"]}
            roles = [{"Ref": role_id}]
            gateway["Resources"][name] = {"Type": "AWS::Lambda::Function", "DependsOn": [name + "Read"], "Properties": {
                "FunctionName": name, "Role": role, "Environment": {"Variables": copy.deepcopy(environment)}}}
            gateway["Resources"][name + "Read"] = {"Type": "AWS::IAM::Policy", "Properties": {"Roles": roles,
                "PolicyDocument": {"Statement": [{"Effect": "Allow", "Action": "secretsmanager:GetSecretValue",
                                                  "Resource": {"Ref": "HttpOriginSecretArn"}}]}}}
        gateway["Resources"]["Version"] = {"Type": "AWS::Lambda::Version", "Properties": {
            "FunctionName": {"Ref": "ApiFunction"}, "Description": {"Ref": "HttpOriginSecretVersionId"}}}
        frontend = {"Parameters": {"HttpOriginSecretVersionId": {}}, "Resources": {
            "Distribution": {"Type": "AWS::CloudFront::Distribution", "Properties": {
                "DistributionConfig": {"Origins": [{"OriginCustomHeaders": [{
                    "HeaderName": "x-origin-verify", "HeaderValue": {"Fn::Join": ["", [
                        "{{resolve:secretsmanager:", {"Ref": "Arn"}, ":SecretString:current::",
                        {"Ref": "HttpOriginSecretVersionId"}, "}}"]]}}]}]}}}}}
        return gateway, frontend

    def test_actual_template_gate_requires_consumer_secret_read_grants(self):
        breaks = {
            "ACTIVATION_SECRET_READ_NOT_GRANTED": (
                lambda g: g["Resources"]["ttobak-qa"].pop("DependsOn"),
                lambda g: g["Resources"]["ttobak-qaRead"]["Properties"]["PolicyDocument"]["Statement"][0].update(Resource="*"),
                lambda g: g["Resources"]["ttobak-mcpRead"]["Properties"].update(Roles=[{"Ref": "OtherRole"}]),
                lambda g: g["Resources"]["ttobak-apiRead"]["Properties"]["PolicyDocument"]["Statement"][0].update(Effect="Deny"),
            ),
            "ACTIVATION_CONSUMER_VPC_UNVERIFIED": (
                lambda g: g["Resources"]["ttobak-api"]["Properties"].update(VpcConfig={"SubnetIds": ["subnet"]}),),
        }
        runner = Cdk(REGION, ACCOUNT)
        for code, mutations in breaks.items():
            for mutate in mutations:
                gateway, frontend = self.activation_templates()
                mutate(gateway)
                with tempfile.TemporaryDirectory() as directory:
                    runner.assembly = Path(directory)
                    (runner.assembly / "TtobakGatewayStack.template.json").write_text(json.dumps(gateway))
                    (runner.assembly / "TtobakFrontendStack.template.json").write_text(json.dumps(frontend))
                    with self.assertRaisesRegex(ReleaseError, f"^{code}$"):
                        runner.validate_activation_templates()

    def test_actual_template_gate_requires_pins_and_rejects_plaintext_without_printing_it(self):
        runner = Cdk(REGION, ACCOUNT)
        gateway, frontend = self.activation_templates()
        with tempfile.TemporaryDirectory() as directory:
            runner.assembly = Path(directory)
            (runner.assembly / "TtobakGatewayStack.template.json").write_text(json.dumps(gateway))
            (runner.assembly / "TtobakFrontendStack.template.json").write_text(json.dumps(frontend))
            runner.validate_activation_templates()
            gateway["Resources"]["ttobak-api"]["Properties"]["Environment"]["Variables"]["ORIGIN_VERIFY_SECRET"] = OLD
            (runner.assembly / "TtobakGatewayStack.template.json").write_text(json.dumps(gateway))
            with self.assertRaisesRegex(ReleaseError, "^ACTIVATION_CONSUMER_NOT_PINNED$"):
                runner.validate_activation_templates()

    def test_preflight_pins_revision_and_catches_change_before_bootstrap(self):
        for field in ("RevisionId", "CodeSha256"):
            world = World()
            release = world.release()
            release.inspect()
            with redirect_stdout(io.StringIO()):
                release.verify_foundation()
            world.functions["ttobak-qa"][field] = "changed"
            with redirect_stdout(io.StringIO()), self.assertRaisesRegex(ReleaseError, "CONSUMER_CHANGED_AFTER_PREFLIGHT"):
                release.bootstrap(OLD)
            self.assertEqual(world.secret_changes, 0)

    def test_consumer_change_during_drain_is_not_adopted(self):
        world = World()
        world.mutate_on_sleep = True
        with self.assertRaisesRegex(ReleaseError, "CONSUMER_CHANGED_AFTER_DEPLOY"):
            self.quiet_run(world.release())
        self.assertEqual(world.doc()["phase"], "seeded")
        self.assertEqual(world.proof, OLD)

    def test_abort_before_producer_keeps_dual_state_and_resume_completes(self):
        world = World()
        world.fail_frontend = True
        with self.assertRaisesRegex(ReleaseError, "CDK_COMMAND_FAILED"):
            self.quiet_run(world.release())
        self.assertEqual(world.doc()["phase"], "rotating")
        self.assertEqual(world.proof, OLD)
        self.assertTrue(all(world.accepts(name, OLD) for name in world.functions))
        deadline = world.doc()["previousExpiresAt"]
        result = self.quiet_run(world.release())
        self.assertEqual(result["status"], "complete")
        retired_dual = [doc for doc in world.versions.values() if doc["phase"] == "rotating"]
        self.assertTrue(all(doc["previousExpiresAt"] == deadline for doc in retired_dual))

    def test_lost_promotion_response_is_reconciled_without_repeating_write(self):
        world = World()
        world.lose_promotion_reply = True
        self.assertEqual(self.quiet_run(world.release())["status"], "complete")
        self.assertEqual(sum(entry[0] == "put_secret" for entry in world.history), 2)

    def test_pending_write_failure_leaves_current_proof_and_resumes_from_authoritative_state(self):
        world = World()
        world.fail_pending_write = True
        with self.assertRaises(RuntimeError):
            self.quiet_run(world.release())
        self.assertEqual(world.doc()["phase"], "seeded")
        self.assertEqual(world.proof, OLD)
        self.assertTrue(all(world.accepts(name, OLD) for name in world.functions))
        self.assertEqual(self.quiet_run(world.release())["status"], "complete")

    def test_late_configuration_change_cannot_receive_complete_receipt(self):
        world = World()
        world.mutate_final_public = True
        with self.assertRaisesRegex(ReleaseError, "CONSUMER_CHANGED_AFTER_PREFLIGHT"):
            self.quiet_run(world.release())

    def test_active_release_is_idempotent_and_does_not_rotate_again(self):
        world = World()
        self.quiet_run(world.release())
        changes, versions = world.secret_changes, set(world.versions)
        self.assertEqual(self.quiet_run(world.release())["status"], "complete")
        self.assertEqual(world.secret_changes, changes)
        self.assertEqual(set(world.versions), versions)

    def test_expired_mixed_state_fails_before_deployment_and_never_renews_deadline(self):
        world = World()
        world.fail_frontend = True
        with self.assertRaises(ReleaseError):
            self.quiet_run(world.release())
        seed = world.doc()["retiredVersionId"]
        world.functions["ttobak-qa"]["Environment"]["Variables"]["ORIGIN_VERIFY_SECRET_VERSION_ID"] = seed
        world.now = world.doc()["previousExpiresAt"] + 1
        changes = world.secret_changes
        before = sum(entry[0] == "deploy" for entry in world.history)
        with self.assertRaisesRegex(ReleaseError, "EXPIRED_MIXED_CUTOVER_REQUIRES_OPERATOR"):
            self.quiet_run(world.release())
        self.assertEqual(world.secret_changes, changes)
        self.assertEqual(sum(entry[0] == "deploy" for entry in world.history), before)

    def test_near_deadline_mixed_state_stops_before_mutation(self):
        world = World()
        world.fail_frontend = True
        with self.assertRaises(ReleaseError):
            self.quiet_run(world.release())
        world.functions["ttobak-qa"]["Environment"]["Variables"]["ORIGIN_VERIFY_SECRET_VERSION_ID"] = world.doc()["retiredVersionId"]
        world.now = world.doc()["previousExpiresAt"] - CACHE_WAIT
        before, changes = len(world.history), world.secret_changes
        with self.assertRaisesRegex(ReleaseError, "INSUFFICIENT_ROTATION_WINDOW"):
            self.quiet_run(world.release())
        self.assertEqual(world.secret_changes, changes)
        self.assertFalse(any(entry[0] == "deploy" for entry in world.history[before:]))

    def test_expired_dual_consumers_roll_forward_without_restoring_old_proof(self):
        world = World()
        world.fail_frontend = True
        with self.assertRaises(ReleaseError):
            self.quiet_run(world.release())
        world.now = world.doc()["previousExpiresAt"] + 1
        self.assertFalse(any(world.accepts(name, OLD) for name in world.functions))
        receipt = self.quiet_run(world.release())
        self.assertEqual(receipt["status"], "complete")
        self.assertTrue(receipt["recoveredExpiredCutover"])
        self.assertTrue(all(world.accepts(name, NEW) and not world.accepts(name, OLD) for name in world.functions))

    def test_expired_rotation_reports_recovery_only_when_a_consumer_had_rejected_the_producer(self):
        for drifted in (True, False):
            world = World()
            release = world.release()
            # Fail after CloudFront already sends the new proof, before retirement.
            release.probe = lambda url: 500 if world.proof == NEW else world.public_status(url)
            with self.assertRaisesRegex(ReleaseError, "CLOUDFRONT_PROBE_FAILED"):
                self.quiet_run(release)
            self.assertEqual((world.doc()["phase"], world.proof), ("rotating", NEW))
            if drifted:
                world.functions["ttobak-qa"]["Environment"]["Variables"] = {"ORIGIN_VERIFY_SECRET": OLD}
            world.now = world.doc()["previousExpiresAt"] + 1
            self.assertEqual(world.accepts("ttobak-qa", NEW), not drifted)
            receipt = self.quiet_run(world.release())
            self.assertEqual(receipt["status"], "complete")
            self.assertEqual(receipt["recoveredExpiredCutover"], drifted)
            self.assertTrue(all(world.accepts(name, NEW) and not world.accepts(name, OLD) for name in world.functions))

    def test_failed_cleanup_deployment_resumes_from_active_state(self):
        world = World()
        world.fail_active_consumers = True
        with self.assertRaisesRegex(ReleaseError, "CDK_COMMAND_FAILED"):
            self.quiet_run(world.release())
        self.assertEqual(world.doc()["phase"], "active")
        self.assertEqual(world.proof, NEW)
        self.assertTrue(all(world.accepts(name, NEW) for name in world.functions))
        versions = set(world.versions)
        self.assertEqual(self.quiet_run(world.release())["status"], "complete")
        self.assertEqual(set(world.versions), versions)

    def test_cdk_never_prints_retired_proof_or_deploys_dependency_closure(self):
        runner = Cdk(REGION, ACCOUNT)
        with mock.patch("http_origin_release.subprocess.run", return_value=SimpleNamespace(returncode=0)) as run, \
                mock.patch.object(runner, "validate_activation_templates"), \
                mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                mock.patch.object(runner, "assembly_hash", return_value="reviewed-assembly"):
            runner.diff()
            runner.deploy("TtobakGatewayStack", {"HttpOriginSecretArn": ARN, "HttpOriginSecretVersionId": "a" * 32})
        for call in run.call_args_list:
            arguments = call.args[0]
            self.assertIn("--exclusively", arguments)
            self.assertNotIn("--all", arguments)
            self.assertNotIn("TtobakKnowledgeStack", arguments)
            self.assertEqual(call.kwargs["stdout"], -3)
            self.assertEqual(call.kwargs["stderr"], -3)
        deploy_args = run.call_args_list[-1].args[0]
        self.assertEqual(deploy_args[deploy_args.index("--app") + 1], str(runner.assembly))

    def test_canary_normalizes_go_and_python_headers_and_rejects_conflicting_markers(self):
        world = World()
        release = world.release()
        release.inspect()
        for headers in (
            {"headers": None, "multiValueHeaders": {"X-Ttobak-Origin-Protocol": ["1"]}},
            {"multiValueHeaders": {"x-ttobak-origin-protocol": ["1"]}},
            {"headers": {"X-Ttobak-Origin-Protocol": "1"}},
        ):
            result = {"statusCode": 404, **headers}
            with mock.patch.object(world, "invoke", return_value={
                "StatusCode": 200, "ExecutedVersion": "$LATEST",
                "Payload": io.BytesIO(json.dumps(result).encode()),
            }):
                self.assertEqual(release.canary("ttobak-api", OLD), (404, "1"))
        result = {"statusCode": 404, "headers": {"x-ttobak-origin-protocol": "1"},
                  "multiValueHeaders": {"X-Ttobak-Origin-Protocol": ["2"]}}
        with mock.patch.object(world, "invoke", return_value={
            "StatusCode": 200, "ExecutedVersion": "$LATEST",
            "Payload": io.BytesIO(json.dumps(result).encode()),
        }):
            self.assertEqual(release.canary("ttobak-api", OLD), (404, None))

    def test_checkout_requires_clean_matching_revision(self):
        # CI exports the real GITHUB_SHA; pin or clear it so each case is deterministic.
        for environment in ({"GITHUB_SHA": "a" * 40}, {}):
            with mock.patch.dict("os.environ", environment, clear=True), \
                    mock.patch("http_origin_release.subprocess.check_output",
                               side_effect=["a" * 40, b" M infra/cdk.json"]):
                with self.assertRaisesRegex(ReleaseError, "CHECKOUT_NOT_CLEAN"):
                    checkout_revision()
        with mock.patch.dict("os.environ", {"GITHUB_SHA": "a" * 40}, clear=True), \
                mock.patch("http_origin_release.subprocess.check_output", side_effect=["a" * 40, b""]):
            self.assertEqual(checkout_revision(), "a" * 40)
        with mock.patch.dict("os.environ", {"GITHUB_SHA": "b" * 40}), \
                mock.patch("http_origin_release.subprocess.check_output", return_value="a" * 40):
            with self.assertRaisesRegex(ReleaseError, "CHECKOUT_REVISION_MISMATCH"):
                checkout_revision()

    def test_deploy_rejects_source_and_reviewed_template_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = Cdk(REGION, ACCOUNT)
            runner.assembly = Path(directory)
            (runner.assembly / "manifest.json").write_text(json.dumps({"version": "1", "artifacts": {
                "TtobakStorageStack": {"type": "aws:cloudformation:stack", "dependencies": []}}}))
            template = runner.assembly / "TtobakStorageStack.template.json"
            template.write_text('{"Resources":{}}')
            with mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                    mock.patch("http_origin_release.subprocess.run", return_value=SimpleNamespace(returncode=0)) as run:
                runner.diff(("TtobakStorageStack",), activation=False)
                template.write_text('{"Resources":{"Changed":{}}}')
                with self.assertRaisesRegex(ReleaseError, "REVIEWED_ASSEMBLY_CHANGED"):
                    runner.deploy("TtobakStorageStack")
                self.assertEqual(run.call_count, 1)
                template.write_text('{"Resources":{}}')
                with mock.patch("http_origin_release.checkout_revision", return_value="b" * 40):
                    with self.assertRaisesRegex(ReleaseError, "CHECKOUT_REVISION_CHANGED"):
                        runner.deploy("TtobakStorageStack")
                self.assertEqual(run.call_count, 1)

    def test_retighten_cannot_bypass_revision_guard(self):
        session = SimpleNamespace(client=lambda *args, **kwargs: SimpleNamespace(
            get_caller_identity=lambda: {"Account": ACCOUNT}))
        modules = {"boto3": SimpleNamespace(Session=lambda **kwargs: session),
                   "botocore.config": SimpleNamespace(Config=lambda **kwargs: None),
                   "botocore.exceptions": SimpleNamespace(ClientError=type("SdkError", (Exception,), {}))}
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.dict("sys.modules", modules), \
                mock.patch("sys.argv", ["release", "retighten", "--receipt", str(Path(directory) / "receipt.json")]), \
                mock.patch("http_origin_release.checkout_revision", side_effect=ReleaseError("CHECKOUT_REVISION_MISMATCH")), \
                mock.patch.object(Cdk, "deploy") as deploy, \
                mock.patch("sys.stderr", io.StringIO()):
            self.assertEqual(main(), 1)
            deploy.assert_not_called()

    def test_invoke_access_denied_has_a_fixed_receipt_before_any_mutation(self):
        try:
            from botocore.exceptions import ClientError
        except ImportError:
            self.skipTest("botocore is required only for this SDK exception boundary test")
        for denied_name in ("ttobak-api:live", "ttobak-api", "ttobak-qa", "ttobak-mcp"):
            with self.subTest(consumer=denied_name):
                world = World()
                release = world.release()
                original_invoke = world.invoke
                private_message = "synthetic-private-provider-diagnostic"

                def invoke(**kwargs):
                    if kwargs["FunctionName"] == denied_name:
                        raise ClientError({"Error": {"Code": "AccessDeniedException",
                                                    "Message": private_message}}, "Invoke")
                    return original_invoke(**kwargs)

                session = SimpleNamespace(client=lambda *args, **kwargs: SimpleNamespace(
                    get_caller_identity=lambda: {"Account": ACCOUNT}))
                with tempfile.TemporaryDirectory() as directory, \
                        mock.patch.dict("sys.modules", {"boto3": SimpleNamespace(Session=lambda **kwargs: session)}), \
                        mock.patch("sys.argv", ["release", "release", "--receipt", str(Path(directory) / "receipt.json")]), \
                        mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                        mock.patch("http_origin_release.Release", return_value=release), \
                        mock.patch.object(world, "invoke", side_effect=invoke), \
                        mock.patch("sys.stdout", io.StringIO()) as stdout, \
                        mock.patch("sys.stderr", io.StringIO()) as stderr:
                    self.assertEqual(main(), 1)
                    receipt = json.loads((Path(directory) / "receipt.json").read_text())
                self.assertEqual(receipt["status"], "incomplete")
                self.assertEqual(receipt["code"], "CONSUMER_INVOKE_ACCESS_DENIED")
                self.assertEqual(world.secret_changes, 0)
                self.assertFalse(any(entry[0] == "deploy" for entry in world.history))
                for value in (stdout.getvalue(), stderr.getvalue(), json.dumps(receipt)):
                    self.assertNotIn(private_message, value)
                    self.assertNotIn(OLD, value)
                    self.assertNotIn(NEW, value)

    def test_other_errors_are_not_misclassified_as_consumer_invoke_permissions(self):
        try:
            from botocore.exceptions import ClientError
        except ImportError:
            self.skipTest("botocore is required only for this SDK exception boundary test")
        private_message = "synthetic-private-provider-diagnostic"
        cases = [
            (ClientError({"Error": {"Code": "AccessDeniedException", "Message": private_message}},
                         "GetSecretValue"), "OPERATION_FAILED_OR_OUTCOME_UNKNOWN"),
            (ClientError({"Error": {"Code": "KMSAccessDeniedException", "Message": private_message}},
                         "Invoke"), "OPERATION_FAILED_OR_OUTCOME_UNKNOWN"),
            (ClientError({"Error": {"Code": "ResourceNotFoundException", "Message": private_message}},
                         "Invoke"), "OPERATION_FAILED_OR_OUTCOME_UNKNOWN"),
            (ClientError({"Error": {"Code": "AccessDenied", "Message": private_message}},
                         "Invoke"), "OPERATION_FAILED_OR_OUTCOME_UNKNOWN"),
            (RuntimeError("AccessDeniedException " + private_message), "OPERATION_FAILED_OR_OUTCOME_UNKNOWN"),
            (ReleaseError("CONSUMER_CHANGED_DURING_PREFLIGHT"), "CONSUMER_CHANGED_DURING_PREFLIGHT"),
        ]
        for error, expected in cases:
            with self.subTest(error_type=type(error).__name__, operation=getattr(error, "operation_name", None)):
                world = World()
                release = world.release()
                session = SimpleNamespace(client=lambda *args, **kwargs: SimpleNamespace(
                    get_caller_identity=lambda: {"Account": ACCOUNT}))
                with tempfile.TemporaryDirectory() as directory, \
                        mock.patch.dict("sys.modules", {"boto3": SimpleNamespace(Session=lambda **kwargs: session)}), \
                        mock.patch("sys.argv", ["release", "release", "--receipt", str(Path(directory) / "receipt.json")]), \
                        mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                        mock.patch("http_origin_release.Release", return_value=release), \
                        mock.patch.object(release, "run", side_effect=error), \
                        mock.patch("sys.stderr", io.StringIO()) as stderr:
                    self.assertEqual(main(), 1)
                    receipt = json.loads((Path(directory) / "receipt.json").read_text())
                self.assertEqual(receipt["code"], expected)
                self.assertNotIn(private_message, stderr.getvalue())
                self.assertNotIn(private_message, json.dumps(receipt))


class CdkAssemblyCacheTests(unittest.TestCase):
    """Synthetic malformed archives plus deploy guards; never deploy or extract."""
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.runner = Cdk(REGION, ACCOUNT)
        self.runner.assembly = Path(self.directory.name)
        self.staged = self.runner.assembly / "asset.synthetic"
        (self.staged / "nested").mkdir(parents=True)
        (self.staged / "empty").mkdir()
        (self.staged / ".cache").mkdir()
        for name, data, mode in (
            ("bootstrap", b"executable\0" * 20000, 0o755),
            (".DS_Store", b"dot file", 0o644),
            (".lock", b"staged lock is an input", 0o600),
            (".cache/.lock", b"nested cache lock is also an input", 0o600),
            ("nested/한국어.txt", b"UTF-8 member", 0o640),
        ):
            path = self.staged / name
            path.write_bytes(data)
            path.chmod(mode)
        self.asset_id = "a" * 64
        self.asset_manifest = self.runner.assembly / "Synthetic.assets.json"
        self.asset_manifest.write_text(json.dumps({"files": {
            self.asset_id: {"source": {"path": self.staged.name, "packaging": "zip"}}}}))
        self.manifest = self.runner.assembly / "manifest.json"
        artifacts = {stack: {"type": "aws:cloudformation:stack", "dependencies": ["Synthetic.assets"]}
                     for stack in STACKS}
        artifacts["Synthetic.assets"] = {
            "type": "cdk:asset-manifest", "properties": {"file": self.asset_manifest.name}}
        self.manifest.write_text(json.dumps({"artifacts": artifacts}))
        self.template = self.runner.assembly / "TtobakStorageStack.template.json"
        self.template.write_text('{"Resources":{}}')
        self.cache = self.runner.assembly / ".cache" / (self.asset_id + ".zip")

    def entries(self):
        return [(path.relative_to(self.staged).as_posix(), path.read_bytes(), path.stat().st_mode & 0xffff)
                for path in sorted(self.staged.rglob("*")) if path.is_file()]

    @staticmethod
    def archive_bytes(entries):
        # Test-only fixture for the pinned format. The private evidence packet
        # separately exercises actual installed CDK packageFile bytes.
        local, central = bytearray(), bytearray()
        for name, data, mode in entries:
            name = name.encode("utf-8")
            compressor = zlib.compressobj(wbits=-15)
            compressed = compressor.compress(data) + compressor.flush()
            crc = zlib.crc32(data)
            offset = len(local)
            local += struct.pack("<4s5H3I2H", b"PK\x03\x04", 20, 0x800, 8, 0, 33,
                                 crc, len(compressed), len(data), len(name), 0) + name + compressed
            central += struct.pack("<4s6H3I5H2I", b"PK\x01\x02", 0x33f, 20, 0x800, 8, 0, 33,
                                   crc, len(compressed), len(data), len(name), 0, 0, 0, 0,
                                   mode << 16, offset) + name
        return bytes(local + central + struct.pack("<4s4H2IH", b"PK\x05\x06", 0, 0, len(entries),
                                                  len(entries), len(central), len(local), 0))

    def write_cache(self, entries=None):
        self.cache.parent.mkdir(exist_ok=True)
        self.cache.write_bytes(self.archive_bytes(self.entries() if entries is None else entries))

    def assert_cache_invalid(self):
        with self.assertRaisesRegex(ReleaseError, "^REVIEWED_ASSEMBLY_CACHE_INVALID$"):
            self.runner.assembly_hash()

    def test_declared_cache_generation_and_reuse_preserve_reviewed_input_digest(self):
        reviewed = self.runner.assembly_hash()
        self.write_cache()
        self.assertEqual(self.runner.assembly_hash(), reviewed)
        self.assertEqual(self.runner.assembly_hash(), reviewed)
        self.assertEqual(self.runner.validated_asset_caches(), {self.cache})

    def test_valid_members_at_and_after_output_boundaries_preserve_digest(self):
        member = self.staged / "bootstrap"
        for size in (65536, 65537, 65538, 131072, 131073, 131074):
            for byte in (b"A", b"x", b"\0"):
                with self.subTest(size=size, byte=byte):
                    self.cache.unlink(missing_ok=True)
                    member.write_bytes(byte * size)
                    reviewed = self.runner.assembly_hash()
                    self.write_cache()
                    self.assertEqual(self.runner.assembly_hash(), reviewed)
                    self.assertEqual(self.runner.assembly_hash(), reviewed)

    def test_buffered_last_byte_must_match_the_staged_source(self):
        member = self.staged / "bootstrap"
        member.write_bytes(b"A" * 65537)
        entries = self.entries()
        changed = [(name, data[:-1] + b"B" if name == "bootstrap" else data, mode)
                   for name, data, mode in entries]
        self.write_cache(changed)
        self.assert_cache_invalid()

    def test_buffered_expansion_beyond_declared_source_size_is_rejected(self):
        member = self.staged / "bootstrap"
        data = b"A" * 65536
        member.write_bytes(data)
        mode = member.stat().st_mode & 0xffff
        # Keep the oversized DEFLATE stream, but forge both headers to claim
        # the staged size and CRC. Its last byte arrives on an empty-input drain.
        entries = [("bootstrap", data + b"A", mode)]
        entries.extend(entry for entry in self.entries() if entry[0] != "bootstrap")
        malicious = bytearray(self.archive_bytes(entries))
        central_offset = struct.unpack_from("<I", malicious, len(malicious) - 6)[0]
        for offset in (14, central_offset + 16):
            struct.pack_into("<I", malicious, offset, zlib.crc32(data))
        for offset in (22, central_offset + 24):
            struct.pack_into("<I", malicious, offset, len(data))
        self.cache.parent.mkdir()
        self.cache.write_bytes(malicious)
        self.assert_cache_invalid()

    def test_existing_cache_is_validated_before_any_deploy(self):
        self.write_cache()
        self.runner.revision = "a" * 40
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        self.runner.assembly_digest = self.runner.assembly_hash()
        entries = self.entries()
        name, data, mode = entries[0]
        entries[0] = (name, data + b"unreviewed", mode)
        self.write_cache(entries)
        with mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                mock.patch.object(self.runner, "command") as command:
            with self.assertRaisesRegex(ReleaseError, "REVIEWED_ASSEMBLY_CACHE_INVALID"):
                self.runner.deploy("TtobakStorageStack")
            command.assert_not_called()

    def test_deploy_allows_generated_cache_but_rejects_template_change_after_command(self):
        self.runner.revision = "a" * 40
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        self.runner.assembly_digest = self.runner.assembly_hash()
        with mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                mock.patch.object(self.runner, "command", side_effect=lambda *a, **kw: self.write_cache()):
            self.runner.deploy("TtobakStorageStack")
        with mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                mock.patch.object(self.runner, "command",
                                  side_effect=lambda *a, **kw: self.template.write_text('{"changed":true}')):
            with self.assertRaisesRegex(ReleaseError, "REVIEWED_ASSEMBLY_CHANGED"):
                self.runner.deploy("TtobakStorageStack")

    def test_source_bytes_modes_and_manifest_remain_guarded_with_matching_cache(self):
        original = self.runner.assembly_hash()
        for name, mode_only in (("bootstrap", False), ("bootstrap", True),
                                (".lock", False), (".cache/.lock", False)):
            with self.subTest(name=name, mode_only=mode_only):
                path = self.staged / name
                data, mode = path.read_bytes(), path.stat().st_mode
                if mode_only:
                    path.chmod(0o644)
                else:
                    path.write_bytes(data + b"changed")
                self.write_cache()
                self.assertNotEqual(self.runner.assembly_hash(), original)
                path.write_bytes(data)
                path.chmod(stat.S_IMODE(mode))
        self.write_cache()
        self.asset_manifest.write_text(self.asset_manifest.read_text() + "\n")
        self.assertNotEqual(self.runner.assembly_hash(), original)

    def test_unknown_nested_and_basename_trick_caches_are_rejected(self):
        self.write_cache()
        for name in ("unknown.zip", "b" * 64 + ".zip", self.asset_id.upper() + ".zip",
                     self.asset_id + ".zip._tmp", ".lock"):
            with self.subTest(name=name):
                bad = self.cache.parent / name
                bad.write_bytes(self.cache.read_bytes())
                self.assert_cache_invalid()
                bad.unlink()
        nested = self.cache.parent / "nested"
        nested.mkdir()
        self.assert_cache_invalid()

    def test_cache_and_staged_symlinks_are_rejected(self):
        self.cache.parent.mkdir()
        self.cache.symlink_to(self.template)
        with self.assertRaisesRegex(ReleaseError, "REVIEWED_ASSEMBLY_SYMLINK"):
            self.runner.assembly_hash()
        self.cache.unlink()
        (self.staged / "link").symlink_to(self.staged / "bootstrap")
        with self.assertRaisesRegex(ReleaseError, "REVIEWED_ASSEMBLY_SYMLINK"):
            self.runner.assembly_hash()

    def test_nonzip_executable_external_and_ambiguous_declarations_cannot_exempt_cache(self):
        self.write_cache()
        for source in (
            {"path": self.staged.name, "packaging": "file"},
            {"path": self.staged.name, "packaging": "zip", "executable": ["command"]},
            {"path": str(self.staged), "packaging": "zip"},
            {"path": "../outside", "packaging": "zip"},
            {"path": ".cache", "packaging": "zip"},
        ):
            with self.subTest(source=source):
                self.asset_manifest.write_text(json.dumps({"files": {self.asset_id: {"source": source}}}))
                self.assert_cache_invalid()
        self.asset_manifest.write_text(json.dumps({"files": {self.asset_id: {
            "source": {"path": self.staged.name, "packaging": "zip"}}}}))
        duplicate = self.runner.assembly / "Other.assets.json"
        (self.runner.assembly / "asset.other").mkdir()
        duplicate.write_text(json.dumps({"files": {self.asset_id: {
            "source": {"path": "asset.other", "packaging": "zip"}}}}))
        data = json.loads(self.manifest.read_text())
        data["artifacts"]["Other.assets"] = {
            "type": "cdk:asset-manifest", "properties": {"file": duplicate.name}}
        self.manifest.write_text(json.dumps(data))
        self.assert_cache_invalid()

    def test_external_source_is_rejected_before_deploy_even_without_cache(self):
        self.asset_manifest.write_text(json.dumps({"files": {self.asset_id: {
            "source": {"path": str(self.staged), "packaging": "zip"}}}}))
        self.runner.revision = "a" * 40
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        self.runner.assembly_digest = "reviewed-before-external-source-change"
        with mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                mock.patch.object(self.runner, "command") as command:
            with self.assertRaisesRegex(ReleaseError, "REVIEWED_ASSEMBLY_CACHE_INVALID"):
                self.runner.deploy("TtobakStorageStack")
            command.assert_not_called()

    def add_unselected_external_zip(self):
        other = self.runner.assembly / "Gateway.assets.json"
        other.write_text(json.dumps({"files": {"b" * 64: {"source": {
            "path": str(self.staged), "packaging": "zip"}}}}))
        data = json.loads(self.manifest.read_text())
        data["artifacts"]["Gateway.assets"] = {
            "type": "cdk:asset-manifest", "properties": {"file": other.name}}
        data["artifacts"]["TtobakGatewayStack"]["dependencies"] = ["Gateway.assets", "TtobakStorageStack"]
        # The deliberately unsafe unselected Knowledge declaration must never
        # become an implicit stack-closure review or deployment prerequisite.
        data["artifacts"]["TtobakKnowledgeStack"] = {
            "type": "aws:cloudformation:stack", "dependencies": ["Gateway.assets"]}
        data["artifacts"]["TtobakStorageStack"]["dependencies"].append("TtobakKnowledgeStack")
        self.manifest.write_text(json.dumps(data))
        return other

    def test_storage_only_review_preserves_retighten_without_following_stack_closure(self):
        self.add_unselected_external_zip()
        self.write_cache()
        with mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                mock.patch.object(self.runner, "command") as command:
            self.runner.diff(("TtobakStorageStack",), activation=False)
            reviewed = self.runner.assembly_digest
            self.runner.deploy("TtobakStorageStack")
            self.assertEqual(command.call_count, 2)
            self.assertEqual(self.runner.assembly_hash(), reviewed)
            with self.assertRaisesRegex(ReleaseError, "^UNREVIEWED_STACK$"):
                self.runner.deploy("TtobakGatewayStack")
            self.assertEqual(command.call_count, 2)
            with self.assertRaisesRegex(ReleaseError, "^REVIEWED_ASSEMBLY_CACHE_INVALID$"):
                self.runner.diff(activation=False)

    def test_selected_scope_and_unselected_inputs_are_in_reviewed_digest(self):
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        original = self.runner.assembly_hash()
        self.runner.reviewed_stacks = ("TtobakGatewayStack",)
        self.assertNotEqual(self.runner.assembly_hash(), original)
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        unselected = self.runner.assembly / "TtobakGatewayStack.template.json"
        unselected.write_text('{"unselected":true}')
        self.assertNotEqual(self.runner.assembly_hash(), original)

    def test_file_content_cannot_impersonate_an_added_input_in_digest_stream(self):
        first, second = self.staged / "zz-first", self.staged / "zz-second"
        payload = b"new staged source"
        second.write_bytes(payload)
        second.chmod(0o644)
        label = (str(second.relative_to(self.runner.assembly)).encode() + b"\0"
                 + stat.S_IMODE(second.stat().st_mode).to_bytes(2, "big") + b"\0")
        second.unlink()
        first.write_bytes(label + payload)
        first.chmod(0o644)
        original = self.runner.assembly_hash()
        first.write_bytes(b"")
        second.write_bytes(payload)
        second.chmod(0o644)
        self.assertNotEqual(self.runner.assembly_hash(), original)

    def test_full_release_external_source_is_rejected_before_publication_command(self):
        self.add_unselected_external_zip()
        self.runner.reviewed_stacks = STACKS
        self.runner.revision = "a" * 40
        self.runner.assembly_digest = "previous-review"
        with mock.patch("http_origin_release.checkout_revision", return_value="a" * 40), \
                mock.patch.object(self.runner, "command") as command:
            with self.assertRaisesRegex(ReleaseError, "^REVIEWED_ASSEMBLY_CACHE_INVALID$"):
                self.runner.deploy("TtobakGatewayStack")
            command.assert_not_called()

    def test_unselected_cached_external_source_cannot_be_exempted(self):
        self.add_unselected_external_zip()
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        self.write_cache()
        (self.cache.parent / ("b" * 64 + ".zip")).write_bytes(self.cache.read_bytes())
        self.assert_cache_invalid()

    def test_bad_unselected_cache_bytes_are_rejected_even_with_safe_staged_source(self):
        other = self.add_unselected_external_zip()
        other.write_text(json.dumps({"files": {"b" * 64: {"source": {
            "path": self.staged.name, "packaging": "zip"}}}}))
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        self.write_cache()
        unselected_cache = self.cache.parent / ("b" * 64 + ".zip")
        unselected_cache.write_bytes(self.cache.read_bytes())
        self.runner.assembly_hash()
        unselected_cache.write_bytes(b"unreviewed bytes")
        self.assert_cache_invalid()

    def test_missing_stack_type_and_unsafe_direct_manifest_identity_fail(self):
        self.runner.reviewed_stacks = ("TtobakStorageStack",)
        original = json.loads(self.manifest.read_text())
        malformed = copy.deepcopy(original)
        malformed["artifacts"]["TtobakStorageStack"]["type"] = "cdk:asset-manifest"
        self.manifest.write_text(json.dumps(malformed))
        self.assert_cache_invalid()
        malformed = copy.deepcopy(original)
        malformed["artifacts"]["Synthetic.assets"]["properties"]["file"] = "../outside.assets.json"
        self.manifest.write_text(json.dumps(malformed))
        self.assert_cache_invalid()

    def test_extra_duplicate_missing_traversal_and_directory_members_are_rejected(self):
        entries = self.entries()
        for malformed in (
            entries + [("extra", b"extra", stat.S_IFREG | 0o644)],
            entries + [entries[0]],
            entries[1:],
            [("../" + name, data, mode) for name, data, mode in entries],
            [("/" + name, data, mode) for name, data, mode in entries],
            [(name + "\0hidden", data, mode) for name, data, mode in entries],
            [(name.replace("/", "\\"), data, mode) for name, data, mode in entries],
            entries + [("empty/", b"", stat.S_IFDIR | 0o755)],
        ):
            with self.subTest(names=[entry[0] for entry in malformed]):
                self.write_cache(malformed)
                self.assert_cache_invalid()

    def test_changed_bytes_types_and_modes_are_rejected(self):
        entries = self.entries()
        name, data, mode = entries[0]
        for changed in ((name, data + b"extra", mode), (name, b"x" * len(data), mode),
                        (name, data, stat.S_IFLNK | 0o777), (name, data, stat.S_IFREG | 0o777)):
            with self.subTest(mode=changed[2], size=len(changed[1])):
                self.write_cache([changed] + entries[1:])
                self.assert_cache_invalid()

    def test_comments_corruption_prefixes_and_trailing_bytes_are_rejected(self):
        original = self.archive_bytes(self.entries())
        for malformed in (b"prefix" + original, original + b"trailing",
                          original[:-1], original[:-2] + b"\x01\0x",
                          original[:40] + bytes([original[40] ^ 0xff]) + original[41:]):
            with self.subTest(length=len(malformed)):
                self.cache.parent.mkdir(exist_ok=True)
                self.cache.write_bytes(malformed)
                self.assert_cache_invalid()

    def test_compressed_size_and_central_count_are_bounded_before_parsing(self):
        self.write_cache()
        original = self.cache.read_bytes()
        # Size bound rejects unbounded data without opening the ZIP parser.
        self.cache.write_bytes(original + b"x" * 400000)
        with mock.patch("http_origin_release.zipfile.ZipFile") as reader:
            self.assert_cache_invalid()
            reader.assert_not_called()
        malformed = bytearray(original)
        struct.pack_into("<H", malformed, len(malformed) - 14, 50000)
        self.cache.write_bytes(malformed)
        with mock.patch("http_origin_release.zipfile.ZipFile") as reader:
            self.assert_cache_invalid()
            reader.assert_not_called()

    def test_expansion_beyond_declared_source_size_is_rejected(self):
        entries = self.entries()
        name, data, mode = entries[0]
        malicious = bytearray(self.archive_bytes([(name, data + b"x" * 300000, mode)] + entries[1:]))
        # A forged member advertises the exact staged size/CRC, while its
        # compressed stream expands beyond that boundary. Do not let a ZIP
        # reader silently truncate the additional output to the advertised size.
        central_offset = struct.unpack_from("<I", malicious, len(malicious) - 6)[0]
        struct.pack_into("<I", malicious, 14, zlib.crc32(data))
        struct.pack_into("<I", malicious, 22, len(data))
        struct.pack_into("<I", malicious, central_offset + 16, zlib.crc32(data))
        struct.pack_into("<I", malicious, central_offset + 24, len(data))
        self.cache.parent.mkdir()
        self.cache.write_bytes(malicious)
        self.assert_cache_invalid()

    def test_noncache_hidden_inputs_are_hashed_and_local_headers_cannot_disagree(self):
        original = self.runner.assembly_hash()
        lookalike = self.runner.assembly / "nested" / ".cache"
        lookalike.mkdir(parents=True)
        (lookalike / ".lock").write_bytes(b"ordinary input")
        self.assertNotEqual(self.runner.assembly_hash(), original)
        self.write_cache()
        data = bytearray(self.cache.read_bytes())
        # Change only the local CRC, leaving the central directory intact.
        struct.pack_into("<I", data, 14, 0)
        self.cache.write_bytes(data)
        self.assert_cache_invalid()

    def test_empty_directory_asset_has_only_empty_zip_and_root_lock_is_transient(self):
        for path in self.staged.rglob("*"):
            if path.is_file():
                path.unlink()
        original = self.runner.assembly_hash()
        self.write_cache([])
        self.assertEqual(self.runner.assembly_hash(), original)
        (self.runner.assembly / ".lock").write_bytes(b"CDK lock")
        self.assertEqual(self.runner.assembly_hash(), original)


if __name__ == "__main__":
    unittest.main()
