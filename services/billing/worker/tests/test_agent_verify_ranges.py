"""agent_verify.py range claims: both endpoints of a percent or money range are typed by the unit written once.
Imports only agent_verify; the index is built from hand-written synthetic input."""
import os, sys, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agent_verify as av  # noqa: E402


def ok(case, src, text):
    case.assertTrue(av.verify(text, av.index(src)), text)


def bad(case, src, text):
    case.assertFalse(av.verify(text, av.index(src)), text)


class PercentRange(unittest.TestCase):
    # 20 exists only as a count/bare number; 30 is the only percent
    ONE_PCT = {"recs": 20, "coverage_pct": 30}
    BOTH_PCT = {"coverage_first_pct": 20, "coverage_last_pct": 30}

    def test_first_endpoint_must_be_a_percent(self):
        for t in ["커버리지 20–30%", "커버리지 20~30%", "커버리지 20∼30%", "커버리지 20-30 퍼센트", "커버리지 20 ~ 30%",
                  "커버리지 20%~30%", "커버리지 20—30퍼센트"]:
            bad(self, self.ONE_PCT, t)
        ok(self, self.ONE_PCT, "커버리지 30%")

    def test_chair_example(self):
        src = {"cost": 60, "coverage_pct": 80}
        bad(self, src, "커버리지 60~80%")
        bad(self, src, "커버리지 60%")
        ok(self, src, "커버리지 80%")

    def test_both_percents_accept(self):
        for t in ["커버리지 20–30%", "커버리지 20~30%", "커버리지 20-30 퍼센트", "커버리지 20%~30%", "커버리지 20 – 30%"]:
            ok(self, self.BOTH_PCT, t)

    def test_second_endpoint_still_checked(self):
        bad(self, {"coverage_first_pct": 20, "recs": 30}, "커버리지 20–30%")

    def test_signed_endpoints(self):
        src = {"delta_pct": [-5, -10]}
        ok(self, src, "−5~−10%")
        bad(self, {"delta_pct": [5, -10]}, "−5~−10%")

    def test_percent_range_in_source_feeds_percent_pool(self):
        src = {"note": "커버리지 20~30% 구간"}
        ok(self, src, "커버리지 20%")
        ok(self, src, "커버리지 30%")


class MoneyRange(unittest.TestCase):
    def test_second_endpoint_is_money(self):
        # 200 exists only as a count; "$100-200" must not verify 200 against the bare pool
        bad(self, {"cost": 100, "recs": 200}, "월 $100–200")
        bad(self, {"cost": 100, "recs": 200}, "월 $100-200")
        ok(self, {"cost": [100, 200]}, "월 $100–200")
        ok(self, {"cost": [100, 200]}, "월 $100~$200")

    def test_scale_applies_to_both_endpoints(self):
        ok(self, {"cost": [1.2e6, 1.5e6]}, "$1.2–1.5M")
        # $1.2 at unit scale is not $1.2M
        bad(self, {"hourly": 1.2, "cost": 1.5e6}, "$1.2–1.5M")
        bad(self, {"cost": [1.2e6, 1.5e6]}, "$1.2–1.5X")

    def test_money_minus_percent_is_not_a_range(self):
        ok(self, {"cost": 100, "coverage_pct": 20}, "$100 - 20%")

    def test_spaced_scale_after_the_range(self):
        # a spaced scale word on the second endpoint multiplies both: never verified at unit scale
        for t in ("$100–200 K", "$100-200 K", "$100~$200 M", "$100 – 200 million", "$1.2–1.5 bn"):
            bad(self, {"cost": [100, 200, 1.2, 1.5]}, t)
        bad(self, {"cost": [100, 200]}, "$200 K")
        ok(self, {"cost": [1e5, 2e5]}, "$100–200K")

    def test_sentence_final_period(self):
        # a period ending the sentence is not a longer number: the range still types both endpoints
        bad(self, {"cost": 100, "recs": 200}, "월 $100–200.")
        ok(self, {"cost": [100, 200]}, "월 $100–200.")
        ok(self, {"cost": [1.2e6, 1.5e6]}, "$1.2–1.5M.")
        ok(self, {"cost": [1.2e6, 1.5e6]}, "$1.2–1.5M, 검토 필요")
        bad(self, {"hourly": 1.2, "cost": 1.5e6}, "$1.2–1.5M.")


class CountRange(unittest.TestCase):
    def test_count_range(self):
        src = {"recs": [3, 5]}
        ok(self, src, "3~5건")
        bad(self, {"recs": 3, "coverage_pct": 5}, "3~5건")

    def test_dash_separators(self):
        # the first endpoint is a count too: a monetary 3 must not authorize it through the bare pool
        for t in ("3–5건", "3—5건", "3-5건", "3 – 5건"):
            ok(self, {"recs": [3, 5]}, t)
            bad(self, {"cost": 3, "recs": 5}, t)


class LeadingDecimal(unittest.TestCase):
    def test_leading_point_is_rejected(self):
        bad(self, {"coverage_pct": 5}, "커버리지 .5%")
        ok(self, {"coverage_pct": 0.5}, "커버리지 0.5%")
        ok(self, {"instance_type": "c7i.8xlarge"}, "c7i.8xlarge 유형")


if __name__ == "__main__":
    unittest.main()
