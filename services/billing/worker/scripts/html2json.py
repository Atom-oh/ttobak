"""Extract embedded <script id="json-*" type="application/json"> tables from a COT360 HTML.
Usage: python -I html2json.py <html> <outdir>

The HTML is untrusted: it is size-capped, scanned linearly (every <script> element is visited once and nothing is
rescanned), and each embedded table is size- and count-capped and must be a JSON object. Nothing is written until
every table has been read. An invalid file exits 3 with {"reason": ...} on stdout; per-table stats go to stderr.
"""
import json, os, re, sys

MAX_HTML = 64 * 2 ** 20    # bytes of the whole page
MAX_TABLE = 32 * 2 ** 20   # bytes of one embedded table
MAX_TABLES = 200
MAX_TAG = 1024             # bytes of one <script ...> opening tag
ATTR_ID = re.compile(r'\bid="(json-[^"]{1,200})"')
ATTR_TYPE = re.compile(r'\btype="application/json"')
UNREADABLE = "COT360 HTML을 읽을 수 없습니다"
TOO_LARGE = "COT360 HTML이 너무 큽니다"


class Rejected(Exception):
    pass


def reject(reason):
    print(json.dumps({"reason": reason}, ensure_ascii=False))
    sys.exit(3)


def tables(h):
    """(id, body) of each JSON table, in page order; one pass over the page."""
    i, n = 0, 0
    while True:
        s = h.find("<script", i)
        if s < 0:
            return
        e = h.find(">", s, s + MAX_TAG)
        if e < 0:
            raise Rejected(UNREADABLE)
        c = h.find("</script>", e + 1)
        if c < 0:
            return  # an unclosed <script> has no body, and nothing after it can be closed either
        i = c + len("</script>")
        tag = h[s:e + 1]
        m = ATTR_ID.search(tag)
        if not (m and ATTR_TYPE.search(tag)):
            continue
        if c - (e + 1) > MAX_TABLE:
            raise Rejected(TOO_LARGE)
        n += 1
        if n > MAX_TABLES:
            raise Rejected(TOO_LARGE)
        yield m.group(1), h[e + 1:c]


def read(path):
    if os.path.getsize(path) > MAX_HTML:
        raise Rejected(TOO_LARGE)
    with open(path, "rb") as fh:
        h = fh.read(MAX_HTML + 1)
    if len(h) > MAX_HTML:
        raise Rejected(TOO_LARGE)
    try:
        h = h.decode("utf-8")
    except UnicodeDecodeError:
        raise Rejected(UNREADABLE)
    out = []
    for bid, body in tables(h):
        try:
            j = json.loads(body)
        except (ValueError, RecursionError):
            raise Rejected(UNREADABLE)
        if not isinstance(j, dict):
            raise Rejected(UNREADABLE)
        out.append((bid, j))
    return out


def main(path, outdir):
    try:
        found = read(path)
    except Rejected as e:
        reject(str(e))
    except MemoryError:
        reject(TOO_LARGE)
    os.makedirs(outdir, exist_ok=True)
    for bid, j in found:
        fn = re.sub(r"[^A-Za-z0-9]+", "_", bid).strip("_") + ".json"
        with open(os.path.join(outdir, fn), "w") as fh:
            json.dump(j, fh)
        rows = j.get("rows", [])
        print(f"{bid}\t{len(rows) if isinstance(rows, list) else 0} rows\t{j.get('headers')}", file=sys.stderr)


if __name__ == "__main__":
    main(*sys.argv[1:])
