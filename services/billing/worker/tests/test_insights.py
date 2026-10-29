"""insights.py: data-transfer explanations follow the computed rate and volume change. Synthetic data only."""
import os, sys, unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import insights  # noqa: E402

E = "acct-a"
PERIOD = [{"entity": E, "source": "cot", "months": ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]}]


def inter_az(cost_first, gb_first, cost_last, gb_last):
    dts = [{"entity": E, "category": "Inter-AZ", "month": "2026-04", "cost": cost_first, "gb": gb_first},
           {"entity": E, "category": "Inter-AZ", "month": "2026-09", "cost": cost_last, "gb": gb_last}]
    with mock.patch.object(insights, "PERIODS", PERIOD):
        out = insights.network_area(E, [], dts)
    return next(t for _, t in out if t.startswith("AZ 간"))


class InterAz(unittest.TestCase):
    def test_rate_increase_is_not_called_volume_growth(self):
        t = inter_az(3000, 300000, 6000, 300000)  # same volume, rate $0.01 -> $0.02
        self.assertIn("전송량 +0%", t)
        self.assertNotIn("단가는 그대로", t)
        self.assertNotIn("전송량 증가가 원인", t)

    def test_volume_growth_at_a_flat_rate(self):
        t = inter_az(3000, 300000, 6000, 600000)
        self.assertIn("단가는 그대로라 전송량 증가가 원인입니다.", t)


SHORT = [{"entity": E, "source": "cot", "months": ["2026-07", "2026-08", "2026-09"]}]


class ShortPeriodGrowth(unittest.TestCase):
    """Three months fill only the later half: constant spend is a level, not growth."""

    def test_constant_db_node_is_not_growth(self):
        types = [{"entity": E, "service": "RDS", "instance_type": "db.r6g.xlarge", "month": m, "cost": 1000.0}
                 for m in SHORT[0]["months"]]
        with mock.patch.object(insights, "PERIODS", SHORT):
            out = insights.data_area(E, [], types)
        self.assertFalse([t for sev, t in out if sev == "growth"], out)

    def test_db_node_growth_with_both_halves(self):
        types = [{"entity": E, "service": "RDS", "instance_type": "db.r6g.xlarge", "month": m, "cost": c}
                 for m, c in zip(PERIOD[0]["months"], (1000, 1000, 1000, 2000, 2000, 2000))]
        with mock.patch.object(insights, "PERIODS", PERIOD):
            out = insights.data_area(E, [], types)
        self.assertTrue(any("db.r6g.xlarge(RDS) +$1.0K/월" in t for sev, t in out if sev == "growth"), out)

    def test_constant_ec2_type_is_not_growth(self):
        ms = SHORT[0]["months"]
        mix = [{"entity": E, "month": m, "total": 5000.0, "by_arch": {}, "by_generation": {}, "by_purchase": {"OD": 5000.0},
                "gpu_cost": 0.0} for m in ms]
        for d in (None, 5000.0):  # ec2_types writes null; a level read as a change must not be reported either
            growth = [{"entity": E, "instance_type": "m6i.large", "d_h2_h1": d, "purchase_mix_last": {"OD": 1.0},
                       "is_new": False, "last": 5000.0}]
            with mock.patch.object(insights, "PERIODS", SHORT):
                out = insights.ec2(E, growth, mix)
            self.assertFalse([t for _, t in out if "가장 많이 늘어난" in t], out)

    def test_new_account_without_a_change(self):
        def acct(n, last):
            return {"entity": E, "account": n * 12, "name": "a" + n, "kind": "신규", "last": last, "share_last": 0.1,
                    "m4": 0.0, "m5": 0.0, "m6": last, "d_h2_h1": None, "pct_h2_h1": None}
        with mock.patch.object(insights, "PERIODS", SHORT):
            out = insights.accounts(E, [acct("1", 100.0), acct("2", 900.0)], [])
        self.assertEqual([t.split(":")[0] for sev, t in out if sev == "new"], ["신규 계정 a2(222222222222)",
                                                                              "신규 계정 a1(111111111111)"])


if __name__ == "__main__":
    unittest.main()
