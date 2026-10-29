"""Shared COT upload test fixtures (moved from test_stage_cot.py): zips, workbooks, Tmp."""
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
