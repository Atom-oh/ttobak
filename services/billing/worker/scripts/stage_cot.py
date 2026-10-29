"""Stage one uploaded COT zip (untrusted) for one account under canonical file names.

Usage: python3 -I stage_cot.py <zip> <work_dir> <acct>
Writes <work>/extracted/<acct>/COT_<acct>_{Template.xlsx, Current_State_Template.xlsx, COH_Template.xlsx,
PPT_Companion.pptx, COT360.html}. Files are recognised by suffix (case-insensitive), whatever the customer
name in front; '_Template.xlsx' is the workbook left after the more specific suffixes.
Exit 0 when staged; 3 with {"reason": ...} on stdout when the upload is invalid; 2 for a bad account id.
"""
import json, os, re, shutil, sys, zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import safezip  # noqa: E402

ACCT = re.compile(r"[A-Za-z0-9_-]{1,64}")
ALLOWED = {".xlsx", ".pptx", ".html"}
# most specific first: '_Template.xlsx' also ends the COH and Current State workbooks
KINDS = ("COT360.html", "COH_Template.xlsx", "Current_State_Template.xlsx", "PPT_Companion.pptx", "Template.xlsx")


def classify(names):
    """{kind: basename} or raise ZipRejected naming the missing or repeated kinds."""
    found, missing = {}, []
    for kind in KINDS:
        hits = [n for n in names if n not in found.values() and n.lower().endswith("_" + kind.lower())]
        if len(hits) > 1:
            raise safezip.ZipRejected(f"같은 종류의 COT 파일이 여러 개입니다: {kind}")
        if hits:
            found[kind] = hits[0]
        else:
            missing.append(kind)
    if missing:
        order = ["Template.xlsx", "Current_State_Template.xlsx", "COH_Template.xlsx", "PPT_Companion.pptx", "COT360.html"]
        raise safezip.ZipRejected("COT 파일 5종 중 누락: " + ", ".join(k for k in order if k in missing))
    extra = [n for n in names if n not in found.values()]
    if extra:
        raise safezip.ZipRejected(f"허용되지 않은 파일: {safezip.show(extra[0])}")
    return found


def reject(reason):
    print(json.dumps({"reason": reason}, ensure_ascii=False))
    return 3


def main(zip_path, work, acct):
    if not ACCT.fullmatch(acct or ""):
        print("invalid account id", file=sys.stderr)
        return 2
    raw = os.path.join(work, "upload_raw")
    dest = os.path.join(work, "extracted", acct)
    try:
        if not zipfile.is_zipfile(zip_path):
            return reject("zip 형식이 아닙니다")
        try:
            names = safezip.extract(zip_path, raw, ALLOWED)
            found = classify(names)
        except safezip.ZipRejected as e:
            return reject(e.reason)
        except (zipfile.BadZipFile, zipfile.LargeZipFile, NotImplementedError, EOFError):
            return reject("zip 형식이 아닙니다")
        os.makedirs(dest)
        for kind, name in found.items():
            os.replace(os.path.join(raw, name), os.path.join(dest, f"COT_{acct}_{kind}"))
        return 0
    finally:
        shutil.rmtree(raw, ignore_errors=True)


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit("usage: stage_cot.py <zip> <work_dir> <acct>")
    sys.exit(main(*sys.argv[1:]))
