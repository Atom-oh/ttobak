"""Shared CUR test fixtures (moved from test_cur.py): synthetic CUR rows and files, normalized rows, zips, Tmp."""
import contextlib, io, json, os, sys, tempfile, unittest, zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))  # services/billing/worker
sys.path.insert(0, os.path.join(ROOT, "scripts"))
sys.path.insert(0, os.path.join(ROOT, "cur"))
import duckdb  # noqa: E402
import safezip  # noqa: E402
import normalize  # noqa: E402
import stage_cur  # noqa: E402

normalize.MEMORY_LIMIT = "200MB"  # safe on a shared box
normalize.THREADS = 1

CUR2 = {
    "line_item_line_item_type": "VARCHAR", "line_item_unblended_cost": "DOUBLE",
    "line_item_usage_start_date": "TIMESTAMP", "line_item_currency_code": "VARCHAR",
    "bill_billing_period_start_date": "TIMESTAMP", "line_item_product_code": "VARCHAR", "product_servicecode": "VARCHAR", "line_item_usage_type": "VARCHAR",
    "product_region_code": "VARCHAR", "line_item_resource_id": "VARCHAR", "bill_payer_account_id": "VARCHAR",
    "line_item_usage_account_id": "VARCHAR", "reservation_effective_cost": "DOUBLE",
    "savings_plan_savings_plan_effective_cost": "DOUBLE",
    "reservation_unused_amortized_upfront_fee_for_billing_period": "DOUBLE",
    "reservation_unused_recurring_fee": "DOUBLE", "savings_plan_total_commitment_to_date": "DOUBLE",
    "savings_plan_used_commitment": "DOUBLE", "resource_tags": "MAP(VARCHAR,VARCHAR)",
}


def row(**kw):
    base = {"line_item_line_item_type": "Usage", "line_item_unblended_cost": 1.0,
            "line_item_usage_start_date": "2026-08-03 00:00:00", "line_item_currency_code": "USD",
            "line_item_product_code": "AmazonEC2", "product_servicecode": "AmazonEC2",
            "line_item_usage_type": "BoxUsage:m5.large", "product_region_code": "ap-northeast-2",
            "line_item_resource_id": "i-0abc", "bill_payer_account_id": "111111111111",
            "line_item_usage_account_id": "222222222222"}
    base.update(kw)
    day = base["line_item_usage_start_date"]
    if "bill_billing_period_start_date" not in kw and day is not None:  # the usage month's bill unless a test says
        base["bill_billing_period_start_date"] = str(day)[:7] + "-01 00:00:00"
    return base


def write_file(path, rows, cols, kind="parquet"):
    """rows: list of dicts; cols: {name: sql type}. Missing keys become NULL."""
    c = duckdb.connect()
    c.execute("SET threads=1")
    c.execute("SET memory_limit='200MB'")
    c.execute("CREATE TABLE t(%s)" % ", ".join('"%s" %s' % (n, t) for n, t in cols.items()))
    for r in rows:
        names = [n for n in cols if n in r]
        c.execute("INSERT INTO t(%s) VALUES (%s)" % (", ".join('"%s"' % n for n in names), ", ".join("?" * len(names))),
                  [r[n] for n in names])
    if kind == "parquet":
        c.execute("COPY t TO '%s' (FORMAT parquet)" % path)
    else:
        c.execute("COPY t TO '%s' (FORMAT csv, HEADER, COMPRESSION gzip)" % path)
    c.close()
    return path


def make_zip(path, members):
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        for name, p in members.items():
            if isinstance(p, bytes):
                z.writestr(name, p)
            else:
                z.write(p, name)
    return path


def read_out(out_dir):
    c = duckdb.connect()
    rows = c.execute("SELECT * FROM read_parquet('%s')" % os.path.join(out_dir, "part-0.parquet")).fetchall()
    cols = [d[0] for d in c.description]
    c.close()
    return [dict(zip(cols, r)) for r in rows]


class Tmp(unittest.TestCase):
    def setUp(self):
        self._d = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.d = self._d.name
        self.out = os.path.join(self.d, "work", "cur_norm")

    def tearDown(self):
        self._d.cleanup()

    def f(self, name):
        return os.path.join(self.d, name)

    def stage(self, zip_path, prev_dir=None):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = stage_cur.main(zip_path, os.path.join(self.d, "work"), "acct-a", prev_dir)
        return rc, buf.getvalue()


IDENT = {"identity_line_item_id": "VARCHAR", "identity_time_interval": "VARCHAR"}


def ident(i, iv="2026-08-03T00:00:00Z/2026-08-03T01:00:00Z"):
    return {"identity_line_item_id": i, "identity_time_interval": iv}


NORM_COLS = {"usage_date": "DATE", "month": "VARCHAR", "payer_account": "VARCHAR", "linked_account": "VARCHAR",
             "service": "VARCHAR", "usage_type": "VARCHAR", "region": "VARCHAR", "resource_id": "VARCHAR",
             "line_item_type": "VARCHAR", "cost": "DOUBLE", "unblended": "DOUBLE", "currency": "VARCHAR",
             "tags": "MAP(VARCHAR,VARCHAR)"}


def nrow(day="2026-08-03", **kw):
    base = {"usage_date": day, "month": day[:7], "payer_account": "111111111111", "linked_account": "222222222222",
            "service": "AmazonEC2", "usage_type": "BoxUsage:m5.large", "region": "ap-northeast-2",
            "resource_id": "i-0abc", "line_item_type": "Usage", "cost": 1.0, "unblended": 1.0, "currency": "USD",
            "tags": {}}
    base.update(kw)
    if "unblended" not in kw:
        base["unblended"] = base["cost"]
    return base


def write_norm(norm_dir, rows):
    """Writes norm_dir/part-0.parquet from nrow() dicts (tags as dict)."""
    os.makedirs(norm_dir, exist_ok=True)
    c = duckdb.connect()
    c.execute("SET threads=1")
    c.execute("SET memory_limit='200MB'")
    c.execute("CREATE TABLE t(%s)" % ", ".join("%s %s" % kv for kv in NORM_COLS.items()))
    for r in rows:
        tags = r["tags"]
        t = ("MAP {%s}" % ", ".join("'%s': '%s'" % (k, v) for k, v in tags.items())) if tags else \
            "CAST(MAP {} AS MAP(VARCHAR,VARCHAR))"
        vals = [r[k] for k in NORM_COLS if k != "tags"]
        c.execute("INSERT INTO t SELECT %s, %s" % (", ".join("?" * len(vals)), t), vals)
    c.execute("COPY t TO '%s' (FORMAT parquet)" % os.path.join(norm_dir, "part-0.parquet"))
    c.close()
    return norm_dir


LEGACY = {"lineItem/LineItemType": "VARCHAR", "lineItem/UnblendedCost": "VARCHAR",
          "lineItem/UsageStartDate": "VARCHAR", "lineItem/CurrencyCode": "VARCHAR",
          "lineItem/ProductCode": "VARCHAR", "lineItem/ResourceId": "VARCHAR", "bill/BillingPeriodStartDate": "VARCHAR"}


def legacy_row(**kw):
    base = {"lineItem/LineItemType": "Usage", "lineItem/UnblendedCost": "2.5",
            "lineItem/UsageStartDate": "2026-07-05T01:00:00Z", "lineItem/CurrencyCode": "USD",
            "lineItem/ProductCode": "AmazonEC2", "lineItem/ResourceId": "i-legacy",
            "bill/BillingPeriodStartDate": "2026-07-01T00:00:00Z"}
    base.update(kw)
    return base
