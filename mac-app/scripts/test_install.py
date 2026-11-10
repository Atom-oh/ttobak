"""Tests for install.sh using stub executables on PATH (stdlib only)."""
import hashlib
import os
import shutil
import stat
import subprocess
import tarfile
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "install.sh")


class InstallTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.bin = os.path.join(self.tmp, "bin")
        self.home = os.path.join(self.tmp, "home")
        self.dest = os.path.join(self.tmp, "apps")
        self.log = os.path.join(self.tmp, "calls.log")
        self.web = os.path.join(self.tmp, "web")
        for d in (self.bin, self.dest, self.web,
                  os.path.join(self.home, "Library/Application Support/ttobak")):
            os.makedirs(d)
        open(self.log, "w").close()
        self.sock = os.path.join(self.home, "Library/Application Support/ttobak/control.sock")
        self.state = os.path.join(self.tmp, "state")
        os.makedirs(self.state)
        self.make_archive()
        self.stub("uname", 'case "$1" in -s) echo Darwin;; -m) echo arm64;; esac')
        self.stub("sw_vers", "echo 15.0")
        # curl: copy file named by URL basename from web dir to -o target.
        self.stub("curl", 'out=""; url=""\n'
                  'while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift;; -*) ;; *) url=$1;; esac; shift; done\n'
                  f'cp "{self.web}/$(basename "$url")" "$out"')
        self.stub("pgrep", f'[ -f "{self.state}/running" ] && [ ! -f "{self.state}/quit" ]')
        self.stub("osascript", f'echo "osascript $*" >> "{self.log}"; touch "{self.state}/quit"')
        self.stub("open", f'echo "open $*" >> "{self.log}"')
        self.stub("xattr", f'echo "xattr $*" >> "{self.log}"')
        self.stub("sleep", "true")
        self.stub("kill", f'echo "kill $*" >> "{self.log}"')
        self.stub("nc", f'echo "nc $*" >> "{self.log}"; cat >/dev/null; '
                  f'[ -f "{self.state}/nc_reply" ] && cat "{self.state}/nc_reply" || exit 1')

    def stub(self, name, body):
        p = os.path.join(self.bin, name)
        with open(p, "w") as f:
            f.write("#!/bin/sh\n" + body + "\n")
        os.chmod(p, os.stat(p).st_mode | stat.S_IXUSR)

    def make_archive(self, sha=None):
        src = os.path.join(self.tmp, "src")
        shutil.rmtree(src, True)
        os.makedirs(os.path.join(src, "TTOBAK.app/Contents"))
        with open(os.path.join(src, "TTOBAK.app/Contents/marker"), "w") as f:
            f.write("new")
        arc = os.path.join(self.web, "TTOBAK_0.2.0_aarch64.app.tar.gz")
        with tarfile.open(arc, "w:gz") as t:
            t.add(os.path.join(src, "TTOBAK.app"), arcname="TTOBAK.app")
        digest = sha or hashlib.sha256(open(arc, "rb").read()).hexdigest()
        with open(os.path.join(self.web, "latest.json"), "w") as f:
            f.write('{"version":"0.2.0","platforms":{"darwin-aarch64":{'
                    '"url":"https://x.test/mac/TTOBAK_0.2.0_aarch64.app.tar.gz",'
                    '"signature":"s","sha256":"%s"}}}' % digest)

    def install_old(self):
        d = os.path.join(self.dest, "TTOBAK.app/Contents")
        os.makedirs(d)
        with open(os.path.join(d, "marker"), "w") as f:
            f.write("old")

    def marker(self):
        with open(os.path.join(self.dest, "TTOBAK.app/Contents/marker")) as f:
            return f.read()

    def run_install(self):
        env = {"PATH": self.bin + ":/usr/bin:/bin", "HOME": self.home,
               "TMPDIR": self.tmp,
               "TTOBAK_INSTALL_BASE": "https://x.test/mac", "TTOBAK_INSTALL_DIR": self.dest}
        return subprocess.run(["sh", SCRIPT], env=env, capture_output=True, text=True,
                              stdin=subprocess.DEVNULL, timeout=60)

    def calls(self):
        return open(self.log).read()

    def set_running(self, reply=None):
        open(os.path.join(self.state, "running"), "w").close()
        open(self.sock, "w").close()  # not a socket; make -S pass below
        if reply is not None:
            with open(os.path.join(self.state, "nc_reply"), "w") as f:
                f.write(reply)

    def real_socket(self):
        import socket
        if os.path.exists(self.sock):
            os.remove(self.sock)
        short = tempfile.mkdtemp(dir="/tmp")
        self.addCleanup(shutil.rmtree, short, True)
        s = socket.socket(socket.AF_UNIX)
        s.bind(os.path.join(short, "c.sock"))
        self.addCleanup(s.close)
        os.symlink(os.path.join(short, "c.sock"), self.sock)

    def test_refuses_intel(self):
        self.stub("uname", 'case "$1" in -s) echo Darwin;; -m) echo x86_64;; esac')
        self.assertEqual(self.run_install().returncode, 2)

    def test_refuses_old_macos(self):
        self.stub("sw_vers", "echo 14.1")
        self.assertEqual(self.run_install().returncode, 2)

    def test_accepts_14_2(self):
        self.stub("sw_vers", "echo 14.2")
        self.assertEqual(self.run_install().returncode, 0)

    def test_checksum_mismatch_installs_nothing(self):
        self.make_archive(sha="0" * 64)
        r = self.run_install()
        self.assertEqual(r.returncode, 3)
        self.assertEqual(os.listdir(self.dest), [])

    def test_unwritable_dir(self):
        os.chmod(self.dest, 0o500)
        self.addCleanup(os.chmod, self.dest, 0o700)
        self.assertEqual(self.run_install().returncode, 6)

    def test_fresh_install(self):
        r = self.run_install()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.marker(), "new")
        self.assertIn("open", self.calls())

    def test_busy_app_refused(self):
        self.install_old()
        self.real_socket()
        open(os.path.join(self.state, "running"), "w").close()
        self.stub("nc", 'cat >/dev/null; echo \'{"v":1,"id":"install","ok":true,"data":{"updateBusy":true}}\'')
        r = self.run_install()
        self.assertEqual(r.returncode, 4)
        self.assertIn("녹음이 끝난 뒤 다시 실행해 주세요.", r.stderr)
        self.assertEqual(self.marker(), "old")
        self.assertNotIn("osascript", self.calls())

    def test_old_app_without_updateBusy_asks_to_quit(self):
        self.install_old()
        self.real_socket()
        open(os.path.join(self.state, "running"), "w").close()
        self.stub("nc", 'cat >/dev/null; echo \'{"v":1,"id":"install","ok":true,"data":{"recording":false}}\'')
        r = self.run_install()
        self.assertEqual(r.returncode, 5)
        self.assertNotIn("osascript", self.calls())
        self.assertEqual(self.marker(), "old")

    def test_socket_missing_exit5(self):
        open(os.path.join(self.state, "running"), "w").close()
        self.assertEqual(self.run_install().returncode, 5)

    def test_idle_app_quit_then_replaced(self):
        self.install_old()
        self.real_socket()
        open(os.path.join(self.state, "running"), "w").close()
        self.stub("nc", 'cat >/dev/null; echo \'{"v":1,"id":"install","ok":true,"data":{"updateBusy":false}}\'')
        r = self.run_install()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn('tell application id "click.atomai.ttobak.mac" to quit', self.calls())
        self.assertEqual(self.marker(), "new")
        self.assertFalse(os.path.exists(os.path.join(self.dest, "TTOBAK.app.previous")))

    def test_failed_move_restores_previous(self):
        self.install_old()
        self.stub("mv", 'case "$1" in */x/TTOBAK.app) exit 1;; esac\n'
                  'exec /bin/mv "$@"')
        r = self.run_install()
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(self.marker(), "old")
        self.assertFalse(os.path.exists(os.path.join(self.dest, "TTOBAK.app.previous")))

    def test_cleanup_failure_of_previous_copy_is_not_an_install_failure(self):
        self.install_old()
        # Removing an existing .previous copy fails; absent ones are fine.
        self.stub("rm", 'for a in "$@"; do case "$a" in *.previous) '
                  '[ ! -e "$a" ] || exit 1;; esac; done\n'
                  'exec /bin/rm "$@"')
        r = self.run_install()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.marker(), "new")
        self.assertIn("open ", self.calls())

    def test_stale_previous_is_restored_when_app_is_missing(self):
        d = os.path.join(self.dest, "TTOBAK.app.previous/Contents")
        os.makedirs(d)
        with open(os.path.join(d, "marker"), "w") as f:
            f.write("old")
        # Fail the new copy so the restored previous app is what remains.
        self.stub("mv", 'case "$1" in */x/TTOBAK.app) exit 1;; esac\n'
                  'exec /bin/mv "$@"')
        r = self.run_install()
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(self.marker(), "old")

    def test_truncated_script_runs_nothing(self):
        with open(SCRIPT) as f:
            text = f.read()
        cut = text[:text.index("\nmain \"$@\"")]
        r = subprocess.run(["sh", "-c", cut], capture_output=True, text=True,
                           stdin=subprocess.DEVNULL, env={"PATH": self.bin + ":/usr/bin:/bin", "HOME": self.home})
        self.assertEqual((r.returncode, r.stdout, r.stderr), (0, "", ""))
        self.assertEqual(self.calls(), "")

    def test_never_calls_kill(self):
        self.install_old()
        self.real_socket()
        open(os.path.join(self.state, "running"), "w").close()
        self.stub("nc", 'cat >/dev/null; echo \'{"data":{"updateBusy":false}}\'')
        self.run_install()
        self.assertNotIn("kill", self.calls())
        with open(SCRIPT) as f:
            self.assertNotRegex(f.read(), r"(^|\s)(kill|killall|pkill)\s")


if __name__ == "__main__":
    unittest.main()
