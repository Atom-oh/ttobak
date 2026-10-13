"""Offline execution of the workflow's actual discovery shell and env export."""

import base64
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("prepare_note_evaluation", ROOT / "scripts/prepare-note-evaluation.py")
PREPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PREPARE)
WORKFLOW = ROOT / ".github/workflows/evaluate-notes.yml"


def discovery_shell():
    lines = WORKFLOW.read_text().splitlines()
    start = lines.index("      - name: Read deployed model configuration")
    begin = lines.index("        run: |", start) + 1
    body = []
    for line in lines[begin:]:
        if line and not line.startswith("          "):
            break
        body.append(line[10:])
    return "\n".join(body)


class PrepareNoteEvaluationTests(unittest.TestCase):
    def config(self):
        return {
            "summaryModelId": "global.openai.synthetic-final",
            "auxiliaryModelId": "global.anthropic.synthetic-auxiliary",
            "region": "us-west-2",
            "codeSha256": base64.b64encode(bytes(32)).decode(),
            "state": "Active",
            "lastUpdateStatus": "Successful",
            "revisionId": "synthetic-revision",
        }

    def test_actual_workflow_exports_both_settings_and_preserves_gates(self):
        text = WORKFLOW.read_text()
        self.assertIn("workflow_dispatch:", text)
        self.assertIn("if: github.ref == 'refs/heads/main'", text)
        self.assertIn("permissions:\n  contents: read", text)
        self.assertNotIn("pull_request", text)
        self.assertNotIn("contents: write", text)
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            config = directory / "fixture.json"
            config.write_text(json.dumps(self.config()))
            aws = directory / "aws"
            aws.write_text(
                "#!/usr/bin/env python3\n"
                "import json,os,sys\n"
                "from pathlib import Path\n"
                "Path(os.environ['ARGS_FILE']).write_text(json.dumps(sys.argv[1:]))\n"
                "print(Path(os.environ['FIXTURE_FILE']).read_text())\n"
            )
            aws.chmod(0o700)
            output = directory / "github-env"
            env = dict(os.environ, PATH=f"{directory}:{os.environ['PATH']}",
                       RUNNER_TEMP=temp, GITHUB_ENV=str(output), GITHUB_RUN_ID="1",
                       GITHUB_RUN_ATTEMPT="1", FIXTURE_FILE=str(config), ARGS_FILE=str(directory / "args.json"))
            subprocess.run(["bash", "-c", discovery_shell()], cwd=ROOT, env=env, check=True, capture_output=True, text=True)
            values = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertEqual(values["BEDROCK_SUMMARY_MODEL_ID"], self.config()["summaryModelId"])
            self.assertEqual(values["BEDROCK_MODEL_ID"], self.config()["auxiliaryModelId"])
            self.assertEqual(values["TTOBAK_NOTE_EVAL_DEPLOYMENT"], str(directory / "note-model.json"))
            args = json.loads((directory / "args.json").read_text())
            self.assertEqual(args[:2], ["lambda", "get-function-configuration"])
            query = args[args.index("--query") + 1]
            for required in ("BEDROCK_SUMMARY_MODEL_ID", "BEDROCK_MODEL_ID", "CodeSha256", "RevisionId"):
                self.assertIn(required, query)

    def test_absent_models_remain_absent_for_exact_go_fallback(self):
        config = self.config()
        config["summaryModelId"] = None
        lines = PREPARE.environment_lines(config, Path("/tmp/deployment.json"))
        self.assertIn("BEDROCK_SUMMARY_MODEL_ID=\n", lines)
        self.assertIn("BEDROCK_MODEL_ID=global.anthropic.synthetic-auxiliary\n", lines)
        config["auxiliaryModelId"] = ""
        lines = PREPARE.environment_lines(config, Path("/tmp/deployment.json"))
        self.assertIn("BEDROCK_MODEL_ID=\n", lines)
        self.assertNotIn("claude-opus", lines)

    def test_invalid_or_unstable_configuration_cannot_export_model_settings(self):
        mutations = [
            ("state", "Pending"), ("lastUpdateStatus", "InProgress"),
            ("revisionId", ""), ("codeSha256", ""),
            ("summaryModelId", "safe\nINJECTED=value"),
            ("auxiliaryModelId", "safe\rINJECTED=value"),
            ("summaryModelId", False), ("auxiliaryModelId", 0),
            ("region", "us-west-2\nINJECTED=value"), ("region", False),
        ]
        for field, value in mutations:
            with self.subTest(field=field, value=value):
                config = self.config()
                config[field] = value
                with self.assertRaises(ValueError):
                    PREPARE.environment_lines(config, Path("/tmp/deployment.json"))


if __name__ == "__main__":
    unittest.main()
