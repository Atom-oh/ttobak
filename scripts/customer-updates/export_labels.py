#!/usr/bin/env python3
"""Export labelled gate decisions for one user (operator tool).

The output contains customer context. Keep it on the operator's machine: it is
written with mode 0600, never printed, and must not be committed or uploaded.
Needs the operator's own AWS credentials with dynamodb:Query on the table and
kms:Decrypt on the record key (--key-id or env KMS_KEY_ID) for the decision and
draft sealing purposes. Stdout carries counts only.

Population bias: only decisions the live gate admitted store their gate input,
so every exported pair already passed the live gate. Precision measured on this
set is meaningful; recall is only among pairs the live gate admitted.
"""

import argparse
import base64
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "..", "..", "backend", "python", "customer-update-agent"))
import sealing  # noqa: E402

from cryptography.hazmat.primitives.ciphers.aead import AESGCM  # noqa: E402

ADOPTED, DISMISSED, NOT_RELEVANT = "adopted", "dismissed", "not_relevant"
FATAL_CODES = ("AccessDenied", "ExpiredToken", "UnrecognizedClient")


class OpenError(Exception):
    """One sealed payload could not be opened; counted as unreadable."""


class FatalError(Exception):
    """KMS access or credential failure; the run aborts."""


def _is_fatal(exc):
    if type(exc).__name__ == "NoCredentialsError":
        return True
    code = (getattr(exc, "response", None) or {}).get("Error", {}).get("Code", "")
    return isinstance(code, str) and code.startswith(FATAL_CODES)


def open_envelope(kms_client, sealed_b64, binding, key_id):
    """Inverse of sealing.seal; mirrors backend/internal/sealing Envelope.Open."""
    try:
        additional = sealing._aad(binding)
        raw = base64.b64decode(sealed_b64, validate=True)
        if len(raw) < 3 or raw[0] != sealing.VERSION:
            raise OpenError()
        n = int.from_bytes(raw[1:3], "big")
        if n == 0 or len(raw) < 3 + n + sealing.NONCE_BYTES + 16:
            raise OpenError()
        wrapped = raw[3:3 + n]
        nonce = raw[3 + n:3 + n + sealing.NONCE_BYTES]
        body = raw[3 + n + sealing.NONCE_BYTES:]
        key = kms_client.decrypt(CiphertextBlob=wrapped, EncryptionContext=dict(binding),
                                 KeyId=key_id)
        return AESGCM(key["Plaintext"]).decrypt(nonce, body, additional)
    except OpenError:
        raise
    except Exception as exc:
        if _is_fatal(exc):
            raise FatalError(type(exc).__name__) from None
        raise OpenError() from None


def label_for(status, dismiss_reason):
    if status == ADOPTED:
        return "needed"
    if status == DISMISSED and dismiss_reason == NOT_RELEVANT:
        return "not_needed"
    return None


def valid_gate_input(gi):
    """question/context are strings; options are {id,text} string objects with both gate ids."""
    if not isinstance(gi, dict) or not isinstance(gi.get("question"), str) \
            or not isinstance(gi.get("context"), str):
        return False
    opts = gi.get("options")
    if not isinstance(opts, list) or not all(
            isinstance(o, dict) and isinstance(o.get("id"), str) and isinstance(o.get("text"), str)
            for o in opts):
        return False
    return {"needed", "not_needed"} <= {o["id"] for o in opts}


def _binding(purpose, user_id, account_id, url_hash):
    return {"purpose": purpose, "userId": user_id, "accountId": account_id, "urlHash": url_hash}


def query_prefix(ddb, table_name, user_id, prefix):
    items, start = [], None
    while True:
        kwargs = {"TableName": table_name,
                  "KeyConditionExpression": "PK = :pk AND begins_with(SK, :p)",
                  "ExpressionAttributeValues": {":pk": {"S": f"USER#{user_id}"}, ":p": {"S": prefix}}}
        if start:
            kwargs["ExclusiveStartKey"] = start
        page = ddb.query(**kwargs)
        items += page.get("Items", [])
        start = page.get("LastEvaluatedKey")
        if not start:
            return items


def _s(item, name):
    v = item.get(name)
    return v.get("S") if isinstance(v, dict) else None


def export(ddb, kms, table_name, user_id, key_id):
    """Returns (rows, counts) with exported, skipped, unreadable and malformed counts.
    Raises FatalError on a KMS access or credential failure."""
    drafts, unreadable = {}, 0
    for it in query_prefix(ddb, table_name, user_id, "CUDRAFT#"):
        try:
            _, account_id, url_hash = _s(it, "SK").split("#", 2)
            payload = json.loads(open_envelope(
                kms, _s(it, "sealed"),
                _binding("ttobak-customer-update-draft", user_id, account_id, url_hash), key_id))
            drafts[(account_id, url_hash)] = (_s(it, "status"), payload.get("dismissReason", ""))
        except FatalError:
            raise
        except Exception:
            unreadable += 1
    rows, skipped, malformed = [], 0, 0
    for it in query_prefix(ddb, table_name, user_id, "CUDEC#"):
        try:
            _, url_hash, account_id = _s(it, "SK").split("#", 2)
            plain = json.loads(open_envelope(
                kms, _s(it, "sealed"),
                _binding("ttobak-customer-update-decision", user_id, account_id, url_hash), key_id))
        except FatalError:
            raise
        except Exception:
            unreadable += 1
            continue
        gi = plain.get("gateInput") if isinstance(plain, dict) else None
        status, reason = drafts.get((account_id, url_hash), (None, ""))
        label = label_for(status, reason)
        if gi is None or label is None:
            skipped += 1
            continue
        if not valid_gate_input(gi):
            malformed += 1
            continue
        score = (it.get("score") or {}).get("N")
        rows.append({"question": gi["question"], "options": gi["options"],
                     "context": gi["context"], "label": label,
                     "lunaScore": float(score) if score is not None else None})
    return rows, {"exported": len(rows), "skipped": skipped, "unreadable": unreadable,
                  "malformed": malformed}


def write_rows(path, rows, force):
    """Create the file 0600 without following symlinks; EXCL unless force."""
    flags = os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW | (os.O_TRUNC if force else os.O_EXCL)
    fd = os.open(path, flags, 0o600)
    try:
        os.fchmod(fd, 0o600)
        fh = os.fdopen(fd, "w", encoding="utf-8")
    except Exception:
        os.close(fd)
        raise
    with fh:
        for r in rows:
            fh.write(json.dumps(r, ensure_ascii=False) + "\n")


def main(argv=None, ddb=None, kms=None):
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--user-id", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--table", default="ttobak-main")
    ap.add_argument("--key-id", default=os.environ.get("KMS_KEY_ID"),
                    help="KMS record key id or ARN passed to Decrypt (required; default: env KMS_KEY_ID)")
    ap.add_argument("--region", default="ap-northeast-2")
    ap.add_argument("--force", action="store_true", help="overwrite an existing regular output file")
    args = ap.parse_args(argv)
    if not args.key_id:
        print("error: --key-id or env KMS_KEY_ID is required", file=sys.stderr)
        return 2
    if os.path.lexists(args.out) and not args.force:
        print(f"error: {args.out} exists; use --force to overwrite", file=sys.stderr)
        return 1
    if ddb is None:
        import boto3
        ddb = boto3.client("dynamodb", region_name=args.region)
        kms = boto3.client("kms", region_name=args.region)
    try:
        rows, counts = export(ddb, kms, args.table, args.user_id, args.key_id)
    except FatalError as exc:
        print(f"error: KMS access failed ({exc}); check credentials and kms:Decrypt", file=sys.stderr)
        return 2
    print(f"exported={counts['exported']} skipped={counts['skipped']} "
          f"unreadable={counts['unreadable']} malformed={counts['malformed']}")
    if counts["exported"] == 0 and counts["unreadable"] > 0:
        print("error: nothing exported and some payloads were unreadable", file=sys.stderr)
        return 1
    try:
        write_rows(args.out, rows, args.force)
    except OSError:
        print(f"error: cannot write {args.out} (exists, is a symlink, or is not writable)",
              file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
