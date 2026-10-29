"""html2json.py: COT360 HTML tables are read linearly and bounded. Synthetic pages only."""
import json, os, subprocess, sys, tempfile, time, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "scripts", "html2json.py")
sys.path.insert(0, os.path.join(HERE, "..", "scripts"))
import html2json as H  # noqa: E402


def table(bid, obj, attrs='type="application/json"'):
    return '<script id="%s" %s>%s</script>' % (bid, attrs, json.dumps(obj))


class Html2Json(unittest.TestCase):
    def setUp(self):
        self.d = tempfile.mkdtemp(dir=os.environ.get("TMPDIR"))

    def run_on(self, html):
        p = os.path.join(self.d, "COT360.html")
        with open(p, "w", encoding="utf-8") as fh:
            fh.write(html)
        out = os.path.join(self.d, "out")
        r = subprocess.run([sys.executable, "-I", SCRIPT, p, out], capture_output=True, text=True)
        return r, out

    def test_extracts_tables_in_any_attribute_order(self):
        page = ("<html><script>var x=1;</script>" + table("json-kpis", {"headers": ["a"], "rows": [[1]]})
                + '<script type="application/json" id="json-svc">{"rows": []}</script></html>')
        r, out = self.run_on(page)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, "")  # stats go to stderr; stdout carries only a reason
        self.assertEqual(sorted(os.listdir(out)), ["json_kpis.json", "json_svc.json"])
        with open(os.path.join(out, "json_kpis.json")) as fh:
            self.assertEqual(json.load(fh), {"headers": ["a"], "rows": [[1]]})

    def test_many_unclosed_openers_are_linear(self):
        page = '<script id="json-x" type="application/json">' * 200000 + "x"
        t = time.monotonic()
        self.assertEqual(list(H.tables(page)), [])
        self.assertLess(time.monotonic() - t, 1.0)

    def test_invalid_pages_exit_3_with_a_reason_and_write_nothing(self):
        for page in (table("json-a", {"rows": []}) + table("json-b", ["not", "an", "object"]),
                     '<script id="json-a" type="application/json">{not json</script>',
                     '<script id="json-a" type="application/json">' + "[" * 200000 + "]" * 200000 + "</script>",
                     "<script " + "a" * 5000 + ">x</script>"):
            r, out = self.run_on(page)
            self.assertEqual(r.returncode, 3, page[:80])
            self.assertEqual(json.loads(r.stdout)["reason"], H.UNREADABLE)
            self.assertFalse(os.path.exists(out))

    def test_caps(self):
        old = (H.MAX_HTML, H.MAX_TABLE, H.MAX_TABLES)
        self.addCleanup(lambda: setattr(H, "MAX_HTML", old[0]) or setattr(H, "MAX_TABLE", old[1])
                        or setattr(H, "MAX_TABLES", old[2]))
        H.MAX_TABLES = 2
        with self.assertRaises(H.Rejected):
            list(H.tables("".join(table("json-%d" % i, {}) for i in range(3))))
        H.MAX_TABLE = 10
        with self.assertRaises(H.Rejected):
            list(H.tables(table("json-a", {"rows": list(range(20))})))
        p = os.path.join(self.d, "big.html")
        with open(p, "w") as fh:
            fh.write("x" * 100)
        H.MAX_HTML = 50
        with self.assertRaises(H.Rejected):
            H.read(p)

    def test_non_utf8_is_unreadable(self):
        p = os.path.join(self.d, "COT360.html")
        with open(p, "wb") as fh:
            fh.write(b"\xff\xfe<script>")
        with self.assertRaises(H.Rejected):
            H.read(p)


if __name__ == "__main__":
    unittest.main()
