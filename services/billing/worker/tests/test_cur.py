"""CUR detection and normalization to amortized Parquet. Synthetic fixtures only (duckdb COPY, zipped with zipfile)."""
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
    "line_item_product_code": "VARCHAR", "product_servicecode": "VARCHAR", "line_item_usage_type": "VARCHAR",
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


class Normalize(Tmp):
    def test_amortized_per_line_type(self):
        rows = [
            row(line_item_line_item_type="Usage", line_item_unblended_cost=10.0, line_item_resource_id="r-usage"),
            row(line_item_line_item_type="DiscountedUsage", line_item_unblended_cost=0.0,
                reservation_effective_cost=4.5, line_item_resource_id="r-disc"),
            row(line_item_line_item_type="SavingsPlanCoveredUsage", line_item_unblended_cost=9.0,
                savings_plan_savings_plan_effective_cost=3.0, line_item_resource_id="r-spc"),
            row(line_item_line_item_type="RIFee", line_item_unblended_cost=7.0,
                reservation_unused_amortized_upfront_fee_for_billing_period=2.0,
                reservation_unused_recurring_fee=1.0, line_item_resource_id="r-rifee"),
            row(line_item_line_item_type="SavingsPlanRecurringFee", line_item_unblended_cost=20.0,
                savings_plan_total_commitment_to_date=20.0, savings_plan_used_commitment=15.0,
                line_item_resource_id="r-sprf"),
        ]
        for t in ("SavingsPlanNegation", "SavingsPlanUpfrontFee", "Fee", "Tax", "Credit", "Refund", "EdpDiscount",
                  "PrivateRateDiscount", "BundledDiscount"):
            rows.append(row(line_item_line_item_type=t, line_item_unblended_cost=99.0, line_item_resource_id="x-" + t))
        rows.append(row(line_item_product_code="AWSSupportBusiness", line_item_resource_id="x-support"))
        rows.append(row(line_item_product_code="OCBPremiumSupport", line_item_resource_id="x-ocb"))
        p = write_file(self.f("a.parquet"), rows, CUR2)
        s = normalize.run([p], self.out)
        got = {r["resource_id"]: r for r in read_out(self.out)}
        self.assertEqual(set(got), {"r-usage", "r-disc", "r-spc", "r-rifee", "r-sprf"})
        self.assertAlmostEqual(got["r-usage"]["cost"], 10.0)
        self.assertAlmostEqual(got["r-disc"]["cost"], 4.5)
        self.assertAlmostEqual(got["r-spc"]["cost"], 3.0)
        self.assertAlmostEqual(got["r-rifee"]["cost"], 3.0)
        self.assertAlmostEqual(got["r-sprf"]["cost"], 5.0)
        self.assertAlmostEqual(got["r-spc"]["unblended"], 9.0)
        self.assertEqual(s["rows"], 5)
        self.assertEqual(s["format"], "cur2-parquet")
        self.assertEqual(s["currency"], "USD")
        self.assertEqual((s["first_day"], s["last_day"], s["months"]), ("2026-08-03", "2026-08-03", ["2026-08"]))
        r = got["r-usage"]
        self.assertEqual((r["month"], r["service"], r["region"], r["usage_type"], r["payer_account"],
                          r["linked_account"], r["line_item_type"], r["currency"]),
                         ("2026-08", "AmazonEC2", "ap-northeast-2", "BoxUsage:m5.large", "111111111111",
                          "222222222222", "Usage", "USD"))
        self.assertEqual(str(r["usage_date"]), "2026-08-03")

    def test_service_falls_back_to_product_code_and_optional_columns(self):
        cols = {k: v for k, v in CUR2.items() if k in (
            "line_item_line_item_type", "line_item_unblended_cost", "line_item_usage_start_date",
            "line_item_currency_code", "line_item_product_code", "product_servicecode")}
        rows = [row(product_servicecode=""), row(product_servicecode=None, line_item_product_code="AmazonS3")]
        s = normalize.run([write_file(self.f("a.parquet"), rows, cols)], self.out)
        got = read_out(self.out)
        self.assertEqual(sorted(r["service"] for r in got), ["AmazonEC2", "AmazonS3"])
        self.assertTrue(all(r["region"] == "" and r["resource_id"] == "" and r["tags"] == {} for r in got))
        self.assertEqual(s["rows"], 2)

    def test_legacy_columns_mapped(self):
        cols = {"lineItem/LineItemType": "VARCHAR", "lineItem/UnblendedCost": "VARCHAR",
                "lineItem/UsageStartDate": "VARCHAR", "lineItem/CurrencyCode": "VARCHAR",
                "lineItem/ProductCode": "VARCHAR", "product/servicecode": "VARCHAR", "product/region": "VARCHAR",
                "lineItem/UsageType": "VARCHAR", "lineItem/ResourceId": "VARCHAR", "bill/PayerAccountId": "VARCHAR",
                "lineItem/UsageAccountId": "VARCHAR", "resourceTags/user:Team": "VARCHAR",
                "resourceTags/user:Env": "VARCHAR"}
        rows = [{"lineItem/LineItemType": "Usage", "lineItem/UnblendedCost": "2.5",
                 "lineItem/UsageStartDate": "2026-08-05T01:00:00Z", "lineItem/CurrencyCode": "USD",
                 "lineItem/ProductCode": "AmazonEC2", "product/servicecode": "", "product/region": "us-east-1",
                 "lineItem/UsageType": "BoxUsage", "lineItem/ResourceId": "i-1", "bill/PayerAccountId": "111",
                 "lineItem/UsageAccountId": "222", "resourceTags/user:Team": "pay", "resourceTags/user:Env": ""}]
        p = write_file(self.f("a.csv.gz"), rows, cols, kind="csv")
        self.assertEqual(normalize.detect([p]), "legacy-csv")
        s = normalize.run([p], self.out)
        r = read_out(self.out)[0]
        self.assertEqual(s["format"], "legacy-csv")
        self.assertEqual(r["tags"], {"user:Team": "pay"})
        self.assertEqual((r["service"], r["region"], r["month"], r["payer_account"], r["linked_account"]),
                         ("AmazonEC2", "us-east-1", "2026-08", "111", "222"))
        self.assertAlmostEqual(r["cost"], 2.5)

    def test_cur2_map_tags(self):
        p = write_file(self.f("a.parquet"), [row(resource_tags={"user_team": "pay"})], CUR2)
        normalize.run([p], self.out)
        self.assertEqual(read_out(self.out)[0]["tags"]["user_team"], "pay")

    def test_cur2_csv_json_tags_and_flat_columns(self):
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        cols["resource_tags"] = "VARCHAR"
        cols["resource_tags_user_env"] = "VARCHAR"
        rows = [row(line_item_unblended_cost="3", resource_tags='{"user_team":"pay"}', resource_tags_user_env="prod"),
                row(line_item_unblended_cost="1", resource_tags="not json", line_item_resource_id="i-2")]
        p = write_file(self.f("a.csv.gz"), rows, cols, kind="csv")
        self.assertEqual(normalize.detect([p]), "cur2-csv")
        normalize.run([p], self.out)
        got = {r["resource_id"]: r["tags"] for r in read_out(self.out)}
        self.assertEqual(got["i-0abc"], {"user_team": "pay", "user_env": "prod"})
        self.assertEqual(got["i-2"], {})

    def test_two_currencies_rejected(self):
        p = write_file(self.f("a.parquet"), [row(), row(line_item_currency_code="KRW")], CUR2)
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.parquet": p}))
        self.assertEqual(rc, 3)
        self.assertIn("통화가 여러 개입니다", json.loads(out)["reason"])

    def test_missing_required_columns(self):
        cols = {"line_item_line_item_type": "VARCHAR", "line_item_usage_start_date": "TIMESTAMP"}
        p = write_file(self.f("a.parquet"), [{"line_item_line_item_type": "Usage"}], cols)
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.parquet": p}))
        self.assertEqual(rc, 3)
        reason = json.loads(out)["reason"]
        self.assertTrue(reason.startswith("필수 열 누락: "))
        self.assertIn("line_item_unblended_cost", reason)
        self.assertIn("line_item_currency_code", reason)
        self.assertNotIn("line_item_usage_start_date", reason)

    def test_merge_newest_wins(self):
        prev = os.path.join(self.d, "prev")
        old = write_file(self.f("old.parquet"), [
            row(line_item_usage_start_date="2026-07-10 00:00:00", line_item_unblended_cost=5.0, line_item_resource_id="a"),
            row(line_item_usage_start_date="2026-08-10 00:00:00", line_item_unblended_cost=7.0, line_item_resource_id="b")], CUR2)
        normalize.run([old], prev)
        new = write_file(self.f("new.parquet"), [
            row(line_item_usage_start_date="2026-08-11 00:00:00", line_item_unblended_cost=2.0, line_item_resource_id="c"),
            row(line_item_usage_start_date="2026-09-01 00:00:00", line_item_unblended_cost=4.0, line_item_resource_id="d")], CUR2)
        s = normalize.run([new], self.out, prev_dir=prev)
        self.assertEqual(s["months"], ["2026-07", "2026-08", "2026-09"])
        self.assertEqual((s["first_day"], s["last_day"], s["rows"]), ("2026-07-10", "2026-09-01", 3))
        tot = {}
        for r in read_out(self.out):
            tot[r["month"]] = tot.get(r["month"], 0) + r["cost"]
        self.assertAlmostEqual(tot["2026-08"], 2.0)
        self.assertAlmostEqual(tot["2026-07"], 5.0)

    def test_merge_currency_mismatch_rejected(self):
        prev = os.path.join(self.d, "prev")
        normalize.run([write_file(self.f("old.parquet"), [row(line_item_usage_start_date="2026-07-10 00:00:00")], CUR2)], prev)
        new = write_file(self.f("new.parquet"), [row(line_item_currency_code="KRW")], CUR2)
        with self.assertRaises(normalize.CurRejected) as cm:
            normalize.run([new], self.out, prev_dir=prev)
        self.assertIn("통화가 여러 개입니다", cm.exception.reason)


class Stage(Tmp):
    def test_no_cur_files(self):
        rc, out = self.stage(make_zip(self.f("up.zip"), {"manifest.json": b"{}"}))
        self.assertEqual(rc, 3)
        self.assertEqual(json.loads(out)["reason"], "CUR 파일을 찾지 못했습니다")

    def test_duplicate_part_names_across_folders_and_manifest(self):
        a = write_file(self.f("a.parquet"), [row(line_item_resource_id="a")], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_usage_start_date="2026-09-02 00:00:00",
                                                 line_item_resource_id="b")], CUR2)
        z = make_zip(self.f("up.zip"), {"BILLING_PERIOD=2026-08/part-00000.snappy.parquet": a,
                                        "BILLING_PERIOD=2026-09/part-00000.snappy.parquet": b,
                                        "BILLING_PERIOD=2026-09/Manifest.json": b'{"a":1}'})
        rc, out = self.stage(z)
        self.assertEqual(rc, 0, out)
        self.assertEqual(sorted(r["resource_id"] for r in read_out(self.out)), ["a", "b"])
        self.assertEqual(json.loads(out)["months"], ["2026-08", "2026-09"])
        self.assertFalse(os.path.exists(os.path.join(self.d, "work", "duckdb_tmp")))

    def test_relative_work_dir(self):
        z = make_zip(self.f("up.zip"), {"a.parquet": write_file(self.f("a.parquet"), [row()], CUR2)})
        cwd = os.getcwd()
        os.chdir(self.d)
        try:
            with contextlib.redirect_stdout(io.StringIO()):
                rc = stage_cur.main("up.zip", "relwork", "acct-a")
        finally:
            os.chdir(cwd)
        self.assertEqual(rc, 0)
        self.assertTrue(os.path.isfile(os.path.join(self.d, "relwork", "cur_norm", "part-0.parquet")))

    def test_gz_that_is_not_csv_rejected(self):
        z = make_zip(self.f("up.zip"), {"bad.csv.gz": b"this is not gzip"})
        rc, out = self.stage(z)
        self.assertEqual(rc, 3)
        self.assertEqual(json.loads(out)["reason"], "CUR 파일을 읽을 수 없습니다: bad.csv.gz")

    def test_bad_account_and_not_zip(self):
        self.assertEqual(stage_cur.main(self.f("x.zip"), self.d, "bad acct!"), 2)
        with open(self.f("x.zip"), "wb") as fh:
            fh.write(b"nope")
        rc, out = self.stage(self.f("x.zip"))
        self.assertEqual((rc, json.loads(out)["reason"]), (3, "zip 형식이 아닙니다"))

    def test_staging_ok_with_prev(self):
        prev = os.path.join(self.d, "prev")
        normalize.run([write_file(self.f("o.parquet"), [row(line_item_usage_start_date="2026-07-01 00:00:00")], CUR2)], prev)
        z = make_zip(self.f("up.zip"), {"n.parquet": write_file(self.f("n.parquet"), [row()], CUR2)})
        rc, out = self.stage(z, prev)
        self.assertEqual(rc, 0, out)
        self.assertEqual(json.loads(out)["months"], ["2026-07", "2026-08"])


IDENT = {"identity_line_item_id": "VARCHAR", "identity_time_interval": "VARCHAR"}


def ident(i, iv="2026-08-03T00:00:00Z/2026-08-03T01:00:00Z"):
    return {"identity_line_item_id": i, "identity_time_interval": iv}


class Review1(Tmp):
    def staged(self, members, prev=None):
        rc, out = self.stage(make_zip(self.f("up.zip"), members), prev)
        return rc, out

    def test_glob_named_entry_is_not_expanded(self):
        a = write_file(self.f("a.parquet"), [row(line_item_resource_id="a", line_item_unblended_cost=1.0)], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_resource_id="b", line_item_unblended_cost=2.0)], CUR2)
        g = write_file(self.f("g.parquet"), [row(line_item_resource_id="g", line_item_unblended_cost=4.0)], CUR2)
        rc, out = self.staged({"a.parquet": a, "b.parquet": b, "*.parquet": g})
        self.assertEqual(rc, 0, out)
        got = read_out(self.out)
        self.assertEqual(sorted(r["resource_id"] for r in got), ["a", "b", "g"])
        self.assertAlmostEqual(sum(r["cost"] for r in got), 7.0)

    def test_glob_name_rejected_by_normalize_directly(self):
        p = write_file(self.f("a.parquet"), [row()], CUR2)
        os.replace(p, self.f("[x].parquet"))
        with self.assertRaises(normalize.CurRejected):
            normalize.run([self.f("[x].parquet")], self.out)

    def test_identity_dedup_across_folders(self):
        cols = dict(CUR2, **IDENT)
        r1 = row(line_item_unblended_cost=3.0, **ident("L1"))
        r2 = row(line_item_unblended_cost=5.0, line_item_resource_id="b", **ident("L2"))
        a = write_file(self.f("a.parquet"), [r1, r2], cols)
        b = write_file(self.f("b.parquet"), [r1, r2], cols)
        rc, out = self.staged({"m/one/p.parquet": a, "m/two/p.parquet": b})
        self.assertEqual(rc, 0, out)
        got = read_out(self.out)
        self.assertEqual(len(got), 2)
        self.assertAlmostEqual(sum(r["cost"] for r in got), 8.0)

    def test_same_month_in_two_folders_without_identity_rejected(self):
        a = write_file(self.f("a.parquet"), [row()], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_resource_id="z")], CUR2)
        rc, out = self.staged({"one/p.parquet": a, "two/p.parquet": b})
        self.assertEqual(rc, 3)
        self.assertEqual(json.loads(out)["reason"], "같은 달의 CUR가 여러 폴더에 있습니다: 2026-08")

    def test_different_months_in_two_folders_ok(self):
        a = write_file(self.f("a.parquet"), [row()], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_usage_start_date="2026-09-02 00:00:00")], CUR2)
        rc, out = self.staged({"one/p.parquet": a, "two/p.parquet": b})
        self.assertEqual(rc, 0, out)

    def test_gz_ratio_rejected(self):
        import gzip
        big = gzip.compress(b"a," * (1 << 20))
        rc, out = self.staged({"x.csv.gz": big})
        self.assertEqual(rc, 3)
        self.assertEqual(json.loads(out)["reason"], "압축률이 비정상적으로 높습니다: x.csv.gz")

    def test_gz_budget_rejected(self):
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        p = write_file(self.f("a.csv.gz"), [row(line_item_unblended_cost="1")], cols, kind="csv")
        old = normalize.MAX_UNCOMPRESSED
        normalize.MAX_UNCOMPRESSED = 50
        try:
            rc, out = self.staged({"a.csv.gz": p})
        finally:
            normalize.MAX_UNCOMPRESSED = old
        self.assertEqual((rc, json.loads(out)["reason"]), (3, "CUR 파일이 너무 큽니다"))

    def test_parquet_footer_caps(self):
        p = write_file(self.f("a.parquet"), [row()], CUR2)
        for attr, val in (("MAX_UNCOMPRESSED", 10), ("MAX_ROWS", 0)):
            old = getattr(normalize, attr)
            setattr(normalize, attr, val)
            try:
                rc, out = self.staged({"a.parquet": p})
            finally:
                setattr(normalize, attr, old)
            self.assertEqual((rc, json.loads(out)["reason"]), (3, "CUR 파일이 너무 큽니다"), attr)

    def test_unreadable_rows_over_threshold_rejected(self):
        p = write_file(self.f("a.parquet"), [row(), row(line_item_usage_start_date=None, line_item_resource_id="bad")], CUR2)
        rc, out = self.staged({"a.parquet": p})
        self.assertEqual(rc, 3)
        self.assertEqual(json.loads(out)["reason"], "CUR 행 중 날짜·금액을 읽을 수 없는 행이 많습니다: 1")

    def test_few_unreadable_rows_reported_as_dropped(self):
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        rows = [row(line_item_unblended_cost="1") for _ in range(2000)]
        rows.append(row(line_item_unblended_cost="oops", line_item_resource_id="bad"))
        p = write_file(self.f("a.csv.gz"), rows, cols, kind="csv")
        s = normalize.run([p], self.out)
        self.assertEqual((s["rows"], s["dropped_rows"]), (2000, 1))
        self.assertTrue(all(r["resource_id"] != "bad" for r in read_out(self.out)))

    def test_empty_currency_ignored(self):
        p = write_file(self.f("a.parquet"), [row(), row(line_item_currency_code=""), row(line_item_currency_code=None)], CUR2)
        s = normalize.run([p], self.out)
        self.assertEqual((s["currency"], s["rows"]), ("USD", 3))

    def test_detect_leaves_input_folder_alone(self):
        d = self.f("in")
        os.makedirs(d)
        p = write_file(os.path.join(d, "a.parquet"), [row()], CUR2)
        self.assertEqual(normalize.detect([p]), "cur2-parquet")
        self.assertEqual(os.listdir(d), ["a.parquet"])

    def test_nested_json_tag_values_do_not_fail_file(self):
        cols = {k: "VARCHAR" for k in CUR2}
        tags = '{"a":"x","b":{"y":1},"c":[1,2],"d":2,"e":null,"f":true}'
        p = write_file(self.f("a.csv.gz"), [row(line_item_unblended_cost="1", resource_tags=tags)], cols, kind="csv")
        normalize.run([p], self.out)
        t = read_out(self.out)[0]["tags"]
        self.assertEqual((t["a"], t["d"]), ("x", "2"))
        self.assertNotIn("e", t)


class FlattenDups(Tmp):
    def test_flatten_dups_prefixes_instead_of_rejecting(self):
        z = make_zip(self.f("up.zip"), {"m1/p.parquet": b"1", "m2/p.parquet": b"2", "m3/p.parquet": b"3"})
        with self.assertRaises(safezip.ZipRejected):
            safezip.extract(z, self.f("x"), {".parquet"})
        names = safezip.extract(z, self.f("y"), {".parquet"}, flatten_dups=True)
        self.assertEqual(sorted(names), ["0001_p.parquet", "0002_p.parquet", "p.parquet"])
        self.assertEqual(sorted(os.listdir(self.f("y"))), sorted(names))
        data = []
        for n in names:
            with open(os.path.join(self.f("y"), n), "rb") as fh:
                data.append(fh.read())
        self.assertEqual(sorted(data), [b"1", b"2", b"3"])


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


class Check(Tmp):
    def setUp(self):
        super().setUp()
        self.norm = os.path.join(self.d, "work", "cur_norm")
        self.data = os.path.join(self.d, "data")
        os.makedirs(self.data)

    def run_check(self, rows, cot=None):
        import check
        write_norm(self.norm, rows)
        path = None
        if cot is not None:
            path = self.f("spend_monthly.json")
            with open(path, "w", encoding="utf-8") as fh:
                json.dump(cot, fh, ensure_ascii=False)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = check.main(self.norm, self.data, "acct-a", path)
        with open(os.path.join(self.data, "checks.json"), encoding="utf-8") as fh:
            return rc, {r["name"]: r for r in json.load(fh)}

    def cot(self, month, spend, entity="acct-a"):
        return {"entity": entity, "month": month, "spend": spend, "bedrock": 0, "gpu_ec2": 0, "saas_marketplace": 0}

    def test_overlap_within_1pct_ok(self):
        rows = [nrow("2026-07-05", cost=60.0), nrow("2026-07-20", cost=40.4), nrow("2026-08-03", cost=5.0)]
        rc, c = self.run_check(rows, [self.cot("2026-07", 100.0), self.cot("2026-06", 9.0), self.cot("2026-07", 1.0, "acct-b")])
        self.assertEqual(rc, 0)
        self.assertTrue(c["cot_overlap:2026-07"]["ok"])
        self.assertAlmostEqual(c["cot_overlap:2026-07"]["got"], 100.4)
        self.assertEqual(c["cot_overlap:2026-07"]["want"], 100.0)
        self.assertEqual(c["cot_overlap:2026-07"]["entity"], "acct-a")
        self.assertNotIn("cot_overlap:2026-06", c)
        self.assertTrue(c["single_currency"]["ok"] and c["period_detected"]["ok"] and c["sum_matches_months"]["ok"])

    def test_overlap_2pct_fails(self):
        rc, c = self.run_check([nrow("2026-07-05", cost=102.0)], [self.cot("2026-07", 100.0)])
        self.assertEqual(rc, 1)
        self.assertFalse(c["cot_overlap:2026-07"]["ok"])
        self.assertTrue(c["single_currency"]["ok"])

    def test_no_cot_skips_overlap(self):
        rc, c = self.run_check([nrow("2026-07-05", cost=2.0)])
        self.assertEqual(rc, 0)
        self.assertFalse([n for n in c if n.startswith("cot_overlap")])
        rc, c = self.run_check([nrow("2026-07-05", cost=2.0)], [])
        self.assertFalse([n for n in c if n.startswith("cot_overlap")])

    def test_two_currencies_fail(self):
        rc, c = self.run_check([nrow(currency="USD"), nrow(currency="KRW")])
        self.assertEqual(rc, 1)
        self.assertFalse(c["single_currency"]["ok"])
        self.assertEqual(c["single_currency"]["got"], 2)


class Datasets(Tmp):
    def setUp(self):
        super().setUp()
        self.norm = os.path.join(self.d, "work", "cur_norm")
        self.data = os.path.join(self.d, "data")

    def build(self, rows, summary=None):
        import datasets
        write_norm(self.norm, rows)
        datasets.main(self.norm, self.data, "acct-a", summary)
        out = {}
        for n in ("cur_summary", "cur_trend", "cur_tag_coverage", "cur_resources", "period"):
            with open(os.path.join(self.data, n + ".json"), encoding="utf-8") as fh:
                out[n] = json.load(fh)
        return out

    def test_trend_ec2_by_tag(self):
        rows = [nrow("2026-08-03", cost=10.0, tags={"user:Team": "a"}), nrow("2026-08-04", cost=5.0, tags={"user:Team": "b"}),
                nrow("2026-08-05", cost=2.5), nrow("2026-07-05", cost=1.5, tags={"user:Team": "a"}),
                nrow("2026-08-05", cost=7.0, service="AmazonS3", usage_type="TimedStorage")]
        o = self.build(rows)
        t = [r for r in o["cur_trend"] if r["dim"] == "tag:user:Team" and r["service"] == "AmazonEC2" and r["grain"] == "month"]
        self.assertEqual({r["key"] for r in t}, {"a", "b", "(태그 없음)"})
        self.assertAlmostEqual(sum(r["cost"] for r in t), 19.0)
        self.assertTrue(all(r["entity"] == "acct-a" for r in t))
        svc = [r for r in o["cur_trend"] if r["dim"] == "service" and r["grain"] == "month" and r["period"] == "2026-08"]
        self.assertEqual({(r["key"], r["service"]) for r in svc}, {("AmazonEC2", "AmazonEC2"), ("AmazonS3", "AmazonS3")})
        keys = [(r["grain"], r["dim"], r["service"], r["period"], r["key"]) for r in o["cur_trend"]]
        self.assertEqual(keys, sorted(keys))
        self.assertEqual(o["period"], [{"entity": "acct-a", "source": "cur", "months": ["2026-07", "2026-08"],
                                        "first": "2026-07", "prev": "2026-07", "last": "2026-08",
                                        "labels": {"2026-07": "7월", "2026-08": "8월"}}])

    def test_top20_folds_rest(self):
        rows = [nrow("2026-08-03", cost=float(i + 1), usage_type="U%02d" % i) for i in range(25)]
        o = self.build(rows)
        t = [r for r in o["cur_trend"] if r["dim"] == "usage_type" and r["grain"] == "month"]
        self.assertEqual(len(t), 21)
        self.assertEqual(sum(1 for r in t if r["key"] == "기타"), 1)
        self.assertAlmostEqual(sum(r["cost"] for r in t), sum(range(1, 26)))
        self.assertAlmostEqual(next(r["cost"] for r in t if r["key"] == "기타"), sum(range(1, 6)))  # the 5 smallest

    def test_day_grain_last_92_days_only(self):
        rows = [nrow("2026-08-31", cost=1.0), nrow("2026-06-01", cost=2.0), nrow("2026-05-31", cost=4.0)]
        o = self.build(rows)
        days = sorted({r["period"] for r in o["cur_trend"] if r["grain"] == "day"})
        self.assertEqual(days, ["2026-06-01", "2026-08-31"])  # 92 days ending 08-31 start 06-01
        months = {r["period"] for r in o["cur_trend"] if r["grain"] == "month"}
        self.assertEqual(months, {"2026-05", "2026-06", "2026-08"})

    def test_no_tags(self):
        o = self.build([nrow("2026-08-03", cost=3.0), nrow("2026-07-03", cost=1.0)])
        self.assertFalse([r for r in o["cur_trend"] if r["dim"].startswith("tag:")])
        self.assertEqual(o["cur_tag_coverage"], [])
        self.assertEqual(o["cur_summary"][0]["rows"], 2)

    def test_tag_signals_new_surge_and_new_service(self):
        import datasets
        ms = ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]
        rows = []
        for i, m in enumerate(ms):
            day = m + "-10"
            rows.append(nrow(day, cost=1000.0, tags={"user:Service": "pay-core", "env": "prod"}, linked_account="333333333333"))
            rows.append(nrow(day, cost=1200.0 if m == ms[-1] else 400.0, tags={"user:Service": "ledger"}))
            if i >= 4:
                rows.append(nrow(day, cost=800.0, tags={"user:Service": "ai-chat"}, region="us-east-1"))
                rows.append(nrow(day, cost=300.0, service="AmazonBedrock", usage_type="tokens", tags={"user:Service": "pay-core"}))
            if i == 5:
                rows.append(nrow(day, cost=1.0, tags={"user:Service": "tiny"}))
        self.build(rows)
        with open(os.path.join(self.data, "cur_tag_signals.json"), encoding="utf-8") as fh:
            sig = {r["tag_value"]: r for r in json.load(fh)}
        self.assertEqual(set(sig), {"pay-core", "ai-chat", "ledger"})  # tiny is under the floor; env is not a signal key
        self.assertEqual(sig["ai-chat"]["kinds"], ["신규"])
        self.assertEqual(sig["ai-chat"]["first_month"], "2026-08")
        self.assertEqual(sig["ai-chat"]["regions"], ["us-east-1"])
        self.assertIsNone(sig["ai-chat"]["growth_pct"])
        self.assertEqual(sig["ledger"]["kinds"], ["급증"])
        self.assertEqual(sig["pay-core"]["kinds"], ["신규 서비스"])
        self.assertEqual(sig["pay-core"]["new_services"], ["AmazonBedrock"])
        self.assertEqual(sig["pay-core"]["accounts"][0], "333333333333")
        self.assertTrue(all(r["entity"] == "acct-a" and r["source"] == "cur" and r["tag_key"] == "user:Service" for r in sig.values()))
        self.assertEqual([c["month"] for c in sig["ledger"]["cost_by_month"]], ms)
        self.assertEqual(datasets.TS.classify("acct-a", ms[:1], {}, [], 0), [])

    def test_tag_coverage_and_top10_keys(self):
        rows = [nrow("2026-08-03", cost=4.0, tags={"k%02d" % i: "v"}) for i in range(12)]
        rows.append(nrow("2026-08-04", cost=6.0, tags={"k00": "v", "k01": "w"}))
        o = self.build(rows)
        dims = {r["dim"] for r in o["cur_trend"] if r["dim"].startswith("tag:")}
        self.assertEqual(len(dims), 10)
        self.assertIn("tag:k00", dims)
        self.assertNotIn("tag:k11", dims)
        cov = {r["tag_key"]: r for r in o["cur_tag_coverage"]}
        self.assertEqual(set(cov), {d[4:] for d in dims})
        self.assertAlmostEqual(cov["k00"]["tagged_cost"], 10.0)
        self.assertAlmostEqual(cov["k00"]["total_cost"], 54.0)
        self.assertAlmostEqual(cov["k00"]["pct"], 10.0 / 54.0 * 100, places=5)
        self.assertEqual(cov["k00"]["month"], "2026-08")

    def test_summary_fields(self):
        rows = [nrow("2026-07-05", cost=2.0), nrow("2026-08-03", cost=3.0, linked_account="333333333333")]
        o = self.build(rows, {"format": "cur2-parquet", "dropped_rows": 3})
        s = o["cur_summary"][0]
        self.assertEqual(s, {"entity": "acct-a", "format": "cur2-parquet", "currency": "USD", "first_day": "2026-07-05",
                             "last_day": "2026-08-03", "months": ["2026-07", "2026-08"], "rows": 2, "dropped_rows": 3,
                             "linked_accounts": 2, "cost_by_month": {"2026-07": 2.0, "2026-08": 3.0},
                             "truncated": False, "dropped": 0})
        self.assertIsNone(self.build(rows)["cur_summary"][0]["format"])

    def test_resources_top500_sorted(self):
        rows = []
        for i in range(502):
            rows.append(nrow("2026-08-03", cost=float(i + 1), resource_id="r%03d" % i, tags={"t": "x"} if i == 501 else {}))
        rows.append(nrow("2026-07-03", cost=9.0, resource_id="r501"))
        rows.append(nrow("2026-08-03", cost=999.0, resource_id=""))
        o = self.build(rows)
        r = o["cur_resources"]
        self.assertEqual(len(r), 500)
        self.assertEqual(r[0]["resource_id"], "r501")
        self.assertEqual((r[0]["cost_last"], r[0]["cost_prev"], r[0]["cost_total"]), (502.0, 9.0, 511.0))
        self.assertEqual(r[0]["tags"], {"t": "x"})
        self.assertEqual(r[0]["service"], "AmazonEC2")
        self.assertEqual([x["cost_last"] for x in r], sorted((x["cost_last"] for x in r), reverse=True))
        self.assertNotIn("r000", {x["resource_id"] for x in r})
        self.assertNotIn("", {x["resource_id"] for x in r})


if __name__ == "__main__":
    unittest.main()


# ---------- final fix wave ----------
class TrendBounds(Datasets):
    def test_day_grain_only_service_and_top3_tags(self):
        rows = []
        for i in range(5):  # tag k0 has the most cost, k4 the least
            rows.append(nrow("2026-08-03", cost=float(100 - i * 10), usage_type="U%d" % i, tags={"k%d" % i: "v"}))
        o = self.build(rows)
        day = {r["dim"] for r in o["cur_trend"] if r["grain"] == "day"}
        month = {r["dim"] for r in o["cur_trend"] if r["grain"] == "month"}
        self.assertEqual(day, {"service", "tag:k0", "tag:k1", "tag:k2"})
        self.assertEqual(month, {"service", "usage_type", "region", "linked_account"} | {"tag:k%d" % i for i in range(5)})
        self.assertEqual((o["cur_summary"][0]["truncated"], o["cur_summary"][0]["dropped"]), (False, 0))

    def test_high_cardinality_capped(self):
        import datasets
        old = datasets.MAX_TREND_ROWS
        self.addCleanup(setattr, datasets, "MAX_TREND_ROWS", old)
        self.assertEqual(old, 200000)
        datasets.MAX_TREND_ROWS = 170
        rows = []
        # 30 days x 3 services (each with its own usage type) and a tag with a new value every day
        for d in range(1, 31):
            for svc in ("AmazonEC2", "AmazonS3", "AmazonRDS"):
                rows.append(nrow("2026-08-%02d" % d, cost=1.0 + d, service=svc, usage_type=svc + "-U",
                                 tags={"team": "t%02d" % d}))
        # month: service, usage_type, region, linked_account one row per service; tag:team 20 values + 기타 per service
        full_month = 3 + 3 + 3 + 3 + 21 * 3
        o = self.build(rows)
        t = o["cur_trend"]
        s = o["cur_summary"][0]
        self.assertLessEqual(len(t), 170)
        self.assertTrue(s["truncated"])
        # before the cap: day service 90 + day tag:team 90 + month 75 = 255; the tag's day rows go first (a tie with
        # service goes against the tag), which leaves 165
        self.assertEqual(s["dropped"], 90)
        self.assertEqual(len(t), 90 + full_month)
        self.assertEqual(len([r for r in t if r["grain"] == "month"]), full_month, "month grain kept")
        self.assertEqual({r["dim"] for r in t if r["grain"] == "day"}, {"service"})
        self.assertAlmostEqual(sum(r["cost"] for r in t if r["grain"] == "day"), sum(3 * (1.0 + d) for d in range(1, 31)))
        keys = [(r["grain"], r["dim"], r["service"], r["period"], r["key"]) for r in t]
        self.assertEqual(keys, sorted(keys))


class DedupOrder(Tmp):
    def test_identity_dedup_newest_file_and_last_row_win(self):
        cols = dict(CUR2, **IDENT)
        a = write_file(self.f("a.parquet"), [row(line_item_unblended_cost=3.0, **ident("L1"))], cols)
        b = write_file(self.f("b.parquet"), [row(line_item_unblended_cost=5.0, **ident("L1")),
                                               row(line_item_unblended_cost=9.0, **ident("L2")),
                                               row(line_item_unblended_cost=11.0, **ident("L2"))], cols)
        normalize.run([a, b], self.out)
        got = sorted(r["cost"] for r in read_out(self.out))
        self.assertEqual(got, [5.0, 11.0])
        # file order decides, not the content: the same rows with the files swapped keep a's value
        out2 = os.path.join(self.d, "work2", "cur_norm")
        normalize.run([b, a], out2)
        self.assertEqual(sorted(r["cost"] for r in read_out(out2)), [3.0, 11.0])

    def test_identity_dedup_csv_newest_file_wins(self):
        cols = dict(CUR2, **IDENT)
        cols.pop("resource_tags")
        a = write_file(self.f("a.csv.gz"), [row(line_item_unblended_cost=3.0, **ident("L1"))], cols, kind="csv")
        b = write_file(self.f("b.csv.gz"), [row(line_item_unblended_cost=5.0, **ident("L1"))], cols, kind="csv")
        plain = []
        import gzip
        for p in (a, b):
            q = p[:-3]
            with gzip.open(p, "rb") as fi, open(q, "wb") as fo:
                fo.write(fi.read())
            plain.append(q)
        normalize.run(plain, self.out)
        self.assertEqual([r["cost"] for r in read_out(self.out)], [5.0])
        normalize.run(plain[::-1], os.path.join(self.d, "w3", "cur_norm"))
        self.assertEqual([r["cost"] for r in read_out(os.path.join(self.d, "w3", "cur_norm"))], [3.0])


class CurrencyTrim(Tmp):
    def test_whitespace_around_currency_is_one_currency(self):
        p = write_file(self.f("a.parquet"), [row(), row(line_item_currency_code=" USD "), row(line_item_currency_code="USD\t")], CUR2)
        s = normalize.run([p], self.out)
        self.assertEqual((s["currency"], s["rows"]), ("USD", 3))
        self.assertEqual({r["currency"] for r in read_out(self.out)}, {"USD"})

    def test_check_trims_currency(self):
        import check
        norm = os.path.join(self.d, "work", "cur_norm")
        write_norm(norm, [nrow(currency="USD"), nrow(currency="USD ")])
        data = os.path.join(self.d, "data")
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = check.main(norm, data, "acct-a")
        with open(os.path.join(data, "checks.json"), encoding="utf-8") as fh:
            c = {r["name"]: r for r in json.load(fh)}
        self.assertEqual(rc, 0)
        self.assertEqual(c["single_currency"]["got"], 1)
