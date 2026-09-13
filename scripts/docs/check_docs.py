#!/usr/bin/env python3
"""Check English documentation, generated context and source-backed inventories."""

from pathlib import Path
import hashlib
import re
import subprocess
import sys
from urllib.parse import unquote

from sync_review_context import BRIDGE, ROOT, render


# Verbatim model output is immutable test evidence, not authored English prose.
# Pin only these archived notes; README and every other Markdown file remain
# subject to the language/history checks. Changing a note must never look like
# translating documentation while silently falsifying the recorded evaluation.
RAW_EVIDENCE = {
    "docs/research/evaluations/2026-09-12-note-quality/notes-are-not-spoken-evidence.md":
        "b29fa7a414eb000eef2f228890d6ad2d7a2d5a6e48bab315ec92f0a0ed535403",
    "docs/research/evaluations/2026-09-12-note-quality/owners-and-deadlines.md":
        "5feb3c0ded2ed2e77b9a0b781b4b7bd634c391aed1dba1ffec3a2ab0912f6e92",
    "docs/research/evaluations/2026-09-12-note-quality/proposal-is-not-decision.md":
        "970b65f053dd8980ed50c9bbd1b77553fb7b27dc47c175a5aad1ea060f4b18e1",
    "docs/research/evaluations/2026-09-12-note-quality/selected-source-numbers-negation.md":
        "746865422fe48825617d2d82d08f4242716055351abd8b438fb83ff52c462ece",
}


def validate_evidence(root: Path) -> list[str]:
    errors = []
    for relative, digest in RAW_EVIDENCE.items():
        path = root / relative
        if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            errors.append(f"{relative}: archived model evidence is missing or modified")
    return errors


def main() -> int:
    errors = validate_evidence(ROOT)
    names = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "--", "*.md"],
        cwd=ROOT, text=True,
    ).splitlines()
    paths = sorted({ROOT / name for name in names if (ROOT / name).is_file()})
    for path in paths:
        text = path.read_text()
        relative = path.relative_to(ROOT)
        if str(relative) in RAW_EVIDENCE:
            continue
        if re.search("[가-힣ㄱ-ㅎㅏ-ㅣ]", text):
            errors.append(f"{relative}: non-English Korean text remains")
        # Code samples may contain illustrative links, not repository references.
        prose = re.sub(r"(?ms)^(```|~~~).*?^\1[ \t]*$", "", text)
        for target in re.findall(r"(?<!!)\[[^\n]*?\]\(([^)\s]+)\)", prose):
            if target.startswith(("#", "/", "<")) or re.match(r"\w+:", target):
                continue
            target = unquote(target.split("#", 1)[0])
            if target and not (path.parent / target).exists():
                errors.append(f"{relative}: broken relative link {target}")
        if (str(relative).startswith(("docs/superpowers/", "docs/research/", ".kiro/specs/"))
                and "historical" not in text[:1000].lower()):
            errors.append(f"{relative}: historical record needs an explicit status")

    expected = render((ROOT / "CLAUDE.md").read_text())
    if (ROOT / "AGENTS.md").read_text() != expected:
        errors.append("AGENTS.md differs from canonical extract; run sync_review_context.py")
    if len(expected.encode()) > 24000:
        errors.append("AGENTS.md exceeds the 24000-byte inline context budget")
    if (ROOT / ".kiro/steering/project-context.md").read_text() != BRIDGE:
        errors.append("Kiro steering differs from the shared-context bridge")
    if re.search(r"AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY|ghp_[A-Za-z0-9]{30,}", expected):
        errors.append("Possible credential in generated review context")

    source = (ROOT / "backend/cmd/api/main.go").read_text()
    routes = re.findall(r'r\.(Get|Post|Put|Delete|Patch)\("([^"\n]+)", ([\w.]+)\)', source)
    inventory = "\n".join(
        f"| {method.upper()} | `{path}` | `{handler}` |" for method, path, handler in routes
    )
    spec = (ROOT / "docs/API-SPEC.md").read_text()
    match = re.search(
        r"<!-- BEGIN GO ROUTES -->\n\| Method \| Path \| Handler \|\n\|---\|---\|---\|\n"
        r"(.*?)\n<!-- END GO ROUTES -->", spec, re.S,
    )
    if not match or match.group(1) != inventory:
        errors.append("docs/API-SPEC.md route inventory differs from Go registrations")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"Documentation checks passed: {len(paths)} Markdown files "
          f"({len(RAW_EVIDENCE)} immutable evidence files), "
          f"{len(routes)} Go routes, {len(expected.encode())}-byte review context.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
