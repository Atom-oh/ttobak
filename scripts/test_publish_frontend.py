"""Exercise the real publication script against a synthetic AWS CLI boundary."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parent / "publish-frontend.sh"
FAKE_AWS = """#!/usr/bin/env python3
import fnmatch,json,os,sys
from pathlib import Path
a=sys.argv[1:]
with open(os.environ['AWS_LOG'],'a') as log:log.write(json.dumps(a)+'\\n')
if '--delete' in a:sys.exit(90)
if a[:2]==['cloudformation','describe-stacks']:
 if os.environ.get('FAIL_PHASE')=='stack':sys.exit(42)
 print(json.dumps([] if os.environ.get('FAIL_PHASE')=='stack-outputs' else [
  {'OutputKey':'SiteBucketName','OutputValue':'stack-site-bucket'},
  {'OutputKey':'DistributionId','OutputValue':'ESTACK123'}]));sys.exit(0)
if a[:2]==['cloudfront','create-invalidation']:print('TEST_INVALIDATION');sys.exit(0)
if a[:2]==['s3','cp'] and a[2].startswith('s3://') and a[3]=='-':
 print(json.loads(Path(os.environ['FAKE_S3_STATE']).read_text())['config.json']['body']);sys.exit(0)
source=Path(a[2]); prefix=a[3].split('/',3)[3] if a[3].count('/')>2 else ''
is_assets=prefix=='_next/static/'
if os.environ.get('FAIL_PHASE')==('assets' if is_assets else 'html' if a[1]=='cp' else 'mutable'):sys.exit(42)
state_path=Path(os.environ['FAKE_S3_STATE']); state=json.loads(state_path.read_text())
rules=[(a[i],a[i+1]) for i in range(4,len(a)-1) if a[i] in ['--include','--exclude']]
cache=a[a.index('--cache-control')+1]
for file in source.rglob('*'):
 if not file.is_file():continue
 name=file.relative_to(source).as_posix(); include=True
 for kind,pattern in rules:
  if fnmatch.fnmatchcase(name,pattern):include=kind=='--include'
 if include:state[prefix+name]={'body':file.read_text(),'cache':cache,'method':a[1]}
state_path.write_text(json.dumps(state))
"""


class FrontendPublicationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.export = self.root / "out"
        (self.export / "_next/static/chunks").mkdir(parents=True)
        (self.export / "_next/static/chunks/new-hash.js").write_text("NEW_CHUNK")
        (self.export / "index.html").write_text('<script src="/_next/static/chunks/new-hash.js"></script>')
        (self.export / "index.txt").write_text("NEW_RSC")
        (self.export / "icon.svg").write_text("NEW_ICON")
        (self.export / "config.json").write_text("BUILD_MUST_NOT_PUBLISH_CONFIG")
        self.store = self.root / "store.json"
        self.store.write_text(json.dumps({
            "config.json": {"body": "INFRA_CONFIG", "cache": "no-cache"},
            "_next/static/chunks/old-hash.js": {"body": "OLD_CHUNK", "cache": "immutable"},
            "index.html": {"body": "OLD_HTML", "cache": "stale"},
        }))
        self.log = self.root / "aws.log"
        (self.root / "aws").write_text(FAKE_AWS)
        (self.root / "aws").chmod(0o755)
        self.env = {**os.environ, "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "S3_BUCKET": "test-site-bucket", "CF_DISTRIBUTION": "ETEST123",
                    "FAKE_S3_STATE": str(self.store), "AWS_LOG": str(self.log)}

    def run_publish(self, **environment):
        result = subprocess.run(["bash", str(SCRIPT), str(self.export)],
                                env={**self.env, **environment}, capture_output=True, text=True)
        calls = [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []
        return result, calls, json.loads(self.store.read_text())

    def test_assets_precede_forced_html_and_old_chunks_and_config_survive(self):
        result, calls, state = self.run_publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([c[:2] for c in calls], [
            ["s3", "sync"], ["s3", "sync"], ["s3", "cp"], ["cloudfront", "create-invalidation"],
        ])
        self.assertTrue(calls[0][3].endswith("/_next/static/"))
        self.assertEqual(state["config.json"]["body"], "INFRA_CONFIG")
        self.assertEqual(state["_next/static/chunks/old-hash.js"]["body"], "OLD_CHUNK")
        self.assertIn("immutable", state["_next/static/chunks/new-hash.js"]["cache"])
        for name in ["index.html", "index.txt"]:
            self.assertEqual(state[name]["method"], "cp")
            self.assertEqual(state[name]["cache"], "no-cache,max-age=0,must-revalidate")
        self.assertEqual(calls[-1][calls[-1].index("--paths") + 1], "/*")
        self.assertNotIn("BUILD_MUST_NOT_PUBLISH_CONFIG", json.dumps(state))

    def test_asset_failure_keeps_previous_html_and_never_invalidates(self):
        result, calls, state = self.run_publish(FAIL_PHASE="assets")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 1)
        self.assertEqual(state["index.html"]["body"], "OLD_HTML")

    def test_runtime_connector_profiles_survive_static_publication_unchanged(self):
        state = json.loads(self.store.read_text())
        config = json.dumps({
            "staticRouterRevision": "segmented-rsc-v1",
            "cognito": {"userPoolClientId": "firstparty123"},
            "mcp": {"url": "/api/mcp", "privateBridgeEnabled": True,
                    "readClientId": "readonly123", "writeClientId": "writer123"},
        })
        state["config.json"] = {"body": config, "cache": "no-cache"}
        self.store.write_text(json.dumps(state))
        result, _, installed = self.run_publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(installed["config.json"], state["config.json"])

    def test_html_failure_leaves_chunks_available_and_never_invalidates(self):
        result, calls, state = self.run_publish(FAIL_PHASE="html")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 3)
        self.assertEqual(state["index.html"]["body"], "OLD_HTML")
        self.assertIn("_next/static/chunks/new-hash.js", state)
        self.assertIn("_next/static/chunks/old-hash.js", state)

    def test_incomplete_export_and_symlinks_fail_before_cloud_writes(self):
        (self.export / "index.html").unlink()
        result, calls, _ = self.run_publish()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])
        (self.export / "index.html").write_text("HTML")
        (self.export / "private-link").symlink_to(self.store)
        result, calls, _ = self.run_publish()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_workflow_and_legacy_frontend_entrypoint_use_the_verified_publisher(self):
        root = SCRIPT.parent.parent
        workflow = (root / ".github/workflows/deploy-frontend.yml").read_text()
        self.assertIn("bash scripts/publish-frontend.sh frontend/out", workflow)
        self.assertIn("cancel-in-progress: false", workflow)
        self.assertIn("test_publish_frontend.py", workflow)
        self.assertNotIn("--delete", workflow)
        legacy = (root / "scripts/deploy.sh").read_text()
        body = legacy.split("deploy_frontend()", 1)[1].split("\n}", 1)[0]
        self.assertIn("publish-frontend.sh", body)
        self.assertNotIn("s3 sync", body)
        self.assertNotIn("cdk deploy --all", legacy)
        self.assertIn('targets=("frontend")', legacy)

    def test_segmented_exports_require_the_deployed_router_before_any_write(self):
        (self.export / "accounts").mkdir()
        (self.export / "accounts/__next.accounts.__PAGE__.txt").write_text("SEGMENT")
        result, calls, state = self.run_publish()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 1)
        self.assertEqual(state["index.html"]["body"], "OLD_HTML")
        self.assertNotIn("INFRA_CONFIG", result.stdout + result.stderr)
        self.log.unlink()
        state["config.json"]["body"] = '{"staticRouterRevision":"segmented-rsc-v1"}'
        self.store.write_text(json.dumps(state))
        result, calls, state = self.run_publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 5)
        self.assertEqual(state["accounts/__next.accounts.__PAGE__.txt"]["method"], "cp")
        self.assertEqual(state["config.json"]["body"], '{"staticRouterRevision":"segmented-rsc-v1"}')

    def test_legacy_entrypoint_uses_current_stack_outputs_before_publication(self):
        project = self.root / "project"
        (project / "scripts").mkdir(parents=True)
        shutil.copy(SCRIPT, project / "scripts/publish-frontend.sh")
        shutil.copy(SCRIPT.parent / "deploy.sh", project / "scripts/deploy.sh")
        shutil.copytree(self.export, project / "frontend/out")
        for failure in ["stack", "stack-outputs", ""]:
            with self.subTest(failure=failure):
                if self.log.exists():
                    self.log.unlink()
                result = subprocess.run(["bash", str(project / "scripts/deploy.sh"), "frontend"],
                                        env={**self.env, "FAIL_PHASE": failure}, capture_output=True, text=True)
                calls = [json.loads(line) for line in self.log.read_text().splitlines()]
                self.assertEqual(calls[0][:2], ["cloudformation", "describe-stacks"])
                if failure:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(len(calls), 1)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(calls[1][3], "s3://stack-site-bucket/_next/static/")
                    self.assertEqual(calls[-1][calls[-1].index("--distribution-id") + 1], "ESTACK123")


if __name__ == "__main__":
    unittest.main()
