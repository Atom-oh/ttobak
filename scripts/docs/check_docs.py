#!/usr/bin/env python3
"""Check English documentation, generated context and source-backed inventories."""

from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import unquote

from sync_review_context import BRIDGE, ROOT, render


def main() -> int:
    errors = []
    names = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "--", "*.md"],
        cwd=ROOT, text=True,
    ).splitlines()
    paths = sorted({ROOT / name for name in names if (ROOT / name).is_file()})
    for path in paths:
        text = path.read_text()
        relative = path.relative_to(ROOT)
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
    print(f"Documentation checks passed: {len(paths)} Markdown files, "
          f"{len(routes)} Go routes, {len(expected.encode())}-byte review context.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
