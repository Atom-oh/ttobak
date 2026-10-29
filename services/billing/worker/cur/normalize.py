"""Normalize AWS CUR exports (CUR 2.0 / Data Exports Parquet or gzip CSV, legacy CUR CSV) to one amortized Parquet.

detect(files) -> "cur2-parquet" | "cur2-csv" | "legacy-csv"
run(files, out_dir, prev_dir=None) -> {format, currency, first_day, last_day, months, rows}
Writes out_dir/part-0.parquet: usage_date, month, payer_account, linked_account, service, usage_type, region,
resource_id, line_item_type, cost (amortized), unblended, currency, tags MAP(VARCHAR,VARCHAR), billing_period
(YYYY-MM of the bill the row came from; it can differ from month for late-arriving line items). With prev_dir, the
billing periods the new files cover replace the carried-over rows of those periods; other carried rows are kept.
Files are explicit paths (never globs). DuckDB runs in memory, limited to the directories involved, with no
extension autoinstall/autoload. Raises CurRejected(reason) for input that cannot be used.
"""
import os, shutil, sys, tempfile

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "scripts"))  # -I drops it
try:  # one malloc arena: DuckDB otherwise reserves ~1 GB of address space per process, which breaks under ulimit -v
    import ctypes
    ctypes.CDLL("libc.so.6").mallopt(-8, 1)  # M_ARENA_MAX
except Exception:  # not glibc: nothing to do
    pass
import duckdb  # noqa: E402
from safezip import show  # noqa: E402

# bill_billing_period_start_date binds rows to the delivery that covers them (see run()); AWS writes it in every
# CUR 2.0 and legacy CUR export, so an export without it is rejected rather than merged by usage month
REQUIRED = ("line_item_line_item_type", "line_item_unblended_cost", "line_item_usage_start_date",
            "line_item_currency_code", "bill_billing_period_start_date")
# CUR 2.0 name -> alternative spellings (legacy CUR CSV, legacy Parquet); the CUR 2.0 name is tried first.
ALIASES = {
    "line_item_line_item_type": ["lineItem/LineItemType"],
    "line_item_unblended_cost": ["lineItem/UnblendedCost"],
    "line_item_usage_start_date": ["lineItem/UsageStartDate"],
    "line_item_currency_code": ["lineItem/CurrencyCode"],
    "bill_billing_period_start_date": ["bill/BillingPeriodStartDate"],
    "line_item_product_code": ["lineItem/ProductCode"],
    "product_servicecode": ["product/servicecode"],
    "line_item_usage_type": ["lineItem/UsageType"],
    "product_region_code": ["product_region", "product/region", "product/regionCode"],
    "line_item_resource_id": ["lineItem/ResourceId"],
    "bill_payer_account_id": ["bill/PayerAccountId"],
    "line_item_usage_account_id": ["lineItem/UsageAccountId"],
    "reservation_effective_cost": ["reservation/EffectiveCost"],
    "savings_plan_savings_plan_effective_cost": ["savingsPlan/SavingsPlanEffectiveCost"],
    "reservation_unused_amortized_upfront_fee_for_billing_period":
        ["reservation/UnusedAmortizedUpfrontFeeForBillingPeriod"],
    "reservation_unused_recurring_fee": ["reservation/UnusedRecurringFee"],
    "savings_plan_total_commitment_to_date": ["savingsPlan/TotalCommitmentToDate"],
    "savings_plan_used_commitment": ["savingsPlan/UsedCommitment"],
    "resource_tags": [],
}
TAG_PREFIXES = ("resource_tags_", "resourceTags/")  # flat columns; the prefix is dropped, the rest is the key
COST_COLS = ("line_item_unblended_cost", "reservation_effective_cost", "savings_plan_savings_plan_effective_cost",
             "reservation_unused_amortized_upfront_fee_for_billing_period", "reservation_unused_recurring_fee",
             "savings_plan_total_commitment_to_date", "savings_plan_used_commitment")
# the cost inputs each amortized line item type needs: a row of that type without them would publish as zero cost
TYPE_COLS = {"DiscountedUsage": ("reservation_effective_cost",),
             "SavingsPlanCoveredUsage": ("savings_plan_savings_plan_effective_cost",),
             "RIFee": ("reservation_unused_amortized_upfront_fee_for_billing_period", "reservation_unused_recurring_fee"),
             "SavingsPlanRecurringFee": ("savings_plan_total_commitment_to_date", "savings_plan_used_commitment")}
# always populated on rows of that type: a blank one (a file without the column, read in a union) is a bad row
ROW_COST = {"Usage": "line_item_unblended_cost", "DiscountedUsage": "reservation_effective_cost",
            "SavingsPlanCoveredUsage": "savings_plan_savings_plan_effective_cost"}
MEMORY_LIMIT = "8GB"  # tests lower these
THREADS = 4
MAX_UNCOMPRESSED = 20 * 2 ** 30  # bytes per upload: Parquet footers' uncompressed size + plain and gunzipped CSV
MAX_ROWS = 2000000000
BAD_ROW_PERMILLE = 1  # in-scope rows that may be unreadable: 0.1%
EMPTY_TAGS = "CAST(MAP {} AS MAP(VARCHAR,VARCHAR))"
OUT_NAME = "part-0.parquet"


class CurRejected(Exception):
    def __init__(self, reason):
        super().__init__(reason)
        self.reason = reason


def _lit(s):
    return "'" + str(s).replace("'", "''") + "'"


def trim_ws(expr):
    """SQL: expr without leading/trailing whitespace (TRIM removes spaces only)."""
    return "regexp_replace(%s, '^\\s+|\\s+$', '', 'g')" % expr


def _q(name):
    return '"' + str(name).replace('"', '""') + '"'


def _label(labels, path):
    base = os.path.basename(path)
    return show((labels or {}).get(base, base))


def _list(paths):
    paths = [os.path.abspath(p) for p in paths]  # allowed_directories compares absolute paths
    for p in paths:
        if any(ch in os.path.basename(p) for ch in "*?["):  # DuckDB expands globs inside a path list
            raise CurRejected("CUR 파일 이름이 올바르지 않습니다")
    return "[" + ", ".join(_lit(p) for p in paths) + "]"


def _kind(files):
    exts = {os.path.splitext(f)[1].lower() for f in files}
    if not files:
        raise CurRejected("CUR 파일을 찾지 못했습니다")
    if exts == {".parquet"}:
        return "parquet"
    if ".parquet" in exts:
        raise CurRejected("CUR 파일 형식이 섞여 있습니다 (Parquet과 CSV)")
    return "csv"


def _connect(files, extra_dirs, tmp):
    os.makedirs(tmp, exist_ok=True)
    c = duckdb.connect()
    c.execute("SET memory_limit=" + _lit(MEMORY_LIMIT))
    c.execute("SET threads=%d" % THREADS)
    c.execute("SET TimeZone='UTC'")
    c.execute("SET temp_directory=" + _lit(tmp))
    c.execute("SET autoinstall_known_extensions=false")
    c.execute("SET autoload_known_extensions=false")
    dirs = sorted({os.path.dirname(os.path.abspath(f)) for f in files} | {os.path.abspath(d) for d in extra_dirs}
                  | {os.path.abspath(tmp)})
    c.execute("SET allowed_directories=" + "[" + ", ".join(_lit(d) for d in dirs) + "]")
    c.execute("SET enable_external_access=false")  # only the allowed directories stay reachable
    c.execute("SET lock_configuration=true")
    return c


connect = _connect  # reused by check.py / datasets.py: same limits, no autoload, no access outside the given directories


def open_norm(norm_dir):
    """(connection, tmp dir to remove) for reading norm_dir/part-*.parquet with the standard lockdown."""
    norm_dir = os.path.abspath(norm_dir)
    tmp = tempfile.mkdtemp(prefix="duckdb_tmp_", dir=os.path.dirname(norm_dir))
    return _connect([], [norm_dir], tmp), tmp


def _source(files, kind):
    if kind == "parquet":
        return "read_parquet(%s, union_by_name=true)" % _list(files)
    return ("read_csv(%s, header=true, delim=',', quote='\"', escape='\"', all_varchar=true, union_by_name=true)"
            % _list(files))


def _columns(con, src):
    cur = con.execute("DESCRIBE SELECT * FROM " + src)
    return {r[0]: r[1] for r in cur.fetchall()}


def check_readable(files, work, labels=None):
    """Each file must open as Parquet / CSV (the reason names the first that does not). Returns
    {"bytes": uncompressed Parquet bytes from the footers, "rows": Parquet rows}; rejects above the caps."""
    tmp = os.path.join(work, "duckdb_tmp")
    total_bytes = total_rows = 0
    for f in files:
        kind = "parquet" if f.lower().endswith(".parquet") else "csv"
        con = _connect([f], [], tmp)
        try:
            con.execute("SELECT * FROM %s LIMIT 0" % _source([f], kind)).fetchall()
            # run() reads the files as one union schema, where a file without a required column reads NULL there;
            # without line_item_line_item_type its rows would fall out of scope silently, not count as bad rows
            res = _resolve(_columns(con, _source([f], kind)))
            missing = [c for c in REQUIRED if c not in res]
            if missing:
                raise CurRejected("필수 열 누락: %s (%s)" % (", ".join(missing), _label(labels, f)))
            if kind == "parquet":
                b = con.execute("SELECT COALESCE(sum(total_uncompressed_size), 0) FROM parquet_metadata(%s)"
                                % _list([f])[1:-1]).fetchone()[0]
                r = con.execute("SELECT COALESCE(sum(num_rows), 0) FROM parquet_file_metadata(%s)"
                                % _list([f])[1:-1]).fetchone()[0]
                total_bytes += int(b)
                total_rows += int(r)
        except duckdb.Error:
            raise CurRejected("CUR 파일을 읽을 수 없습니다: " + _label(labels, f))
        finally:
            con.close()
        if total_bytes > MAX_UNCOMPRESSED or total_rows > MAX_ROWS:
            raise CurRejected("CUR 파일이 너무 큽니다")
    return {"bytes": total_bytes, "rows": total_rows}


def _detect(cols, kind):
    if kind == "parquet":
        return "cur2-parquet"
    if "line_item_unblended_cost" in cols:
        return "cur2-csv"
    if "lineItem/UnblendedCost" in cols:
        return "legacy-csv"
    return "cur2-csv"  # neither marker: the missing-column check reports it


def detect(files, work=None, labels=None):
    kind = _kind(files)
    own = work is None
    tmp = tempfile.mkdtemp(prefix="cur_detect_") if own else os.path.join(work, "duckdb_tmp")
    con = _connect(files, [], tmp)
    try:
        try:
            cols = _columns(con, _source(files, kind))
        except duckdb.Error:
            raise CurRejected("CUR 파일을 읽을 수 없습니다: " + _label(labels, files[0]))
        return _detect(cols, kind)
    finally:
        con.close()
        if own:
            shutil.rmtree(tmp, ignore_errors=True)


def _resolve(cols):
    """{CUR 2.0 name: actual column name} for every known column present."""
    out = {}
    for name, alts in ALIASES.items():
        for cand in [name] + alts:
            if cand in cols:
                out[name] = cand
                break
    return out


def _tags_expr(cols, res):
    parts = []
    if "resource_tags" in res:
        c = _q(res["resource_tags"])
        if cols[res["resource_tags"]].upper().startswith("MAP"):
            parts.append("CAST(%s AS MAP(VARCHAR,VARCHAR))" % c)
        else:  # CUR 2.0 CSV carries the tags as one JSON object per row
            parts.append("CASE WHEN json_valid(%s) AND json_type(%s) = 'OBJECT' "
                         "THEN json_transform(%s, '\"MAP(VARCHAR,VARCHAR)\"') END" % (c, c, c))
    flat = []
    for name in cols:
        for pre in TAG_PREFIXES:
            if name.startswith(pre) and len(name) > len(pre):
                flat.append("{'k': %s, 'v': CAST(%s AS VARCHAR)}" % (_lit(name[len(pre):]), _q(name)))
                break
    if flat:
        parts.append("map_from_entries(list_filter([%s], x -> x.v IS NOT NULL AND x.v <> ''))" % ", ".join(flat))
    parts = ["COALESCE(%s, %s)" % (p, EMPTY_TAGS) for p in parts]
    if not parts:
        return EMPTY_TAGS
    expr = parts[0]
    for p in parts[1:]:
        expr = "map_concat(%s, %s)" % (expr, p)
    return "map_from_entries(list_filter(map_entries(%s), e -> e.value IS NOT NULL AND e.value <> ''))" % expr


def _parts(cols, src):
    """(base SQL, has identity columns). base: in-scope rows with the output columns plus bad (date or cost unreadable),
    ident_id, ident_iv."""
    # the files are read as one union schema and each field resolves to one spelling: with both a CUR 2.0 and a
    # legacy spelling present, the other dialect's rows would read as empty and drop out of scope silently
    if any(c in cols for c in REQUIRED) and any(a in cols for c in REQUIRED for a in ALIASES[c]):
        raise CurRejected("CUR 형식이 섞여 있습니다 (레거시 CUR와 CUR 2.0)")
    res = _resolve(cols)
    missing = [c for c in REQUIRED if c not in res]
    if missing:
        raise CurRejected("필수 열 누락: " + ", ".join(missing))

    def s(name):  # text column, '' when absent or NULL
        return "COALESCE(CAST(%s AS VARCHAR), '')" % _q(res[name]) if name in res else "''"

    def n(name):  # numeric column, 0 when absent or unparsable
        return "COALESCE(TRY_CAST(%s AS DOUBLE), 0)" % _q(res[name]) if name in res else "0.0"

    ltype, product = s("line_item_line_item_type"), s("line_item_product_code")
    cost = ("CASE %s WHEN 'Usage' THEN %s WHEN 'DiscountedUsage' THEN %s WHEN 'SavingsPlanCoveredUsage' THEN %s "
            "WHEN 'RIFee' THEN %s + %s WHEN 'SavingsPlanRecurringFee' THEN %s - %s END" % (
                ltype, n("line_item_unblended_cost"), n("reservation_effective_cost"),
                n("savings_plan_savings_plan_effective_cost"),
                n("reservation_unused_amortized_upfront_fee_for_billing_period"), n("reservation_unused_recurring_fee"),
                n("savings_plan_total_commitment_to_date"), n("savings_plan_used_commitment")))
    day = "TRY_CAST(substr(CAST(%s AS VARCHAR), 1, 10) AS DATE)" % _q(res["line_item_usage_start_date"])
    period = ("strftime(TRY_CAST(substr(CAST(%s AS VARCHAR), 1, 10) AS DATE), '%%Y-%%m')"
              % _q(res["bill_billing_period_start_date"]))
    badcost = " OR ".join(["(%(c)s IS NOT NULL AND TRIM(CAST(%(c)s AS VARCHAR)) <> '' AND TRY_CAST(%(c)s AS DOUBLE) IS NULL)"
                           % {"c": _q(res[c])} for c in COST_COLS if c in res]
                          + ["(%s = %s AND NULLIF(TRIM(CAST(%s AS VARCHAR)), '') IS NULL)" % (ltype, _lit(t), _q(res[c]))
                             for t, c in ROW_COST.items() if c in res])
    ident = "identity_line_item_id" in cols and "identity_time_interval" in cols
    service = "COALESCE(NULLIF(%s, ''), %s)" % (s("product_servicecode"), product)
    sql = """
        SELECT %(day)s AS usage_date, %(payer)s AS payer_account, %(linked)s AS linked_account,
               %(service)s AS service, %(utype)s AS usage_type, %(region)s AS region, %(rid)s AS resource_id,
               %(ltype)s AS line_item_type, %(cost)s AS cost, %(unb)s AS unblended,
               %(cur)s AS currency, %(tags)s AS tags, %(period)s AS billing_period,
               (%(day)s IS NULL OR %(period)s IS NULL OR %(badcost)s) AS bad,
               %(iid)s AS ident_id, %(iiv)s AS ident_iv, file_index AS ident_file, %(frow)s AS ident_row
        FROM %(src)s
        WHERE %(ltype)s IN ('Usage', 'DiscountedUsage', 'SavingsPlanCoveredUsage', 'RIFee', 'SavingsPlanRecurringFee')
          AND NOT (%(product)s LIKE 'AWSSupport%%' OR %(product)s = 'OCBPremiumSupport')
    """ % {"day": day, "period": period, "payer": s("bill_payer_account_id"), "linked": s("line_item_usage_account_id"),
           "service": service, "utype": s("line_item_usage_type"), "region": s("product_region_code"),
           "rid": s("line_item_resource_id"), "ltype": ltype, "cost": cost, "unb": n("line_item_unblended_cost"),
           "cur": trim_ws(s("line_item_currency_code")), "tags": _tags_expr(cols, res), "src": src, "product": product,
           "badcost": badcost or "FALSE",
           # row order inside a file: Parquet only (the CSV reader has no file_row_number)
           "frow": "file_row_number" if src.startswith("read_parquet(") else "0",
           "iid": "COALESCE(CAST(%s AS VARCHAR), '')" % _q("identity_line_item_id") if ident else "''",
           "iiv": "COALESCE(CAST(%s AS VARCHAR), '')" % _q("identity_time_interval") if ident else "''"}
    return sql, ident


OUT_COLS = ("usage_date, strftime(usage_date, '%Y-%m') AS month, payer_account, linked_account, service, usage_type, "
            "region, resource_id, line_item_type, cost, unblended, currency, tags, billing_period")


def _normalized_sql(cols, src):
    """(normalized SQL: bad rows out, one row per (identity_line_item_id, identity_time_interval) when present;
    SQL for (in-scope rows, bad rows))."""
    base, ident = _parts(cols, src)
    stats = "SELECT count(*), count(*) FILTER (WHERE bad) FROM (%s)" % base
    if ident:
        # repeated ids within one source folder (run() refuses a bill spread over folders): the later file in path
        # order wins, then the later row in it; for CSV, whose rows have no file_row_number, a content order keeps
        # the choice deterministic
        norm = ("SELECT %s FROM (SELECT *, row_number() OVER (PARTITION BY ident_id, ident_iv ORDER BY ident_file DESC, "
                "ident_row DESC, cost DESC, unblended DESC, usage_type, resource_id, CAST(tags AS VARCHAR)) AS rn FROM (%s) "
                "WHERE NOT bad) WHERE ident_id = '' OR rn = 1" % (OUT_COLS, base))
    else:
        norm = "SELECT %s FROM (%s) WHERE NOT bad" % (OUT_COLS, base)
    return norm, stats


def _check_type_cols(con, cols, sql):
    """Rows of an amortized line item type need that type's cost columns in the schema."""
    res = _resolve(cols)
    need = {t: [c for c in cs if c not in res] for t, cs in TYPE_COLS.items()}
    need = {t: cs for t, cs in need.items() if cs}
    if not need:
        return
    present = {r[0] for r in con.execute("SELECT DISTINCT line_item_type FROM (%s) WHERE line_item_type IN (%s)"
                                         % (sql, ", ".join(_lit(t) for t in need))).fetchall()}
    missing = ["%s (%s 행)" % (c, t) for t in TYPE_COLS if t in present for c in need[t]]
    if missing:
        raise CurRejected("필수 열 누락: " + ", ".join(missing))


def _currency_reason(currencies):
    return "통화가 여러 개입니다: " + ", ".join(show(c, 12) for c in sorted(currencies)[:10])


def _check_folders(con, files, kind, groups, labels):
    """One bill (billing_period, the key run() replaces carried rows by) must not come from files in more than one
    source folder (two report versions or assemblies of it); a late line item of a later bill dated in an earlier
    month is not a second copy of the earlier bill. Identity columns do not make this safe: line item ids are not stable across reports, and assembly
    folder names do not sort by age, so neither dedup nor 'the later folder wins' can pick the right copy."""
    by_folder = {}
    for f in files:
        by_folder.setdefault(groups.get(os.path.basename(f), ""), []).append(f)
    if len(by_folder) < 2:
        return
    seen, dup = {}, set()
    for folder in sorted(by_folder):
        src = _source(by_folder[folder], kind)
        norm, _ = _normalized_sql(_columns(con, src), src)
        for (m,) in con.execute("SELECT DISTINCT billing_period FROM (%s)" % norm).fetchall():
            if m in seen and seen[m] != folder:
                dup.add(m)
            seen.setdefault(m, folder)
    if dup:
        raise CurRejected("같은 달의 CUR가 여러 폴더에 있습니다: " + ", ".join(sorted(dup)))


def run(files, out_dir, prev_dir=None, labels=None, groups=None):
    """labels: {basename: name to show}; groups: {basename: source folder in the upload}."""
    files = list(files)
    kind = _kind(files)
    out_dir = os.path.abspath(out_dir)
    prev_file = os.path.join(prev_dir, OUT_NAME) if prev_dir else None
    if prev_file and not os.path.isfile(prev_file):
        prev_file = None
    tmp = os.path.join(os.path.dirname(out_dir), "duckdb_tmp")
    os.makedirs(out_dir, exist_ok=True)
    con = _connect(files, [out_dir] + ([os.path.dirname(os.path.abspath(prev_file))] if prev_file else []), tmp)
    unreadable = "CUR 파일을 읽을 수 없습니다: " + _label(labels, files[0])
    try:
        src = _source(files, kind)
        try:
            cols = _columns(con, src)
        except duckdb.Error:
            raise CurRejected(unreadable)
        fmt = _detect(cols, kind)
        sql, stats = _normalized_sql(cols, src)
        try:
            _check_type_cols(con, cols, sql)
            if groups:
                _check_folders(con, files, kind, groups, labels)
            total, bad = con.execute(stats).fetchone()
            if bad * 1000 > total * BAD_ROW_PERMILLE:
                raise CurRejected("CUR 행 중 날짜·금액을 읽을 수 없는 행이 많습니다: %d" % bad)
            # the bills this delivery covers, not its usage months: a late line item dated in an earlier month
            # belongs to the later bill and must not displace the earlier bill's carried-over rows
            periods = [r[0] for r in con.execute("SELECT DISTINCT billing_period FROM (%s)" % sql).fetchall()]
        except duckdb.Error:
            raise CurRejected(unreadable)
        if not periods:
            raise CurRejected("집계할 CUR 행이 없습니다")
        full = "SELECT * FROM (%s)" % sql
        if prev_file:
            prev_src = "read_parquet(%s)" % _lit(os.path.abspath(prev_file))
            # a carried file written before billing_period existed (or whose rows carry it as NULL): its usage
            # months stand in for its bills, and are written out as billing_period so later hops keep the rows
            carried = ("SELECT * REPLACE (COALESCE(billing_period, month) AS billing_period) FROM %s"
                       if "billing_period" in _columns(con, prev_src) else "SELECT *, month AS billing_period FROM %s")
            full += (" UNION ALL BY NAME SELECT * FROM (%s) WHERE billing_period NOT IN (%s)"
                     % (carried % prev_src, ", ".join(_lit(m) for m in periods)))
        cur = [r[0] for r in con.execute("SELECT DISTINCT %s AS c FROM (%s) WHERE NULLIF(%s, '') IS NOT NULL"
                                         % (trim_ws("currency"), full, trim_ws("currency"))).fetchall()]
        if len(cur) > 1:
            raise CurRejected(_currency_reason(cur))
        target = os.path.join(out_dir, OUT_NAME)
        part = target + ".tmp"
        con.execute("COPY (%s) TO %s (FORMAT parquet)" % (full, _lit(part)))
        os.replace(part, target)
        lo, hi, rows = con.execute("SELECT min(usage_date), max(usage_date), count(*) FROM read_parquet(%s)"
                                   % _lit(target)).fetchone()
        months = [r[0] for r in con.execute("SELECT DISTINCT month FROM read_parquet(%s) ORDER BY month"
                                            % _lit(target)).fetchall()]
        return {"format": fmt, "currency": cur[0] if cur else "", "first_day": lo.isoformat(),
                "last_day": hi.isoformat(), "months": months, "rows": rows, "dropped_rows": bad}
    finally:
        con.close()
        shutil.rmtree(tmp, ignore_errors=True)
