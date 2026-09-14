"""Behavioral CLI tests; no network, credentials or model calls."""

from concurrent.futures import ThreadPoolExecutor
import importlib.util
import threading
from types import SimpleNamespace
from unittest.mock import patch as mock_patch
import json
import hashlib
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import role_review
import run_role
from run_role import scrub as scrub_raw


ENGINE = Path(__file__).with_name("role_review.py")
HEAD = "a" * 40
BASE = "b" * 40
FRONTEND = "dashboard/frontend/components/Button.tsx"
TAGS = ("codex", "kiro-fable", "kiro-sol", "claude-self")


def patch(path=FRONTEND, before="old label", after="new label"):
    return (
        f"diff --git a/{path} b/{path}\n"
        f"--- a/{path}\n+++ b/{path}\n"
        f"@@ -1 +1 @@\n-{before}\n+{after}\n"
    )


class RoleReviewTests(unittest.TestCase):
    def test_publication_redacts_expression_defaults_and_punctuated_keys(self):
        import run_role
        import synthesize_roles

        canary = "SYNTHETIC_ROLLOUT_CANARY"
        cases = [f'password = settings.PASSWORD {operator} "{canary}"\nPUBLIC_AFTER'
                 for operator in ("||", "??", "or")]
        cases += [f'password = (old {operator}\n "{canary}")'
                  for operator in ("||", "??", "or")]
        cases += [f'password: "old {operator}\n{canary}"\nPUBLIC_AFTER'
                  for operator in ("||", "??", "or")]
        cases += [f'password = settings.PASSWORD{before}{operator}{after}"{canary}"\nPUBLIC_AFTER'
                  for operator in ("||", "??", "or")
                  for before, after in ((" ", "\n    "), ("\n    ", " "))]
        cases += [prefix + json.dumps({key: canary}) + suffix
                  for key in ("/prod/db/password", "password[0]", "api key (prod)")
                  for prefix, suffix in (("", ""), ("Evidence: ", "\nPUBLIC_AFTER"))]
        for index, evidence in enumerate(cases):
            with self.subTest(case=index):
                self.work = self.root / f"publication-{index}"
                plan = self.prepare()
                response = self.response("claude-self", findings=[{
                    "severity": "MINOR", "path": FRONTEND,
                    "condition": "When quoting a configuration example", "evidence": evidence,
                }])
                with mock_patch.object(run_role, "execute", return_value=(0, json.dumps(response), "")):
                    run_role.run(self.work, "claude-self")
                result = self.read("slot/claude-self-result.json")
                self.assertTrue(result["valid"], result["failure_codes"])
                self.assertEqual(result["response"]["reviewed_paths"], [FRONTEND])
                self.assertEqual(result["response"]["findings"][0]["path"], FRONTEND)
                for tag, role in plan["roles"].items():
                    if role["required"] and tag != "claude-self":
                        self.record(tag)
                self.cli("aggregate", "--work", self.work)
                self.assertEqual(self.read("role-summary.json")["mode"], "deterministic")
                with mock_patch.dict(synthesize_roles.os.environ, {"GITHUB_ENV": str(self.root / "test-env")}), \
                        mock_patch.object(synthesize_roles, "execute",
                                          side_effect=AssertionError("Unexpected chair call")):
                    synthesize_roles.synthesize(self.work, self.work / "review.md")
                for name in ("slot/claude-self-result.json", "role-summary.json",
                             "deterministic-review.md", "review.md"):
                    self.assertNotIn(canary, (self.work / name).read_text())
                    if evidence.endswith("PUBLIC_AFTER"):
                        self.assertIn("PUBLIC_AFTER", (self.work / name).read_text())
                self.assertTrue((self.work / "review.md").read_text().rstrip().endswith("VERDICT: PASS"))


    def test_ordinary_prose_scrub_has_bounded_runtime(self):
        prose = "The password is required and the token is optional. " * 80
        script = "import json,sys; from role_review import scrub; print(json.dumps(scrub(json.load(sys.stdin))))"
        result = subprocess.run([sys.executable, "-c", script], input=json.dumps(prose),
                                text=True, capture_output=True, cwd=ENGINE.parent, timeout=3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), prose)
        canary = "SYNTHETIC_ROLLOUT_CANARY"
        repeated = "Evidence: " + json.dumps([{"password": canary}] * 300, separators=(",", ":"))
        result = subprocess.run([sys.executable, "-c", script], input=json.dumps(repeated),
                                text=True, capture_output=True, cwd=ENGINE.parent, timeout=3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(canary, json.loads(result.stdout))

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.work = self.root / "work"
        self.diff = self.root / "raw.diff"
        self.context = self.root / "context.md"
        self.context.write_text("Trusted base: preserve accepted ADR scopes.\n")

    def tearDown(self):
        self.temp.cleanup()

    def cli(self, *args, expected=0):
        result = subprocess.run(
            [sys.executable, str(ENGINE), *map(str, args)],
            capture_output=True, text=True, timeout=10,
        )
        self.assertEqual(
            result.returncode, expected,
            f"command={args!r}\nstdout={result.stdout}\nstderr={result.stderr}",
        )
        return result

    def prepare(self, diff=None, expected=0, head=HEAD, extra=()):
        self.diff.write_text(patch() if diff is None else diff)
        self.cli(
            "prepare", "--diff", self.diff, "--context", self.context,
            "--head", head, "--base", BASE, "--work", self.work, *extra, expected=expected,
        )
        return self.read("role-plan.json")

    def read(self, name):
        return json.loads((self.work / name).read_text())

    def response(self, tag, **changes):
        plan = self.read("role-plan.json")
        paths = plan["roles"][tag]["paths"]
        result = {
            "head_sha": plan["head_sha"],
            "role": plan["roles"][tag]["role"],
            "scope_complete": True,
            "reviewed_paths": paths,
            "checks": [{"path": paths[0], "evidence": "Checked the changed branch and its caller."}],
            "findings": [],
            "uncertainties": [],
        }
        result.update(changes)
        return result

    def record(self, tag, response=None, raw=None, stderr="", rc=0, expected=0):
        output = self.root / f"{tag}-output.txt"
        diagnostic = self.root / f"{tag}-stderr.txt"
        output.write_text(raw if raw is not None else json.dumps(
            self.response(tag) if response is None else response
        ))
        diagnostic.write_text(stderr)
        receipt = self.work / "slot" / f"{tag}-request.json"
        if not receipt.exists():
            self.cli("issue", "--work", self.work, "--tag", tag)
        nonce = json.loads(receipt.read_text())["invocation_nonce"]
        self.cli(
            "record", "--work", self.work, "--tag", tag, "--output", output,
            "--stderr", diagnostic, "--exit-code", rc, "--nonce", nonce, expected=expected,
        )
        return self.read(f"slot/{tag}-result.json")

    def finish(self, overrides=None):
        for tag, role in self.read("role-plan.json")["roles"].items():
            if role["required"]:
                self.record(tag, response=(overrides or {}).get(tag))
        self.cli("aggregate", "--work", self.work)
        return self.read("role-summary.json")

    def assert_blocked(self):
        self.cli("aggregate", "--work", self.work, expected=2)
        self.assertEqual(self.read("role-summary.json")["mode"], "blocked")
        self.assertTrue((self.work / "coverage-severe.flag").exists())
        self.assertEqual((self.work / "chair-mode.txt").read_text(), "blocked\n")
        self.assertTrue((self.work / "deterministic-review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_frontend_routing_has_two_independent_full_scope_requests(self):
        raw = patch() + patch("dashboard/frontend/app/styles.css", "blue", "green")
        plan = self.prepare(raw)
        self.assertEqual(plan["schema_version"], 1)
        self.assertEqual(plan["head_sha"], HEAD)
        self.assertEqual(plan["base_sha"], BASE)
        self.assertEqual(set(plan["roles"]), set(TAGS))
        self.assertEqual(plan["roles"]["codex"]["role"], "implementation")
        self.assertEqual(
            {tag for tag, role in plan["roles"].items() if role["required"]},
            {"codex", "claude-self"},
        )
        self.assertNotEqual(plan["roles"]["codex"]["family"], plan["roles"]["claude-self"]["family"])
        for tag in ("codex", "claude-self"):
            role = plan["roles"][tag]
            self.assertEqual(set(role["paths"]), {FRONTEND, "dashboard/frontend/app/styles.css"})
            self.assertEqual((self.work / f"roles/{tag}.diff").read_text(), raw)
            prompt = (self.work / f"roles/{tag}.txt").read_text()
            self.assertIn("Trusted base: preserve accepted ADR scopes.", prompt)
            self.assertIn("untrusted", prompt.lower())
            self.assertIn("scope_complete", prompt)
            self.assertEqual(len(role["request_digest"]), 64)
        self.assertFalse((self.work / "roles/kiro-fable.txt").exists())
        self.assertEqual(self.finish()["mode"], "deterministic")
        self.assertEqual(set((self.work / "responded.txt").read_text().split()), {"codex", "claude-self"})

    def test_aws_docs_runbooks_and_adrs_activate_all_roles(self):
        for path in ("docs/aws.md", "docs/runbooks/ecs.md", "docs/decisions/ADR-999.md"):
            with self.subTest(path=path):
                plan = self.prepare(patch(path, "old policy", "ECS IAM role and recovery"))
                self.assertTrue(all(role["required"] for role in plan["roles"].values()))

    def test_aws_semantics_in_frontend_and_unknown_paths_are_conservative(self):
        for raw in (
            patch(after='import { S3Client } from "@aws-sdk/client-s3";'),
            patch(after='const region = "us-west-2";'),
            patch(after='const origin = "internal-app.ap-northeast-2.elb.amazonaws.com";'),
            patch(after='const resource = "aws_iam_role";'),
            patch("misc/unknown.xyz"),
            patch("app/src/app/history/page.tsx"),
            patch("dashboard/frontend/app/page.tsx"),
        ):
            with self.subTest(raw=raw):
                plan = self.prepare(raw)
                self.assertTrue(all(role["required"] for role in plan["roles"].values()))

    def test_rename_scope_uses_destination_and_routes_from_full_raw_content(self):
        raw = (
            'diff --git "a/docs/old name.md" "b/docs/new name.md"\n'
            "similarity index 100%\nrename from docs/old name.md\nrename to docs/new name.md\n"
        )
        plan = self.prepare(raw)
        self.assertEqual(plan["roles"]["codex"]["paths"], ["docs/new name.md"])

    def test_unquoted_spaces_use_patch_metadata_not_shlex(self):
        path = "dashboard/frontend/components/A large Button.tsx"
        plan = self.prepare(patch(path))
        self.assertEqual(plan["roles"]["codex"]["paths"], [path])

    def test_authoritative_path_manifest_and_incomplete_manifest(self):
        manifest = self.root / "paths.json"
        manifest.write_text(json.dumps([FRONTEND]))
        self.prepare(extra=("--paths", manifest))
        manifest.write_text(json.dumps(["wrong.tsx"]))
        self.prepare(extra=("--paths", manifest), expected=2)
        self.assert_blocked()

    def test_regular_file_to_symlink_has_two_blocks_for_one_path(self):
        raw = (
            "diff --git a/link b/link\ndeleted file mode 100644\n"
            "--- a/link\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n"
            "diff --git a/link b/link\nnew file mode 120000\n"
            "--- /dev/null\n+++ b/link\n@@ -0,0 +1 @@\n+target\n"
        )
        manifest = self.root / "paths.json"
        manifest.write_text('["link"]')
        self.assertEqual(self.prepare(raw, extra=("--paths", manifest))["paths"], ["link"])
        self.assertEqual(self.prepare(raw)["paths"], ["link"])

    def test_context_default_supports_24000_bytes_and_lower_configured_cap(self):
        self.context.write_text("x" * 22892)
        self.prepare()
        self.prepare(extra=("--context-cap", "12288"), expected=2)
        self.assert_blocked()

    def test_revision_identifiers_must_be_full_hex_sha(self):
        self.prepare(head="HEAD", expected=2)
        self.assert_blocked()

    def test_decoded_credential_strings_are_scrubbed_before_public_json(self):
        self.prepare()
        secret = "ghp_" + "A" * 36
        response = self.response("codex", findings=[{
            "severity": "MINOR", "path": FRONTEND, "condition": "On submission",
            "evidence": "Credential " + secret,
        }])
        raw = json.dumps(response).replace("ghp_", "\\u0067hp_")
        result = self.record("codex", raw=raw)
        self.assertTrue(result["valid"])
        self.assertNotIn(secret, json.dumps(result))
        self.record("claude-self")
        self.cli("aggregate", "--work", self.work)
        self.assertNotIn(secret, (self.work / "deterministic-review.md").read_text())

    def test_decoded_values_cover_existing_repository_credential_patterns(self):
        cases = [
            ("xox" + "b-" + "A" * 35, "A" * 35),
            ("AI" + "za" + "B" * 35, "B" * 35),
            ("Authorization: Basic " + "C" * 40, "C" * 40),
            ('{"Authorization": "Basic ' + "Q" * 12 + '"}', "Q" * 12),
            ('access_token="' + "D" * 35 + '"', "D" * 35),
            ('client_secret="' + "E" * 35 + '"', "E" * 35),
            ("aws_access_key_id=" + "F" * 35, "F" * 35),
            ("AWS_SESSION_TOKEN=\n" + "G" * 35, "G" * 35),
            ("postgresql://user:database-private-value@database.local/app", "database-private-value"),
            ("mongodb+srv://user:document-private-value@database.local/app", "document-private-value"),
            ("https://hooks.slack.com/services/T123/B123/webhook-private-value", "webhook-private-value"),
            ('MasterUserPassword = "master-private-value"', "master-private-value"),
            ('dbPassword: "database-private-value"', "database-private-value"),
            ("password: |\n  block-private-value\nnext: safe", "block-private-value"),
            ("- name: DATABASE_PASSWORD\n  value: env-private-value", "env-private-value"),
            ("mongodb://:empty-user-private@database.local/app", "empty-user-private"),
            ("Cookie: session=cookie-private-value", "cookie-private-value"),
            ('originSecret="origin-private-value"', "origin-private-value"),
            ('mcpToken="mcp-private-value"', "mcp-private-value"),
            ("x-origin-verify: origin-header-private", "origin-header-private"),
            ("prefix-eyJ" + "A" * 20 + "." + "B" * 20 + "." + "C" * 20, "C" * 20),
        ]
        for index, (text, secret) in enumerate(cases):
            with self.subTest(kind=text.split("=", 1)[0][:24]):
                self.work = self.root / f"decoded-pattern-{index}"
                self.prepare()
                response = self.response("codex")
                response["checks"][0]["evidence"] = text
                escaped = json.dumps(response).replace(secret, "".join("\\u" + format(ord(char), "04x") for char in secret))
                result = self.record("codex", raw=escaped)
                self.assertNotIn(secret, json.dumps(result))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                self.assertNotIn(secret, (self.work / "deterministic-review.md").read_text())

    def test_decoded_multiline_and_control_split_credentials_are_scrubbed(self):
        cases = [
            ("-----BEGIN PRIVATE KEY-----\nPRIVATE_MATERIAL\n-----END PRIVATE KEY-----", "PRIVATE_MATERIAL"),
            ("-----BEGIN PRIVATE KEY-----\nUNTERMINATED_PRIVATE_MATERIAL", "UNTERMINATED_PRIVATE_MATERIAL"),
            ("ghp_" + "A" * 18 + "\x1b[31m" + "B" * 18, "B" * 18),
            ("ghp_" + "A" * 18 + "\u200b" + "B" * 18, "B" * 18),
            ("ghp_" + "A" * 18 + "\x9b;31m" + "B" * 18, "B" * 18),
            ("ghp_" + "A" * 18 + "\x9dhidden\x9c" + "B" * 18, "B" * 18),
            ("AWS_SECRET_ACCESS_KEY=PRIVATE_ACCESS_SECRET", "PRIVATE_ACCESS_SECRET"),
        ]
        for index, (credential, secret) in enumerate(cases):
            with self.subTest(index=index):
                self.work = self.root / f"secret-{index}"
                self.prepare()
                response = self.response("codex", checks=[{"path": FRONTEND, "evidence": credential}])
                result = self.record("codex", raw=json.dumps(response, ensure_ascii=True))
                self.assertNotIn(secret, json.dumps(result))

    def test_truncated_patch_or_bare_header_cannot_claim_complete_input(self):
        for raw in (
            f"diff --git a/{FRONTEND} b/{FRONTEND}\n",
            patch().rsplit("+new label", 1)[0],
            "diff --git a/new.py b/new.py\nnew file mode 100644\n--- /dev/null\n+++ b/new.py\n",
            "diff --git a/old.py b/old.py\ndeleted file mode 100644\n--- a/old.py\n+++ /dev/null\n",
            "diff --git a/new.py b/new.py\nnew file mode 100644\nindex 0000000..7898192\n",
            "diff --git a/new.py b/new.py\nnew file mode 100644\n",
        ):
            with self.subTest(raw=raw):
                self.prepare(raw, expected=2)
                self.assert_blocked()

    def test_empty_file_creation_has_explicit_empty_blob_evidence(self):
        raw = "diff --git a/empty b/empty\nnew file mode 100644\nindex 0000000..e69de29\n"
        self.assertTrue(self.prepare(raw)["input_complete"])

    def test_reprepare_cannot_reuse_old_successful_results(self):
        self.prepare()
        self.finish()
        self.prepare()
        self.assertFalse(list((self.work / "slot").glob("*-result.json")))
        self.assert_blocked()

    def test_reissue_retains_terminal_failure_and_blocks_clean_replacement(self):
        self.prepare()
        self.record("codex", stderr="[warn] failed to set model", expected=2)
        self.cli("issue", "--work", self.work, "--tag", "codex")
        self.record("codex")
        self.record("claude-self")
        self.assert_blocked()
        history = self.read("slot/codex-attempts.json")
        self.assertIn("model_selection_diagnostic", history[0]["failure_codes"])

    def test_issued_request_persists_the_exact_framed_payload(self):
        self.prepare()
        self.cli("issue", "--work", self.work, "--tag", "codex")
        request = self.read("slot/codex-request.json")
        nonce = request["invocation_nonce"]
        payload = (self.work / "requests/codex.input").read_text()
        self.assertTrue(payload.startswith(f"BEGIN DIFF {nonce}\n"))
        self.assertTrue(payload.endswith(f"\nEND DIFF {nonce}\n"))
        self.assertIn(self.diff.read_text(), payload)
        self.prepare()
        self.assertFalse((self.work / "slot/codex-request.json").exists())

    def test_corrupt_attempt_history_produces_a_blocked_summary(self):
        self.prepare()
        self.finish()
        (self.work / "slot/codex-attempts.json").write_text("{broken")
        self.assert_blocked()
        self.assertIn("invalid_attempt_history:codex", self.read("role-summary.json")["failures"])

    def test_hunkless_content_changes_cannot_claim_complete_input(self):
        headers = "diff --git a/file.txt b/file.txt\n"
        cases = (
            headers + "new file mode 100644\n",
            headers + "new file mode 100644\nindex 0000000..1234567\n",
            headers + "new file mode 100644\nindex 0000000..1234567\n--- /dev/null\n+++ b/file.txt\n",
            headers + "deleted file mode 100644\nindex 1234567..0000000\n",
            headers + "old mode 100644\nnew mode 100755\nindex 1234567..abcdef0\n",
            "diff --git a/old.txt b/new.txt\nsimilarity index 85%\n"
            "rename from old.txt\nrename to new.txt\nindex 1234567..abcdef0\n",
            "diff --git a/old.txt b/new.txt\nsimilarity index 85%\n"
            "copy from old.txt\ncopy to new.txt\n",
        )
        for index, raw in enumerate(cases):
            with self.subTest(index=index):
                self.work = self.root / f"cut-metadata-{index}"
                self.prepare(raw, expected=2)
                self.assert_blocked()

    def test_genuinely_empty_files_and_pure_copies_need_no_hunk(self):
        for raw, expected_path in (
            ("diff --git a/empty.txt b/empty.txt\nnew file mode 100644\n"
             "index 0000000..e69de29\n", "empty.txt"),
            ("diff --git a/empty.txt b/empty.txt\ndeleted file mode 100644\n"
             "index e69de29..0000000\n", "empty.txt"),
            ("diff --git a/old.txt b/new.txt\nsimilarity index 100%\n"
             "copy from old.txt\ncopy to new.txt\n", "new.txt"),
        ):
            with self.subTest(raw=raw):
                self.assertEqual(self.prepare(raw)["paths"], [expected_path])

    def test_unterminated_private_keys_are_removed_from_public_results(self):
        for index, kind in enumerate(("", "RSA ", "EC ", "OPENSSH ")):
            with self.subTest(kind=kind):
                self.work = self.root / f"unterminated-key-{index}"
                self.prepare()
                secret = "SYNTHETIC_PRIVATE_FRAGMENT"
                evidence = f"-----BEGIN {kind}PRIVATE KEY-----\n{secret}\ncut off"
                response = self.response("codex", findings=[{
                    "severity": "MINOR", "path": FRONTEND,
                    "condition": "When diagnostics contain a partial key", "evidence": evidence,
                }])
                result = self.record("codex", raw=json.dumps(response))
                self.assertTrue(result["valid"])
                self.assertNotIn(secret, json.dumps(result))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                self.assertNotIn(secret, (self.work / "deterministic-review.md").read_text())

    def test_unterminated_quoted_credentials_are_redacted_after_both_scrubbers(self):
        secret = "SYNTHETIC_UNTERMINATED_CREDENTIAL"
        for quote in ('"', "'"):
            with self.subTest(quote=quote):
                evidence = "password=" + quote + secret
                self.assertNotIn(secret, role_review.scrub(evidence))
                self.assertNotIn(secret, role_review.scrub(scrub_raw(evidence)))

    def test_separator_runs_scrub_within_a_bounded_subprocess(self):
        code = (
            "import json,sys; from role_review import scrub; "
            "print(json.dumps(scrub(json.load(sys.stdin))))"
        )
        for separator in ("_", "-"):
            with self.subTest(separator=separator):
                run = separator * 8000
                text = ("deployment_password_suffix=SYNTHETIC_BEFORE " + run
                        + " provider-token-suffix=SYNTHETIC_AFTER")
                try:
                    result = subprocess.run(
                        [sys.executable, "-c", code], cwd=ENGINE.parent,
                        input=json.dumps(text), capture_output=True, text=True, timeout=3,
                    )
                except subprocess.TimeoutExpired:
                    self.fail("Scrubbing one 8000-character separator run exceeded three seconds")
                self.assertEqual(result.returncode, 0, result.stderr)
                scrubbed = json.loads(result.stdout)
                self.assertIn(run, scrubbed)
                self.assertNotIn("SYNTHETIC_BEFORE", scrubbed)
                self.assertNotIn("SYNTHETIC_AFTER", scrubbed)

    def bounded_scrub(self, text):
        try:
            result = subprocess.run(
                [sys.executable, "-c",
                 "import json,sys; from role_review import scrub; print(json.dumps(scrub(json.load(sys.stdin))))"],
                cwd=ENGINE.parent, input=json.dumps(text), capture_output=True, text=True, timeout=3,
            )
        except subprocess.TimeoutExpired:
            self.fail("Scrubbing the bounded adversarial input exceeded three seconds")
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_long_scheme_candidates_do_not_rescan_suffixes(self):
        noise = "x" * 200000
        text = ('password="SYNTHETIC_BEFORE" ' + noise
                + ' mongodb+srv://user:SYNTHETIC_AFTER@database.local/path')
        scrubbed = self.bounded_scrub(text)
        self.assertIn(noise, scrubbed)
        self.assertNotIn("SYNTHETIC_BEFORE", scrubbed)
        self.assertNotIn("SYNTHETIC_AFTER", scrubbed)

    def test_repeated_credential_keywords_use_one_identifier_consumption(self):
        for word in ("token", "password", "secret"):
            for delimiter in ("", ": SYNTHETIC_NAMED_CREDENTIAL"):
                with self.subTest(word=word, delimiter=bool(delimiter)):
                    identifier = "prefix_" + word * 4000 + "_suffix"
                    text = ("password=SYNTHETIC_BEFORE " + identifier + delimiter
                            + " api_key=SYNTHETIC_AFTER")
                    scrubbed = self.bounded_scrub(text)
                    if not delimiter:
                        self.assertIn(identifier, scrubbed)
                    self.assertNotIn("SYNTHETIC_NAMED_CREDENTIAL", scrubbed)
                    self.assertNotIn("SYNTHETIC_BEFORE", scrubbed)
                    self.assertNotIn("SYNTHETIC_AFTER", scrubbed)

    def test_other_scrub_patterns_have_bounded_adversarial_runs(self):
        cases = {
            "jwt-prefixes": "eyJ-" * 50000,
            "pem-prefix": "-----BEGIN " + "PRIVATE KEY " * 4000,
            "scheme-punctuation": "scheme" + "+.-" * 30000,
            "authorization-space": "Authorization" + " " * 50000,
            "yaml-space": "password: |" + " " * 50000,
            "environment-name": "name: " + "TOKEN_" * 8000,
            "cookie-space": " " * 50000 + "cookie",
            "bearer": "Bearer " + "a" * 50000,
            "github-token": "github_pat_" + "a" * 50000,
            "api-token": "sk-" + "a" * 50000,
            "slack-token": "xoxb-" + "a-" * 25000,
            "google-key": "AIza" + "a" * 50000,
            "aws-key-prefix": "AKIA" + "A" * 50000,
            "jwt-body": "eyJ" + "a" * 50000 + ".body.signature",
            "slack-hook": "https://hooks.slack.com/services/" + "a" * 50000,
            "origin-header": "x-origin-verify: " + "a" * 50000,
            "ansi-parameters": "\x1b[" + ";" * 50000 + "m",
            "control-string": "\x1b]" + "a" * 50000 + "\x07",
        }
        for name, payload in cases.items():
            with self.subTest(pattern=name):
                scrubbed = self.bounded_scrub(
                    "password=SYNTHETIC_BEFORE " + payload + " api_key=SYNTHETIC_AFTER",
                )
                self.assertNotIn("SYNTHETIC_BEFORE", scrubbed)
                self.assertNotIn("SYNTHETIC_AFTER", scrubbed)
                if name == "jwt-prefixes":
                    self.assertIn(payload, scrubbed)

    def test_yaml_long_indentation_before_bare_cr_in_valid_review_is_bounded(self):
        self.prepare()
        evidence = ("api_key=SYNTHETIC_BEFORE\npassword: |\n" + " " * 50000
                    + "\rX\n  SYNTHETIC_INSIDE\npassword: |\n  SYNTHETIC_BLOCK\n"
                    "token=SYNTHETIC_AFTER")
        # X ends the first block; use a separate credential key for subsequent block content.
        evidence = evidence.replace("  SYNTHETIC_INSIDE", "  api_key=SYNTHETIC_INSIDE")
        response = self.response("codex", checks=[{"path": FRONTEND, "evidence": evidence}])
        scrubbed = self.bounded_scrub(response)
        self.assertIn("X", scrubbed["checks"][0]["evidence"])
        result = self.record("codex", response=response)
        self.assertTrue(result["valid"])
        for marker in ("SYNTHETIC_BEFORE", "SYNTHETIC_INSIDE", "SYNTHETIC_BLOCK", "SYNTHETIC_AFTER"):
            self.assertNotIn(marker, json.dumps(scrubbed))
            self.assertNotIn(marker, json.dumps(result))

    def test_yaml_block_lines_blank_lines_and_endings_preserve_redaction(self):
        for newline in ("\n", "\r\n", "\r"):
            for indent in (" ", "\t"):
                for style in ("|", "|-", "|+", ">", ">-", ">+"):
                    with self.subTest(newline=repr(newline), indent=repr(indent), style=style):
                        text = newline.join([
                            "api_key=SYNTHETIC_BEFORE", "password: " + style,
                            indent * 50000, "", indent + "SYNTHETIC_INSIDE",
                            "", indent + "SYNTHETIC_SECOND",
                            "public: SAFE_OUTSIDE", "token=SYNTHETIC_AFTER",
                        ])
                        scrubbed = self.bounded_scrub(text)
                        self.assertIn("public: SAFE_OUTSIDE", scrubbed)
                        for marker in ("BEFORE", "INSIDE", "SECOND", "AFTER"):
                            self.assertFalse("SYNTHETIC_" + marker in scrubbed, "Block credential leaked")
                        eof = "password: " + style + newline + indent * 50000 + "SYNTHETIC_EOF"
                        self.assertNotIn("SYNTHETIC_EOF", self.bounded_scrub(eof))

    def test_yaml_environment_values_support_all_line_endings(self):
        for newline in ("\n", "\r\n", "\r"):
            with self.subTest(newline=repr(newline)):
                text = ("name: DATABASE_PASSWORD" + " " * 50000 + newline
                        + "\t" * 50000 + "value: SYNTHETIC_ENVIRONMENT" + newline
                        + "public: SAFE_OUTSIDE")
                scrubbed = self.bounded_scrub(text)
                self.assertFalse("SYNTHETIC_ENVIRONMENT" in scrubbed, "Environment credential leaked")
                self.assertIn("public: SAFE_OUTSIDE", scrubbed)

    def test_yaml_named_values_redact_complete_lines_without_losing_public_fields(self):
        for newline in ("\n", "\r\n", "\r"):
            for prefix in ("", "+ ", "- "):
                for value in ("alpha,SYNTHETIC_PRIVATE_SUFFIX", "alpha SYNTHETIC_PRIVATE_SUFFIX",
                              '"alpha, SYNTHETIC_PRIVATE_SUFFIX"', r'"alpha,\"SYNTHETIC_PRIVATE_SUFFIX"'):
                    with self.subTest(newline=repr(newline), prefix=prefix, value=value):
                        text = (prefix + "name: DB_PASSWORD" + newline + prefix + "value: " + value
                                + newline + "public: KEEP_AFTER")
                        clean = self.bounded_scrub(text)
                        self.assertNotIn("SYNTHETIC_PRIVATE_SUFFIX", clean)
                        self.assertIn("public: KEEP_AFTER", clean)

    def test_json_credential_keys_are_scrubbed_without_colliding_or_losing_values(self):
        keys = ["ghp_" + "A" * 30, "AKIA" + "B" * 16,
                "eyJ" + "C" * 20 + "." + "D" * 20 + "." + "E" * 20]
        value = {**{key: f"KEEP_{i}" for i, key in enumerate(keys)},
                 "[REDACTED]": "KEEP_LITERAL", "[REDACTED-KEY-1]": "KEEP_ALIAS"}
        clean = role_review.scrub(value)
        self.assertEqual(len(clean), len(value))
        self.assertCountEqual(clean.values(), value.values())
        self.assertEqual(clean["[REDACTED]"], "KEEP_LITERAL")
        self.assertEqual(clean["[REDACTED-KEY-1]"], "KEEP_ALIAS")
        for index, evidence in enumerate((
                value, {"nested": [value]}, json.dumps(value),
                "Quoted: " + json.dumps(json.dumps(value)))):
            with self.subTest(shape=index):
                self.work = self.root / f"key-shape-{index}"
                self.prepare()
                text = evidence if isinstance(evidence, str) else json.dumps(evidence)
                self.record("codex", self.response("codex", findings=[{
                    "severity": "MINOR", "path": FRONTEND,
                    "condition": "When evidence contains JSON keys", "evidence": text,
                }]))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                for path in ("slot/codex-result.json", "role-summary.json", "deterministic-review.md"):
                    published = (self.work / path).read_text()
                    for key in keys:
                        self.assertNotIn(key, published)
                    for item in value.values():
                        self.assertIn(item, published)

    def assert_private_evidence_redacted(self, evidence, marker, context):
        self.prepare()
        text = evidence if isinstance(evidence, str) else json.dumps(evidence)
        self.record("codex", self.response("codex", findings=[{
            "severity": "MINOR", "path": FRONTEND,
            "condition": "When diagnostics contain credentials", "evidence": text,
        }]))
        self.record("claude-self")
        self.cli("aggregate", "--work", self.work)
        for name in ("slot/codex-result.json", "role-summary.json", "deterministic-review.md"):
            published = (self.work / name).read_text()
            self.assertNotIn(marker, published)
            self.assertIn(context, published)

    def test_structured_path_credentials_keep_whole_value_redaction(self):
        for index, key in enumerate(("database/password", r"database\password", "/config/database/api_key")):
            value = {key: "SYNTHETIC_PRIVATE_PATH", "public": "KEEP_CONTEXT"}
            for shape, evidence in enumerate((
                    value, {"nested": [value]}, json.dumps(value),
                    "Quoted: " + json.dumps(json.dumps(value)))):
                with self.subTest(key=key, shape=shape):
                    self.work = self.root / f"path-value-{index}-{shape}"
                    self.assert_private_evidence_redacted(evidence, "SYNTHETIC_PRIVATE_PATH", "KEEP_CONTEXT")
        long_key = "segment/" * 10000 + "password"
        self.assertNotIn("SYNTHETIC_PRIVATE_PATH", self.bounded_scrub(
            json.dumps({long_key: "SYNTHETIC_PRIVATE_PATH"}),
        ))

    def test_pem_line_arrays_redact_whole_keys_and_preserve_ordinary_strings(self):
        for index, kind in enumerate(("", "RSA ", "EC ", "OPENSSH ")):
            lines = ["KEEP_BEFORE", f"-----BEGIN {kind}PRIVATE KEY-----",
                     "SYNTHETIC_PRIVATE_BODY", f"-----END {kind}PRIVATE KEY-----", "KEEP_AFTER"]
            for shape, evidence in enumerate((lines, {"nested": lines}, json.dumps(lines),
                                              "Quoted: " + json.dumps(json.dumps(lines)))):
                with self.subTest(kind=kind, shape=shape):
                    self.work = self.root / f"pem-array-{index}-{shape}"
                    self.assert_private_evidence_redacted(evidence, "SYNTHETIC_PRIVATE_BODY", "KEEP_AFTER")
            clean = role_review.scrub(lines)
            self.assertEqual(len(clean), len(lines))
            self.assertEqual(clean[0], "KEEP_BEFORE")
            self.assertEqual(clean[-1], "KEEP_AFTER")
        ordinary = ["KEEP_ONE", "KEEP_TWO", "a line without credential markers"]
        self.assertEqual(role_review.scrub(ordinary), ordinary)
        partial = ["KEEP_BEFORE", "-----BEGIN PRIVATE KEY-----", "SYNTHETIC_PRIVATE_BODY"]
        self.assertNotIn("SYNTHETIC_PRIVATE_BODY", json.dumps(role_review.scrub(partial)))
        inline = ["KEEP_BEFORE -----BEGIN PRIVATE KEY-----", "SYNTHETIC_PRIVATE_BODY",
                  "-----END PRIVATE KEY----- KEEP_AFTER"]
        self.assertIn("KEEP_AFTER", json.dumps(role_review.scrub(inline)))
        self.assertNotIn("SYNTHETIC_PRIVATE_BODY", json.dumps(role_review.scrub(inline)))
        repeated = ["KEEP_BEFORE -----BEGIN PRIVATE KEY-----x-----END PRIVATE KEY----- "
                    "KEEP_MIDDLE -----BEGIN RSA PRIVATE KEY-----", "SYNTHETIC_PRIVATE_BODY",
                    "-----END RSA PRIVATE KEY----- KEEP_AFTER"]
        clean = json.dumps(role_review.scrub(repeated))
        self.assertIn("KEEP_MIDDLE", clean)
        self.assertNotIn("SYNTHETIC_PRIVATE_BODY", clean)
        colored = ["-----BEGIN PRI\x1b[31mVATE KEY-----", "SYNTHETIC_PRIVATE_BODY", "-----END PRIVATE KEY-----"]
        self.assertNotIn("SYNTHETIC_PRIVATE_BODY", json.dumps(role_review.scrub(colored)))

    def test_pem_record_arrays_are_masked_before_json_and_quote_decoding(self):
        records = [{"line": "KEEP_BEFORE"}, {"line": "-----BEGIN PRIVATE KEY-----"},
                   {"line": "SYNTHETIC_PRIVATE_BODY"}, {"line": "-----END PRIVATE KEY-----"},
                   {"line": "KEEP_AFTER"}]
        serialized = json.dumps(records)
        cases = [serialized, json.dumps({"nested": records, "public": "KEEP_AFTER"}),
                 json.dumps(serialized), json.dumps({"nested": serialized}),
                 "Quoted: " + json.dumps(serialized)]
        for index, evidence in enumerate(cases):
            with self.subTest(shape=index):
                clean = role_review.scrub(evidence)
                self.assertNotIn("SYNTHETIC_PRIVATE_BODY", clean)
                self.assertIn("KEEP_AFTER", clean)
                if index < 4:
                    json.loads(clean)
                self.work = self.root / f"pem-record-array-{index}"
                self.prepare()
                response = self.response("codex", findings=[{
                    "severity": "MINOR", "path": FRONTEND, "condition": "On structured PEM evidence",
                    "evidence": evidence,
                }])
                self.record("codex", raw=scrub_raw(json.dumps(response)))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                for name in ("slot/codex-result.json", "role-summary.json", "deterministic-review.md"):
                    published = (self.work / name).read_text()
                    self.assertNotIn("SYNTHETIC_PRIVATE_BODY", published)
                    self.assertIn("KEEP_AFTER", published)

    def test_concatenated_pem_literals_are_redacted_before_fragments(self):
        begin, body, end = "-----BEGIN PRIVATE KEY-----", "SYNTHETIC_PRIVATE_BODY", "-----END PRIVATE KEY-----"
        complete = 'const pem = ' + " + ".join(json.dumps(s) for s in (begin, body, end)) + "; KEEP_AFTER"
        split = ("-----BEGIN ", "PRIVATE KEY-----", body, "-----END ", "PRIVATE KEY-----")
        cases = [complete, json.dumps(complete), complete.replace('"', r'\"'),
                 'const pem = ' + " + ".join(json.dumps(s) for s in split) + "; KEEP_AFTER",
                 'const pem = ' + " + ".join(repr(s) for s in split) + "; KEEP_AFTER",
                 {"nested": [complete, [begin, body, end, "KEEP_AFTER"]]}]
        for index, evidence in enumerate(cases):
            with self.subTest(shape=index):
                self.work = self.root / f"concat-pem-{index}"
                self.assert_private_evidence_redacted(evidence, body, "KEEP_AFTER")
        long_split = ["-----BEGIN ", "PRIVATE KEY-----", *([body] * 5000), "-----END ", "PRIVATE KEY-----"]
        self.assertNotIn(body, self.bounded_scrub(" + ".join(json.dumps(s) for s in long_split) + "; KEEP_AFTER"))

    def test_raw_generated_alias_keeps_values_but_user_credential_fields_stay_masked(self):
        self.prepare()
        token = "ghp_" + "A" * 36
        value = {token: "KEEP_ASSOCIATED_VALUE", "user_token": "SYNTHETIC_USER_SECRET",
                 "[REDACTED-GH-TOKEN]/password": "SYNTHETIC_PATH_SECRET"}
        response = self.response("claude-self", findings=[{
            "severity": "MINOR", "path": FRONTEND, "condition": "On raw alias generation",
            "evidence": json.dumps(value),
        }])
        with mock_patch.object(run_role, "execute", return_value=(0, json.dumps(response), "")):
            run_role.run(self.work, "claude-self")
        self.record("codex")
        self.cli("aggregate", "--work", self.work)
        for name in ("slot/claude-self-result.json", "role-summary.json", "deterministic-review.md"):
            published = (self.work / name).read_text()
            self.assertIn("KEEP_ASSOCIATED_VALUE", published)
            self.assertNotIn(token, published)
            self.assertNotIn("SYNTHETIC_USER_SECRET", published)
            self.assertNotIn("SYNTHETIC_PATH_SECRET", published)

    def test_mixed_quoted_credentials_are_fully_redacted_after_raw_scrubbing(self):
        values = [
            """password = 'a"SYNTHETIC_PRIVATE_SUFFIX'""",
            '''password = "a'SYNTHETIC_PRIVATE_SUFFIX"''',
            r'''password = "a\"SYNTHETIC_PRIVATE_SUFFIX"''',
            r"""password = 'a\'SYNTHETIC_PRIVATE_SUFFIX'""",
            r'''password = "a\\SYNTHETIC_PRIVATE_SUFFIX"''',
            r'''name="DB_PASSWORD", value="a\"SYNTHETIC_PRIVATE_SUFFIX"''',
        ]
        for index, evidence in enumerate(values):
            with self.subTest(case=index):
                self.work = self.root / f"mixed-quote-{index}"
                self.prepare()
                response = self.response("codex", findings=[{
                    "severity": "MINOR", "path": FRONTEND, "condition": "When credentials are quoted",
                    "evidence": evidence + "\nKEEP_CONTEXT",
                }])
                self.record("codex", raw=scrub_raw(json.dumps(response)))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                for name in ("slot/codex-result.json", "role-summary.json", "deterministic-review.md"):
                    published = (self.work / name).read_text()
                    self.assertNotIn("SYNTHETIC_PRIVATE_SUFFIX", published)
                    self.assertIn("KEEP_CONTEXT", published)
        long_value = 'password = "' + (r'a\"' * 20000) + 'SYNTHETIC_PRIVATE_SUFFIX"\nKEEP_CONTEXT'
        clean = self.bounded_scrub(long_value)
        self.assertNotIn("SYNTHETIC_PRIVATE_SUFFIX", clean)
        self.assertIn("KEEP_CONTEXT", clean)

    def test_nonsecret_quotes_paths_and_diff_boundaries_remain_exact(self):
        cases = ["docs/'release notes'.md", '- "old"\n+ "new"',
                 '- "old"\r\n+ "new"', '"old" + "new"', "'old' + 'new'",
                 r'docs/\"release notes\".md', '{ "safe" : [ "old", "new" ] }']
        for value in cases:
            with self.subTest(value=value):
                self.assertEqual(role_review.scrub(value), value)
        path = "docs/'release notes'.md"
        self.prepare(patch(path))
        summary = self.finish()
        self.assertEqual(summary["mode"], "deterministic")
        for result in (self.work / "slot").glob("*-result.json"):
            self.assertEqual(json.loads(result.read_text())["response"]["reviewed_paths"], [path])

    def test_nonsecret_diff_evidence_is_published_without_literal_joining(self):
        self.prepare()
        evidence = """- "old"\n+ "new"\nKeep docs/'release notes'.md unchanged."""
        response = self.response("codex", findings=[{
            "severity": "MINOR", "path": FRONTEND, "condition": "On the changed lines", "evidence": evidence,
        }])
        summary = self.finish({"codex": response})
        self.assertEqual(summary["findings"][0]["evidence"], evidence)

    def test_structured_key_and_diff_formats_have_bounded_processing(self):
        for separator in (".", ":", "-", "_"):
            with self.subTest(separator=separator):
                name = ("token" + separator) * 8000
                clean = self.bounded_scrub(json.dumps({name: "SYNTHETIC_PRIVATE"}))
                self.assertNotIn("SYNTHETIC_PRIVATE", clean)
        for sign in ("+", "-"):
            for newline in ("\n", "\r\n", "\r"):
                with self.subTest(sign=sign, newline=repr(newline)):
                    text = (sign + "password: |" + newline + sign + " " * 50000
                            + newline + sign + "  SYNTHETIC_BLOCK" + newline
                            + "public: KEEP" + newline + sign + "Cookie: SYNTHETIC_COOKIE")
                    clean = self.bounded_scrub(text)
                    self.assertNotIn("SYNTHETIC_BLOCK", clean)
                    self.assertNotIn("SYNTHETIC_COOKIE", clean)
                    self.assertIn("public: KEEP", clean)
        for text in (" " * 50000 + "not-cookie", "+" + " " * 50000 + "not-cookie",
                     'name="DB_PASSWORD"' + " " * 50000 + 'value="SYNTHETIC_HEADER"',
                     'HeaderName="X-Origin-Verify",\n+ ' + " " * 50000 + 'HeaderValue="SYNTHETIC_HEADER"'):
            clean = self.bounded_scrub(text)
            self.assertNotIn("SYNTHETIC_HEADER", clean)

    def test_decoded_output_budget_counts_string_values_in_utf8_bytes(self):
        limit = 1024 * 1024
        allowed = {"first": "*" * (limit // 2), "second": "*" * (limit // 2)}
        self.assertEqual(role_review.scrub(allowed), allowed)
        with self.assertRaisesRegex(role_review.Invalid, "^output_byte_limit$"):
            role_review.scrub({"first": allowed["first"], "second": allowed["second"] + "*"})
        with self.assertRaisesRegex(role_review.Invalid, "^output_byte_limit$"):
            role_review.scrub("é" * (limit // 2 + 1))
        with self.assertRaisesRegex(role_review.Invalid, "^output_byte_limit$"):
            role_review.scrub({"password": "*" * (limit + 1)})
        with self.assertRaisesRegex(role_review.Invalid, "^output_byte_limit$"):
            role_review.scrub({"password": allowed["first"], "public": allowed["second"] + "*"})

    def test_oversized_review_is_blocked_not_truncated_or_replaced_by_a_clean_retry(self):
        self.prepare()
        response = self.response("codex", checks=[{"path": FRONTEND, "evidence": "é" * 525000}])
        raw = json.dumps(response, ensure_ascii=False)
        self.assertLess(len(raw), 1024 * 1024)
        self.assertGreater(len(raw.encode()), 1024 * 1024)
        result = self.record("codex", raw=raw, expected=2)
        self.assertIn("output_byte_limit", result["failure_codes"])
        self.assertIsNone(result["response"])
        self.record("claude-self")
        self.assert_blocked()
        self.cli("issue", "--work", self.work, "--tag", "codex")
        self.record("codex")
        self.assert_blocked()

    def test_nonzero_record_checks_both_streams_and_retains_overflow_on_reissue(self):
        for index, stderr in enumerate(("", "Monthly request limit reached", "*" * (1024 * 1024 + 1))):
            with self.subTest(stderr_case=index):
                self.work = self.root / f"failed-overflow-{index}"
                self.prepare()
                result = self.record("codex", raw="*" * (1024 * 1024 + 1),
                                     stderr=stderr, rc=1, expected=2)
                self.assertIn("cli_nonzero_exit", result["failure_codes"])
                self.assertIn("output_byte_limit", result["failure_codes"])
                if index == 1:
                    self.assertIn("quota_diagnostic", result["failure_codes"])
                self.assertIsNone(result["response"])
                self.cli("issue", "--work", self.work, "--tag", "codex")
                self.record("codex")
                self.record("claude-self")
                self.assert_blocked()

    def test_final_record_envelope_rejects_redaction_growth_but_allows_metadata(self):
        limit = 1024 * 1024
        for count, expected in ((0, 0), (2000, 2)):
            with self.subTest(replacements=count):
                self.work = self.root / f"record-expansion-{count}"
                self.prepare()
                response = self.response("codex", checks=[{
                    "path": FRONTEND, "evidence": "token=x " * count,
                }])
                raw = json.dumps(response, separators=(",", ":"))
                response["checks"][0]["evidence"] += "*" * (limit - 64 - len(raw.encode()))
                raw = json.dumps(response, separators=(",", ":"))
                self.assertEqual(len(raw.encode()), limit - 64)
                result = self.record("codex", raw=raw, expected=expected)
                self.assertLessEqual((self.work / "slot/codex-result.json").stat().st_size, limit + 4096)
                if expected:
                    self.assertIn("output_byte_limit", result["failure_codes"])
                    self.assertIsNone(result["response"])
                    self.assert_blocked()
                else:
                    self.assertTrue(result["valid"])

    def test_oversized_stderr_and_history_remain_blocking(self):
        self.prepare()
        result = self.record("codex", stderr="*" * (1024 * 1024 + 1), expected=2)
        self.assertIn("output_byte_limit", result["failure_codes"])
        self.assert_blocked()
        self.work = self.root / "oversized-history"
        self.prepare()
        self.finish()
        (self.work / "slot/codex-attempts.json").write_text(
            json.dumps(["é" * 525000], ensure_ascii=False),
        )
        self.assert_blocked()
        self.assertIn("invalid_attempt_history:codex", self.read("role-summary.json")["failures"])
        self.assertNotIn("é", (self.work / "role-summary.json").read_text())

    def test_unterminated_quoted_credentials_cannot_reach_published_results(self):
        secret = "SYNTHETIC_UNTERMINATED_CREDENTIAL"
        for index, quote in enumerate(('"', "'")):
            with self.subTest(quote=quote):
                self.work = self.root / f"unterminated-quote-{index}"
                self.prepare()
                response = self.response("codex", findings=[{
                    "severity": "MINOR", "path": FRONTEND, "condition": "On diagnostic output",
                    "evidence": "password=" + quote + secret,
                }])
                result = self.record("codex", raw=scrub_raw(json.dumps(response)))
                self.assertTrue(result["valid"])
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                for name in ("slot/codex-result.json", "role-summary.json", "deterministic-review.md"):
                    self.assertNotIn(secret, (self.work / name).read_text())

    def test_validated_results_cannot_be_reissued_to_discard_candidates(self):
        major = {"severity": "MAJOR", "path": FRONTEND,
                 "condition": "On update", "evidence": "The changed write loses source data."}
        for index, changes in enumerate(({"findings": [major]},
                                         {"uncertainties": ["The source guard needs verification."]},
                                         {})):
            with self.subTest(changes=changes):
                self.work = self.root / f"validated-reissue-{index}"
                self.prepare()
                before = self.finish({"codex": self.response("codex", **changes)})
                files = ("slot/codex-result.json", "slot/codex-request.json")
                saved = {name: (self.work / name).read_bytes() for name in files}
                self.cli("issue", "--work", self.work, "--tag", "codex", expected=2)
                self.assertEqual(saved, {name: (self.work / name).read_bytes() for name in files})
                self.assertFalse((self.work / "slot/codex-attempts.json").exists())
                self.cli("aggregate", "--work", self.work)
                after = self.read("role-summary.json")
                self.assertEqual(after["mode"], before["mode"])
                self.assertEqual(after["findings"], before["findings"])
                self.assertEqual(after["uncertainties"], before["uncertainties"])

    def test_cross_process_reissue_cannot_clear_an_active_record_claim(self):
        self.prepare()
        self.record("claude-self")
        nonce, _, _ = role_review.issue_request(self.work, "codex")
        receipt = (self.work / "slot/codex-request.json").read_bytes()
        output, stderr = self.root / "pending.json", self.root / "pending.stderr"
        output.write_text(json.dumps(self.response("codex")))
        stderr.write_text("[warn] failed to set model")
        result_path = self.work / "slot/codex-result.json"
        entered, release = threading.Event(), threading.Event()
        original = role_review.write_json

        def held_write(path, value):
            if path == result_path:
                entered.set()
                if not release.wait(10):
                    raise AssertionError("record race did not release the pending writer")
            return original(path, value)

        args = SimpleNamespace(work=self.work, tag="codex", output=output,
                               stderr=stderr, nonce=nonce, exit_code=1)
        with mock_patch.object(role_review, "write_json", side_effect=held_write):
            with ThreadPoolExecutor(max_workers=1) as pool:
                pending = pool.submit(role_review.record, args)
                try:
                    self.assertTrue(entered.wait(5))
                    self.cli("issue", "--work", self.work, "--tag", "codex", expected=2)
                    self.assertEqual((self.work / "slot/codex-request.json").read_bytes(), receipt)
                    self.assertTrue((self.work / "slot/codex.record-claim").exists())
                finally:
                    release.set()
                self.assertEqual(pending.result(timeout=5), 2)
        self.cli("issue", "--work", self.work, "--tag", "codex")
        self.record("codex")
        self.assert_blocked()
        self.assertIn("model_selection_diagnostic",
                      self.read("slot/codex-attempts.json")[0]["failure_codes"])

    def test_abandoned_record_claim_cannot_be_cleared_by_reissue(self):
        self.prepare()
        self.cli("issue", "--work", self.work, "--tag", "codex")
        receipt = (self.work / "slot/codex-request.json").read_bytes()
        (self.work / "slot/codex.record-claim").touch()
        self.cli("issue", "--work", self.work, "--tag", "codex", expected=2)
        self.assertEqual((self.work / "slot/codex-request.json").read_bytes(), receipt)
        self.assertTrue((self.work / "slot/codex.record-claim").exists())
        self.assert_blocked()

    def test_charset_escapes_cannot_split_recoverable_credentials(self):
        for index, escape in enumerate(("\x1b(B", "\x1b)0", "\x1b#8", "\x1b%G")):
            with self.subTest(escape=repr(escape)):
                self.work = self.root / f"charset-{index}"
                self.prepare()
                evidence = "ghp_" + "A" * 18 + escape + "B" * 18
                response = self.response("codex", checks=[{"path": FRONTEND, "evidence": evidence}])
                result = self.record("codex", raw=json.dumps(response))
                self.assertNotIn("B" * 18, json.dumps(result))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                self.assertNotIn("B" * 18, (self.work / "deterministic-review.md").read_text())

    def test_aws_sdk_credential_field_names_are_redacted(self):
        for index, key in enumerate(("SecretAccessKey", "SessionToken", "AccessKeyId")):
            with self.subTest(key=key):
                self.work = self.root / f"sdk-key-{index}"
                self.prepare()
                secret = "SYNTHETIC_PRIVATE_SDK_VALUE"
                evidence = json.dumps({key: secret})
                response = self.response("codex", checks=[{"path": FRONTEND, "evidence": evidence}])
                self.assertNotIn(secret, json.dumps(self.record("codex", response=response)))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                self.assertNotIn(secret, (self.work / "deterministic-review.md").read_text())

    def test_concurrent_record_cannot_overwrite_a_failed_attempt(self):
        self.prepare()
        self.record("claude-self")
        spec = importlib.util.spec_from_file_location("record_race_test", ENGINE)
        engine = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(engine)
        output, stderr = self.root / "held-response.json", self.root / "race.stderr"
        output.write_text(json.dumps(self.response("codex")))
        stderr.write_text("")
        nonce, _, _ = engine.issue_request(self.work, "codex")
        args = dict(work=self.work, tag="codex", output=output, stderr=stderr, nonce=nonce)
        entered, release = threading.Event(), threading.Event()
        original = engine.text_file

        def hold_response(path, *args):
            if Path(path) == output:
                entered.set()
                if not release.wait(10):
                    raise AssertionError("record race did not release the first writer")
            return original(path, *args)

        with mock_patch.object(engine, "text_file", side_effect=hold_response):
            with ThreadPoolExecutor(max_workers=1) as pool:
                pending = pool.submit(engine.record, SimpleNamespace(**args, exit_code=0))
                try:
                    self.assertTrue(entered.wait(5))
                    self.assertEqual(engine.record(SimpleNamespace(**args, exit_code=1)), 2)
                finally:
                    release.set()
                self.assertEqual(pending.result(timeout=5), 0)
        self.assert_blocked()

    def test_mode_only_and_git_octal_quoted_paths(self):
        for raw, expected_path in (
            ("diff --git a/a file.sh b/a file.sh\nold mode 100644\nnew mode 100755\n", "a file.sh"),
            ('diff --git "a/caf\\303\\251.ts" "b/caf\\303\\251.ts"\n'
             '--- "a/caf\\303\\251.ts"\n+++ "b/caf\\303\\251.ts"\n@@ -1 +1 @@\n-a\n+b\n',
             "café.ts"),
        ):
            with self.subTest(raw=raw):
                plan = self.prepare(raw)
                self.assertEqual(plan["paths"], [expected_path])

    def test_duplicate_record_cannot_replace_a_failed_attempt_with_pass(self):
        self.prepare()
        self.record("codex", rc=1, expected=2)
        self.record("codex", expected=2)
        self.record("claude-self")
        self.assert_blocked()

    def test_deterministic_report_audits_role_routing_and_failure_codes(self):
        self.prepare()
        self.finish()
        report = (self.work / "deterministic-review.md").read_text()
        for value in ("codex", "kiro-fable", "kiro-sol", "claude-self", "implementation",
                      "clear_frontend_only", "inactive"):
            self.assertIn(value, report)
        self.work = self.root / "quota-report"
        self.prepare()
        self.record("codex", stderr="Error: quota exceeded for this account", expected=2)
        self.assert_blocked()
        report = (self.work / "deterministic-review.md").read_text()
        self.assertIn("quota", report)
        self.assertIn("missing_result:claude-self", report)

    def test_source_omission_flag_is_never_ignored(self):
        self.prepare()
        self.finish()
        (self.work / "source-omission.flag").touch()
        self.assert_blocked()

    def test_plan_and_request_digests_bind_head_context_and_full_diff(self):
        first = self.prepare()
        identical = self.prepare()
        self.assertEqual(first["plan_digest"], identical["plan_digest"])
        self.context.write_text("Changed trusted context.\n")
        second = self.prepare()
        self.assertNotEqual(first["plan_digest"], second["plan_digest"])
        self.assertNotEqual(
            first["roles"]["codex"]["request_digest"],
            second["roles"]["codex"]["request_digest"],
        )
        third = self.prepare(patch(after="different label"), head="c" * 40)
        self.assertNotEqual(second["plan_digest"], third["plan_digest"])

    def test_oversized_diff_blocks_without_truncating_role_input(self):
        prefix = patch()
        raw = prefix + "+" + "x" * (95001 - len(prefix) - 1)
        self.prepare(raw, expected=2)
        self.assertEqual((self.work / "roles/codex.diff").read_text(), raw)
        self.assert_blocked()

    def test_line_limit_and_empty_or_unparseable_inputs_block(self):
        for raw in (patch() + "+x\n" * 3001, "", "not a git diff\n"):
            with self.subTest(raw=raw[:30]):
                self.prepare(raw, expected=2)
                self.assert_blocked()

    def test_utf8_byte_limit_is_not_a_character_limit(self):
        self.prepare(patch(after="é" * 48000), expected=2)
        self.assert_blocked()

    def test_empty_context_blocks_preparation(self):
        self.context.write_text("")
        self.prepare(expected=2)
        self.assert_blocked()

    def test_minor_and_info_findings_allow_deterministic_summary(self):
        self.prepare()
        findings = [
            {"severity": severity, "path": FRONTEND, "condition": "When the label is empty",
             "evidence": "The changed fallback branch has no accessible name."}
            for severity in ("MINOR", "INFO")
        ]
        summary = self.finish({"codex": self.response("codex", findings=findings)})
        self.assertEqual(summary["mode"], "deterministic")
        rendered = (self.work / "deterministic-review.md").read_text()
        self.assertIn("MINOR", rendered)
        self.assertTrue(rendered.endswith("VERDICT: PASS\n"))

    def test_critical_major_or_uncertainties_require_chair_review(self):
        for severity in ("CRITICAL", "MAJOR", None):
            with self.subTest(severity=severity):
                self.work = self.root / f"work-{severity}"
                self.prepare()
                update = {"uncertainties": ["The caller contract is unavailable."]} if severity is None else {
                    "findings": [{"severity": severity, "path": FRONTEND,
                                  "condition": "On concurrent submissions",
                                  "evidence": "The changed code drops an in-flight update."}]
                }
                summary = self.finish({"codex": self.response("codex", **update)})
                self.assertEqual(summary["mode"], "review")
                self.assertFalse((self.work / "deterministic-review.md").exists())
                self.assertFalse((self.work / "coverage-severe.flag").exists())

    def test_single_json_fence_and_kiro_prefixes_are_supported(self):
        for wrapper in (
            lambda s: "```json\n" + s + "\n```",
            lambda s: "\n".join("> " + line for line in s.splitlines()),
            lambda s: "> ```json\n> " + s + "\n> ```",
        ):
            with self.subTest(wrapper=wrapper):
                self.work = self.root / str(id(wrapper))
                self.prepare()
                result = self.record("codex", raw=wrapper(json.dumps(self.response("codex"))))
                self.assertTrue(result["valid"])

    def test_blank_malformed_extra_text_and_duplicate_json_keys_reject(self):
        for raw in ("", "glob found no files", "{}", "[]", "```json\n{}\n```\nPASS",
                    '{"head_sha":"x","head_sha":"y"}'):
            with self.subTest(raw=raw):
                self.work = self.root / str(abs(hash(raw)))
                self.prepare()
                self.assertFalse(self.record("codex", raw=raw, expected=2)["valid"])
                self.assert_blocked()

    def test_missing_paths_false_scope_empty_checks_and_wrong_role_reject(self):
        changes = (
            {"reviewed_paths": []}, {"scope_complete": False}, {"scope_complete": "true"},
            {"checks": []}, {"checks": [{"path": FRONTEND, "evidence": " "}]},
            {"checks": [{"path": "not/changed.ts", "evidence": "claimed"}]},
            {"role": "claude-self"}, {"head_sha": "c" * 40},
        )
        for index, update in enumerate(changes):
            with self.subTest(update=update):
                self.work = self.root / f"scope-{index}"
                self.prepare()
                self.record("codex", self.response("codex", **update), expected=2)
                self.assert_blocked()

    def test_findings_need_known_severity_changed_path_condition_and_evidence(self):
        good = {"severity": "MAJOR", "path": FRONTEND, "condition": "When clicked",
                "evidence": "The changed handler raises."}
        for key, bad in (("severity", "PASS"), ("path", "other.py"), ("condition", ""), ("evidence", "")):
            with self.subTest(key=key):
                self.work = self.root / key
                self.prepare()
                finding = dict(good, **{key: bad})
                self.record("codex", self.response("codex", findings=[finding]), expected=2)
                self.assert_blocked()

    def test_nonzero_and_specific_stderr_failures_block_even_valid_json(self):
        for index, (rc, stderr) in enumerate((
            (1, ""), (0, "ERROR: INVALID_MODEL_ID secret=do-not-publish-this"),
            (0, "Warning: falling back to another model"),
            (0, "Error: quota exceeded for this account"),
            (0, "An error occurred (ThrottlingException) when invoking the model"),
            (0, "Error: MONTHLY_REQUEST_COUNT"),
            (0, "Error: UsageLimitReachedError"),
            (0, "Warning: Json supplied at /agent/profile.json is invalid"),
        )):
            with self.subTest(rc=rc, stderr=stderr):
                self.work = self.root / f"diagnostic-{index}"
                self.prepare()
                self.record("codex", rc=rc, stderr=stderr, expected=2)
                self.assert_blocked()
                for file in self.work.rglob("*"):
                    if file.is_file():
                        self.assertNotIn("do-not-publish-this", file.read_text())

    def test_echoed_prompt_words_are_not_diagnostic_failures(self):
        self.prepare()
        result = self.record("codex", stderr=(
            "Review quota handling, fallback logic and model-selection tests.\n"
            "+ const note = 'quota exceeded';\n"
        ))
        self.assertTrue(result["valid"])

    def test_observed_kiro_diagnostics_reject_valid_json(self):
        diagnostics = (
            "[warn] failed to set model opus ... Method not found",
            "Monthly request limit reached",
            "Error: no agent with name inline-review found",
            "Falling back to user specified default",
        )
        for index, diagnostic in enumerate(diagnostics):
            with self.subTest(diagnostic=diagnostic):
                self.work = self.root / f"observed-kiro-{index}"
                self.prepare()
                self.record("codex", stderr=diagnostic, expected=2)
                self.assert_blocked()

    def test_echoed_diagnostic_examples_in_diff_are_not_runtime_failures(self):
        self.prepare()
        result = self.record("codex", stderr=(
            '+ "[warn] failed to set model opus ... Method not found"\n'
            "+ Monthly request limit reached\n"
            "+ Error: no agent with name X found\n"
            "+ Falling back to user specified default\n"
        ))
        self.assertTrue(result["valid"])

    def test_missing_result_is_blocked_even_when_other_role_found_major(self):
        self.prepare()
        finding = {"severity": "MAJOR", "path": FRONTEND, "condition": "When clicked", "evidence": "Fails."}
        self.record("codex", self.response("codex", findings=[finding]))
        self.assert_blocked()

    def test_stale_plan_request_and_result_tag_fingerprints_block(self):
        for key in ("plan_digest", "request_digest", "head_sha", "tag"):
            with self.subTest(key=key):
                self.work = self.root / f"stale-{key}"
                self.prepare()
                for tag in ("codex", "claude-self"):
                    self.record(tag)
                p = self.work / "slot/codex-result.json"
                data = json.loads(p.read_text())
                data[key] = "wrong"
                p.write_text(json.dumps(data))
                self.assert_blocked()

    def test_offroster_and_inactive_results_cannot_supply_coverage(self):
        for name in ("intruder", "kiro-fable"):
            with self.subTest(name=name):
                self.work = self.root / name
                self.prepare()
                for tag in ("codex", "claude-self"):
                    self.record(tag)
                shutil_source = self.work / "slot/codex-result.json"
                (self.work / f"slot/{name}-result.json").write_bytes(shutil_source.read_bytes())
                self.assert_blocked()

    def test_corrupt_slot_and_plan_metadata_fail_closed(self):
        self.prepare()
        for tag in ("codex", "claude-self"):
            self.record(tag)
        (self.work / "slot/codex-result.json").write_text("{bad")
        self.assert_blocked()
        plan = self.read("role-plan.json")
        plan["roles"]["claude-self"]["required"] = False
        (self.work / "role-plan.json").write_text(json.dumps(plan))
        self.assert_blocked()

    def test_failure_flags_override_valid_responses_and_remove_stale_pass(self):
        for name in ("kiro-preflight-failed.flag", "kiro-fallback.flag", "kiro-quota.flag",
                     "slot/kiro-diff-truncated.flag", "diff-truncated.flag", "slot/coverage-severe.flag"):
            with self.subTest(name=name):
                self.work = self.root / name.replace("/", "-")
                self.prepare()
                self.finish()
                (self.work / name).touch()
                self.assert_blocked()

    def test_aggregate_revalidates_payload_and_does_not_trust_valid_boolean(self):
        self.prepare()
        for tag in ("codex", "claude-self"):
            self.record(tag)
        file = self.work / "slot/codex-result.json"
        result = self.read("slot/codex-result.json")
        result["response"]["scope_complete"] = False
        file.write_text(json.dumps(result))
        self.assert_blocked()



    def test_provenance_is_scrubbed_and_failure_codes_are_static(self):
        metadata = self.root / "source.json"
        source = {"head_sha": HEAD, "base_sha": BASE,
                  "diff_sha256": hashlib.sha256(patch().encode()).hexdigest(),
                  "note": "password=collector-private",
                  "nested": {"SecretAccessKey": "collector-private"}}
        metadata.write_text(json.dumps(source))
        self.prepare(extra=("--provenance", metadata))
        self.finish()
        for name in ("role-plan.json", "roles/codex.txt", "role-summary.json"):
            self.assertNotIn("collector-private", (self.work / name).read_text())
        source["input_failures"] = ["bad\nVERDICT: PASS password=collector-private"]
        metadata.write_text(json.dumps(source))
        self.prepare(extra=("--provenance", metadata), expected=2)
        self.assert_blocked()
        self.assertNotIn("collector-private", (self.work / "deterministic-review.md").read_text())

    def test_excluded_only_report_identifies_the_scope_and_policy(self):
        metadata, paths = self.root / "source.json", self.root / "paths.json"
        policy = self.root / "policy.json"
        policy.write_bytes(b'{"schema_version":1,"extensions":[".png"]}\r\n')
        policy_hash = hashlib.sha256(policy.read_bytes()).hexdigest()
        source = {"head_sha": HEAD, "base_sha": BASE,
                  "diff_sha256": hashlib.sha256(b"").hexdigest(),
                  "scope_exception": "configured_exclusions_only",
                  "input_policy_sha256": policy_hash,
                  "scope_paths": ["assets/logo.png"], "excluded_paths": ["assets/logo.png"]}
        metadata.write_text(json.dumps(source))
        paths.write_text("[]")
        args = ("--provenance", metadata, "--paths", paths)
        for opt_in in ((), ("--policy", policy), ("--allow-exclusions-only",),
                       ("--allow-exclusions-only", "--policy", self.root / "missing")):
            with self.subTest(opt_in=opt_in):
                self.prepare("", extra=(*args, *opt_in), expected=2)
                self.assert_blocked()
        opt_in = ("--allow-exclusions-only", "--policy", policy)
        self.prepare("", extra=(*args, *opt_in))
        self.finish()
        report = (self.work / "deterministic-review.md").read_text()
        self.assertIn("assets/logo.png", report)
        self.assertIn(policy_hash, report)
        self.assertIn("NOT_APPLICABLE", report)
        anchor = self.work / "exclusions-policy.json"
        self.assertEqual(anchor.read_bytes(), policy.read_bytes())
        anchor.write_bytes(anchor.read_bytes() + b" ")
        self.assert_blocked()
        policy.write_bytes(policy.read_bytes().replace(b"\r\n", b"\n"))
        self.prepare("", extra=(*args, *opt_in), expected=2)
        self.assert_blocked()
        source["diff_sha256"] = hashlib.sha256(patch().encode()).hexdigest()
        source["input_policy_sha256"] = hashlib.sha256(policy.read_bytes()).hexdigest()
        metadata.write_text(json.dumps(source))
        paths.write_text(json.dumps([FRONTEND]))
        self.prepare(extra=(*args, *opt_in), expected=2)
        self.assert_blocked()

    def test_sensitive_key_and_name_value_shapes_never_reach_public_evidence(self):
        secret = "SYNTHETIC_PRIVATE_SHAPE"
        cases = [{key: secret} for key in (
            "spring.datasource.password", "aws.secret_access_key", "X-Origin-Verify",
            "Authorization", "pwd", "dsn", "connectionString")]
        cases += [
            {"name": "DATABASE_PASSWORD", "value": secret},
            {"HeaderName": "X-Origin-Verify", "HeaderValue": secret},
            'name = "DB_PASSWORD", value = "' + secret + '"',
            json.dumps({"name": "DATABASE_PASSWORD", "value": secret}),
            json.dumps({"SecretString": json.dumps({"password": secret})}),
            'Evidence: ' + json.dumps({"detail": json.dumps({"password": secret})}),
            r'{\"password\":\"' + secret + r'\"}',
        ]
        metadata = self.root / "source.json"
        metadata.write_text(json.dumps({
            "head_sha": HEAD, "base_sha": BASE,
            "diff_sha256": hashlib.sha256(patch().encode()).hexdigest(),
            "cases": cases, "safe": "PUBLIC_KEEP",
        }))
        self.prepare(extra=("--provenance", metadata))
        for name in ("role-plan.json", "roles/codex.txt"):
            self.assertNotIn(secret, (self.work / name).read_text())
            self.assertIn("PUBLIC_KEEP", (self.work / name).read_text())
        for index, evidence in enumerate(cases):
            with self.subTest(index=index):
                self.work = self.root / f"shapes-{index}"
                self.prepare()
                text = evidence if isinstance(evidence, str) else json.dumps(evidence)
                response = self.response("codex", checks=[{"path": FRONTEND, "evidence": text}])
                self.record("codex", response)
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                for name in ("slot/codex-result.json", "role-summary.json", "deterministic-review.md"):
                    self.assertNotIn(secret, (self.work / name).read_text())

    def test_valid_results_cannot_be_reissued_to_discard_findings_or_uncertainty(self):
        for kind in ("CRITICAL", "MAJOR", "uncertain", "clean"):
            with self.subTest(kind=kind):
                self.work = self.root / kind
                self.prepare()
                update = {} if kind == "clean" else (
                    {"uncertainties": ["Caller contract is unavailable."]} if kind == "uncertain" else
                    {"findings": [{"severity": kind, "path": FRONTEND,
                                  "condition": "On concurrent submissions", "evidence": "Update is lost."}]})
                self.record("codex", self.response("codex", **update))
                self.record("claude-self")
                before = {p: p.read_bytes() for p in self.work.rglob("*") if p.is_file()}
                self.cli("issue", "--work", self.work, "--tag", "codex", expected=2)
                self.assertEqual(before, {p: p.read_bytes() for p in self.work.rglob("*") if p.is_file()})
                self.cli("aggregate", "--work", self.work)
                self.assertEqual(self.read("role-summary.json")["mode"],
                                 "deterministic" if kind == "clean" else "review")

    def test_protocol_decoded_credential_patterns(self):
        cases = [
            ("xox" + "b-" + "A" * 35, "A" * 35),
            ("AI" + "za" + "B" * 35, "B" * 35),
            ("Authorization: Basic " + "C" * 40, "C" * 40),
            ('{"Authorization": "Basic ' + "Q" * 12 + '"}', "Q" * 12),
            ('access_token="' + "D" * 35 + '"', "D" * 35),
            ('client_secret="' + "E" * 35 + '"', "E" * 35),
            ("aws_access_key_id=" + "F" * 35, "F" * 35),
            ("AWS_SESSION_TOKEN=\n" + "G" * 35, "G" * 35),
            ("postgresql://user:database-private-value@database.local/app", "database-private-value"),
            ("mongodb+srv://user:document-private-value@database.local/app", "document-private-value"),
            ("https://hooks.slack.com/services/T123/B123/webhook-private-value", "webhook-private-value"),
            ('MasterUserPassword = "master-private-value"', "master-private-value"),
            ('dbPassword: "database-private-value"', "database-private-value"),
            ("password: |\n  block-private-value\nnext: safe", "block-private-value"),
            ("- name: DATABASE_PASSWORD\n  value: env-private-value", "env-private-value"),
            ("+  - name: DATABASE_PASSWORD\n+    value: added-env-private", "added-env-private"),
            ("-password: |\n-  removed-block-private\n next: safe", "removed-block-private"),
            ("mongodb://:empty-user-private@database.local/app", "empty-user-private"),
            ("Cookie: session=cookie-private-value", "cookie-private-value"),
            ('originSecret="origin-private-value"', "origin-private-value"),
            ('mcpToken="mcp-private-value"', "mcp-private-value"),
            ("x-origin-verify: origin-header-private", "origin-header-private"),
        ]
        for index, (text, secret) in enumerate(cases):
            with self.subTest(kind=text.split("=", 1)[0][:24]):
                self.work = self.root / f"decoded-pattern-{index}"
                self.prepare()
                response = self.response("codex")
                response["checks"][0]["evidence"] = text
                escaped = json.dumps(response).replace(secret, "".join("\\u" + format(ord(char), "04x") for char in secret))
                result = self.record("codex", raw=escaped)
                self.assertNotIn(secret, json.dumps(result))
                self.record("claude-self")
                self.cli("aggregate", "--work", self.work)
                self.assertNotIn(secret, (self.work / "deterministic-review.md").read_text())


    def test_protocol_concurrent_failure_and_reissue_remain_blocking(self):
        self.prepare()
        self.record("claude-self")
        spec = importlib.util.spec_from_file_location("record_race_test", ENGINE)
        engine = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(engine)
        output, stderr = self.root / "held-response.json", self.root / "race.stderr"
        output.write_text(json.dumps(self.response("codex")))
        stderr.write_text("Quota exceeded")
        nonce, _, _ = engine.issue_request(self.work, "codex")
        args = dict(work=self.work, tag="codex", output=output, stderr=stderr, nonce=nonce)
        entered, release = threading.Event(), threading.Event()
        original = engine.text_file

        def hold_response(path, *args):
            if Path(path) == stderr:
                entered.set()
                if not release.wait(10):
                    raise AssertionError("record race did not release the first writer")
            return original(path, *args)

        with mock_patch.object(engine, "text_file", side_effect=hold_response):
            with ThreadPoolExecutor(max_workers=1) as pool:
                pending = pool.submit(engine.record, SimpleNamespace(**args, exit_code=0))
                try:
                    self.assertTrue(entered.wait(5))
                    with self.assertRaises(engine.Invalid):
                        engine.issue_request(self.work, "codex")
                    self.assertEqual(engine.record(SimpleNamespace(**args, exit_code=1)), 2)
                finally:
                    release.set()
                self.assertEqual(pending.result(timeout=5), 2)
        self.assert_blocked()
        engine.issue_request(self.work, "codex")
        self.record("codex")
        self.assert_blocked()

if __name__ == "__main__":
    unittest.main()
