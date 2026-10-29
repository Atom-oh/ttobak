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


if __name__ == "__main__":
    unittest.main()
