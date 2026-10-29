"""Generic SA-session inputs: the verified AWS What's New catalog (whats_new.json) and the focus dataset.
focus.json starts empty for every entity; a hand-written customer playbook (sessions_<customer>.py)
may fill focus.json and sessions.json afterwards. The helpers below are shared with such playbooks.
Usage: python -I sessions.py <dashboard_json> <outdir>"""
import json, os, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from build_data import hj, fl  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
# Verified against aws.amazon.com/about-aws/whats-new (AWS Knowledge MCP + web search); announcement dates are facts
# about the announcements, not the billing period
with open(os.path.join(HERE, "aws_whats_new.json"), encoding="utf-8") as _f:
    WN = json.load(_f)


def usd(x):
    return f"${x:,.0f}"


def svc(x, name):
    for lst in ("top_services", "growth_services", "decline_services", "new_services"):
        for s in x[lst]:
            if s["name"] == name:
                return s
    return None


def svc_all(e, name):
    """Months for any service, straight from the service-cost sheet."""
    import trend as T
    t = T.table(T.pivot(e, "Data_Service_Costs"))
    return t.get(name, {"months": [0] * 6})["months"]


def chg(v):
    return f"{usd(v[0])} → {usd(v[-1])}" + (f" ({(v[-1] / v[0] - 1) * 100:+.0f}%)" if v[0] else "")


def lambda_dt(e):
    j = hj(e, "Lambda_Costs") or {"rows": []}
    return [sum(fl(r[i]) for r in j["rows"] if "DataTransfer-Out" in str(r[0])) for i in range(1, 7)]


def main(src, outdir):
    json.load(open(src))  # the dataset must exist and parse, as for any playbook run after this step
    json.dump([{"id": k, **v} for k, v in WN.items()], open(os.path.join(outdir, "whats_new.json"), "w"), ensure_ascii=False)
    json.dump([], open(os.path.join(outdir, "focus.json"), "w"), ensure_ascii=False)
    print("whats_new", len(WN), "focus 0")


if __name__ == "__main__":
    main(*sys.argv[1:])
