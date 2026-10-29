"""run_account.sh end to end on synthetic COT uploads (exit codes, STEP lines, work/out dirs)."""
import contextlib, io, json, os, re, subprocess, sys, tempfile, unittest, zipfile
from cot_fixtures import *  # noqa: E402,F401,F403 (shared COT upload fixtures and the Tmp base, moved from test_stage_cot.py)


def run_account(*args, env=None):
    e = dict(os.environ, **(env or {}))
    return subprocess.run(["bash", RUN_ACCOUNT, *args], capture_output=True, text=True, env=e)


class RunAccount(Tmp):
    def steps(self, p):
        return [l for l in p.stdout.splitlines() if l.startswith("STEP ")]

    def test_bad_account_refused(self):
        make_zip(self.zip, five())
        for acct in ("../x", "a b", "", "x" * 65, "a/b"):
            p = run_account(self.zip, self.work, os.path.join(self.d, "out"), acct)
            self.assertEqual(p.returncode, 2, acct)
            self.assertFalse(os.path.exists(self.work))

    def test_non_empty_work_refused(self):
        make_zip(self.zip, five())
        os.makedirs(self.work)
        wr("[]", os.path.join(self.work, "checks.json"), "w")
        p = run_account(self.zip, self.work, os.path.join(self.d, "out"), "acct-a")
        self.assertEqual(p.returncode, 2)
        self.assertEqual(self.steps(p), [])

    def test_invalid_upload_exit_3(self):
        wr(b"nope", self.zip, "wb")
        p = run_account(self.zip, self.work, os.path.join(self.work, "out"), "acct-a")
        self.assertEqual(p.returncode, 3)
        self.assertEqual(self.steps(p), ["STEP validate running", "STEP validate failed"])
        self.assertEqual(json.loads([l for l in p.stdout.splitlines() if l.startswith("{")][0]), {"reason": "zip 형식이 아닙니다"})

    def test_unreadable_xlsx_is_not_ok(self):
        make_zip(self.zip, five())  # 5 names, but the bytes are not workbooks
        p = run_account(self.zip, self.work, os.path.join(self.work, "out"), "acct-a")
        self.assertNotIn(p.returncode, (0, 1))
        self.assertEqual(self.steps(p)[-1], "STEP extract failed")

    def upload(self, sheet_rows, html):
        """The five files with a real workbook and the given COT360 page."""
        p = os.path.join(self.d, "t.xlsx")
        xlsx(p, {"Data Service Costs": sheet_rows})
        m = five()
        m["COT_OrigCo_Template.xlsx"] = rd(p, "rb")
        m["COT_OrigCo_COT360.html"] = html
        make_zip(self.zip, m)

    def test_empty_period_exit_3(self):
        self.upload([HDR], b"<html></html>")
        p = run_account(self.zip, self.work, os.path.join(self.work, "out"), "acct-a")
        self.assertEqual(p.returncode, 3, p.stderr[-500:])
        self.assertEqual(self.steps(p), ["STEP validate running", "STEP validate done", "STEP extract running", "STEP extract failed"])
        self.assertIn({"reason": "COT 기간을 찾지 못했습니다"}, [json.loads(l) for l in p.stdout.splitlines() if l.startswith("{")])

    def test_workbook_without_workbook_xml_exit_3_with_reason(self):
        m = five()
        bad = io.BytesIO()
        with zipfile.ZipFile(bad, "w") as z:
            z.writestr("xl/other.xml", "<x/>")
        m["COT_OrigCo_Template.xlsx"] = bad.getvalue()
        make_zip(self.zip, m)
        p = run_account(self.zip, self.work, os.path.join(self.work, "out"), "acct-a")
        self.assertEqual(p.returncode, 3, p.stderr[-500:])
        self.assertEqual(self.steps(p)[-1], "STEP extract failed")
        self.assertIn({"reason": "COT 엑셀 파일을 읽을 수 없습니다"}, [json.loads(l) for l in p.stdout.splitlines() if l.startswith("{")])
        self.assertEqual([l for l in p.stdout.splitlines() if not l.startswith(("STEP ", "{"))], [])

    def test_period_crash_is_exit_4(self):
        p = os.path.join(self.d, "t.xlsx")
        xlsx(p, {"Data Other": [HDR]})  # no Data_Service_Costs sheet: period.py raises
        m = five()
        m["COT_OrigCo_Template.xlsx"] = rd(p, "rb")
        m["COT_OrigCo_COT360.html"] = b"<html></html>"
        make_zip(self.zip, m)
        r = run_account(self.zip, self.work, os.path.join(self.work, "out"), "acct-a")
        self.assertEqual(r.returncode, 4, r.stderr[-500:])
        self.assertEqual(self.steps(r)[-1], "STEP extract failed")

    def test_malformed_cot360_html_is_an_invalid_upload(self):
        # html2json reads the untrusted page like the other converters: a broken table is exit 3 with a reason
        self.upload([HDR, ["", "OrigCo", "['EC2']", "2026-03-01", "5"]],
                    b'<script id="json-x" type="application/json">{not json</script>')
        p = run_account(self.zip, self.work, os.path.join(self.work, "out"), "acct-a")
        self.assertEqual(p.returncode, 3, p.stderr[-500:])
        self.assertEqual(self.steps(p)[-1], "STEP extract failed")
        self.assertIn('"reason": "COT360 HTML을 읽을 수 없습니다"', p.stdout)

    def test_entry_count_includes_skipped(self):
        make_zip(self.zip, {**five(), **{f"__MACOSX/._{i}": b"" for i in range(10)}})
        with self.assertRaises(safezip.ZipRejected):
            safezip.extract(self.zip, os.path.join(self.d, "x"), {".xlsx", ".pptx", ".html"}, max_entries=10)

    def test_stdout_carries_only_steps_and_reasons(self):
        sh = rd(RUN_ACCOUNT, encoding="utf-8")
        for line in sh.splitlines():
            if re.search(r'python3 -I "\$S/', line) and not re.search(r"period\.py|stage_cot\.py|xlsx2csv\.py|html2json\.py", line):
                self.assertRegex(line, r">&2|>\s*/dev/null", line)


if __name__ == "__main__":
    unittest.main()
