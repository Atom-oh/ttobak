"""agents.py Bedrock adapter (bedrock_invoke): stop reasons, retries, schema validation. Fake client only."""
import json, os, random, sys, tempfile, threading, time, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agents  # noqa: E402
from agent_fixtures import A, B, cur, ds  # noqa: E402,F401 (shared datasets, moved from test_agents.py)


class FakeClient:
    def __init__(self, replies):
        self.replies, self.n = list(replies), 0

    def converse(self, **kw):
        self.n += 1
        self.kw = kw
        r = self.replies.pop(0)
        if isinstance(r, Exception):
            raise r
        return r


def reply(content, stop="tool_use"):
    return {"output": {"message": {"content": content}}, "stopReason": stop}


SCHEMA = agents.SCHEMAS["submit_findings"]
GOOD = {"findings": [{"title": "t", "detail": "d", "so_what": "s", "datasets": [], "confidence": "높음"}]}


class Adapter(unittest.TestCase):
    def inv(self, client):
        return agents.bedrock_invoke(client=client, model_id="m", sleep=lambda s: None)

    def test_tool_use(self):
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)
        self.assertNotIn("temperature", json.dumps(c.kw.get("inferenceConfig", {})))
        self.assertEqual(c.kw["toolConfig"]["toolChoice"], {"auto": {}})

    def test_json_in_text(self):
        c = FakeClient([reply([{"text": "결과: " + json.dumps(GOOD, ensure_ascii=False)}], "end_turn")])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)

    def test_retries_then_succeeds(self):
        thr = agents.ClientError({"Error": {"Code": "ThrottlingException", "Message": "slow"}}, "Converse")
        c = FakeClient([reply([{"text": "cut"}], "max_tokens"), thr, reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)
        self.assertEqual(c.n, 3)

    def test_fail_closed_after_three(self):
        c = FakeClient([reply([{"text": "no json"}], "end_turn")] * 3)
        with self.assertRaises(agents.AgentError):
            self.inv(c)("s", "u", "submit_findings", SCHEMA)
        self.assertEqual(c.n, 3)

    def test_token_limit(self):
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.inv(c)("s", "u", "submit_findings", SCHEMA)
        self.assertEqual(c.kw["inferenceConfig"], {"maxTokens": agents.MAX_TOKENS})

    def test_transport_errors_retry(self):
        class Timeout(agents.BotoCoreError):
            pass
        c = FakeClient([Timeout(), Timeout(), reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)
        c = FakeClient([Timeout()] * 3)
        with self.assertRaises(agents.AgentError):
            self.inv(c)("s", "u", "submit_findings", SCHEMA)

    def test_blocked_stop_reason_rejected(self):
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}], "content_filtered"),
                        reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}], "guardrail_intervened"),
                        reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}], "content_filtered")])
        with self.assertRaises(agents.AgentError):
            self.inv(c)("s", "u", "submit_findings", SCHEMA)

    def test_client_error_message_not_carried(self):
        deny = agents.ClientError({"Error": {"Code": "AccessDeniedException",
                                             "Message": "User: arn:aws:sts::123456789012:assumed-role/r/x is not authorized"}}, "Converse")
        with self.assertRaises(agents.AgentError) as cm:
            self.inv(FakeClient([deny]))("s", "u", "submit_findings", SCHEMA)
        self.assertEqual(str(cm.exception), "submit_findings: AccessDeniedException")

    def test_published_errors_carry_no_sdk_text(self):
        def boom(*a):
            raise RuntimeError("arn:aws:lambda:ap-northeast-2:123456789012:function:fsi")
        with tempfile.TemporaryDirectory() as c:
            rows = agents.generate(ds(), boom, c, model_id="m", workers=2)["notes"]
        errs = [x for r in rows for x in r["errors"]]
        self.assertTrue(errs)
        self.assertNotIn("123456789012", json.dumps(errs))
        self.assertIn("synthesis: RuntimeError", errs)

    def test_unexpected_exception_fails_closed(self):
        def boom(*a):
            raise RuntimeError("socket")
        with tempfile.TemporaryDirectory() as c:
            rows = agents.generate(ds(), boom, c, model_id="m", workers=2)["notes"]
        self.assertEqual({r["kind"] for r in rows}, {"meta"})

    def test_schema_violation_retries(self):
        bad = {"findings": [{"title": 1}]}
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": bad}}]),
                        reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)


if __name__ == "__main__":
    unittest.main()
