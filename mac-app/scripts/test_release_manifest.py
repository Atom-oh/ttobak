"""Tests for release_manifest.py and the release.sh guards (stdlib only).

release.sh is exercised against stub git/aws/npx/security executables on PATH
in a copied project tree; nothing here builds, tags, pushes or calls AWS.
"""
import base64
import datetime
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
MANIFEST = os.path.join(HERE, "release_manifest.py")
RELEASE = os.path.join(HERE, "release.sh")
LEAF = "C1D1D726FA434621D65DED0261A722AA70924A26"
URL = "https://ttobak.atomai.click/mac/TTOBAK_0.2.0_aarch64.app.tar.gz"


def fake_pubkey(key_id=b"\x01\x02\x03\x04\x05\x06\x07\x08"):
    inner = "untrusted comment: minisign public key: test\n%s\n" % (
        base64.b64encode(b"Ed" + key_id + b"\x00" * 32).decode())
    return base64.b64encode(inner.encode()).decode()


def fake_signature(version="0.2.0", key_id=b"\x01\x02\x03\x04\x05\x06\x07\x08",
                   file_name="TTOBAK_0.2.0_aarch64.app.tar.gz"):
    comment = "timestamp:1794291959\tfile:%s" % file_name
    if version is not None:
        comment += "\tversion:%s" % version
    inner = "untrusted comment: signature from tauri secret key\n%s\ntrusted comment: %s\n%s\n" % (
        base64.b64encode(b"ED" + key_id + b"\x11" * 64).decode(), comment,
        base64.b64encode(b"\x22" * 64).decode())
    return base64.b64encode(inner.encode()).decode()


def run_manifest(*args):
    return subprocess.run([sys.executable, MANIFEST, *args], capture_output=True, text=True)


class ManifestTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.archive = os.path.join(self.tmp, "TTOBAK_0.2.0_aarch64.app.tar.gz")
        with open(self.archive, "wb") as f:
            f.write(b"archive-bytes" * 100)
        self.sig = self.archive + ".sig"
        self.write_sig(fake_signature())

    def write_sig(self, text):
        with open(self.sig, "w") as f:
            f.write(text)

    def build(self, *extra):
        return run_manifest("build", "--version", "0.2.0", "--notes", "녹음 안정성 개선",
                            "--url", URL, "--signature-file", self.sig,
                            "--archive", self.archive, *extra)

    def test_build_fields_types_and_platform_key(self):
        r = self.build()
        self.assertEqual(r.returncode, 0, r.stderr)
        m = json.loads(r.stdout)
        self.assertEqual(set(m), {"version", "notes", "pub_date", "platforms"})
        self.assertEqual(m["version"], "0.2.0")
        self.assertEqual(m["notes"], "녹음 안정성 개선")
        self.assertIsInstance(m["pub_date"], str)
        self.assertTrue(m["pub_date"].endswith("Z"))
        when = datetime.datetime.strptime(m["pub_date"], "%Y-%m-%dT%H:%M:%SZ")
        now = datetime.datetime.utcnow()
        self.assertLess(abs((now - when).total_seconds()), 300)
        self.assertEqual(list(m["platforms"]), ["darwin-aarch64"])
        entry = m["platforms"]["darwin-aarch64"]
        self.assertEqual(set(entry), {"url", "signature", "sha256"})
        self.assertEqual(entry["url"], URL)
        self.assertEqual(entry["signature"], fake_signature())
        for value in entry.values():
            self.assertIsInstance(value, str)

    def test_sha256_matches_the_archive(self):
        r = self.build()
        self.assertEqual(r.returncode, 0, r.stderr)
        with open(self.archive, "rb") as f:
            want = hashlib.sha256(f.read()).hexdigest()
        self.assertEqual(json.loads(r.stdout)["platforms"]["darwin-aarch64"]["sha256"], want)

    def test_build_requires_signature_bound_to_the_version(self):
        for sig in (fake_signature(version=None), fake_signature(version="0.1.9")):
            with self.subTest(sig=sig[:12]):
                self.write_sig(sig)
                r = self.build()
                self.assertNotEqual(r.returncode, 0)
                self.assertEqual(r.stdout, "")

    def test_build_checks_signature_key_id_against_pubkey(self):
        self.assertEqual(self.build("--pubkey", fake_pubkey()).returncode, 0)
        r = self.build("--pubkey", fake_pubkey(key_id=b"\x09" * 8))
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("key", r.stderr.lower())

    def test_build_rejects_url_not_naming_the_archive_or_not_https(self):
        for url in ("http://ttobak.atomai.click/mac/TTOBAK_0.2.0_aarch64.app.tar.gz",
                    "https://ttobak.atomai.click/mac/TTOBAK_0.1.0_aarch64.app.tar.gz"):
            with self.subTest(url=url):
                r = run_manifest("build", "--version", "0.2.0", "--notes", "n", "--url", url,
                                 "--signature-file", self.sig, "--archive", self.archive)
                self.assertNotEqual(r.returncode, 0)

    def test_build_rejects_non_semver_version(self):
        r = run_manifest("build", "--version", "0.2", "--notes", "n", "--url", URL,
                         "--signature-file", self.sig, "--archive", self.archive)
        self.assertNotEqual(r.returncode, 0)

    def published(self, version):
        path = os.path.join(self.tmp, "published.json")
        with open(path, "w") as f:
            json.dump({"version": version, "platforms": {}}, f)
        return path

    def test_check_newer(self):
        cases = [("0.1.9", 0), ("0.2.0", 1), ("0.3.0", 1), ("0.10.0", 1), ("1.0.0", 1)]
        for published, code in cases:
            with self.subTest(published=published):
                r = run_manifest("check-newer", "--published", self.published(published),
                                 "--version", "0.2.0")
                self.assertEqual(r.returncode, code, r.stderr)
        r = run_manifest("check-newer", "--published", self.published("0.9.0"),
                         "--version", "0.10.0")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_check_newer_accepts_missing_published_file(self):
        r = run_manifest("check-newer", "--published", os.path.join(self.tmp, "absent.json"),
                         "--version", "0.2.0")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_check_newer_allow_equal_only_relaxes_equality(self):
        def run(published):
            return run_manifest("check-newer", "--published", self.published(published),
                                "--version", "0.2.0", "--allow-equal").returncode
        self.assertEqual(run("0.2.0"), 0)
        self.assertEqual(run("0.3.0"), 1)

    def test_check_newer_rejects_malformed_published(self):
        path = os.path.join(self.tmp, "bad.json")
        for body in ("<html>", '{"version": 3}', '{"version": "x"}'):
            with self.subTest(body=body):
                with open(path, "w") as f:
                    f.write(body)
                r = run_manifest("check-newer", "--published", path, "--version", "0.2.0")
                self.assertNotEqual(r.returncode, 0)

    def test_read_version(self):
        conf = os.path.join(self.tmp, "tauri.conf.json")
        cargo = os.path.join(self.tmp, "Cargo.toml")
        with open(conf, "w") as f:
            json.dump({"version": "0.2.0"}, f)
        with open(cargo, "w") as f:
            f.write('[package]\nname = "x"\nversion = "0.2.0"\n\n[dependencies]\n'
                    'serde = { version = "1" }\n')
        r = run_manifest("read-version", "--tauri-conf", conf, "--cargo-toml", cargo)
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "0.2.0"), r.stderr)
        with open(cargo, "w") as f:
            f.write('[package]\nname = "x"\nversion = "0.1.0"\n')
        r = run_manifest("read-version", "--tauri-conf", conf, "--cargo-toml", cargo)
        self.assertNotEqual(r.returncode, 0)


# --------------------------------------------------------------------------
# release.sh guard tests

GIT_STUB = r'''#!/bin/sh
echo "git $*" >> "$CALLS"
[ "$1" = "-C" ] && shift 2
case "$1" in
  status) printf '%s' "${GIT_DIRTY:-}" ;;
  fetch) ;;
  rev-parse)
    shift
    for a in "$@"; do last=$a; done
    case "$last" in
      HEAD|HEAD^\{commit\}) echo "$HEAD_SHA" ;;
      origin/main|origin/main^\{commit\}) echo "$MAIN_SHA" ;;
      refs/tags/*) [ -n "${LOCAL_TAG_SHA:-}" ] || exit 1; echo "$LOCAL_TAG_SHA" ;;
      *) exit 1 ;;
    esac ;;
  ls-remote)
    if [ -n "${REMOTE_TAG_SHA:-}" ]; then
      printf 'aaaa\trefs/tags/mac-v0.2.0\n%s\trefs/tags/mac-v0.2.0^{}\n' "$REMOTE_TAG_SHA"
    fi ;;
  for-each-ref)
    if [ -n "${TAG_STORE:-}" ] && [ -f "$TAG_STORE" ]; then cat "$TAG_STORE"; echo
    else printf '%s\n' "${TAG_NOTES:-}"; fi ;;
  tag)
    # Emulate git: without --cleanup=verbatim the message is stripped.
    if [ -n "${TAG_STORE:-}" ]; then
      verbatim=0; msg=""
      while [ $# -gt 0 ]; do
        case "$1" in --cleanup=verbatim) verbatim=1;; -m) msg=$2; shift;; esac
        shift
      done
      if [ $verbatim -eq 1 ]; then printf '%s' "$msg" > "$TAG_STORE"
      else printf '%s' "$msg" | grep -v '^#' | sed 's/[[:space:]]*$//' > "$TAG_STORE"; fi
    fi ;;
  push) ;;
  *) exit 99 ;;
esac
'''

AWS_STUB = r'''#!/usr/bin/env python3
import json, os, shutil, sys
a = sys.argv[1:]
with open(os.environ["CALLS"], "a") as log:
    log.write("aws " + json.dumps(a) + "\n")
s3 = os.environ["S3_DIR"]
def opt(name):
    return a[a.index(name) + 1]
if a[:2] == ["cloudformation", "list-exports"]:
    q = opt("--query")
    # The real CLI applies --query per page: emulate three pages, one match.
    value = "ttobak-site-bucket" if "TtobakSiteBucketName" in q else "ETESTDIST1"
    if os.environ.get("EXPORTS_MISSING"):
        value = ""
    print("\n" + value + "\n")
elif a[:2] == ["s3api", "head-object"]:
    if os.path.exists(os.path.join(s3, opt("--key"))):
        print("{}")
    else:
        sys.stderr.write("An error occurred (404) when calling the HeadObject operation: Not Found\n")
        sys.exit(254)
elif a[:2] == ["s3", "cp"]:
    src, dst = a[2], a[3]
    if src.startswith("s3://"):
        shutil.copy(os.path.join(s3, src.split("/", 3)[3]), dst)
    else:
        key = dst.split("/", 3)[3]
        os.makedirs(os.path.dirname(os.path.join(s3, key)), exist_ok=True)
        shutil.copy(src, os.path.join(s3, key))
elif a[:2] == ["cloudfront", "create-invalidation"]:
    print("TESTINV")
else:
    sys.exit(98)
'''


class ReleaseGuardTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.root = os.path.join(self.tmp, "repo")
        self.app = os.path.join(self.root, "mac-app")
        os.makedirs(os.path.join(self.app, "scripts"))
        os.makedirs(os.path.join(self.app, "src-tauri"))
        for name in ("release.sh", "release_manifest.py", "install.sh"):
            shutil.copy(os.path.join(HERE, name), os.path.join(self.app, "scripts", name))
        self.write_config("0.2.0", "0.2.0", fake_pubkey())
        self.bin = os.path.join(self.tmp, "bin")
        self.s3 = os.path.join(self.tmp, "s3")
        os.makedirs(self.bin)
        os.makedirs(os.path.join(self.s3, "mac"))
        self.calls = os.path.join(self.tmp, "calls.log")
        open(self.calls, "w").close()
        self.stub("git", GIT_STUB)
        self.stub("aws", AWS_STUB)
        self.stub("npx", 'echo "npx $*" >> "$CALLS"; exit 97')
        self.stub("codesign", 'echo "codesign $*" >> "$CALLS"; exit 96')
        self.stub("uname", 'case "$1" in -s) echo Darwin;; -m) echo arm64;; esac')
        self.stub("security", 'echo "security $*" >> "$CALLS"\n'
                  'case "$1" in find-identity) printf \'  1) %s "TTOBAK Dev"\\n\' "$IDENTITY_SHA";; '
                  '*) exit 44;; esac')
        self.env = dict(os.environ, PATH=self.bin + os.pathsep + os.environ["PATH"],
                        CALLS=self.calls, S3_DIR=self.s3, HEAD_SHA="h" * 40,
                        MAIN_SHA="h" * 40, IDENTITY_SHA=LEAF, TAG_NOTES="notes")
        for key in ("TAURI_SIGNING_PRIVATE_KEY_PASSWORD", "TAURI_SIGNING_PRIVATE_KEY"):
            self.env.pop(key, None)
        self.env["TAURI_SIGNING_PRIVATE_KEY_PATH"] = os.path.join(self.tmp, "absent.key")

    def write_config(self, conf_version, cargo_version, pubkey):
        with open(os.path.join(self.app, "src-tauri", "tauri.conf.json"), "w") as f:
            json.dump({"version": conf_version, "plugins": {"updater": {
                "endpoints": ["https://ttobak.atomai.click/mac/latest.json"],
                "pubkey": pubkey, "requireSignedVersion": True}}}, f)
        with open(os.path.join(self.app, "src-tauri", "Cargo.toml"), "w") as f:
            f.write('[package]\nname = "ttobak-mac"\nversion = "%s"\n' % cargo_version)

    def stub(self, name, body):
        path = os.path.join(self.bin, name)
        with open(path, "w") as f:
            f.write(body if body.startswith("#!") else "#!/bin/sh\n" + body + "\n")
        os.chmod(path, 0o755)

    def put_s3(self, name, data):
        with open(os.path.join(self.s3, "mac", name), "wb") as f:
            f.write(data)

    def run_release(self, *args, **env):
        r = subprocess.run(["bash", os.path.join(self.app, "scripts", "release.sh"), *args],
                           env=dict(self.env, **env), capture_output=True, text=True,
                           stdin=subprocess.DEVNULL)
        with open(self.calls) as f:
            calls = f.read().splitlines()
        return r, calls

    def mutations(self, calls):
        out = []
        for c in calls:
            if c.startswith("git ") and (" tag " in c or " push " in c):
                out.append(c)
            elif c.startswith("aws ") and "create-invalidation" in c:
                out.append(c)
            elif c.startswith("aws ") and '"cp"' in c:
                args = json.loads(c[4:])
                if not args[2].startswith("s3://"):
                    out.append(c)
        return out

    def assert_refused_before_work(self, r, calls, needle):
        self.assertNotEqual(r.returncode, 0)
        self.assertIn(needle, r.stderr)
        self.assertEqual(self.mutations(calls), [])
        self.assertFalse([c for c in calls if c.startswith(("npx", "codesign"))])

    def test_syntax(self):
        r = subprocess.run(["bash", "-n", RELEASE], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_requires_notes(self):
        r, calls = self.run_release()
        self.assertEqual(r.returncode, 2)
        self.assertEqual(calls, [])
        r, calls = self.run_release("--dry-run", "  ")
        self.assertEqual(r.returncode, 2)

    def test_refuses_dirty_tree(self):
        r, calls = self.run_release("notes", GIT_DIRTY=" M mac-app/x\n")
        self.assert_refused_before_work(r, calls, "clean")
        self.assertFalse([c for c in calls if c.startswith("aws")])

    def test_refuses_head_not_origin_main(self):
        r, calls = self.run_release("notes", MAIN_SHA="m" * 40)
        self.assert_refused_before_work(r, calls, "origin/main")

    def test_refuses_version_mismatch(self):
        self.write_config("0.2.0", "0.1.0", fake_pubkey())
        r, calls = self.run_release("notes")
        self.assert_refused_before_work(r, calls, "version")

    def test_refuses_missing_export(self):
        r, calls = self.run_release("notes", EXPORTS_MISSING="1")
        self.assert_refused_before_work(r, calls, "could not read CloudFormation exports")

    def test_refuses_placeholder_pubkey(self):
        self.write_config("0.2.0", "0.2.0", "REPLACE_WITH_TTOBAK_UPDATER_PUBKEY")
        r, calls = self.run_release("notes")
        self.assert_refused_before_work(r, calls, "pubkey")

    def test_refuses_other_signing_identity(self):
        r, calls = self.run_release("notes", IDENTITY_SHA="AB" * 20)
        self.assert_refused_before_work(r, calls, "TTOBAK Dev")

    def test_refuses_version_not_newer_than_published(self):
        self.put_s3("latest.json", json.dumps({"version": "0.2.0"}).encode())
        r, calls = self.run_release("notes")
        self.assert_refused_before_work(r, calls, "newer")

    def test_refuses_tag_on_another_commit(self):
        for env in ({"LOCAL_TAG_SHA": "o" * 40}, {"REMOTE_TAG_SHA": "o" * 40}):
            with self.subTest(env=env):
                r, calls = self.run_release("notes", **env)
                self.assert_refused_before_work(r, calls, "another commit")

    def test_new_release_needs_signing_key_before_build(self):
        r, calls = self.run_release("notes")
        self.assert_refused_before_work(r, calls, "TAURI_SIGNING_PRIVATE_KEY_PATH")

    def local_artifacts(self, sig):
        art = os.path.join(self.app, "src-tauri", "target", "release-artifacts", "0.2.0")
        os.makedirs(art)
        with open(os.path.join(art, "TTOBAK_0.2.0_aarch64.app.tar.gz"), "wb") as f:
            f.write(b"built-archive")
        with open(os.path.join(art, "TTOBAK_0.2.0_aarch64.app.tar.gz.sig"), "w") as f:
            f.write(sig)

    def test_resume_refuses_when_remote_signature_differs(self):
        self.local_artifacts(fake_signature())
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz.sig", b"other-signature")
        r, calls = self.run_release("notes", REMOTE_TAG_SHA="h" * 40)
        self.assert_refused_before_work(r, calls, "signature")

    def test_resume_refuses_without_local_build_of_published_archive(self):
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz.sig", fake_signature().encode())
        r, calls = self.run_release("notes", REMOTE_TAG_SHA="h" * 40)
        self.assert_refused_before_work(r, calls, "local")

    def test_resume_refuses_different_notes(self):
        self.local_artifacts(fake_signature())
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz.sig", fake_signature().encode())
        r, calls = self.run_release("other notes", REMOTE_TAG_SHA="h" * 40)
        self.assert_refused_before_work(r, calls, "notes")

    def test_tag_is_created_verbatim_and_resume_accepts_markdown_notes(self):
        notes = "# Heading  \nline two   \n\n  - item"
        store = os.path.join(self.tmp, "tagstore")
        # A fresh release creates the tag with the exact notes.
        with open(os.path.join(HERE, "release.sh")) as f:
            tag_lines = [l for l in f if " tag -a " in l]
        self.assertEqual(len(tag_lines), 1)
        self.assertIn("--cleanup=verbatim", tag_lines[0])
        # Resume compares against what git stored from that same command.
        subprocess.run([os.path.join(self.bin, "git"), "tag", "-a", "--cleanup=verbatim",
                        "mac-v0.2.0", "-m", notes, "h" * 40],
                       env=dict(self.env, TAG_STORE=store, CALLS=self.calls), check=True)
        self.local_artifacts(fake_signature())
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz.sig", fake_signature().encode())
        r, calls = self.run_release(notes, REMOTE_TAG_SHA="h" * 40, TAG_STORE=store)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_resume_with_matching_signature_uploads_only_install_and_manifest(self):
        self.local_artifacts(fake_signature())
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz.sig", fake_signature().encode())
        r, calls = self.run_release("notes", REMOTE_TAG_SHA="h" * 40)
        self.assertEqual(r.returncode, 0, r.stderr)
        muts = self.mutations(calls)
        self.assertEqual(len(muts), 3, muts)
        self.assertTrue(json.loads(muts[0][4:])[3].endswith("/mac/install.sh"))
        self.assertTrue(json.loads(muts[1][4:])[3].endswith("/mac/latest.json"))
        inv = json.loads(muts[2][4:])
        self.assertEqual(inv[:2], ["cloudfront", "create-invalidation"])
        self.assertEqual(inv[inv.index("--distribution-id") + 1], "ETESTDIST1")
        self.assertIn("/mac/latest.json", inv)
        self.assertIn("/mac/install.sh", inv)
        for m in muts[:2]:
            self.assertIn("no-cache,max-age=0,must-revalidate", json.loads(m[4:]))
        self.assertFalse([c for c in calls if c.startswith(("npx", "codesign"))])
        with open(os.path.join(self.s3, "mac", "latest.json")) as f:
            manifest = json.load(f)
        self.assertEqual(manifest["version"], "0.2.0")
        self.assertEqual(manifest["notes"], "notes")
        self.assertEqual(manifest["platforms"]["darwin-aarch64"]["sha256"],
                         hashlib.sha256(b"built-archive").hexdigest())
        self.assertNotIn("TESTINV", r.stderr)

    def test_resume_uploads_missing_signature_only_for_the_same_archive(self):
        self.local_artifacts(fake_signature())
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"a-different-archive")
        r, calls = self.run_release("notes", REMOTE_TAG_SHA="h" * 40)
        self.assert_refused_before_work(r, calls, "archive")
        os.remove(self.calls)
        open(self.calls, "w").close()
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        r, calls = self.run_release("notes", REMOTE_TAG_SHA="h" * 40)
        self.assertEqual(r.returncode, 0, r.stderr)
        keys = [json.loads(m[4:])[3].rsplit("/", 1)[1] for m in self.mutations(calls)[:-1]]
        self.assertEqual(keys, ["TTOBAK_0.2.0_aarch64.app.tar.gz.sig", "install.sh", "latest.json"])

    def test_resume_local_tag_unpushed_pushes_before_upload(self):
        self.local_artifacts(fake_signature())
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz.sig", fake_signature().encode())
        r, calls = self.run_release("notes", LOCAL_TAG_SHA="h" * 40)
        self.assertEqual(r.returncode, 0, r.stderr)
        muts = self.mutations(calls)
        self.assertIn(" push ", muts[0])
        self.assertFalse([m for m in muts if " tag " in m])

    def test_dry_run_performs_no_mutation(self):
        self.local_artifacts(fake_signature())
        self.put_s3("TTOBAK_0.2.0_aarch64.app.tar.gz", b"built-archive")
        r, calls = self.run_release("--dry-run", "notes", LOCAL_TAG_SHA="h" * 40)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.mutations(calls), [])
        self.assertIn("dry-run", r.stdout)
        self.assertIn("latest.json", r.stdout)
        self.assertFalse(os.path.exists(os.path.join(self.s3, "mac", "latest.json")))


if __name__ == "__main__":
    unittest.main()
