"""Raw COT sheets for the BI explorer: every 'Data *' sheet as one flat JSON table.

Usage: python3 -I raw_export.py <csv_out_dir> <dashboard_data_dir>
  csv_out_dir/<Entity>/Data_*.csv (from xlsx2csv.py)
Writes raw_<slug>.json per sheet and raw_catalog.json into dashboard_data_dir.

Each sheet has an API block (Cost Explorer / COH fields, starting at column 5) and,
after a gap, Excel helper columns. Row-aligned helper columns (e.g. account Name,
Data Transfer Type, Family/Size/Generation) are kept; summary tables that only fill a
few rows are not. Rows: {entity, month?, dim, cost?, usage?, usage_unit?, <source columns>}.
"""
import ast, collections, csv, datetime, json, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
from entities import ENTITIES  # noqa: E402
MAX_BYTES = 6 * 1024 * 1024

TITLES = {  # sheet -> (title_ko, description_ko, dim_label)
    "Data_Service_Costs": ("서비스별 비용", "AWS 서비스 × 월 Amortized 비용", "Service"),
    "Data_Linked_Account_Costs": ("연결 계정별 비용", "연결 계정 × 월 Amortized 비용, 계정 이름 포함", "Linked Account"),
    "Data_Amortized_Costs": ("비용 구성(Amortized)", "Record type(사용·약정·할인 등) × 월 Amortized 비용", "Record type"),
    "Data_Unblended_Costs": ("비용 구성(Unblended)", "Record type × 월 Unblended 비용", "Record type"),
    "Data_On_Demand_Usage": ("온디맨드 인스턴스 사용", "인스턴스 유형 × 월 온디맨드 비용·시간", "Instance type"),
    "Data_Savings_Plan_Usage": ("Savings Plans 인스턴스 사용", "인스턴스 유형 × 월 SP 적용 비용·시간", "Instance type"),
    "Data_Reserved_Instance_Usage": ("RI 인스턴스 사용", "인스턴스 유형 × 월 RI 적용 비용·시간 (RDS·ElastiCache 포함)", "Instance type"),
    "Data_Spot_Usage": ("Spot 인스턴스 사용", "인스턴스 유형 × 월 Spot 비용·시간", "Instance type"),
    "Data_EC2_Running_Hours": ("EC2 구매 옵션별 시간", "구매 옵션 × 월 EC2 비용·실행 시간", "Purchase option"),
    "Data_RDS_Running_Hours": ("RDS 구매 옵션별 시간", "구매 옵션 × 월 RDS 비용·실행 시간", "Purchase option"),
    "Data_Data_Transfer": ("데이터 전송", "Usage type(구간) × 월 전송 비용·GB", "Usage type"),
    "Data_Storage": ("스토리지", "Usage type × 월 스토리지 비용·사용량", "Usage type"),
    "Data_EC2_RI_Coverage": ("EC2 RI 커버리지", "인스턴스 유형·리전 × 월 RI 커버 시간과 온디맨드 비용", "Instance type"),
    "Data_RDS_RI_Coverage": ("RDS RI 커버리지", "인스턴스 유형·엔진 × 월 RI 커버리지", "Instance type"),
    "Data_EC_RI_Coverage": ("ElastiCache RI 커버리지", "노드 유형·엔진 × 월 RI 커버리지", "Instance type"),
    "Data_OS_RI_Coverage": ("OpenSearch RI 커버리지", "인스턴스 유형 × 월 RI 커버리지", "Instance type"),
    "Data_EC2_SP_Coverage": ("SP 커버리지", "인스턴스 패밀리·서비스·리전 × 월 SP 커버리지", "Instance family"),
    "Data_EC2_SP_Utilization": ("SP 사용률", "월별 SP 약정 사용률·순절감", "Month"),
    "Data_SP_Inventory": ("SP 보유 목록", "보유 Savings Plans별 약정액·만료일·사용률", "Savings Plan"),
    "Data_EC2_RI_Utilization": ("EC2 RI 사용률", "보유 EC2 RI별 사용률", "Reservation"),
    "Data_RDS_RI_Utilization": ("RDS RI 사용률", "보유 RDS RI별 사용률", "Reservation"),
    "Data_EC_RI_Utilization": ("ElastiCache RI 사용률", "보유 ElastiCache RI별 사용률", "Reservation"),
    "Data_OS_RI_Utilization": ("OpenSearch RI 사용률", "보유 OpenSearch RI별 사용률", "Reservation"),
    "Data_EC2_RI_Recommendation": ("EC2 RI 구매 권고", "계정·인스턴스 유형별 RI 구매 권고와 예상 절감", "Account"),
    "Data_RDS_RI_Recommendation": ("RDS RI 구매 권고", "RDS RI 구매 권고와 예상 절감", "Account"),
    "Data_EC_RI_Recommendation": ("ElastiCache RI 구매 권고", "ElastiCache RI 구매 권고와 예상 절감", "Account"),
    "Data_EC2_SP_Recommendation": ("SP 구매 권고", "Savings Plans 구매 권고와 예상 절감", "Account"),
    "Data_COH_All": ("COH 최적화 권고 리소스", "리소스별 권고(유형 변경·Graviton·중지 등), 월 비용·절감 추정, 태그", "Resource"),
}
DROP = {"Report Name", "AccountID", "Count", "estimated", "currencyCode", "metrics.AmortizedCost.unit",
        "metrics.UnblendedCost.unit", "lastRefreshTimestamp", "tags", "recommendationLookbackPeriodInDays"}
# Excel helper columns worth keeping: row-aligned, human-readable labels for the API keys.
HELPERS = {"Linked Account", "Name", "Account Name", "Data Transfer Type", "Data Transfer From", "Data Transfer To",
           "Region Code", "Region Name", "Storage Type", "Storage Option", "Instance Type", "Tenancy", "Region",
           "Platform", "Family", "Size", "Generation", "Latest Generation", "Modern Size", "Cache Engine",
           "Deployment option", "estimatedTotalDedupedSavings", "Action to Resource Clean", "Spend After Savings",
           "Time to Expiration", "Type", "Utilization", "Net Savings", "Missed Savings"}
STR_COL = re.compile(r"account|arn$|arn\b|leaseid|recommendationid|resourceid|offeringid|^key$|^value$", re.I)
DATE_COL = re.compile(r"date|start|end|expiration", re.I)


def blank(h):
    return not h.strip()


def blocks(hdr):
    """API block [5, end) and whitelisted helper columns after it (first occurrence of each name)."""
    end = len(hdr)
    for j in range(6, len(hdr)):
        if blank(hdr[j]) and (j + 1 >= len(hdr) or blank(hdr[j + 1])):
            end = j
            break
    api = list(range(5, end))
    api_names = {hdr[j].strip() for j in api}
    helper, seen = [], set()
    for j in range(end, len(hdr)):
        n = hdr[j].strip()
        if n in HELPERS and n not in seen and n not in api_names:
            seen.add(n)
            helper.append(j)
    return api, helper


def serial_to_date(v):
    try:
        f = float(v)
    except ValueError:
        return v[:10] if re.match(r"\d{4}-\d{2}-\d{2}", v) else v
    if 20000 < f < 80000:
        return (datetime.date(1899, 12, 30) + datetime.timedelta(days=int(f))).isoformat()
    return v


def month_of(v):
    d = serial_to_date(v)
    return d[:7] if re.match(r"\d{4}-\d{2}", d) else None


def parse_keys(v):
    try:
        x = ast.literal_eval(v)
        return " | ".join(str(i) for i in x) if isinstance(x, (list, tuple)) else str(x)
    except Exception:
        return v


def tags_of(v):
    try:
        return {t["key"]: t["value"] for t in ast.literal_eval(v)} if v not in ("", "[]", "nan") else {}
    except Exception:
        return {}


def load(base, entity, sheet):
    p = os.path.join(base, entity, sheet + ".csv")
    if not os.path.exists(p):
        return None, None
    rows = list(csv.reader(open(p, encoding="utf-8")))
    hi = next((i for i, r in enumerate(rows[:10]) if len(r) > 2 and r[1] == "Report Name"), None)
    if hi is None:
        return None, None
    return rows[hi], [r for r in rows[hi + 1:] if len(r) > 2 and r[1].strip() == entity]


def to_num(v):
    try:
        f = float(v)
        return int(f) if f.is_integer() and abs(f) < 1e15 and "." not in v else f
    except ValueError:
        return None


def export_sheet(base, sheet):
    out, rows_by_entity, skipped, dropped_tags = [], {}, [], {}
    for e in ENTITIES:
        hdr, data = load(base, e, sheet)
        if hdr is None:
            skipped.append(e)
            continue
        api, helper = blocks(hdr)
        # keep a helper column only if it is filled on (nearly) every data row, i.e. row-aligned
        # (blank is allowed where a label does not apply, e.g. Storage Option, so the bar is 40%)
        helper = [j for j in helper if data and sum(1 for r in data if j < len(r) and r[j].strip()) >= 0.4 * len(data)
                  and hdr[j].strip() not in ("Linked Account", "Name")]
        # Linked Account / Name is a lookup list (one row per account), not row-aligned: join it on the account id
        acct_name = {}
        if "Linked Account" in [h.strip() for h in hdr] and "Name" in [h.strip() for h in hdr]:
            la, nm = [h.strip() for h in hdr].index("Linked Account"), [h.strip() for h in hdr].index("Name")
            acct_name = {re.sub(r"\D", "", r[la]): r[nm].strip() for r in data if nm < len(r) and r[nm].strip()}
        names, seen = {}, set()
        for j in api + helper:  # noqa: helper columns renamed on clash with API names
            n = hdr[j].strip()
            if not n:
                continue
            if n in seen:
                n = n + " (calc)"
            seen.add(n)
            names[j] = n
        si = next((j for j, n in names.items() if n == "timePeriod.start"), None)
        starts = {serial_to_date(r[si]) for r in data if si is not None and si < len(r)}
        daily = len(starts) > len({d[:7] for d in starts}) + 1  # day-level rows (e.g. running hours)
        tag_keys = []
        if sheet == "Data_COH_All":
            ti = next(j for j, n in names.items() if n == "tags")
            cnt = collections.Counter(k for r in data for k in tags_of(r[ti]))
            tag_keys = [k for k, c in cnt.most_common() if c >= 0.01 * len(data)]
            dropped_tags[e] = [k for k in cnt if k not in tag_keys]
        for r in data:
            row = {"entity": e}
            cells = {n: (r[j].strip() if j < len(r) else "") for j, n in names.items()}
            if "timePeriod.start" in cells:
                m = month_of(cells["timePeriod.start"])
                if m:
                    row["month"] = m
                    if daily:
                        row["date"] = serial_to_date(cells["timePeriod.start"])
            if "keys" in cells:
                row["dim"] = parse_keys(cells["keys"])
                if acct_name.get(row["dim"]):
                    row["account_name"] = acct_name[row["dim"]]
            else:
                for c in ("attributes.instanceType", "attributes.INSTANCE_TYPE_FAMILY", "currentResourceSummary",
                          "savingsPlanArn", "key", "accountId"):
                    if cells.get(c):
                        row["dim"] = cells[c]
                        break
            for src, dst in (("metrics.AmortizedCost.amount", "cost"), ("metrics.UnblendedCost.amount", "cost"),
                             ("metrics.UsageQuantity.amount", "usage")):
                if src in cells and cells[src] != "":
                    row[dst] = to_num(cells[src])
            if cells.get("metrics.UsageQuantity.unit"):
                row["usage_unit"] = cells["metrics.UsageQuantity.unit"]
            for n, v in cells.items():
                if n in DROP or n.startswith("metrics.") or n in ("keys", "timePeriod.start", "timePeriod.end") or v == "":
                    continue
                if STR_COL.search(n):
                    row[n] = v
                elif DATE_COL.search(n) and to_num(v) is not None and 20000 < float(v) < 80000:
                    row[n] = serial_to_date(v)
                else:
                    x = to_num(v)
                    row[n] = x if x is not None else v
            if tag_keys:
                tg = tags_of(cells.get("tags", ""))
                for k in tag_keys:
                    if tg.get(k):
                        row["tag:" + k] = tg[k]
            out.append(row)
        rows_by_entity[e] = len(data)
    return out, rows_by_entity, skipped, dropped_tags


def columns(rows):
    order, kinds = [], {}
    for r in rows:
        for k, v in r.items():
            if k not in kinds:
                order.append(k)
                kinds[k] = collections.Counter()
            kinds[k]["num" if isinstance(v, (int, float)) else "str"] += 1
    cols = []
    for k in order:
        t = "month" if k == "month" else ("num" if kinds[k]["num"] >= kinds[k]["str"] else "str")
        cols.append({"name": k, "type": t})
    return cols


def main(base, out_dir):
    sheets = sorted(f[:-4] for f in os.listdir(os.path.join(base, ENTITIES[0])) if f.startswith("Data_") and f.endswith(".csv"))
    catalog, report = [], []
    for sheet in sheets:
        rows, by_ent, skipped, dropped_tags = export_sheet(base, sheet)
        if not rows:
            report.append(f"skip {sheet}: no header/rows in any entity")
            continue
        slug = re.sub(r"[^a-z0-9]+", "_", sheet.lower()).strip("_")
        slug = slug[len("data_"):] if slug.startswith("data_") else slug
        rid = "raw_" + slug
        cols = columns(rows)
        for c in cols:
            if c["type"] == "str":
                for r in rows:
                    if c["name"] in r and not isinstance(r[c["name"]], str):
                        r[c["name"]] = str(r[c["name"]])
        path = os.path.join(out_dir, rid + ".json")
        body = json.dumps(rows, ensure_ascii=False, separators=(",", ":"))
        if len(body.encode()) > MAX_BYTES:
            raise SystemExit(f"{rid} is {len(body.encode())} bytes > {MAX_BYTES}")
        open(path, "w", encoding="utf-8").write(body)
        t = TITLES.get(sheet, (sheet[5:].replace("_", " "), sheet, "dim"))
        catalog.append({"id": rid, "title_ko": t[0], "sheet": sheet, "description_ko": t[1], "dim_label": t[2],
                        "columns": cols, "rows_by_entity": by_ent, "bytes": len(body.encode())})
        note = f" skipped={skipped}" if skipped else ""
        if dropped_tags:
            note += " dropped_tags=" + json.dumps({k: len(v) for k, v in dropped_tags.items()})
        report.append(f"{rid}: {len(rows)} rows, {len(body.encode())} B, {len(cols)} cols{note}")
    json.dump(catalog, open(os.path.join(out_dir, "raw_catalog.json"), "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print("\n".join(report))
    print("total bytes", sum(c["bytes"] for c in catalog))


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
