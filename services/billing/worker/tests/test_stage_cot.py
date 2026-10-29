"""Uploaded COT zips are untrusted: safe extraction, staging under canonical names, and the one-account runner's
refusals. Synthetic zips only (built here with zipfile)."""
import contextlib, io, json, os, re, subprocess, sys, tempfile, unittest, zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))  # services/billing/worker
SCRIPTS = os.path.join(ROOT, "scripts")
RUN_ACCOUNT = os.path.join(ROOT, "run_account.sh")
sys.path.insert(0, SCRIPTS)
import safezip  # noqa: E402
import stage_cot  # noqa: E402

KINDS = ("Template.xlsx", "Current_State_Template.xlsx", "COH_Template.xlsx", "PPT_Companion.pptx", "COT360.html")


from fileutil import rd, wr  # noqa: E402

def five(prefix="COT_OrigCo_"):
    return {prefix + k: b"x" * 10 for k in KINDS}


def make_zip(path, members, compression=zipfile.ZIP_DEFLATED):
    """members: {name: bytes} or a list of (ZipInfo|name, bytes)."""
    items = members.items() if isinstance(members, dict) else members
    with zipfile.ZipFile(path, "w", compression) as z:
        for name, data in items:
            z.writestr(name, data)
    return path


class Tmp(unittest.TestCase):
    def setUp(self):
        self._d = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.d = self._d.name
        self.zip = os.path.join(self.d, "up.zip")
        self.work = os.path.join(self.d, "work")

    def tearDown(self):
        self._d.cleanup()

    def stage(self, acct="acct-a"):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = stage_cot.main(self.zip, self.work, acct)
        out = buf.getvalue().strip()
        return rc, (json.loads(out)["reason"] if out else None)


class StageCot(Tmp):
    def test_valid_five_files(self):
        make_zip(self.zip, five())
        self.assertEqual(self.stage(), (0, None))
        got = sorted(os.listdir(os.path.join(self.work, "extracted", "acct-a")))
        self.assertEqual(got, sorted("COT_acct-a_" + k for k in KINDS))

    def test_restage_replaces_the_previous_stage(self):
        make_zip(self.zip, five())
        self.assertEqual(self.stage(), (0, None))
        dest = os.path.join(self.work, "extracted", "acct-a")
        wr(b"stale", os.path.join(dest, "leftover.bin"), "wb")
        make_zip(self.zip, {k: b"y" * 10 for k in five("COT_Other_")})
        self.assertEqual(self.stage(), (0, None))
        self.assertEqual(sorted(os.listdir(dest)), sorted("COT_acct-a_" + k for k in KINDS))
        self.assertEqual(rd(os.path.join(dest, "COT_acct-a_Template.xlsx"), "rb"), b"y" * 10)

    def test_case_insensitive_suffix(self):
        make_zip(self.zip, {("cot_origco_" + k).lower(): b"x" for k in KINDS})
        self.assertEqual(self.stage()[0], 0)
        self.assertTrue(os.path.exists(os.path.join(self.work, "extracted", "acct-a", "COT_acct-a_Template.xlsx")))

    def test_nested_folder_and_macosx(self):
        members = {"COT_X/": b""}
        members.update({"COT_X/" + k: v for k, v in five().items()})
        members["__MACOSX/COT_X/._a"] = b"apple"
        members["COT_X/._COT_OrigCo_COT360.html"] = b"apple"
        make_zip(self.zip, members)
        self.assertEqual(self.stage(), (0, None))
        self.assertEqual(len(os.listdir(os.path.join(self.work, "extracted", "acct-a"))), 5)

    def test_path_traversal_rejected(self):
        make_zip(self.zip, {**five(), "../evil.html": b"x"})
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertIn("..", reason)
        self.assertFalse(os.path.exists(os.path.join(self.d, "evil.html")))

    def test_absolute_path_rejected(self):
        make_zip(self.zip, {**five(), "/etc/evil.html": b"x"})
        self.assertEqual(self.stage()[0], 3)

    def test_symlink_rejected(self):
        zi = zipfile.ZipInfo("COT_OrigCo_COT360.html")
        zi.external_attr = (0o120777 << 16)
        members = [(k, v) for k, v in five().items() if not k.endswith("COT360.html")] + [(zi, b"/etc/passwd")]
        make_zip(self.zip, members)
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertIn("심볼릭 링크", reason)

    def test_zip_bomb_ratio_rejected(self):
        make_zip(self.zip, {"COT_OrigCo_COT360.html": b"\0" * 2 ** 20})
        with self.assertRaises(safezip.ZipRejected) as cm:
            safezip.extract(self.zip, os.path.join(self.d, "x"), {".html"}, max_ratio=10)
        self.assertIn("압축률", cm.exception.reason)
        self.assertEqual(os.listdir(os.path.join(self.d, "x")) if os.path.exists(os.path.join(self.d, "x")) else [], [])

    def test_total_size_and_entry_count_rejected(self):
        make_zip(self.zip, five())
        with self.assertRaises(safezip.ZipRejected):
            safezip.extract(self.zip, os.path.join(self.d, "x"), {".xlsx", ".pptx", ".html"}, max_total=49)
        with self.assertRaises(safezip.ZipRejected):
            safezip.extract(self.zip, os.path.join(self.d, "y"), {".xlsx", ".pptx", ".html"}, max_entries=4)

    def test_lying_header_size_stopped_while_streaming(self):
        # the header claims 10 bytes; the stream is cut at the running total, not trusted from the header
        make_zip(self.zip, {"COT_OrigCo_COT360.html": b"a" * 5000})
        raw = bytearray(rd(self.zip, "rb"))
        for sig in (b"PK\x03\x04", b"PK\x01\x02"):
            i = raw.find(sig)
            off = 22 if sig == b"PK\x03\x04" else 24
            raw[i + off:i + off + 4] = (10).to_bytes(4, "little")
        wr(bytes(raw), self.zip, "wb")
        with self.assertRaises(Exception):
            safezip.extract(self.zip, os.path.join(self.d, "x"), {".html"}, max_total=100)
        out = os.path.join(self.d, "x", "COT_OrigCo_COT360.html")
        self.assertFalse(os.path.exists(out) and os.path.getsize(out) > 100)

    def test_encrypted_rejected(self):
        make_zip(self.zip, five(), zipfile.ZIP_STORED)
        raw = bytearray(rd(self.zip, "rb"))  # zipfile cannot write encrypted entries: set the flag bit
        i = raw.find(b"PK\x03\x04")
        raw[i + 6] |= 0x1
        j = raw.find(b"PK\x01\x02")
        raw[j + 8] |= 0x1
        wr(bytes(raw), self.zip, "wb")
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertIn("암호", reason)

    def test_duplicate_basename_rejected(self):
        make_zip(self.zip, {**five(), "other/COT_OrigCo_COT360.html": b"y"})
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertIn("중복", reason)

    def test_missing_file_reason_lists_names(self):
        m = five()
        for k in [k for k in m if k.endswith(("COH_Template.xlsx", "COT360.html"))]:
            del m[k]
        make_zip(self.zip, m)
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertTrue(reason.startswith("COT 파일 5종 중 누락: "), reason)
        self.assertIn("COH_Template.xlsx", reason)
        self.assertIn("COT360.html", reason)

    def test_two_of_a_kind_rejected(self):
        make_zip(self.zip, {**five(), "COT_Other_COT360.html": b"y"})
        self.assertEqual(self.stage()[0], 3)

    def test_disallowed_extension(self):
        make_zip(self.zip, {**five(), "setup.exe": b"MZ"})
        rc, reason = self.stage()
        self.assertEqual((rc, reason), (3, "허용되지 않은 파일: setup.exe"))

    def test_not_a_zip(self):
        wr(b"not a zip at all", self.zip, "wb")
        self.assertEqual(self.stage(), (3, "zip 형식이 아닙니다"))

    def test_backslash_and_drive_letter_names(self):
        make_zip(self.zip, {"COT_X\\" + k: v for k, v in five().items()})  # Windows-made nested folder
        self.assertEqual(self.stage(), (0, None))
        for bad in ("..\\evil.html", "C:/x/evil.html", "C:evil.html", "\\evil.html"):
            os.remove(self.zip)
            make_zip(self.zip, {**five(), bad: b"x"})
            self.assertEqual(self.stage()[0], 3, bad)

    def test_case_insensitive_duplicate_basenames(self):
        make_zip(self.zip, {**five(), "b/cot_origco_cot360.HTML": b"y"})
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertIn("중복", reason)

    def test_korean_and_space_names(self):
        make_zip(self.zip, five("COT_한빛 금융 (본사)_"))
        self.assertEqual(self.stage(), (0, None))
        self.assertEqual(len(os.listdir(os.path.join(self.work, "extracted", "acct-a"))), 5)

    def test_long_name_rejected(self):
        make_zip(self.zip, {**five(), "가" * 90 + ".html": b"x"})  # 270 bytes in UTF-8
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertIn("너무 깁니다", reason)

    def test_unsupported_compression_rejected(self):
        make_zip(self.zip, five(), zipfile.ZIP_STORED)
        raw = bytearray(rd(self.zip, "rb"))
        i, j = raw.find(b"PK\x03\x04"), raw.find(b"PK\x01\x02")
        raw[i + 8:i + 10] = raw[j + 10:j + 12] = (99).to_bytes(2, "little")
        wr(bytes(raw), self.zip, "wb")
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertIn("압축 방식", reason)

    def test_corrupt_compressed_stream_rejected(self):
        # the central directory reads fine; the entry's compressed bytes do not decompress
        for method in (zipfile.ZIP_DEFLATED, zipfile.ZIP_BZIP2, zipfile.ZIP_LZMA):
            m = five()
            m["COT_OrigCo_Template.xlsx"] = bytes(range(256)) * 64
            make_zip(self.zip, m, method)
            raw = bytearray(rd(self.zip, "rb"))
            i = raw.find(b"COT_OrigCo_Template.xlsx") + len("COT_OrigCo_Template.xlsx")  # local header name end
            for k in range(i + 8, i + 40):
                raw[k] ^= 0xFF
            wr(bytes(raw), self.zip, "wb")
            self.assertTrue(zipfile.is_zipfile(self.zip))
            rc, reason = self.stage()
            self.assertEqual((rc, reason), (3, "압축을 풀 수 없는 항목입니다: COT_OrigCo_Template.xlsx"), method)
            self.assertFalse(os.path.exists(os.path.join(self.work, "upload_raw")))

    def test_echoed_name_capped_and_clean(self):
        make_zip(self.zip, {**five(), "evil\x07\x1b[31m" + "a" * 200 + ".exe": b"x"})
        rc, reason = self.stage()
        self.assertEqual(rc, 3)
        self.assertLessEqual(len(reason), len("허용되지 않은 파일: ") + 80)
        self.assertFalse(re.search(r"[\x00-\x1f\x7f]", reason), reason)

    def test_bad_account_refused(self):
        make_zip(self.zip, five())
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(stage_cot.main(self.zip, self.work, "../x"), 2)
        self.assertFalse(os.path.exists(os.path.join(self.d, "x")))


def xlsx(path, sheets):
    """Minimal xlsx: {sheet name: [[cell, ...], ...]} with inline strings."""
    wb = "".join(f'<sheet name="{n}" sheetId="{i + 1}" r:id="rId{i + 1}"/>' for i, n in enumerate(sheets))
    rels = "".join(f'<Relationship Id="rId{i + 1}" Target="worksheets/sheet{i + 1}.xml"/>' for i in range(len(sheets)))
    ns = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"'
    rns = 'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("xl/workbook.xml", f"<workbook {ns} {rns}><sheets>{wb}</sheets></workbook>")
        z.writestr("xl/_rels/workbook.xml.rels", f'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">{rels}</Relationships>')
        for i, rows in enumerate(sheets.values()):
            body = "".join("<row>" + "".join(f'<c r="{chr(65 + j)}{r + 1}" t="inlineStr"><is><t>{v}</t></is></c>' for j, v in enumerate(row) if v != "")
                           + "</row>" for r, row in enumerate(rows))
            z.writestr(f"xl/worksheets/sheet{i + 1}.xml", f"<worksheet {ns}><sheetData>{body}</sheetData></worksheet>")


HDR = ["", "Report Name", "keys", "timePeriod.start", "metrics.AmortizedCost.amount"]


class Relabel(Tmp):
    def test_report_name_becomes_account(self):
        import csv, xlsx2csv
        p = os.path.join(self.d, "t.xlsx")
        xlsx(p, {"Data Service Costs": [HDR, ["", "OrigCo", "['EC2']", "2026-03-01", "5"], ["", "", "side", "", ""]]})
        with contextlib.redirect_stdout(io.StringIO()) as out:
            xlsx2csv.main(p, self.d, "Data", "acct-a")
        self.assertEqual(out.getvalue(), "")  # stats go to stderr; stdout is for the reason only
        rows = list(csv.reader(rd(os.path.join(self.d, "Data_Service_Costs.csv")).splitlines()))
        self.assertEqual([r[1] for r in rows], ["Report Name", "acct-a", ""])

    def test_non_utf8_locale_keeps_korean_text(self):
        # the CSV is UTF-8 whatever the locale (Counted budgets UTF-8 bytes), and cotload reads it back as UTF-8
        p = os.path.join(self.d, "t.xlsx")
        xlsx(p, {"Data Service Costs": [HDR, ["", "OrigCo", "['한국어 서비스']", "2026-03-01", "5"]]})
        env = dict(os.environ, LC_ALL="C", LANG="C", PYTHONCOERCECLOCALE="0", PYTHONUTF8="0", COT_CSV_DIR=self.d)
        r = subprocess.run([sys.executable, "-I", "-X", "utf8=0", os.path.join(SCRIPTS, "xlsx2csv.py"), p,
                            os.path.join(self.d, "acct-a"), "Data", "acct-a"], capture_output=True, text=True, env=env)
        self.assertEqual((r.returncode, r.stdout), (0, ""), r.stderr[-500:])
        self.assertIn("한국어 서비스", rd(os.path.join(self.d, "acct-a", "Data_Service_Costs.csv"), encoding="utf-8"))
        code = ("import sys; sys.path.insert(0, %r); import cotload; "
                "print(cotload.load('acct-a', 'Data_Service_Costs')[0]['keys'].encode('utf-8').hex())" % SCRIPTS)
        r = subprocess.run([sys.executable, "-I", "-X", "utf8=0", "-c", code], capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 0, r.stderr[-500:])
        self.assertEqual(bytes.fromhex(r.stdout.strip()).decode("utf-8"), "['한국어 서비스']")

    def test_two_report_names_refused(self):
        import xlsx2csv
        p = os.path.join(self.d, "t.xlsx")
        xlsx(p, {"Data Service Costs": [HDR, ["", "OrigCo", "a", "2026-03-01", "1"], ["", "OtherCo", "b", "2026-03-01", "1"]]})
        with self.assertRaises(SystemExit), contextlib.redirect_stdout(io.StringIO()):
            xlsx2csv.main(p, self.d, "Data", "acct-a")

    def test_inner_zip_bomb_refused(self):
        import xlsx2csv
        p = os.path.join(self.d, "t.xlsx")
        xlsx(p, {"Data Service Costs": [HDR] + [["", "OrigCo", "a" * 50, "2026-03-01", "1"]] * 4000})
        with self.assertRaises(SystemExit) as cm, contextlib.redirect_stdout(io.StringIO()) as buf:
            xlsx2csv.main(p, self.d, "Data", "acct-a", max_ratio=10)
        self.assertEqual(cm.exception.code, 3)
        self.assertIn("압축률", json.loads(buf.getvalue())["reason"])


class SheetCaps(Tmp):
    def sparse(self, rows, col="XFD"):
        """A tiny workbook: one cell at a far-right column on every row."""
        p = os.path.join(self.d, "s.xlsx")
        ns = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"'
        rns = 'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
        body = "".join(f'<row><c r="{col}{r + 1}"><v>1</v></c></row>' for r in range(rows))
        with zipfile.ZipFile(p, "w", zipfile.ZIP_DEFLATED) as z:
            z.writestr("xl/workbook.xml", f'<workbook {ns} {rns}><sheets><sheet name="Data X" sheetId="1" r:id="rId1"/></sheets></workbook>')
            z.writestr("xl/_rels/workbook.xml.rels", '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
                       '<Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>')
            z.writestr("xl/worksheets/sheet1.xml", f"<worksheet {ns}><sheetData>{body}</sheetData></worksheet>")
        return p

    def convert(self, p, **kw):
        import time, xlsx2csv
        t = time.time()
        with self.assertRaises(SystemExit) as cm, contextlib.redirect_stdout(io.StringIO()) as buf:
            xlsx2csv.main(p, os.path.join(self.d, "csv"), "Data", "acct-a", **kw)
        return cm.exception.code, json.loads(buf.getvalue())["reason"], time.time() - t

    def test_sparse_far_column_amplification_refused(self):
        p = self.sparse(2000)  # ~40 KB zipped; 2000 x 16384 padded cells
        self.assertLess(os.path.getsize(p), 100_000)
        code, reason, secs = self.convert(p)
        self.assertEqual(code, 3)
        self.assertIn("너무 큽니다", reason)
        self.assertLess(secs, 10)
        out = os.path.join(self.d, "csv", "Data_X.csv")
        self.assertLess(os.path.getsize(out), 25 * 2 ** 20)  # streamed up to the cap, never held whole

    def test_row_cap(self):
        code, reason, _ = self.convert(self.sparse(50, col="A"), max_rows=10)
        self.assertEqual((code, "너무 큽니다" in reason), (3, True))

    def test_column_past_xfd_refused(self):
        code, reason, _ = self.convert(self.sparse(1, col="XFE"))
        self.assertEqual((code, reason), (3, "COT 엑셀 파일을 읽을 수 없습니다"))
        code, _, _ = self.convert(self.sparse(1, col="AAAA"))
        self.assertEqual(code, 3)


NS_M = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"'
NS_R = 'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'


def workbook(path, sheets, parts, shared=None):
    """sheets: [(name, target)]; parts: {target: sheetData inner xml}; shared: list of shared strings."""
    wb = "".join(f'<sheet name="{n}" sheetId="{i + 1}" r:id="rId{i + 1}"/>' for i, (n, _) in enumerate(sheets))
    rels = "".join(f'<Relationship Id="rId{i + 1}" Target="{t}"/>' for i, (_, t) in enumerate(sheets))
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("xl/workbook.xml", f"<workbook {NS_M} {NS_R}><sheets>{wb}</sheets></workbook>")
        z.writestr("xl/_rels/workbook.xml.rels", f'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">{rels}</Relationships>')
        if shared is not None:
            z.writestr("xl/sharedStrings.xml", f"<sst {NS_M}>" + "".join(f"<si><t>{x}</t></si>" for x in shared) + "</sst>")
        for t, body in parts.items():
            z.writestr("xl/" + t, f"<worksheet {NS_M}><sheetData>{body}</sheetData></worksheet>")
    return path


class WorkbookCaps(Tmp):
    """The archive ratio check would catch these highly compressible fixtures first; it is lifted (an attacker can pad
    with incompressible bytes) so each test reaches the cap it is about."""

    def convert(self, p, **kw):
        return SheetCaps.convert(self, p, max_ratio=10 ** 9, **kw)
    def test_shared_string_amplification_refused(self):
        rows = "".join(f'<row><c r="A{r + 1}" t="s"><v>0</v></c></row>' for r in range(200))  # 200 x 100 KB
        p = workbook(os.path.join(self.d, "w.xlsx"), [("Data X", "worksheets/s1.xml")], {"worksheets/s1.xml": rows}, ["x" * 100_000])
        self.assertLess(os.path.getsize(p), 50_000)
        code, reason, _ = self.convert(p, max_out=10 * 2 ** 20)
        self.assertEqual((code, "변환 결과가 너무 큽니다" in reason), (3, True))
        self.assertLessEqual(os.path.getsize(os.path.join(self.d, "csv", "Data_X.csv")), 10 * 2 ** 20 + 200_000)

    def test_shared_index_validated(self):
        for v in ("5", "-1", "x", ""):
            p = workbook(os.path.join(self.d, "w.xlsx"), [("Data X", "worksheets/s1.xml")],
                         {"worksheets/s1.xml": f'<row><c r="A1" t="s"><v>{v}</v></c></row>'}, ["a"])
            self.assertEqual(self.convert(p)[0], 3, v)

    def test_empty_rows_counted_and_not_retained(self):
        p = workbook(os.path.join(self.d, "w.xlsx"), [("Data X", "worksheets/s1.xml")], {"worksheets/s1.xml": "<row/>" * 50})
        self.assertEqual(self.convert(p, max_rows=10)[0], 3)
        n = 1_500_000
        p = workbook(os.path.join(self.d, "big.xlsx"), [("Data X", "worksheets/s1.xml")], {"worksheets/s1.xml": "<row/>" * n})
        # peak Python heap (tracemalloc) of the conversion alone, in a bounded child. Streaming stays near 1 MB; keeping
        # the 1.5M parsed rows costs ~130 MB. Unlike ru_maxrss this does not inherit the forking test runner's RSS.
        code = ("import sys, tracemalloc; sys.path.insert(0, sys.argv[1]); import xlsx2csv; tracemalloc.start(); "
                "xlsx2csv.main(sys.argv[2], sys.argv[3], 'Data', max_ratio=10 ** 9, max_rows=2_000_000); "
                "print(tracemalloc.get_traced_memory()[1])")
        r = subprocess.run([sys.executable, "-I", "-c", code, SCRIPTS, p, os.path.join(self.d, "csv")], capture_output=True,
                           text=True, timeout=300)
        self.assertEqual(r.returncode, 0, r.stderr[-300:])
        self.assertLess(int(r.stdout.split()[-1]), 20 * 2 ** 20)  # bytes: the parsed rows are not kept

    def test_cells_per_row_capped(self):
        p = workbook(os.path.join(self.d, "w.xlsx"), [("Data X", "worksheets/s1.xml")],
                     {"worksheets/s1.xml": "<row>" + '<c r="A1"/>' * 70_000 + "</row>"})
        code, reason, _ = self.convert(p)
        self.assertEqual((code, "행이 너무 큽니다" in reason), (3, True))

    def test_caps_are_workbook_wide(self):
        rows = "".join(f'<row><c r="A{r + 1}"><v>1</v></c></row>' for r in range(8))
        p = workbook(os.path.join(self.d, "w.xlsx"), [(f"Data {i}", f"worksheets/s{i}.xml") for i in range(3)],
                     {f"worksheets/s{i}.xml": rows for i in range(3)})
        self.assertEqual(self.convert(p, max_rows=20)[0], 3)  # 8 rows per sheet, 24 in the workbook

    def test_sheet_count_capped(self):
        p = workbook(os.path.join(self.d, "w.xlsx"), [(f"Data {i}", "worksheets/s1.xml") for i in range(201)], {"worksheets/s1.xml": ""})
        code, reason, _ = self.convert(p)
        self.assertEqual((code, "시트가 너무 많습니다" in reason), (3, True))

    def test_repeated_target_refused(self):
        p = workbook(os.path.join(self.d, "w.xlsx"), [("Data A", "worksheets/s1.xml"), ("Data B", "/xl/worksheets/./s1.xml")],
                     {"worksheets/s1.xml": '<row><c r="A1"><v>1</v></c></row>'})
        code, reason, _ = self.convert(p)
        self.assertEqual((code, "같은 데이터" in reason), (3, True))

    def test_colliding_csv_names_refused(self):
        p = workbook(os.path.join(self.d, "w.xlsx"), [("Data X", "worksheets/s1.xml"), ("Data-x", "worksheets/s2.xml")],
                     {"worksheets/s1.xml": "", "worksheets/s2.xml": ""})
        code, reason, _ = self.convert(p)
        self.assertEqual((code, "이름이 겹칩니다" in reason), (3, True))


class Round3(Tmp):
    """Small caps passed as keywords; nothing here allocates more than a few tens of MB."""

    def convert(self, p, **kw):
        return SheetCaps.convert(self, p, max_ratio=10 ** 9, **kw)

    def one_sheet(self, body, shared=None, name="Data X"):
        return workbook(os.path.join(self.d, "w.xlsx"), [(name, "worksheets/s1.xml")], {"worksheets/s1.xml": body}, shared)

    def test_row_bytes_bounded_before_the_row_is_built(self):
        import tracemalloc
        row = "<row>" + "".join(f'<c r="{a}{b}1" t="s"><v>0</v></c>' for a in "ABCDEFGHIJ" for b in "ABCDEFGHIJKLMNOPQRSTUVWXYZ") + "</row>"
        p = self.one_sheet(row, ["y" * 20_000])  # 260 cells x 20 KB = ~5 MB if it were written
        tracemalloc.start()
        code, reason, _ = self.convert(p, max_row_bytes=2 ** 20)
        peak = tracemalloc.get_traced_memory()[1]
        tracemalloc.stop()
        self.assertEqual((code, "변환 결과가 너무 큽니다" in reason), (3, True))
        self.assertEqual(os.path.getsize(os.path.join(self.d, "csv", "Data_X.csv")), 0)  # refused before writing
        self.assertLess(peak, 3 * 2 ** 20)
        code, _, _ = self.convert(p, max_out=2 ** 20)  # also against the bytes left in the workbook budget
        self.assertEqual(code, 3)

    def test_shared_string_caps(self):
        code, reason, _ = self.convert(self.one_sheet("", ["z" * 2000]), max_str=1000)
        self.assertEqual((code, "공유 문자열" in reason), (3, True))
        code, reason, _ = self.convert(self.one_sheet("", ["z" * 500] * 10), max_shared=2000)
        self.assertEqual((code, "공유 문자열" in reason), (3, True))

    def test_shared_strings_streamed_like_text_of(self):
        import xlsx2csv
        p = os.path.join(self.d, "w.xlsx")
        with zipfile.ZipFile(p, "w") as z:
            z.writestr("xl/sharedStrings.xml", f'<sst {NS_M}><si><t>a</t></si><si><r><t>b</t></r><r><t>c</t></r></si><si/></sst>')
        self.assertEqual(xlsx2csv.shared_strings(zipfile.ZipFile(p), xlsx2csv.Budget()), (["a", "bc", ""], [1, 2, 0]))
        with zipfile.ZipFile(p, "w") as z:
            z.writestr("xl/sharedStrings.xml", f"<sst {NS_M}><si><si><t>a</t></si></si></sst>")
        with self.assertRaises(xlsx2csv.Rejected):
            xlsx2csv.shared_strings(zipfile.ZipFile(p), xlsx2csv.Budget())

    def test_nested_row_and_depth_refused(self):
        self.assertEqual(self.convert(self.one_sheet('<row><row><c r="A1"><v>1</v></c></row></row>'))[0], 3)
        deep = '<row><c r="A1"><is>' + "<r>" * 20 + "</r>" * 20 + "</is></c></row>"
        code, reason, _ = self.convert(self.one_sheet(deep))
        self.assertEqual((code, "너무 깊습니다" in reason), (3, True))

    def test_nested_elements_under_a_row_counted(self):
        body = '<row><c r="A1" t="inlineStr"><is>' + "<r><t>x</t></r>" * 40_000 + "</is></c></row>"
        code, reason, _ = self.convert(self.one_sheet(body))
        self.assertEqual((code, "행이 너무 큽니다" in reason), (3, True))

    def test_long_sheet_name_refused(self):
        code, reason, _ = self.convert(self.one_sheet("", name="Data " + "x" * 300))
        self.assertEqual((code, "이름이 너무 깁니다" in reason), (3, True))

    def test_cli_rejects_extra_arguments(self):
        cli = os.path.join(SCRIPTS, "xlsx2csv.py")
        for extra in ([], ["a", "b", "c", "d", "e"]):
            r = subprocess.run([sys.executable, "-I", cli, *(extra or ["only-one"])], capture_output=True, text=True)
            self.assertEqual(r.returncode, 2, extra)
            self.assertNotIn("Traceback", r.stderr)

    def test_cli_memory_limit_ends_as_exit_3(self):
        # 16 x 1 MB of hex (incompressible enough for the ratio check); the subprocess allows ~8 MB past its start
        p = self.one_sheet("", [os.urandom(500_000).hex() for _ in range(16)])
        code = ("import resource, runpy, sys, csv, json, posixpath, re, zipfile, zlib, unicodedata, errno, stat; "
                "import xml.etree.ElementTree; "  # imported before the limit, so only the conversion is constrained
                "vm = int([l for l in open('/proc/self/status') if l.startswith('VmSize')][0].split()[1]) * 1024; "
                "lim = vm + 8 * 2 ** 20; resource.setrlimit(resource.RLIMIT_AS, (lim, lim)); "
                "cli, sys.argv = sys.argv[1], [sys.argv[1], sys.argv[2], sys.argv[3], 'Data']; "
                "runpy.run_path(cli, run_name='__main__')")
        cli = os.path.join(SCRIPTS, "xlsx2csv.py")
        r = subprocess.run([sys.executable, "-I", cli, p, os.path.join(self.d, "csv0"), "Data"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr[-400:])  # the same workbook converts under the normal limit
        r = subprocess.run([sys.executable, "-I", "-c", code, cli, p, os.path.join(self.d, "csv")], capture_output=True, text=True)
        self.assertEqual(r.returncode, 3, r.stderr[-400:])
        self.assertEqual(json.loads(r.stdout.strip().splitlines()[-1]), {"reason": "COT 엑셀 파일이 너무 큽니다"})


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

    def test_crash_is_exit_4(self):
        self.upload([HDR, ["", "OrigCo", "['EC2']", "2026-03-01", "5"]],
                    b'<script id="json-x" type="application/json">{not json</script>')
        p = run_account(self.zip, self.work, os.path.join(self.work, "out"), "acct-a")
        self.assertEqual(p.returncode, 4, p.stderr[-500:])
        self.assertEqual(self.steps(p)[-1], "STEP extract failed")

    def test_entry_count_includes_skipped(self):
        make_zip(self.zip, {**five(), **{f"__MACOSX/._{i}": b"" for i in range(10)}})
        with self.assertRaises(safezip.ZipRejected):
            safezip.extract(self.zip, os.path.join(self.d, "x"), {".xlsx", ".pptx", ".html"}, max_entries=10)

    def test_stdout_carries_only_steps_and_reasons(self):
        sh = rd(RUN_ACCOUNT, encoding="utf-8")
        for line in sh.splitlines():
            if re.search(r'python3 -I "\$S/', line) and not re.search(r"period\.py|stage_cot\.py|xlsx2csv\.py", line):
                self.assertRegex(line, r">&2|>\s*/dev/null", line)


if __name__ == "__main__":
    unittest.main()
