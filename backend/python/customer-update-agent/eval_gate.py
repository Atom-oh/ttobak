"""Evaluate a decision-gate backend against exported labels (operator tool).

Usage: python3 eval_gate.py --labels labels.jsonl --backend luna|module:Class [--threshold 0.7]

Labels come from scripts/customer-updates/export_labels.py and contain customer
context: keep them on the operator's machine. This tool prints counts and
rates only, never label text.

Population bias: only decisions the live gate admitted (needed at 0.7 or above)
store their gate input, so every labelled pair already passed the live gate.
Precision is the meaningful number; recall is measured only among pairs the live
gate admitted and says nothing about articles it rejected.
"""

import argparse
import importlib
import json
import sys

import gate

MIN_PAIRS = 300
RECALL = "recall (among pairs the live gate admitted)"


def has_hangul(text):
    return any("가" <= ch <= "힣" for ch in text or "")


def load_labels(path):
    rows = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            if not line.strip():
                continue
            row = json.loads(line)
            if row.get("label") in (gate.NEEDED, gate.NOT_NEEDED):
                rows.append(row)
    return rows


def load_backend(spec):
    if spec == "luna":
        import llm

        agent = llm.build_agent("gate", gate.GATE_SYSTEM_PROMPT)
        return gate.LunaGate(lambda prompt, required: llm.ask_json(agent, prompt, required))
    module, sep, name = spec.partition(":")
    if not sep or not module or not name:
        raise SystemExit("backend must be luna or module:Class")
    return getattr(importlib.import_module(module), name)()


def predict(backend, row, threshold):
    """True when the backend says needed. Raises on a backend error."""
    options = [gate.Option(o["id"], o["text"]) for o in row["options"]]
    scores = {s.id: s.score for s in backend.score_options(row["question"], options, row["context"])}
    return gate.is_needed(scores.get(gate.NEEDED, 0.0), scores.get(gate.NOT_NEEDED, 0.0), threshold)


def rates(pairs):
    """pairs: (predicted_needed, labelled_needed). Returns (precision, recall); None when undefined."""
    tp = sum(1 for p, a in pairs if p and a)
    fp = sum(1 for p, a in pairs if p and not a)
    fn = sum(1 for p, a in pairs if not p and a)
    return (tp / (tp + fp) if tp + fp else None, tp / (tp + fn) if tp + fn else None)


def _fmt(value):
    return "n/a" if value is None else f"{value:.3f}"


def main(argv=None, out=None):
    out = out or sys.stdout
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--labels", required=True)
    ap.add_argument("--backend", required=True, help="luna or module:Class")
    ap.add_argument("--threshold", type=float, default=gate.NEEDED_THRESHOLD)
    args = ap.parse_args(argv)
    rows = load_labels(args.labels)
    print(f"n={len(rows)}", file=out)
    if len(rows) < MIN_PAIRS:
        print(f"need at least {MIN_PAIRS} labelled pairs", file=sys.stderr)
        return 1
    backend = load_backend(args.backend)
    results, errors = [], 0
    for r in rows:
        try:
            pred = predict(backend, r, args.threshold)
        except Exception:
            errors += 1  # production treats a gate error as not_needed
            pred = False
        results.append((pred, r["label"] == gate.NEEDED, has_hangul(r["context"])))
    print(f"errors={errors}", file=out)
    precision, recall = rates([(p, a) for p, a, _ in results])
    print(f"precision={_fmt(precision)} {RECALL}={_fmt(recall)}", file=out)
    korean = [(p, a) for p, a, k in results if k]
    kp, kr = rates(korean)
    print(f"korean n={len(korean)} precision={_fmt(kp)} {RECALL}={_fmt(kr)}", file=out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
