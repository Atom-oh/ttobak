import importlib.util
import io
import json
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "audio_playback_backfill", Path(__file__).with_name("audio_playback_backfill.py"))
backfill = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backfill)


class RemuxSourceRule(unittest.TestCase):
    def test_matches_go_key_rule(self):
        accepted = [
            "audio/u1/m1/1793_recording_1793.webm",
            "audio/u1/m1/recording_recovered_0123456789abcdef0123456789abcdef.webm",
            "audio/u1/m1/part_001_1793_a.ogg",
            "audio/u1/m1/UPPER.WEBM",
        ]
        rejected = [
            "audio/u1/m1/recording_progress.webm", "audio/u1/m1/a.m4a",
            "audio/u1/m1/crop_result_0123456789abcdef0123456789abcdef.wav",
            "audio/u1/a.webm", "audio/u1/m1/x/a.webm", "images/u1/m1/a.webm",
            "audio/../m1/a.webm", "audio/u1/./a.webm", "audio//m1/a.webm",
            "audio/u1/m1/a%2Fb.webm", "audio/u1/m1/a\\b.webm", "audio/u1/m1/ a.webm",
            None, 5,
        ]
        for key in accepted:
            self.assertTrue(backfill.is_remux_source(key), key)
        for key in rejected:
            self.assertFalse(backfill.is_remux_source(key), key)


class CandidateSelection(unittest.TestCase):
    def test_uses_current_meeting_keys_only(self):
        items = [
            {"SK": "MEETING#a", "audioKey": "audio/u/a/1_x.webm"},
            {"SK": "MEETING#b", "audioKey": "audio/u/b/legacy.webm",
             "audioKeys": ["audio/u/b/part_000_1_x.webm", "", "audio/u/b/part_001_1_x.m4a"]},
            {"SK": "MEETING#c", "audioKey": "audio/u/c/recording_progress.webm"},
            {"SK": "ANALYSIS#summary", "audioKey": "audio/u/d/1_x.webm"},
            {"SK": "MEETING#e", "audioKey": "audio/u/a/1_x.webm"},
        ]
        self.assertEqual(backfill.candidate_keys(items),
                         ["audio/u/a/1_x.webm", "audio/u/b/part_000_1_x.webm"])


class FakeTable:
    def __init__(self, pages):
        self.pages, self.calls = pages, []

    def scan(self, **kwargs):
        self.calls.append(kwargs)
        return self.pages[len(self.calls) - 1]


class FakeLambda:
    def __init__(self, replies):
        self.replies, self.payloads = replies, []

    def invoke(self, **kwargs):
        payload = json.loads(kwargs["Payload"])
        self.payloads.append(payload)
        reply = self.replies[payload["key"]]
        out = {"Payload": io.BytesIO(json.dumps(reply.get("body", {})).encode())}
        if reply.get("error"):
            out["FunctionError"] = "Unhandled"
        return out


class ScanAndInvoke(unittest.TestCase):
    def test_scan_follows_pagination_with_projection(self):
        table = FakeTable([{"Items": [{"SK": "MEETING#a"}], "LastEvaluatedKey": {"PK": "x"}},
                           {"Items": [{"SK": "MEETING#b"}]}])
        self.assertEqual([i["SK"] for i in backfill.scan_meetings(table)], ["MEETING#a", "MEETING#b"])
        self.assertEqual(table.calls[1]["ExclusiveStartKey"], {"PK": "x"})
        self.assertNotIn("transcript", table.calls[0]["ProjectionExpression"])

    def test_invoke_reports_outcome_and_errors(self):
        client = FakeLambda({
            "audio/u/a/1.webm": {"body": {"outcome": "published"}},
            "audio/u/b/1.webm": {"error": True, "body": {"errorMessage": "remux: ffmpeg failed"}},
        })
        self.assertEqual(backfill.invoke(client, "fn", "audio/u/a/1.webm"), ("audio/u/a/1.webm", "published", ""))
        key, outcome, detail = backfill.invoke(client, "fn", "audio/u/b/1.webm")
        self.assertEqual((outcome, detail), ("error", "remux: ffmpeg failed"))
        self.assertEqual(client.payloads[0], {"key": "audio/u/a/1.webm"})


if __name__ == "__main__":
    unittest.main()
