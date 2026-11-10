#!/usr/bin/env python3
"""Pure helpers for mac-app/scripts/release.sh (stdlib only, Python 3.9+).

  build --version V --notes N --url U --signature-file F --archive A [--pubkey K]
      Print the Tauri updater latest.json (plus pub_date and sha256). Refuses a
      signature whose trusted comment is not bound to V (`version:V`, written by
      `tauri signer sign --app-version V`) or, with --pubkey, one made by a
      different minisign key than the configured public key.
  check-newer --published FILE --version V [--allow-equal]
      Exit 0 when V is newer than FILE's version (missing FILE is OK), else 1.
      --allow-equal also accepts V == published (release resume only).
      A malformed FILE exits 2.
  read-version --tauri-conf FILE --cargo-toml FILE
      Print the version when both files agree on a plain MAJOR.MINOR.PATCH
      version, else exit 1.
"""
import argparse
import base64
import binascii
import datetime
import hashlib
import json
import os
import re
import sys

SEMVER = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
PLATFORM = "darwin-aarch64"


class ReleaseError(Exception):
    pass


def parse_version(text):
    m = SEMVER.match(text) if isinstance(text, str) else None
    if not m:
        raise ReleaseError("not a MAJOR.MINOR.PATCH version: %r" % (text,))
    return tuple(int(part) for part in m.groups())


def _b64_text(value, what):
    try:
        return base64.b64decode(value.strip(), validate=True).decode("utf-8")
    except (binascii.Error, UnicodeDecodeError, ValueError):
        raise ReleaseError("%s is not base64 minisign text" % what)


def _key_id(line, what):
    try:
        raw = base64.b64decode(line.strip(), validate=True)
    except (binascii.Error, ValueError):
        raise ReleaseError("%s has an undecodable key line" % what)
    if len(raw) < 10:
        raise ReleaseError("%s key line is too short" % what)
    return raw[2:10]


def signature_info(signature):
    """Return (key_id, trusted_comment) of a Tauri (base64-wrapped) minisign signature."""
    lines = _b64_text(signature, "signature").splitlines()
    if len(lines) < 4 or not lines[2].startswith("trusted comment: "):
        raise ReleaseError("signature is not in minisign format")
    return _key_id(lines[1], "signature"), lines[2][len("trusted comment: "):]


def pubkey_id(pubkey):
    lines = _b64_text(pubkey, "pubkey").splitlines()
    if len(lines) < 2:
        raise ReleaseError("pubkey is not in minisign format")
    return _key_id(lines[1], "pubkey")


def signed_version(trusted_comment):
    for field in trusted_comment.split("\t"):
        if field.startswith("version:"):
            return field[len("version:"):]
    return None


def build_manifest(version, notes, url, signature, archive_path, pubkey=None, now=None):
    parse_version(version)
    name = "TTOBAK_%s_aarch64.app.tar.gz" % version
    if not url.startswith("https://") or url.rsplit("/", 1)[-1] != name:
        raise ReleaseError("url must be https and end with %s" % name)
    if os.path.basename(archive_path) != name:
        raise ReleaseError("archive must be named %s" % name)
    signature = signature.strip()
    key_id, comment = signature_info(signature)
    bound = signed_version(comment)
    if bound != version:
        raise ReleaseError("signature is bound to version %r, not %r; sign with "
                           "`tauri signer sign --app-version %s`" % (bound, version, version))
    if pubkey is not None and pubkey_id(pubkey) != key_id:
        raise ReleaseError("signature key id does not match the configured updater pubkey")
    digest = hashlib.sha256()
    with open(archive_path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            digest.update(chunk)
    when = now or datetime.datetime.now(datetime.timezone.utc)
    return {
        "version": version,
        "notes": notes,
        "pub_date": when.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "platforms": {PLATFORM: {"url": url, "signature": signature,
                                 "sha256": digest.hexdigest()}},
    }


def is_newer(version, published_path, allow_equal=False):
    want = parse_version(version)
    if not os.path.exists(published_path):
        return True
    try:
        with open(published_path, encoding="utf-8") as f:
            published = json.load(f)
    except (ValueError, UnicodeDecodeError):
        raise ReleaseError("published manifest is not JSON")
    if not isinstance(published, dict):
        raise ReleaseError("published manifest is not an object")
    have = parse_version(published.get("version"))
    return want > have or (allow_equal and want == have)


def cargo_package_version(text):
    section = None
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("["):
            section = stripped
            continue
        if section == "[package]":
            m = re.match(r'^version\s*=\s*"([^"]*)"\s*(#.*)?$', stripped)
            if m:
                return m.group(1)
    raise ReleaseError("Cargo.toml has no [package] version")


def read_version(tauri_conf, cargo_toml):
    with open(tauri_conf, encoding="utf-8") as f:
        conf_version = json.load(f).get("version")
    with open(cargo_toml, encoding="utf-8") as f:
        cargo_version = cargo_package_version(f.read())
    parse_version(conf_version)
    if conf_version != cargo_version:
        raise ReleaseError("tauri.conf.json version %r != Cargo.toml version %r"
                           % (conf_version, cargo_version))
    return conf_version


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    for flag in ("--version", "--notes", "--url", "--signature-file", "--archive"):
        b.add_argument(flag, required=True)
    b.add_argument("--pubkey")
    c = sub.add_parser("check-newer")
    c.add_argument("--published", required=True)
    c.add_argument("--version", required=True)
    c.add_argument("--allow-equal", action="store_true")
    r = sub.add_parser("read-version")
    r.add_argument("--tauri-conf", required=True)
    r.add_argument("--cargo-toml", required=True)
    args = parser.parse_args(argv)
    try:
        if args.cmd == "build":
            with open(args.signature_file, encoding="utf-8") as f:
                signature = f.read()
            manifest = build_manifest(args.version, args.notes, args.url, signature,
                                      args.archive, args.pubkey)
            json.dump(manifest, sys.stdout, ensure_ascii=False, indent=2)
            sys.stdout.write("\n")
            return 0
        if args.cmd == "check-newer":
            if is_newer(args.version, args.published, args.allow_equal):
                return 0
            sys.stderr.write("version %s is not newer than the published release\n"
                             % args.version)
            return 1
        print(read_version(args.tauri_conf, args.cargo_toml))
        return 0
    except (ReleaseError, OSError, ValueError) as e:
        sys.stderr.write("release_manifest: %s\n" % e)
        return 2 if args.cmd == "check-newer" else 1


if __name__ == "__main__":
    sys.exit(main())
