"""Synthetic model-transition preflight tests; no AWS clients or customer data."""
from datetime import datetime, timezone
import json
from pathlib import Path
import tempfile
import unittest

from http_origin_release import Cdk, ReleaseError

ACCOUNT = "111111111111"
NOW = 1800000000
SOL = "global.openai.gpt-6.1-sol"
TERRA = "global.openai.gpt-6.1-terra"
LUNA = "global.openai.gpt-6-luna"


class ModelCutoverTests(unittest.TestCase):
    def fixture(self, retained=(SOL,), age=1000):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        cdk = Cdk("ap-northeast-2", ACCOUNT)
        cdk.assembly = Path(directory.name)
        resources = {}
        exports = {}
        gateway = {}
        configurations = {}
        for name, role, field, metadata, target, old, retained_models, timeout in (
            ("ttobak-summarize", "SummaryRole", "BEDROCK_SUMMARY_MODEL_ID",
             "BEDROCK_SUMMARY_RETAIN_MODEL_IDS", TERRA, SOL, retained, 900),
            ("ttobak-api", "ApiRole", "BEDROCK_TRANSLATE_MODEL_ID",
             "BEDROCK_TRANSLATE_RETAIN_MODEL_IDS", LUNA, LUNA, (), 30),
        ):
            resources[role] = {"Type": "AWS::IAM::Role", "Properties": {"RoleName": role}}
            exports[role] = {"Export": {"Name": role}, "Value": {"Fn::GetAtt": [role, "Arn"]}}
            profiles = list(dict.fromkeys((target, *retained_models)))
            arn = lambda tail: {"Fn::Join": ["", ["arn:", {"Ref": "AWS::Partition"}, tail]]}
            resources[role + "Policy"] = {
                "Type": "AWS::IAM::Policy",
                "Properties": {"Roles": [{"Ref": role}], "PolicyDocument": {"Statement": [{
                    "Sid": "InvokeFinalSummaryModel" if role == "SummaryRole" else "InvokeInterpreterModel",
                    "Effect": "Allow", "Action": "bedrock:InvokeModel",
                    "Resource": [value for profile in profiles for value in (
                        arn(":bedrock:*::foundation-model/" + profile[7:]),
                        arn(f":bedrock:*:{ACCOUNT}:inference-profile/{profile}"),
                    )],
                }]}},
            }
            gateway[role + "Function"] = {"Type": "AWS::Lambda::Function", "Properties": {
                "FunctionName": name, "Role": {"Fn::ImportValue": role},
                "Environment": {"Variables": {field: target, metadata: ",".join(retained_models)}},
            }}
            configurations[name] = {
                "FunctionArn": f"arn:aws:lambda:ap-northeast-2:{ACCOUNT}:function:{name}",
                "Environment": {"Variables": {field: old}},
                "LastModified": datetime.fromtimestamp(NOW - age, timezone.utc).isoformat(),
                "Timeout": timeout,
                "Role": f"arn:aws:iam::{ACCOUNT}:role/{role}",
            }
        configurations["ttobak-api:live"] = configurations["ttobak-api"]
        (cdk.assembly / "TtobakAiStack.template.json").write_text(json.dumps({"Resources": resources, "Outputs": exports}))
        (cdk.assembly / "TtobakGatewayStack.template.json").write_text(json.dumps({"Resources": gateway}))
        return cdk, configurations

    def test_exact_old_and_new_grants_allow_transition(self):
        cdk, configs = self.fixture()
        cdk.validate_model_cutover(configs, NOW)

    def test_changing_model_without_outgoing_grant_fails_before_release(self):
        cdk, configs = self.fixture(retained=())
        with self.assertRaisesRegex(ReleaseError, "MODEL_TRANSITION_GRANT_MISSING"):
            cdk.validate_model_cutover(configs, NOW)

    def test_grant_on_another_role_cannot_authorize_summary(self):
        cdk, configs = self.fixture()
        path = cdk.assembly / "TtobakAiStack.template.json"
        template = json.loads(path.read_text())
        template["Resources"]["SummaryRolePolicy"]["Properties"]["Roles"] = [{"Ref": "ApiRole"}]
        path.write_text(json.dumps(template))
        with self.assertRaisesRegex(ReleaseError, "MODEL_TRANSITION_GRANT_MISSING"):
            cdk.validate_model_cutover(configs, NOW)

    def test_recent_retained_model_cannot_be_removed_while_old_calls_can_run(self):
        cdk, configs = self.fixture(retained=(), age=100)
        summary = configs["ttobak-summarize"]
        summary["Environment"]["Variables"] = {
            "BEDROCK_SUMMARY_MODEL_ID": TERRA, "BEDROCK_SUMMARY_RETAIN_MODEL_IDS": SOL,
        }
        with self.assertRaisesRegex(ReleaseError, "MODEL_RETENTION_DRAIN_PENDING"):
            cdk.validate_model_cutover(configs, NOW)

    def test_retention_can_be_removed_after_the_lambda_timeout_and_margin(self):
        cdk, configs = self.fixture(retained=(), age=1000)
        configs["ttobak-summarize"]["Environment"]["Variables"] = {
            "BEDROCK_SUMMARY_MODEL_ID": TERRA, "BEDROCK_SUMMARY_RETAIN_MODEL_IDS": SOL,
        }
        cdk.validate_model_cutover(configs, NOW)

    def test_api_live_and_latest_must_both_keep_their_models(self):
        cdk, configs = self.fixture()
        configs["ttobak-api:live"] = {
            **configs["ttobak-api"],
            "Environment": {"Variables": {"BEDROCK_TRANSLATE_MODEL_ID": TERRA}},
        }
        with self.assertRaisesRegex(ReleaseError, "MODEL_TRANSITION_GRANT_MISSING"):
            cdk.validate_model_cutover(configs, NOW)

    def test_missing_summary_model_is_not_assumed_to_be_sol(self):
        cdk, configs = self.fixture()
        configs["ttobak-summarize"]["Environment"]["Variables"] = {}
        with self.assertRaisesRegex(ReleaseError, "MODEL_ACTIVE_CONFIGURATION_MISSING"):
            cdk.validate_model_cutover(configs, NOW)

    def test_retired_api_models_require_alias_drain_even_if_version_is_old(self):
        cdk, configs = self.fixture()
        configs["ttobak-api"]["Environment"]["Variables"]["BEDROCK_TRANSLATE_RETAIN_MODEL_IDS"] = TERRA
        self.assertEqual(cdk.validate_model_cutover(configs, NOW), 90)

    def test_deployed_role_cannot_differ_from_reviewed_function_role(self):
        cdk, configs = self.fixture()
        configs["ttobak-summarize"]["Role"] = f"arn:aws:iam::{ACCOUNT}:role/OtherRole"
        with self.assertRaisesRegex(ReleaseError, "MODEL_DEPLOYED_ROLE_CHANGED"):
            cdk.validate_model_cutover(configs, NOW)

    def test_recent_rollback_without_retention_metadata_still_requires_drain(self):
        cdk, configs = self.fixture(retained=(), age=1)
        configs["ttobak-summarize"]["Environment"]["Variables"] = {"BEDROCK_SUMMARY_MODEL_ID": TERRA}
        with self.assertRaisesRegex(ReleaseError, "MODEL_RETENTION_DRAIN_PENDING"):
            cdk.validate_model_cutover(configs, NOW)

    def test_api_rollback_without_retention_metadata_still_requires_alias_drain(self):
        cdk, configs = self.fixture(retained=(), age=1000)
        configs["ttobak-summarize"]["Environment"]["Variables"] = {"BEDROCK_SUMMARY_MODEL_ID": TERRA}
        self.assertEqual(cdk.validate_model_cutover(configs, NOW), 90)

    def test_unknown_summary_change_time_cannot_establish_drain(self):
        cdk, configs = self.fixture()
        configs["ttobak-summarize"].pop("LastModified")
        with self.assertRaisesRegex(ReleaseError, "MODEL_RETENTION_DRAIN_UNVERIFIED"):
            cdk.validate_model_cutover(configs, NOW)


if __name__ == "__main__":
    unittest.main()
