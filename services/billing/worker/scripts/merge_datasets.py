"""Merge per-account dataset dirs (run_account.sh outputs) into one dashboard data dir.

Usage: python3 -I merge_datasets.py <out_dir> <in_dir>...            (in entity order)
       python3 -I merge_datasets.py --dashboard <out_json> <in_json>...
Row arrays are concatenated in the given order; datasets whose producer sorts by entity are re-sorted the
same way. whats_new (no entity) keeps the first row per id; raw_catalog entries are unioned per table with
their columns and byte size recomputed from the merged table. Files in out_dir that no input carries (e.g.
agent_notes.json) are left alone. Steps that read more than one account at once (the hand-written playbook,
briefing over its focus rows, the final cross-check) are re-run by the caller over the merged dir.
"""
import json, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import raw_export  # noqa: E402

# producers that sort their rows by entity first (accounts.py, ec2_types.py)
SORTED_BY_ENTITY = {"accounts", "account_resources", "ec2_type_growth"}


def _load(p):
    with open(p, encoding="utf-8") as f:
        return json.load(f)


def _dump(p, rows, compact=False):
    body = json.dumps(rows, ensure_ascii=False, separators=(",", ":") if compact else None)
    with open(p, "w", encoding="utf-8") as f:
        f.write(body)
    return len(body.encode())


def _by_id(parts):
    out, seen = [], set()
    for rows in parts:
        for r in rows:
            if r.get("id") not in seen:
                seen.add(r.get("id"))
                out.append(r)
    return out


def _catalog(parts, out_dir):
    merged = {}
    for rows in parts:
        for c in rows:
            m = merged.setdefault(c["id"], {**c, "rows_by_entity": {}})
            m["rows_by_entity"].update(c["rows_by_entity"])
    out = []
    for c in sorted(merged.values(), key=lambda c: c["sheet"]):
        p = os.path.join(out_dir, c["id"] + ".json")
        rows = _load(p) if os.path.exists(p) else []
        cols = raw_export.columns(rows)
        for col in cols:  # as raw_export: a string column holds strings only
            if col["type"] == "str":
                for r in rows:
                    if col["name"] in r and not isinstance(r[col["name"]], str):
                        r[col["name"]] = str(r[col["name"]])
        size = _dump(p, rows, compact=True)
        if size > raw_export.MAX_BYTES:
            sys.exit(f"{c['id']} is {size} bytes > {raw_export.MAX_BYTES}")
        out.append({**c, "columns": cols, "bytes": size})
    return out


def merge_dirs(out_dir, in_dirs):
    os.makedirs(out_dir, exist_ok=True)
    names = []
    for d in in_dirs:
        names += [f[:-5] for f in sorted(os.listdir(d)) if f.endswith(".json") and f[:-5] not in names]
    later = []
    for n in names:
        parts = [_load(os.path.join(d, n + ".json")) for d in in_dirs if os.path.exists(os.path.join(d, n + ".json"))]
        if not all(isinstance(p, list) for p in parts):
            sys.exit(f"{n}: not a row array, cannot merge")
        if n == "raw_catalog":
            later.append(parts)
            continue
        if n == "whats_new":
            rows = _by_id(parts)
        else:
            rows = [r for p in parts for r in p]
            if not all(isinstance(r, dict) and r.get("entity") for r in rows):
                sys.exit(f"{n}: rows without an entity, cannot merge")
            if n in SORTED_BY_ENTITY:
                rows.sort(key=lambda r: r["entity"])  # stable: keeps each producer's order inside an entity
        _dump(os.path.join(out_dir, n + ".json"), rows, compact=n.startswith("raw_"))
    for parts in later:  # after the raw tables it describes
        with open(os.path.join(out_dir, "raw_catalog.json"), "w", encoding="utf-8") as f:
            json.dump(_catalog(parts, out_dir), f, ensure_ascii=False, indent=1)
    return names


def merge_dashboard(out_json, in_jsons):
    docs = [_load(p) for p in in_jsons]
    months = docs[0]["months"] if docs else []
    if any(d["months"] != months for d in docs):
        print("merge_datasets: accounts have different billing periods; dashboard_data.json keeps the first", file=sys.stderr)
    ents = {}
    for d in docs:
        ents.update(d["entities"])
    with open(out_json, "w", encoding="utf-8") as f:
        json.dump({"months": months, "entities": ents}, f, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    a = sys.argv[1:]
    if len(a) >= 3 and a[0] == "--dashboard":
        merge_dashboard(a[1], a[2:])
    elif len(a) >= 2 and not a[0].startswith("--"):
        print("merged", len(merge_dirs(a[0], a[1:])), "datasets")
    else:
        sys.exit(__doc__)
