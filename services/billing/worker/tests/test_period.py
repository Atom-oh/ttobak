"""period.py: month detection, slots, labels, shift. Synthetic data only."""
import csv, os, sys, tempfile, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import cotload  # noqa: E402
import period  # noqa: E402


class Period(unittest.TestCase):
    def test_six_months(self):
        m = ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]
        self.assertEqual(period.labels(m)["2026-09"], "9월")
        self.assertEqual(period.slots(m), m)

    def test_short_and_cross_year(self):
        m = ["2025-11", "2025-12", "2026-01"]
        self.assertEqual(period.slots(m), [None, None, None] + m)
        self.assertEqual(period.labels(m)["2025-12"], "25년 12월")
        self.assertEqual(period.labels(m)["2026-01"], "26년 1월")

    def test_shift(self):
        self.assertEqual(period.shift("2026-09", 6), "2027-03")
        self.assertEqual(period.shift("2026-01", -2), "2025-11")

    def test_unset_env_constants(self):
        if os.environ.get("COT_MONTHS"):
            self.skipTest("COT_MONTHS set")
        self.assertEqual(period.PRESENT, [])
        self.assertEqual(period.MONTHS, [None] * 6)
        self.assertIsNone(period.LAST)

    def test_detect_keeps_last_six(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, "acct-a"))
            with open(os.path.join(d, "acct-a", "Data_Service_Costs.csv"), "w", newline="") as f:
                w = csv.writer(f)
                w.writerow(["", "Report Name", "timePeriod.start", "cost", "a", "b", "c", "d"])
                for i in (1, 2, 3, 4, 5, 6, 7, 8):
                    for ent in ("acct-a", "acct-b"):
                        w.writerow(["", ent, f"2026-{i:02d}-01", "1", "", "", "", ""])
                    w.writerow(["", "acct-a", f"2026-{i:02d}-15", "1", "", "", "", ""])
            old = cotload.OUT
            cotload.OUT = d
            try:
                self.assertEqual(period.detect("acct-a"), [f"2026-{i:02d}" for i in range(3, 9)])
            finally:
                cotload.OUT = old

    def sheet(self, d, rows, header=True):
        os.makedirs(os.path.join(d, "acct-a"))
        with open(os.path.join(d, "acct-a", "Data_Service_Costs.csv"), "w", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            if header:
                w.writerow(["", "Report Name", "timePeriod.start", "cost", "a", "b", "c", "d"])
            for r in rows:
                w.writerow(r)

    def detect_in(self, d):
        old = cotload.OUT
        cotload.OUT = d
        try:
            return period.detect("acct-a")
        finally:
            cotload.OUT = old

    def test_implausible_serial_is_unreadable(self):
        for bad in ("1e12", "inf", "-1e9", "3000000"):
            with self.assertRaises(ValueError, msg=bad):
                cotload.month({"timePeriod.start": bad})
        self.assertEqual(cotload.month({"timePeriod.start": "46082"}), "2026-03")

    def test_unreadable_dates_or_header_find_no_period(self):
        # an untrusted workbook cell or layout: no period (run_account.sh exits 3), not a crash (exit 4)
        for rows, header in (([["", "acct-a", "2026-03-01", "1"], ["", "acct-a", "1e12", "1"]], True),
                             ([["", "acct-a", "inf", "1"]], True),
                             ([["", "acct-a", "2026-03-01", "1"]], False)):
            with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
                self.sheet(d, rows, header)
                self.assertEqual(self.detect_in(d), [], (rows, header))


if __name__ == "__main__":
    unittest.main()
