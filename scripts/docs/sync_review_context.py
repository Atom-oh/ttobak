#!/usr/bin/env python3
"""Publish the deliberately selected review sections of the canonical guide."""

import hashlib
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parents[2]
SECTIONS = (
    "Authority and review scope",
    "Stack and source map",
    "Verification commands",
    "Application boundaries",
    "Feature invariants",
    "Security requirements and accepted limits",
    "Specialist PR review",
)
BRIDGE = """---
name: project-context
inclusion: always
---

# Project Context

#[[file:AGENTS.md]]
"""


def render(source: str) -> str:
    digest = hashlib.sha256(source.encode()).hexdigest()[:12]
    parts = re.split(r"(?m)^## ", source)[1:]
    selected = {part.splitlines()[0]: "## " + part.strip() for part in parts}
    missing = set(SECTIONS) - selected.keys()
    if missing:
        raise ValueError(f"Missing canonical sections: {sorted(missing)}")
    marker = (
        f"<!-- generated-by: co-agent · source: CLAUDE.md · claude-md-sha: {digest} · "
        "DO NOT EDIT: run python3 scripts/docs/sync_review_context.py -->"
    )
    return (
        marker + "\n# TTOBAK review context\n\n"
        "Shared by Codex, Kiro, and the CI review panel. Extracted from the\n"
        "canonical CLAUDE.md; delivery procedures and historical records are omitted.\n\n"
        + "\n\n".join(selected[name] for name in SECTIONS) + "\n"
    )


def main() -> None:
    agents = ROOT / "AGENTS.md"
    if agents.exists() and "generated-by: co-agent" not in agents.read_text():
        raise SystemExit("AGENTS.md is handwritten; refusing to overwrite it.")
    bridge = ROOT / ".kiro/steering/project-context.md"
    if bridge.exists() and "#[[file:AGENTS.md]]" not in bridge.read_text():
        raise SystemExit("Kiro bridge is handwritten; merge it explicitly.")
    content = render((ROOT / "CLAUDE.md").read_text())
    if len(content.encode()) > 24000:
        raise SystemExit("Review context exceeds 24000 bytes; shorten canonical sections.")
    agents.write_text(content)
    bridge.parent.mkdir(parents=True, exist_ok=True)
    bridge.write_text(BRIDGE)
    print(f"Updated AGENTS.md ({len(content.encode())} bytes) and Kiro bridge.")


if __name__ == "__main__":
    main()
