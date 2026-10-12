#!/usr/bin/env python3
"""Validate nonsecret Lambda discovery data and export raw production settings.

The Go service constructor resolves summary -> auxiliary -> production default.
Do not duplicate a model default here or substitute auxiliary for final notes.
"""

import base64
import binascii
import json
from pathlib import Path
import re
import sys


def environment_lines(config, snapshot_path):
    if config.get("state") != "Active" or config.get("lastUpdateStatus") != "Successful":
        raise ValueError("Summarize deployment is not stable")
    revision = config.get("revisionId")
    if not isinstance(revision, str) or not revision:
        raise ValueError("Deployment revision is required")
    try:
        digest = base64.b64decode(config.get("codeSha256") or "", validate=True)
    except (ValueError, TypeError, binascii.Error) as error:
        raise ValueError("Invalid deployed code hash") from error
    if len(digest) != 32:
        raise ValueError("Invalid deployed code hash")
    def optional_string(key):
        value = config.get(key)
        if value is None:
            return ""
        if not isinstance(value, str):
            raise ValueError("Invalid deployed configuration type")
        return value

    summary = optional_string("summaryModelId")
    auxiliary = optional_string("auxiliaryModelId")
    region = optional_string("region") or "us-west-2"
    for model in (summary, auxiliary):
        if not isinstance(model, str) or (model and not re.fullmatch(r"[A-Za-z0-9:/._-]+", model)):
            raise ValueError("Invalid deployed model configuration")
    if not isinstance(region, str) or not re.fullmatch(r"[a-z0-9-]+", region):
        raise ValueError("Invalid deployed region")
    path = str(snapshot_path.resolve())
    if "\n" in path or "\r" in path:
        raise ValueError("Invalid deployment snapshot path")
    return (
        f"BEDROCK_SUMMARY_MODEL_ID={summary}\n"
        f"BEDROCK_MODEL_ID={auxiliary}\n"
        f"BEDROCK_REGION={region}\n"
        f"TTOBAK_NOTE_EVAL_DEPLOYMENT={path}\n"
    )


def main():
    if len(sys.argv) != 3:
        raise SystemExit("Usage: prepare-note-evaluation.py snapshot.json github-env")
    snapshot = Path(sys.argv[1])
    try:
        config = json.loads(snapshot.read_text())
        if not isinstance(config, dict):
            raise ValueError("Invalid deployment snapshot")
        lines = environment_lines(config, snapshot)
    except (ValueError, OSError) as error:
        raise SystemExit(str(error)) from error
    with Path(sys.argv[2]).open("a") as output:
        output.write(lines)


if __name__ == "__main__":
    main()
