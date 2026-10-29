"""CUR detection and normalization to amortized Parquet. Synthetic fixtures only (duckdb COPY, zipped with zipfile)."""
import contextlib, io, json, os, sys, tempfile, unittest, zipfile
from cur_fixtures import *  # noqa: E402,F401,F403 (shared CUR fixtures and the Tmp base, moved from test_cur.py)


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
            "line_item_currency_code", "bill_billing_period_start_date", "line_item_product_code",
            "product_servicecode")}
        rows = [row(product_servicecode=""), row(product_servicecode=None, line_item_product_code="AmazonS3")]
        s = normalize.run([write_file(self.f("a.parquet"), rows, cols)], self.out)
        got = read_out(self.out)
        self.assertEqual(sorted(r["service"] for r in got), ["AmazonEC2", "AmazonS3"])
        self.assertTrue(all(r["region"] == "" and r["resource_id"] == "" and r["tags"] == {} for r in got))
        self.assertEqual(s["rows"], 2)

    def test_legacy_columns_mapped(self):
        cols = {"lineItem/LineItemType": "VARCHAR", "lineItem/UnblendedCost": "VARCHAR",
                "lineItem/UsageStartDate": "VARCHAR", "lineItem/CurrencyCode": "VARCHAR",
                "bill/BillingPeriodStartDate": "VARCHAR",
                "lineItem/ProductCode": "VARCHAR", "product/servicecode": "VARCHAR", "product/region": "VARCHAR",
                "lineItem/UsageType": "VARCHAR", "lineItem/ResourceId": "VARCHAR", "bill/PayerAccountId": "VARCHAR",
                "lineItem/UsageAccountId": "VARCHAR", "resourceTags/user:Team": "VARCHAR",
                "resourceTags/user:Env": "VARCHAR"}
        rows = [{"lineItem/LineItemType": "Usage", "lineItem/UnblendedCost": "2.5",
                 "lineItem/UsageStartDate": "2026-08-05T01:00:00Z", "lineItem/CurrencyCode": "USD",
                 "bill/BillingPeriodStartDate": "2026-08-01T00:00:00Z",
                 "lineItem/ProductCode": "AmazonEC2", "product/servicecode": "", "product/region": "us-east-1",
                 "lineItem/UsageType": "BoxUsage", "lineItem/ResourceId": "i-1", "bill/PayerAccountId": "111",
                 "lineItem/UsageAccountId": "222", "resourceTags/user:Team": "pay", "resourceTags/user:Env": ""}]
        p = write_file(self.f("a.csv.gz"), rows, cols, kind="csv")
        self.assertEqual(normalize.detect([p]), "legacy-csv")
        s = normalize.run([p], self.out)
        r = read_out(self.out)[0]
        self.assertEqual(s["format"], "legacy-csv")
        self.assertEqual(r["tags"], {"user:Team": "pay"})
        self.assertEqual((r["service"], r["region"], r["month"], r["payer_account"], r["linked_account"],
                          r["billing_period"]), ("AmazonEC2", "us-east-1", "2026-08", "111", "222", "2026-08"))
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

    def _month_totals(self):
        tot = {}
        for r in read_out(self.out):
            tot[r["month"]] = tot.get(r["month"], 0) + r["cost"]
        return tot

    def test_merge_late_rows_of_next_bill_keep_carried_month(self):
        prev = os.path.join(self.d, "prev")
        normalize.run([write_file(self.f("old.parquet"), [
            row(line_item_usage_start_date="2026-08-%02d 00:00:00" % d, line_item_unblended_cost=10.0,
                line_item_resource_id="aug-%d" % d) for d in range(1, 11)], CUR2)], prev)
        new = write_file(self.f("new.parquet"), [
            row(line_item_usage_start_date="2026-09-02 00:00:00", line_item_unblended_cost=4.0, line_item_resource_id="sep"),
            # dated in August, billed in September: late line items of the September bill
            row(line_item_usage_start_date="2026-08-31 00:00:00", bill_billing_period_start_date="2026-09-01 00:00:00",
                line_item_unblended_cost=1.5, line_item_resource_id="late")], CUR2)
        s = normalize.run([new], self.out, prev_dir=prev)
        self.assertEqual(s["months"], ["2026-08", "2026-09"])
        self.assertEqual(s["rows"], 12)
        tot = self._month_totals()
        self.assertAlmostEqual(tot["2026-08"], 101.5)
        self.assertAlmostEqual(tot["2026-09"], 4.0)

    def test_merge_redelivered_bill_replaces_its_rows(self):
        prev = os.path.join(self.d, "prev")
        normalize.run([write_file(self.f("old.parquet"), [
            row(line_item_usage_start_date="2026-08-10 00:00:00", line_item_unblended_cost=7.0, line_item_resource_id="a"),
            row(line_item_usage_start_date="2026-09-03 00:00:00", line_item_unblended_cost=3.0, line_item_resource_id="b"),
            row(line_item_usage_start_date="2026-08-31 00:00:00", bill_billing_period_start_date="2026-09-01 00:00:00",
                line_item_unblended_cost=1.0, line_item_resource_id="late-v1")], CUR2)], prev)
        new = write_file(self.f("new.parquet"), [
            row(line_item_usage_start_date="2026-09-04 00:00:00", line_item_unblended_cost=5.0, line_item_resource_id="c"),
            row(line_item_usage_start_date="2026-08-31 00:00:00", bill_billing_period_start_date="2026-09-01 00:00:00",
                line_item_unblended_cost=2.0, line_item_resource_id="late-v2")], CUR2)
        normalize.run([new], self.out, prev_dir=prev)
        self.assertEqual(sorted(r["resource_id"] for r in read_out(self.out)), ["a", "c", "late-v2"])
        tot = self._month_totals()
        self.assertAlmostEqual(tot["2026-08"], 9.0)
        self.assertAlmostEqual(tot["2026-09"], 5.0)

    def test_merge_carried_file_without_billing_period_uses_its_months(self):
        prev = os.path.join(self.d, "prev")
        os.makedirs(prev)
        c = duckdb.connect()
        c.execute("COPY (SELECT DATE '2026-07-10' AS usage_date, '2026-07' AS month, 5.0 AS cost, 'a' AS resource_id "
                  "UNION ALL SELECT DATE '2026-08-10', '2026-08', 7.0, 'b') TO '%s' (FORMAT parquet)"
                  % os.path.join(prev, "part-0.parquet"))
        c.close()
        new = write_file(self.f("new.parquet"), [
            row(line_item_usage_start_date="2026-08-11 00:00:00", line_item_unblended_cost=2.0, line_item_resource_id="c"),
            row(line_item_usage_start_date="2026-07-31 00:00:00", bill_billing_period_start_date="2026-08-01 00:00:00",
                line_item_unblended_cost=1.0, line_item_resource_id="late")], CUR2)
        normalize.run([new], self.out, prev_dir=prev)
        self.assertEqual(sorted(r["resource_id"] for r in read_out(self.out)), ["a", "c", "late"])

    def _legacy_prev(self, prev, with_null_period=False):
        os.makedirs(prev)
        c = duckdb.connect()
        c.execute("COPY (SELECT DATE '2026-07-10' AS usage_date, '2026-07' AS month, 5.0 AS cost, 'a' AS resource_id%s "
                  "UNION ALL SELECT DATE '2026-08-10', '2026-08', 7.0, 'b'%s) TO '%s' (FORMAT parquet)"
                  % (", CAST(NULL AS VARCHAR) AS billing_period" if with_null_period else "",
                     ", NULL" if with_null_period else "", os.path.join(prev, "part-0.parquet")))
        c.close()

    def test_legacy_carried_rows_survive_the_second_hop(self):
        prev = os.path.join(self.d, "prev")
        self._legacy_prev(prev)
        hop1 = os.path.join(self.d, "hop1")
        normalize.run([write_file(self.f("aug.parquet"), [
            row(line_item_usage_start_date="2026-08-11 00:00:00", line_item_unblended_cost=2.0,
                line_item_resource_id="c")], CUR2)], hop1, prev_dir=prev)
        self.assertEqual({r["resource_id"]: r["billing_period"] for r in read_out(hop1)}, {"a": "2026-07", "c": "2026-08"})
        normalize.run([write_file(self.f("sep.parquet"), [
            row(line_item_usage_start_date="2026-09-02 00:00:00", line_item_resource_id="d")], CUR2)],
            self.out, prev_dir=hop1)
        self.assertEqual(sorted(r["resource_id"] for r in read_out(self.out)), ["a", "c", "d"])

    def test_carried_null_billing_period_falls_back_to_month(self):
        # a carried file whose legacy rows were written with a NULL billing_period
        prev = os.path.join(self.d, "prev")
        self._legacy_prev(prev, with_null_period=True)
        normalize.run([write_file(self.f("aug.parquet"), [
            row(line_item_usage_start_date="2026-08-11 00:00:00", line_item_resource_id="c")], CUR2)],
            self.out, prev_dir=prev)
        self.assertEqual({r["resource_id"]: r["billing_period"] for r in read_out(self.out)}, {"a": "2026-07", "c": "2026-08"})

    def test_billing_period_column_required(self):
        cols = {k: v for k, v in CUR2.items() if k != "bill_billing_period_start_date"}
        with self.assertRaises(normalize.CurRejected) as cm:
            normalize.run([write_file(self.f("a.parquet"), [row()], cols)], self.out)
        self.assertEqual(cm.exception.reason, "필수 열 누락: bill_billing_period_start_date")

    def test_unreadable_billing_period_is_a_bad_row(self):
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        rows = [row(line_item_unblended_cost="1") for _ in range(2000)]
        rows.append(row(bill_billing_period_start_date="soon", line_item_resource_id="bad"))
        s = normalize.run([write_file(self.f("a.csv.gz"), rows, cols, kind="csv")], self.out)
        self.assertEqual((s["rows"], s["dropped_rows"]), (2000, 1))

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


class MixedDialects(Tmp):
    def test_legacy_and_cur2_csv_in_one_folder_rejected(self):
        old = write_file(self.f("old.csv.gz"), [legacy_row()], LEGACY, kind="csv")
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        new = write_file(self.f("new.csv.gz"), [row(line_item_unblended_cost="4")], cols, kind="csv")
        rc, out = self.stage(make_zip(self.f("up.zip"), {"cur/a.csv.gz": old, "cur/b.csv.gz": new}))
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "CUR 형식이 섞여 있습니다 (레거시 CUR와 CUR 2.0)")

    def test_legacy_and_cur2_csv_in_different_folders_rejected(self):
        old = write_file(self.f("old.csv.gz"), [legacy_row()], LEGACY, kind="csv")
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        new = write_file(self.f("new.csv.gz"), [row(line_item_unblended_cost="4")], cols, kind="csv")
        rc, out = self.stage(make_zip(self.f("up.zip"), {"legacy/a.csv.gz": old, "cur2/b.csv.gz": new}))
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "CUR 형식이 섞여 있습니다 (레거시 CUR와 CUR 2.0)")


class EffectiveCostColumns(Tmp):
    def test_discounted_usage_without_effective_cost_column_rejected(self):
        cols = {k: v for k, v in CUR2.items() if k != "reservation_effective_cost"}
        p = write_file(self.f("a.parquet"), [row(), row(line_item_line_item_type="DiscountedUsage",
                                                        line_item_unblended_cost=0.0, line_item_resource_id="d")], cols)
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.parquet": p}))
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "필수 열 누락: reservation_effective_cost (DiscountedUsage 행)")

    def test_sp_covered_usage_without_effective_cost_column_rejected(self):
        cols = {k: v for k, v in CUR2.items() if k != "savings_plan_savings_plan_effective_cost"}
        p = write_file(self.f("a.parquet"), [row(line_item_line_item_type="SavingsPlanCoveredUsage")], cols)
        with self.assertRaises(normalize.CurRejected) as cm:
            normalize.run([p], self.out)
        self.assertEqual(cm.exception.reason,
                         "필수 열 누락: savings_plan_savings_plan_effective_cost (SavingsPlanCoveredUsage 행)")

    def test_fee_rows_without_their_columns_rejected(self):
        drop = ("reservation_unused_recurring_fee", "savings_plan_used_commitment")
        cols = {k: v for k, v in CUR2.items() if k not in drop}
        p = write_file(self.f("a.parquet"), [row(line_item_line_item_type="RIFee"),
                                             row(line_item_line_item_type="SavingsPlanRecurringFee")], cols)
        with self.assertRaises(normalize.CurRejected) as cm:
            normalize.run([p], self.out)
        self.assertEqual(cm.exception.reason, "필수 열 누락: reservation_unused_recurring_fee (RIFee 행), "
                                              "savings_plan_used_commitment (SavingsPlanRecurringFee 행)")

    def test_columns_not_needed_without_those_rows(self):
        cols = {k: v for k, v in CUR2.items() if not k.startswith(("reservation_", "savings_plan_"))}
        s = normalize.run([write_file(self.f("a.parquet"), [row()], cols)], self.out)
        self.assertEqual(s["rows"], 1)

    def test_effective_cost_missing_in_one_file_is_a_bad_row(self):
        # union_by_name: the file without the column reads NULL there; the rows must not pass as zero cost
        du = dict(line_item_line_item_type="DiscountedUsage", line_item_unblended_cost=0.0)
        a = write_file(self.f("a.parquet"), [row(reservation_effective_cost=4.0, **du)], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_resource_id="b", **du)],
                       {k: v for k, v in CUR2.items() if k != "reservation_effective_cost"})
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.parquet": a, "b.parquet": b}))
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "CUR 행 중 날짜·금액을 읽을 수 없는 행이 많습니다: 1")

    def test_unblended_cost_missing_in_one_file_is_a_bad_row(self):
        # the same union read: a Usage row of the file without line_item_unblended_cost must not publish as zero
        # (normalize alone counts it as a bad row; staging rejects the file earlier for the missing column)
        a = write_file(self.f("a.parquet"), [row(line_item_unblended_cost=4.0)], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_resource_id="b")],
                       {k: v for k, v in CUR2.items() if k != "line_item_unblended_cost"})
        with self.assertRaises(normalize.CurRejected) as cm:
            normalize.run([a, b], self.out)
        self.assertEqual(cm.exception.reason, "CUR 행 중 날짜·금액을 읽을 수 없는 행이 많습니다: 1")
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.parquet": a, "b.parquet": b}))
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "필수 열 누락: line_item_unblended_cost (b.parquet)")

    def test_line_item_type_missing_in_one_file_rejected(self):
        # in the union that file's type reads NULL and its rows would drop out of scope before the bad-row count
        a = write_file(self.f("a.parquet"), [row(line_item_unblended_cost=4.0)], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_resource_id="b")],
                       {k: v for k, v in CUR2.items() if k != "line_item_line_item_type"})
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.parquet": a, "b.parquet": b}))
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "필수 열 누락: line_item_line_item_type (b.parquet)")

    def test_line_item_type_missing_in_one_csv_rejected(self):
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        a = write_file(self.f("a.csv.gz"), [row(line_item_unblended_cost="4")], cols, kind="csv")
        b = write_file(self.f("b.csv.gz"), [row(line_item_unblended_cost="2", line_item_resource_id="b")],
                       {k: v for k, v in cols.items() if k != "line_item_line_item_type"}, kind="csv")
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.csv.gz": a, "b.csv.gz": b}))
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "필수 열 누락: line_item_line_item_type (b.csv.gz)")


class PlainCsvBudget(Tmp):
    def files(self):
        import gzip
        cols = {k: "VARCHAR" for k in CUR2 if k != "resource_tags"}
        gz = write_file(self.f("a.csv.gz"), [row(line_item_unblended_cost="1")], cols, kind="csv")
        b = write_file(self.f("b.csv.gz"), [row(line_item_unblended_cost="2", line_item_resource_id="b")], cols, kind="csv")
        with gzip.open(b, "rb") as fh:
            plain = fh.read()
        with gzip.open(gz, "rb") as fh:
            n_gz = len(fh.read())
        return gz, plain, n_gz

    def test_plain_csv_counts_toward_the_budget(self):
        gz, plain, n_gz = self.files()
        old = normalize.MAX_UNCOMPRESSED
        self.addCleanup(setattr, normalize, "MAX_UNCOMPRESSED", old)
        normalize.MAX_UNCOMPRESSED = max(len(plain), n_gz) + 1  # each alone fits, together they do not
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.csv.gz": gz, "b.csv": plain}))
        self.assertEqual((rc, json.loads(out)["reason"]), (3, "CUR 파일이 너무 큽니다"))
        normalize.MAX_UNCOMPRESSED = len(plain) - 1
        rc, out = self.stage(make_zip(self.f("up2.zip"), {"b.csv": plain}))
        self.assertEqual((rc, json.loads(out)["reason"]), (3, "CUR 파일이 너무 큽니다"))

    def test_plain_and_gz_within_budget_ok(self):
        gz, plain, n_gz = self.files()
        old = normalize.MAX_UNCOMPRESSED
        self.addCleanup(setattr, normalize, "MAX_UNCOMPRESSED", old)
        normalize.MAX_UNCOMPRESSED = len(plain) + n_gz
        rc, out = self.stage(make_zip(self.f("up.zip"), {"a.csv.gz": gz, "b.csv": plain}))
        self.assertEqual(rc, 0, out)


class CorruptStream(Tmp):
    def test_corrupt_deflate_entry_rejected(self):
        p = write_file(self.f("a.parquet"), [row() for _ in range(50)], CUR2)
        z = make_zip(self.f("up.zip"), {"a.parquet": p})
        with open(z, "rb") as fh:
            raw = bytearray(fh.read())
        i = raw.find(b"a.parquet") + len("a.parquet")  # end of the local header's name
        for k in range(i + 8, i + 40):
            raw[k] ^= 0xFF
        with open(z, "wb") as fh:
            fh.write(bytes(raw))
        rc, out = self.stage(z)
        self.assertEqual((rc, json.loads(out)["reason"]), (3, "압축을 풀 수 없는 항목입니다: a.parquet"))


if __name__ == "__main__":
    unittest.main()
