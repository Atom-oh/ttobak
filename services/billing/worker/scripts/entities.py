"""Entities of one pipeline run and their display names.

ENTITIES: $COT_ENTITIES (comma list) when set, else the sorted dir names under $COT_CSV_DIR.
NAMES: {id: name} from the JSON file at $COT_NAMES; an id without a name shows as itself.
"""
import json, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import cotload  # noqa: E402


class _Names(dict):
    def __missing__(self, key):
        return key


def _entities():
    env = [e.strip() for e in os.environ.get("COT_ENTITIES", "").split(",") if e.strip()]
    if env:
        return env
    base = cotload.OUT
    if not os.path.isdir(base):
        return []
    return sorted(e for e in os.listdir(base) if os.path.isdir(os.path.join(base, e)) and not e.startswith("."))


def _names():
    p = os.environ.get("COT_NAMES")
    if not p or not os.path.exists(p):
        return _Names()
    with open(p, encoding="utf-8") as f:
        d = json.load(f)
    return _Names({str(k): str(v) for k, v in d.items() if v})


def present(rows):
    """Entities that have rows, in ENTITIES order first, then the rest sorted (dataset-only steps)."""
    have = {r["entity"] for r in rows if r.get("entity")}
    return [e for e in ENTITIES if e in have] + sorted(have - set(ENTITIES))


ENTITIES = _entities()
NAMES = _names()
