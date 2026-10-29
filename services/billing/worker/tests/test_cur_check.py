"""CUR review fixes and checks (check.py) over normalized CUR. Synthetic fixtures only."""
import contextlib, io, json, os, sys, tempfile, unittest, zipfile
from cur_fixtures import *  # noqa: E402,F401,F403 (shared CUR fixtures and the Tmp base, moved from test_cur.py)


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

    def test_identity_same_month_in_two_folders_rejected(self):
        # line item ids are not stable across report versions, and assembly folders (UUIDs) do not sort by age:
        # neither dedup nor "newest folder wins" is safe, so two copies of a month are refused even with identity
        cols = dict(CUR2, **IDENT)
        r1 = row(line_item_unblended_cost=3.0, **ident("L1"))
        r2 = row(line_item_unblended_cost=5.0, line_item_resource_id="b", **ident("L2"))
        a = write_file(self.f("a.parquet"), [r1, r2], cols)
        b = write_file(self.f("b.parquet"), [r1, r2], cols)
        rc, out = self.staged({"m/one/p.parquet": a, "m/two/p.parquet": b})
        self.assertEqual(rc, 3)
        self.assertEqual(json.loads(out)["reason"], "같은 달의 CUR가 여러 폴더에 있습니다: 2026-08")

    def test_identity_two_assemblies_with_different_ids_rejected(self):
        cols = dict(CUR2, **IDENT)
        a = write_file(self.f("a.parquet"), [row(line_item_unblended_cost=3.0, **ident("A1"))], cols)
        b = write_file(self.f("b.parquet"), [row(line_item_unblended_cost=3.0, **ident("B1"))], cols)
        rc, out = self.staged({"20260801-20260901/0f3c-assembly/r-1.parquet": a,
                               "20260801-20260901/9a1e-assembly/r-1.parquet": b})
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "같은 달의 CUR가 여러 폴더에 있습니다: 2026-08")

    def test_identity_parts_in_one_folder_dedup(self):
        cols = dict(CUR2, **IDENT)
        r1 = row(line_item_unblended_cost=3.0, **ident("L1"))
        a = write_file(self.f("a.parquet"), [r1], cols)
        b = write_file(self.f("b.parquet"), [r1, row(line_item_unblended_cost=5.0, line_item_resource_id="b",
                                                     **ident("L2"))], cols)
        rc, out = self.staged({"m/one/p1.parquet": a, "m/one/p2.parquet": b})
        self.assertEqual(rc, 0, out)
        self.assertAlmostEqual(sum(r["cost"] for r in read_out(self.out)), 8.0)

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

    def test_folder_per_bill_with_late_row_of_prior_month_ok(self):
        # the folder check follows billing_period, the key carried rows are replaced by: a late line item of the
        # September bill dated in August is not a second copy of the August bill
        a = write_file(self.f("a.parquet"), [row(line_item_unblended_cost=3.0)], CUR2)
        b = write_file(self.f("b.parquet"), [
            row(line_item_usage_start_date="2026-09-02 00:00:00", line_item_resource_id="sep"),
            row(line_item_usage_start_date="2026-08-31 00:00:00", bill_billing_period_start_date="2026-09-01 00:00:00",
                line_item_resource_id="late")], CUR2)
        rc, out = self.staged({"aug/p.parquet": a, "sep/p.parquet": b})
        self.assertEqual(rc, 0, out)
        self.assertEqual(sorted(r["resource_id"] for r in read_out(self.out)), ["i-0abc", "late", "sep"])

    def test_same_bill_in_two_folders_rejected_even_across_months(self):
        a = write_file(self.f("a.parquet"), [row()], CUR2)
        b = write_file(self.f("b.parquet"), [row(line_item_usage_start_date="2026-09-02 00:00:00",
                                                 bill_billing_period_start_date="2026-08-01 00:00:00")], CUR2)
        rc, out = self.staged({"one/p.parquet": a, "two/p.parquet": b})
        self.assertEqual(rc, 3, out)
        self.assertEqual(json.loads(out)["reason"], "같은 달의 CUR가 여러 폴더에 있습니다: 2026-08")

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


if __name__ == "__main__":
    unittest.main()
