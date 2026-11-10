#!/usr/bin/env python3
"""Pre-check that a branch's AI review input fits the CI diff limits.

Mirrors scripts/pr-review/prepare_roles.py: merge-base diff, same git options,
and paths selected by the base revision's role-input-scope.json policy.
Exit 0 within limits, 1 over a limit, 2 on usage or git errors.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import prepare_roles  # noqa: E402
import role_review  # noqa: E402

WARN_RATIO = 0.9
GIT_OPTIONS = [
    "--literal-pathspecs", "-c", "diff.noprefix=false", "diff", "--no-ext-diff",
    "--no-textconv", "--no-color", "--no-renames",
]


class GitError(Exception):
    pass


def git(*arguments):
    result = subprocess.run(["git", *arguments], capture_output=True)
    if result.returncode != 0:
        raise GitError(result.stderr.decode("utf-8", "replace").strip() or "git failed")
    return result.stdout


def default_base():
    try:
        result = subprocess.run(
            ["gh", "pr", "view", "--json", "baseRefName", "-q", ".baseRefName"],
            capture_output=True, timeout=10,
        )
        name = result.stdout.decode().strip()
        if result.returncode == 0 and re.fullmatch(r"[\w./-]+", name):
            return f"origin/{name}"
    except (OSError, subprocess.SubprocessError):
        pass
    return "origin/main"


def per_file_bytes(diff):
    sizes = {}
    for chunk in re.split(rb"(?m)^(?=diff --git )", diff):
        match = re.match(rb"diff --git a/(.*?) b/", chunk)
        if match:
            sizes[match.group(1).decode("utf-8", "replace")] = len(chunk)
    return sizes


def measure(base, head):
    merge_base = git("merge-base", base, head).decode().strip()
    base_sha = git("rev-parse", base).decode().strip()
    options = [*GIT_OPTIONS, merge_base, head]
    names = [p for p in git(*options, "--name-only", "-z", "--").decode("utf-8").split("\0") if p]
    paths, excluded, _ = prepare_roles.selected_paths(base_sha, names)
    diff = git(*options, "--", *paths) if paths else b""
    lines = len(diff.split(b"\n")) - int(diff.endswith(b"\n")) if diff else 0
    largest = sorted(per_file_bytes(diff).items(), key=lambda item: -item[1])[:5]
    return {
        "base": base, "head": head, "merge_base": merge_base,
        "bytes": len(diff), "lines": lines,
        "max_bytes": role_review.MAX_DIFF_BYTES, "max_lines": role_review.MAX_DIFF_LINES,
        "kept_files": len(paths), "excluded_files": len(excluded),
        "largest": [{"path": p, "bytes": b} for p, b in largest],
    }


def evaluate(report):
    over, warn = [], []
    for name in ("bytes", "lines"):
        limit = report[f"max_{name}"]
        if report[name] > limit:
            over.append(name)
        elif report[name] > limit * WARN_RATIO:
            warn.append(name)
    return over, warn


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base")
    parser.add_argument("--head", default="HEAD")
    parser.add_argument("--json", action="store_true")
    arguments = parser.parse_args(argv)
    try:
        report = measure(arguments.base or default_base(), arguments.head)
    except (GitError, ValueError, OSError, UnicodeError) as error:
        print(f"review size check error: {error}", file=sys.stderr)
        return 2
    over, warn = evaluate(report)
    report["over"], report["warn"] = over, warn
    if arguments.json:
        print(json.dumps(report, indent=2))
    else:
        print(f"Review input vs {report['base']} (merge-base {report['merge_base'][:12]}): "
              f"{report['bytes']} bytes / {report['max_bytes']}, "
              f"{report['lines']} lines / {report['max_lines']}, "
              f"{report['kept_files']} files kept, {report['excluded_files']} excluded")
        if over or warn:
            print("Largest kept files:")
            for item in report["largest"]:
                print(f"  {item['bytes']:>8}  {item['path']}")
        if warn and not over:
            print(f"WARNING: over 90% of the review limit ({', '.join(warn)}).")
        if over:
            print(f"ERROR: the AI review would fail on its {' and '.join(over)} limit. "
                  "Split this change into smaller stacked PRs.")
    return 1 if over else 0


if __name__ == "__main__":
    sys.exit(main())
