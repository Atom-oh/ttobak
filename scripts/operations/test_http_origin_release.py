import copy
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

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
        return copy.deepcopy(self.functions[FunctionName])

    def get_alias(self, **kwargs):
        return {"FunctionVersion": self.alias_version, "RevisionId": self.alias_revision}

    def invoke(self, FunctionName, InvocationType, Payload):
        event = json.loads(Payload)
        self.history.append(("invoke", FunctionName))
        self.assert_probe(event, FunctionName)
        accepted = self.accepts(FunctionName, event["headers"].get("x-origin-verify"))
        status = Release.expected_status(FunctionName) if accepted else 403
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
            # Imported AiStack roles render as an export; the MCP role is stack-local.
            role = {"Fn::GetAtt": ["McpRole", "Arn"]} if name == "ttobak-mcp" else {"Fn::ImportValue": f"Ai:{name}"}
            roles = [{"Ref": "McpRole"}] if name == "ttobak-mcp" else [{"Fn::Select": [1, {"Fn::Split": ["/", role]}]}]
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
            (runner.assembly / "manifest.json").write_text('{"version":"1"}')
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
                   "botocore.config": SimpleNamespace(Config=lambda **kwargs: None)}
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.dict("sys.modules", modules), \
                mock.patch("sys.argv", ["release", "retighten", "--receipt", str(Path(directory) / "receipt.json")]), \
                mock.patch("http_origin_release.checkout_revision", side_effect=ReleaseError("CHECKOUT_REVISION_MISMATCH")), \
                mock.patch.object(Cdk, "deploy") as deploy, \
                mock.patch("sys.stderr", io.StringIO()):
            self.assertEqual(main(), 1)
            deploy.assert_not_called()


if __name__ == "__main__":
    unittest.main()
