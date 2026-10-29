"""Stdlib-only xlsx -> CSV dumper. Usage: python -I xlsx2csv.py <xlsx> <outdir> [sheet-prefix] [report-name]

report-name: the account id written into the 'Report Name' column of every raw-data row, so loaders that
select rows by entity find an uploaded workbook's rows under the account id (a sheet holding more than one
report name is refused). The workbook is untrusted: its archive is size/ratio-checked before it is read, and
an unreadable or oversized workbook exits 3 with {"reason": ...} on stdout. Rows are streamed to the CSV as
they are parsed and detached from the tree; the workbook is capped in sheets, rows (empty ones included), cells
(counting a row's padding, so sparse far-right cells cannot amplify), cells per row and CSV bytes written
(so a shared string repeated in many cells cannot amplify either); a row's CSV size is bounded from cached value
lengths before the row is built, and sharedStrings.xml is streamed with its own caps. The CLI also lowers the
address-space limit (RLIMIT_AS, ~3 GiB) so any remaining amplification ends as exit 3, not a host-wide OOM.
Per-sheet stats go to stderr; stdout carries only the reason line.
"""
import csv, json, os, posixpath, re, sys, zipfile, zlib
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import safezip  # noqa: E402

NS = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main",
      "r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships"}
MAX_COL = 16383  # XFD, the last Excel column
# workbook-wide caps; real COT Data sheets total ~54K rows, ~2.2M cells, ~5MB of CSV, 67 sheets
MAX_ROWS = 1_000_000  # every <row>, empty ones included
MAX_CELLS = 20_000_000  # counting the blanks each row is padded with
MAX_UNZIPPED = 256 * 2 ** 20  # the workbook archive; real: ~120MB
MAX_OUT = MAX_UNZIPPED  # CSV bytes written
MAX_SHEETS = 200
MAX_ROW_ELEMS = 4 * (MAX_COL + 1)  # elements inside one <row> (cells and their children, nested ones too)
MAX_ROW_BYTES = 16 * 2 ** 20  # upper bound of one row's CSV line, checked before the row is built
MAX_DEPTH = 16  # element nesting in a worksheet or sharedStrings part
MAX_STR = 2 ** 20  # one shared string, UTF-8 bytes
MAX_SHARED = 64 * 2 ** 20  # all shared strings, UTF-8 bytes
MAX_SHARED_N = 5_000_000  # shared string count
MAX_SI_ELEMS = 10_000  # elements inside one <si>
MAX_AS = 3 * 2 ** 30  # CLI address-space limit
M = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
ROW, SI, T = M + "row", M + "si", M + "t"
RID = "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}id"


def col_idx(ref):
    letters = re.match(r"[A-Z]{1,3}(?=[0-9])", ref).group(0)  # XFD is 3 letters
    n = 0
    for ch in letters:
        n = n * 26 + ord(ch) - 64
    return n - 1


def text_of(el):
    return "".join(t.text or "" for t in el.iter("{%s}t" % NS["m"]))


class Rejected(Exception):
    pass


def reject(reason):
    print(json.dumps({"reason": reason}, ensure_ascii=False))
    sys.exit(3)


class Relabel:
    """Sets the 'Report Name' cell of the raw-data rows to name, row by row (rows without one stay blank).
    The header is looked for in the first 10 rows; a second distinct report name below it is refused."""

    def __init__(self, name):
        self.name, self.n, self.header, self.seen = name, 0, False, None

    def __call__(self, r):
        if not self.header:
            if self.n < 10 and len(r) > 2 and r[1] == "Report Name":
                self.header = True
            self.n += 1
            return r
        if len(r) > 1 and r[1].strip():
            v = r[1].strip()
            if self.seen is None:
                self.seen = v
            elif v != self.seen:
                raise Rejected("COT 데이터에 보고서 이름이 여러 개입니다")
            r[1] = self.name
        return r


def ulen(s):
    return len(s.encode("utf-8", "surrogatepass"))


def main(path, outdir, prefix="", report_name="", max_ratio=200, **caps):
    """caps: max_rows, max_cells, max_out, max_sheets, max_row_bytes, max_str, max_shared (tests pass small ones)."""
    os.makedirs(outdir, exist_ok=True)
    try:
        z = zipfile.ZipFile(path)
        safezip.check(z.infolist(), max_entries=5000, max_total=MAX_UNZIPPED, max_ratio=max_ratio)
    except safezip.ZipRejected as e:
        reject(e.reason)
    except (zipfile.BadZipFile, OSError):
        reject("COT 엑셀 파일을 읽을 수 없습니다")
    try:
        convert(z, outdir, prefix, report_name, Budget(**caps))
    except Rejected as e:
        reject(str(e))
    except MemoryError:
        reject("COT 엑셀 파일이 너무 큽니다")
    except (KeyError, IndexError, TypeError, AttributeError, ValueError, NotImplementedError, ET.ParseError,
            zipfile.BadZipFile, zlib.error) as e:
        print(f"xlsx2csv: {type(e).__name__}", file=sys.stderr)  # the type only: messages can quote cell data
        reject("COT 엑셀 파일을 읽을 수 없습니다")


class Budget:
    """Workbook-wide totals against the caps; any overrun is an invalid upload."""

    def __init__(self, max_rows=MAX_ROWS, max_cells=MAX_CELLS, max_out=MAX_OUT, max_sheets=MAX_SHEETS,
                 max_row_bytes=MAX_ROW_BYTES, max_str=MAX_STR, max_shared=MAX_SHARED):
        self.max_rows, self.max_cells, self.max_out, self.max_sheets = int(max_rows), int(max_cells), int(max_out), int(max_sheets)
        self.max_row_bytes, self.max_str, self.max_shared = int(max_row_bytes), int(max_str), int(max_shared)
        self.rows = self.cells = self.out = 0

    def row_bytes(self, n):
        """n: an upper bound of the next row's CSV bytes; refused past the row cap or the bytes left."""
        if n > self.max_row_bytes or self.out + n > self.max_out:
            raise Rejected("COT 엑셀 변환 결과가 너무 큽니다")

    def row(self, cells):
        self.rows += 1
        self.cells += cells
        if self.rows > self.max_rows or self.cells > self.max_cells:
            raise Rejected("COT 엑셀 시트가 너무 큽니다")

    def wrote(self, n):
        self.out += n
        if self.out > self.max_out:
            raise Rejected("COT 엑셀 변환 결과가 너무 큽니다")


class Counted:
    """A text file that reports the UTF-8 bytes written to the budget."""

    def __init__(self, f, budget):
        self.f, self.budget = f, budget

    def write(self, s):
        self.budget.wrote(len(s.encode("utf-8", "surrogatepass")))
        return self.f.write(s)


def cell_value(c, shared):
    """(value, UTF-8 bytes); a shared string's length comes from the cache, not a fresh encode."""
    typ = c.get("t")
    v = c.find("m:v", NS)
    if typ == "s" and v is not None:
        i = int((v.text or "").strip())
        if not 0 <= i < len(shared[0]):
            raise Rejected("COT 엑셀 파일을 읽을 수 없습니다")
        return shared[0][i], shared[1][i]
    if typ == "inlineStr":
        is_el = c.find("m:is", NS)
        val = text_of(is_el) if is_el is not None else (v.text if v is not None else "")
    else:
        val = v.text if v is not None else ""
    val = val or ""
    return val, ulen(val)


def shared_strings(z, budget):
    """([strings], [UTF-8 lengths]) from xl/sharedStrings.xml, streamed: each <si> is the join of its <t> texts
    (as text_of), capped per string, in total and in count; finished elements are detached."""
    strs, lens, total = [], [], 0
    if "xl/sharedStrings.xml" not in z.namelist():
        return strs, lens
    stack, cur, cur_len, elems = [], None, 0, 0
    for ev, el in ET.iterparse(z.open("xl/sharedStrings.xml"), events=("start", "end")):
        if ev == "start":
            stack.append(el)
            if len(stack) > MAX_DEPTH:
                raise Rejected("COT 엑셀 파일 구조가 너무 깊습니다")
            if el.tag == SI:
                if cur is not None:
                    raise Rejected("COT 엑셀 파일을 읽을 수 없습니다")
                cur, cur_len, elems = [], 0, 0
            elif cur is not None:
                elems += 1
                if elems > MAX_SI_ELEMS:
                    raise Rejected("COT 엑셀 공유 문자열이 너무 큽니다")
            continue
        stack.pop()
        if cur is not None and el.tag == T and el.text:
            cur.append(el.text)
            cur_len += ulen(el.text)
            if cur_len > budget.max_str:
                raise Rejected("COT 엑셀 공유 문자열이 너무 큽니다")
        if el.tag == SI:
            strs.append("".join(cur))
            lens.append(cur_len)
            total += cur_len
            cur = None
            if total > budget.max_shared or len(strs) > MAX_SHARED_N:
                raise Rejected("COT 엑셀 공유 문자열이 너무 큽니다")
        if stack and cur is None:  # inside an <si> the parts stay until the <si> ends
            stack[-1].remove(el)
    return strs, lens


def rows_of(z, member, shared, budget):
    """Rows of one worksheet as lists, streamed; padded to the row's last cell. Every finished element outside a
    row, and every finished row, is detached from its parent so the tree never grows. A row's CSV size is
    bounded (2*bytes+3 per value for quoting, 1 per padding cell) before the row is built."""
    stack, in_row, row_elems = [], False, 0
    for ev, el in ET.iterparse(z.open(member), events=("start", "end")):
        if ev == "start":
            stack.append(el)
            if len(stack) > MAX_DEPTH:
                raise Rejected("COT 엑셀 파일 구조가 너무 깊습니다")
            if el.tag == ROW:
                if in_row:
                    raise Rejected("COT 엑셀 파일을 읽을 수 없습니다")  # rows do not nest
                in_row, row_elems = True, 0
            elif in_row:
                row_elems += 1
                if row_elems > MAX_ROW_ELEMS:
                    raise Rejected("COT 엑셀 행이 너무 큽니다")
            continue
        stack.pop()
        if el.tag == ROW:
            vals, est = {}, 2
            for c in el.findall("m:c", NS):
                ci = col_idx(c.get("r"))
                if ci > MAX_COL:
                    raise ValueError("column out of range")
                vals[ci] = cell_value(c, shared)
            in_row = False
            width = max(vals) + 1 if vals else 0
            budget.row(width)
            if stack:
                stack[-1].remove(el)
            if vals:
                budget.row_bytes(2 + sum(2 * n + 3 for _, n in vals.values()) + (width - len(vals)))
                yield [vals[i][0] if i in vals else "" for i in range(width)]
        elif not in_row and stack:
            stack[-1].remove(el)


def convert(z, outdir, prefix, report_name, budget=None):
    budget = budget or Budget()
    shared = shared_strings(z, budget)
    wb = ET.fromstring(z.read("xl/workbook.xml"))
    rels = ET.fromstring(z.read("xl/_rels/workbook.xml.rels"))
    target = {r.get("Id"): r.get("Target") for r in rels}
    sheets = list(wb.find("m:sheets", NS))
    if len(sheets) > budget.max_sheets:
        raise Rejected("COT 엑셀 시트가 너무 많습니다")
    parts, files = set(), set()
    for sh in sheets:
        name = sh.get("name")
        if prefix and not name.startswith(prefix):
            continue
        t = target[sh.get(RID)].lstrip("/")
        t = posixpath.normpath(t if t.startswith("xl/") else "xl/" + t)
        if not t.startswith("xl/"):
            raise Rejected("COT 엑셀 파일을 읽을 수 없습니다")
        if t in parts:
            raise Rejected("COT 엑셀 시트가 같은 데이터를 가리킵니다")
        parts.add(t)
        base = re.sub(r"[^A-Za-z0-9]+", "_", name).strip("_") + ".csv"
        if len(base) > 255:  # ASCII after sanitizing: one byte per character
            raise Rejected("COT 엑셀 시트 이름이 너무 깁니다")
        if base.lower() in files:
            raise Rejected("COT 엑셀 시트 이름이 겹칩니다")
        files.add(base.lower())
        fix = Relabel(report_name) if report_name else (lambda r: r)
        n = width = 0
        with open(os.path.join(outdir, base), "w", newline="", encoding="utf-8") as f:  # as Counted measures
            w = csv.writer(Counted(f, budget))
            for row in rows_of(z, t, shared, budget):
                w.writerow(fix(row))
                n, width = n + 1, max(width, len(row))
        print(f"{name}\t{n} rows\t{width} cols", file=sys.stderr)


def limit_memory(n=MAX_AS):
    """Lower the address-space limit to n (never above the current hard limit)."""
    import resource
    soft, hard = resource.getrlimit(resource.RLIMIT_AS)
    cap = n if hard == resource.RLIM_INFINITY else min(n, hard)
    if soft == resource.RLIM_INFINITY or soft > cap:
        resource.setrlimit(resource.RLIMIT_AS, (cap, hard))


if __name__ == "__main__":
    if not 2 <= len(sys.argv) - 1 <= 4:
        print("usage: xlsx2csv.py <xlsx> <outdir> [sheet-prefix] [report-name]", file=sys.stderr)
        sys.exit(2)
    limit_memory()
    try:
        main(*sys.argv[1:])
    except MemoryError:
        reject("COT 엑셀 파일이 너무 큽니다")
