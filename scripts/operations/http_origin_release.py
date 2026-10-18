#!/usr/bin/env python3
"""Coordinate an existing deployment's HTTP origin migration without printing proofs."""
import argparse
from dataclasses import dataclass, field
import hmac
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import stat
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zipfile
import zlib

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("origin_contract", ROOT / "backend/python/qa/origin_proof.py")
CONTRACT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CONTRACT)
SECRET_NAME = "/ttobak/http/origin-verify"
RETIRED_STAGE = "TTOBAK_HTTP_RETIRED"
CACHE_WAIT = 65  # 60-second cache + in-flight lookup + scheduling margin.
MIN_TRANSITION_WINDOW = 3600  # Operational headroom, not a CloudFormation completion guarantee.
OVERLAP = 24 * 60 * 60
STACKS = (
    "TtobakWebSearchGatewayStack", "TtobakStorageStack", "TtobakAuthStack",
    "TtobakAiStack", "TtobakEdgeAuthStack", "TtobakGatewayStack",
    "TtobakCrawlerStack", "TtobakResearchAgentStack", "TtobakWhisperStack",
    "TtobakFrontendStack",
)
DISCOVERY = "/.well-known/oauth-protected-resource"
PUBLIC_DOC_PROBE = "/api/public/docs/__origin_migration_probe__"
VERSION = re.compile(r"^[A-Za-z0-9-]{32,64}$")


class ReleaseError(Exception):
    """Fixed category only. Never attach SDK messages or secret-bearing objects."""


def require(condition, category):
    if not condition:
        raise ReleaseError(category)


def equal(left, right):
    return isinstance(left, str) and isinstance(right, str) and hmac.compare_digest(
        left.encode("utf-8", "surrogatepass"), right.encode("utf-8", "surrogatepass"))


def checkout_revision():
    revision = subprocess.check_output(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True, stderr=subprocess.DEVNULL).strip()
    require(not os.environ.get("GITHUB_SHA") or os.environ["GITHUB_SHA"] == revision,
            "CHECKOUT_REVISION_MISMATCH")
    status = subprocess.check_output(
        ["git", "status", "--porcelain", "--untracked-files=normal"],
        cwd=ROOT, stderr=subprocess.DEVNULL)
    require(not status, "CHECKOUT_NOT_CLEAN")
    return revision


@dataclass
class State:
    arn: str
    version: str
    document: dict = field(repr=False)
    stages: tuple = ()


class Cdk:
    def __init__(self, region, account, profile=None, with_mcp=True):
        self.env = dict(os.environ, CDK_DEFAULT_REGION=region, AWS_DEFAULT_REGION=region, AWS_REGION=region)
        if account:
            self.env["CDK_DEFAULT_ACCOUNT"] = account
        else:
            self.env.pop("CDK_DEFAULT_ACCOUNT", None)
        self.profile = profile
        self.required_readers = {"ttobak-api", "ttobak-qa"} | ({"ttobak-mcp"} if with_mcp else set())
        self.assembly = ROOT / "build/verification/http-origin-assembly"
        self.revision = None
        self.assembly_digest = None
        self.reviewed_stacks = None

    def command(self, arguments, *, from_assembly=False):
        # Even `cdk diff` can print the OLD plaintext proof when removing it.
        # Discard all child diagnostics; report only fixed phase/stack categories.
        # Operators inspect CloudFormation events privately if a deploy fails.
        command = ["npx", "cdk", *arguments, "--output", str(self.assembly)]
        if from_assembly:
            command += ["--app", str(self.assembly)]
        if self.profile:
            command += ["--profile", self.profile]
        result = subprocess.run(command, cwd=ROOT / "infra", env=self.env,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        require(result.returncode == 0, "CDK_COMMAND_FAILED")

    def assembly_hash(self):
        require(self.assembly.is_dir() and not self.assembly.is_symlink(),
                "REVIEWED_ASSEMBLY_MISSING")
        require((self.assembly / "manifest.json").is_file(), "REVIEWED_ASSEMBLY_MISSING")
        paths = sorted(self.assembly.rglob("*"))
        for path in paths:
            require(not path.is_symlink(), "REVIEWED_ASSEMBLY_SYMLINK")
            require(path.is_dir() or stat.S_ISREG(path.stat().st_mode),
                    "REVIEWED_ASSEMBLY_SPECIAL_FILE")
        caches = self.validated_asset_caches()
        digest = hashlib.sha256()
        digest.update(b"reviewed-exclusive-stacks\0")
        for stack in self.reviewed_stacks or STACKS:
            digest.update(stack.encode() + b"\0")
        for path in paths:
            if path.is_file() and path != self.assembly / ".lock" and path not in caches:
                digest.update(str(path.relative_to(self.assembly)).encode() + b"\0")
                # CDK preserves staged Unix modes in ZIP members. Pin these as
                # reviewed inputs too; changing both source/cache modes is drift.
                digest.update(stat.S_IMODE(path.stat().st_mode).to_bytes(2, "big") + b"\0")
                # Frame contents so embedded path/mode bytes cannot impersonate
                # a second file in the concatenated reviewed input stream.
                digest.update(path.stat().st_size.to_bytes(8, "big") + b"\0")
                with path.open("rb") as source:
                    for block in iter(lambda: source.read(1024 * 1024), b""):
                        digest.update(block)
        return digest.hexdigest()

    def validated_asset_caches(self):
        cache = self.assembly / ".cache"
        require(not cache.exists() or cache.is_dir(), "REVIEWED_ASSEMBLY_CACHE_INVALID")
        try:
            candidates = list(cache.iterdir()) if cache.exists() else []
            require(all(path.is_file() and re.fullmatch(r"[0-9a-f]{64}\.zip", path.name) is not None
                        for path in candidates), "REVIEWED_ASSEMBLY_CACHE_INVALID")
            manifest = json.loads((self.assembly / "manifest.json").read_text())
            artifacts = manifest.get("artifacts", {})
            selected = set()
            for stack in self.reviewed_stacks or STACKS:
                artifact = artifacts.get(stack)
                require(isinstance(artifact, dict) and artifact.get("type") == "aws:cloudformation:stack",
                        "REVIEWED_ASSEMBLY_CACHE_INVALID")
                dependencies = artifact.get("dependencies", [])
                require(isinstance(dependencies, list), "REVIEWED_ASSEMBLY_CACHE_INVALID")
                for dependency in dependencies:
                    referenced = artifacts.get(dependency)
                    require(isinstance(referenced, dict), "REVIEWED_ASSEMBLY_CACHE_INVALID")
                    if referenced.get("type") == "cdk:asset-manifest":
                        selected.add(dependency)
                    # Do not traverse CloudFormation stack dependencies:
                    # deploy --exclusively publishes only selected stack assets.
            asset_manifests = {}
            for identity, artifact in artifacts.items():
                if artifact.get("type") != "cdk:asset-manifest":
                    continue
                name = artifact["properties"]["file"]
                require(isinstance(name, str) and Path(name).name == name
                        and name.endswith(".assets.json"), "REVIEWED_ASSEMBLY_CACHE_INVALID")
                assets = json.loads((self.assembly / name).read_text())
                asset_manifests[identity] = assets.get("files", {})
            needed = {path.stem for path in candidates}
            for identity in selected:
                needed.update(asset_id for asset_id, asset in asset_manifests[identity].items()
                              if asset["source"].get("packaging") == "zip")
            declared = {}
            for assets in asset_manifests.values():
                for asset_id, asset in assets.items():
                    source = asset["source"]
                    if source.get("packaging") != "zip" or asset_id not in needed:
                        continue
                    relative = source.get("path")
                    require(re.fullmatch(r"[0-9a-f]{64}", asset_id) is not None
                            and "executable" not in source and isinstance(relative, str),
                            "REVIEWED_ASSEMBLY_CACHE_INVALID")
                    path = Path(relative)
                    require(not path.is_absolute() and path.parts
                            and ".." not in path.parts and path.parts[0] != ".cache"
                            and path.as_posix() == relative and "\\" not in relative,
                            "REVIEWED_ASSEMBLY_CACHE_INVALID")
                    staged = self.assembly / path
                    require(staged.is_dir() and (asset_id not in declared or declared[asset_id] == staged),
                            "REVIEWED_ASSEMBLY_CACHE_INVALID")
                    declared[asset_id] = staged
            validated = set()
            for path in candidates:
                require(path.stem in declared, "REVIEWED_ASSEMBLY_CACHE_INVALID")
                self.validate_cached_zip(path, declared[path.stem])
                validated.add(path)
            return validated
        except (OSError, ValueError, KeyError, TypeError, AttributeError,
                zipfile.BadZipFile, zlib.error, struct.error, OverflowError):
            raise ReleaseError("REVIEWED_ASSEMBLY_CACHE_INVALID") from None

    @staticmethod
    def validate_cached_zip(cache, staged):
        # Pinned CDK packageFile writes only direct .cache/<asset-id>.zip outputs
        # and REUSES existing bytes. Never exempt a directory or filename alone.
        # Read-only validation accepts its ZIP32/DEFLATE format, including dot
        # files, UTF-8 names and executable modes; no extraction or second ZIP
        # producer. Future unsupported formats stop until separately reviewed.
        category = "REVIEWED_ASSEMBLY_CACHE_INVALID"
        files = {path.relative_to(staged).as_posix(): path
                 for path in staged.rglob("*") if path.is_file()}
        sizes = {name: path.stat().st_size for name, path in files.items()}
        names = {name: name.encode("utf-8") for name in files}
        require(len(files) < 65535 and all("\\" not in name and "\0" not in name
                and len(names[name]) < 65536 and sizes[name] < 2**32 for name in files), category)
        # Bound central-directory parsing and compressed input from staged
        # member counts/bytes before ZipFile constructs any member objects.
        central_size = sum(46 + len(name) for name in names.values())
        archive_limit = 22 + sum(76 + 2 * len(names[name]) + size + (size + 999) // 1000 + 64
                                 for name, size in sizes.items())
        archive_size = cache.stat().st_size
        require(22 <= archive_size <= archive_limit and archive_size < 2**32, category)
        with cache.open("rb") as raw:
            raw.seek(-22, os.SEEK_END)
            end = struct.unpack("<4s4H2IH", raw.read(22))
            require(end[:3] == (b"PK\x05\x06", 0, 0)
                    and end[3] == end[4] == len(files) and end[5] == central_size
                    and end[6] + central_size + 22 == archive_size and end[7] == 0, category)
            with zipfile.ZipFile(raw) as archive:
                infos = archive.infolist()
                require(not archive.comment and len(infos) == len(files)
                        and len({info.filename for info in infos}) == len(files)
                        and {info.filename for info in infos} == files.keys()
                        and archive.start_dir == end[6], category)
                cursor, central = 0, end[6]
                for info in infos:
                    path, name = files[info.filename], names[info.filename]
                    mode = path.stat().st_mode & 0xffff
                    require(stat.S_ISREG(mode) and info.file_size == sizes[info.filename]
                            and info.orig_filename == info.filename
                            and info.header_offset == cursor and info.create_system == 3
                            and info.create_version == 63 and info.extract_version == 20
                            and info.flag_bits == 0x800 and info.compress_type == zipfile.ZIP_DEFLATED
                            and info.date_time == (1980, 1, 1, 0, 0, 0)
                            and not info.extra and not info.comment and info.volume == 0
                            and info.internal_attr == 0 and info.external_attr == mode << 16
                            and info.compress_size <= info.file_size + (info.file_size + 999) // 1000 + 64,
                            category)
                    local = struct.pack("<4s5H3I2H", b"PK\x03\x04", 20, 0x800, 8, 0, 33,
                                        info.CRC, info.compress_size, info.file_size, len(name), 0) + name
                    raw.seek(cursor)
                    require(raw.read(len(local)) == local, category)
                    decompressor = zlib.decompressobj(-15)
                    remaining, received, crc = info.compress_size, 0, 0
                    with path.open("rb") as source:
                        while remaining:
                            compressed = raw.read(min(65536, remaining))
                            require(compressed, category)
                            remaining -= len(compressed)
                            while compressed:
                                output = decompressor.decompress(compressed, 65536)
                                compressed = decompressor.unconsumed_tail
                                require(not decompressor.unused_data
                                        and received + len(output) <= info.file_size
                                        and source.read(len(output)) == output, category)
                                received += len(output)
                                crc = zlib.crc32(output, crc)
                        # max_length can leave output or the stream terminator
                        # buffered even when all compressed input was consumed.
                        # Drain with the same byte bound and source checks.
                        while not decompressor.eof:
                            output = decompressor.decompress(b"", 65536)
                            require((output or decompressor.eof)
                                    and not decompressor.unused_data
                                    and not decompressor.unconsumed_tail
                                    and received + len(output) <= info.file_size
                                    and source.read(len(output)) == output, category)
                            received += len(output)
                            crc = zlib.crc32(output, crc)
                        require(decompressor.eof and not decompressor.unused_data
                                and not decompressor.unconsumed_tail
                                and received == info.file_size and not source.read(1)
                                and crc == info.CRC, category)
                    cursor += len(local) + info.compress_size
                    # Exact local/central/end layout rejects comments, extras,
                    # hidden payloads, overlaps, gaps, descriptors and trailing
                    # data instead of silently ignoring arbitrary ZIP bytes.
                    expected = struct.pack("<4s6H3I5H2I", b"PK\x01\x02", 0x33f, 20, 0x800, 8, 0, 33,
                                           info.CRC, info.compress_size, info.file_size, len(name),
                                           0, 0, 0, 0, mode << 16, info.header_offset) + name
                    raw.seek(central)
                    require(raw.read(len(expected)) == expected, category)
                    central += len(expected)
                require(cursor == end[6] and central == archive_size - 22, category)

    def diff(self, stacks=STACKS, *, activation=True):
        stacks = tuple(stacks)
        require(bool(stacks) and len(stacks) == len(set(stacks))
                and all(stack in STACKS for stack in stacks), "UNAPPROVED_STACK")
        self.assembly_digest = None
        self.reviewed_stacks = stacks
        self.revision = checkout_revision()
        self.command(["diff", *stacks, "--exclusively", "--no-change-set"])
        if activation:
            self.validate_activation_templates()
        require(checkout_revision() == self.revision, "CHECKOUT_REVISION_CHANGED")
        self.assembly_digest = self.assembly_hash()

    @staticmethod
    def listed(value):
        return value if isinstance(value, list) else [] if value is None else [value]

    @staticmethod
    def role_identity(value, stack, templates, exports, *, arn):
        # Resolve ARN/name exports to the same exact reviewed IAM resource.
        if isinstance(value, dict) and set(value) == {"Fn::ImportValue"}:
            name = value["Fn::ImportValue"]
            bindings = exports.get(name, []) if isinstance(name, str) else []
            if len(bindings) != 1:
                return None
            stack, value = bindings[0]
        if not isinstance(value, dict):
            return None
        if arn:
            parts = value.get("Fn::GetAtt")
            if set(value) != {"Fn::GetAtt"} or not isinstance(parts, list) or len(parts) != 2 or parts[1] != "Arn":
                return None
            logical_id = parts[0]
        else:
            if set(value) != {"Ref"}:
                return None
            logical_id = value["Ref"]
        if (isinstance(logical_id, str)
                and templates[stack].get("Resources", {}).get(logical_id, {}).get("Type") == "AWS::IAM::Role"):
            return stack, logical_id
        return None

    @staticmethod
    def grants_proof_read(statement):
        return (isinstance(statement, dict) and statement.get("Effect") == "Allow"
                and "secretsmanager:GetSecretValue" in Cdk.listed(statement.get("Action"))
                and {"Ref": "HttpOriginSecretArn"} in Cdk.listed(statement.get("Resource")))

    def validate_activation_templates(self):
        # The coordinator can be reviewed and shipped before activation. Refuse
        # all mutations when the paired ARN/version-only IaC is not installed.
        templates = {path.name: json.loads(path.read_text()) for path in self.assembly.glob("*.template.json")}
        gateway = templates["TtobakGatewayStack.template.json"]
        frontend = templates["TtobakFrontendStack.template.json"]
        exports = {}
        for stack, template in templates.items():
            for output in template.get("Outputs", {}).values():
                name = output.get("Export", {}).get("Name")
                if isinstance(name, str):
                    exports.setdefault(name, []).append((stack, output.get("Value")))
        require({"HttpOriginSecretArn", "HttpOriginSecretVersionId"} <= gateway.get("Parameters", {}).keys()
                and "HttpOriginSecretVersionId" in frontend.get("Parameters", {}), "ACTIVATION_TEMPLATE_NOT_READY")
        functions = {item["Properties"].get("FunctionName"): item
                     for item in gateway.get("Resources", {}).values() if item.get("Type") == "AWS::Lambda::Function"}
        require(self.required_readers <= functions.keys(), "ACTIVATION_CONSUMER_MISSING")
        policies = {key: item.get("Properties", {}) for key, item in gateway.get("Resources", {}).items()
                    if item.get("Type") == "AWS::IAM::Policy"}
        for name in ("ttobak-api", "ttobak-qa", "ttobak-mcp"):
            if name not in functions:
                continue
            properties = functions[name]["Properties"]
            env = properties.get("Environment", {}).get("Variables", {})
            require(env.get("ORIGIN_VERIFY_SECRET_ARN") == {"Ref": "HttpOriginSecretArn"}
                    and env.get("ORIGIN_VERIFY_SECRET_VERSION_ID") == {"Ref": "HttpOriginSecretVersionId"}
                    and "ORIGIN_VERIFY_SECRET" not in env, "ACTIVATION_CONSUMER_NOT_PINNED")
            # One GatewayStack deployment switches every consumer to ARN lookup. An
            # ungranted or unreachable read would fail all origin checks closed at once.
            require("VpcConfig" not in properties, "ACTIVATION_CONSUMER_VPC_UNVERIFIED")
            role = self.role_identity(properties.get("Role"), "TtobakGatewayStack.template.json", templates, exports, arn=True)
            require(role is not None and any(
                key in self.listed(functions[name].get("DependsOn")) and any(
                    self.role_identity(value, "TtobakGatewayStack.template.json", templates, exports, arn=False) == role
                    for value in self.listed(policy.get("Roles")))
                and any(self.grants_proof_read(statement)
                        for statement in self.listed(policy.get("PolicyDocument", {}).get("Statement")))
                for key, policy in policies.items()), "ACTIVATION_SECRET_READ_NOT_GRANTED")
        versions = [item["Properties"] for item in gateway["Resources"].values()
                    if item.get("Type") == "AWS::Lambda::Version"]
        require(any("HttpOriginSecretVersionId" in json.dumps(item.get("Description"))
                    and "ApiFunction" in json.dumps(item.get("FunctionName")) for item in versions),
                "ACTIVATION_ALIAS_NOT_VERSIONED")
        origins = [origin for item in frontend.get("Resources", {}).values()
                   if item.get("Type") == "AWS::CloudFront::Distribution"
                   for origin in item["Properties"]["DistributionConfig"].get("Origins", [])]
        headers = [header["HeaderValue"] for origin in origins for header in origin.get("OriginCustomHeaders", [])
                   if header["HeaderName"].lower() == "x-origin-verify"]
        require(any("HttpOriginSecretVersionId" in json.dumps(value) and "SecretString:current::" in json.dumps(value)
                    for value in headers), "ACTIVATION_PRODUCER_NOT_PINNED")
        require(all(not isinstance(value, str) or value.startswith("{{resolve:secretsmanager:")
                    for value in headers), "ACTIVATION_PLAINTEXT_ORIGIN")

    def deploy(self, stack, parameters=None):
        require(stack in STACKS, "UNAPPROVED_STACK")
        require(self.revision is not None and self.assembly_digest is not None,
                "ASSEMBLY_NOT_REVIEWED")
        require(self.reviewed_stacks is not None and stack in self.reviewed_stacks, "UNREVIEWED_STACK")
        require(checkout_revision() == self.revision, "CHECKOUT_REVISION_CHANGED")
        require(self.assembly_hash() == self.assembly_digest, "REVIEWED_ASSEMBLY_CHANGED")
        args = ["deploy", stack, "--exclusively", "--require-approval", "never", "--previous-parameters"]
        for name, value in (parameters or {}).items():
            require(name in ("HttpOriginSecretArn", "HttpOriginSecretVersionId"), "UNAPPROVED_PARAMETER")
            args += ["--parameters", f"{stack}:{name}={value}"]
        self.command(args, from_assembly=True)
        require(self.assembly_hash() == self.assembly_digest, "REVIEWED_ASSEMBLY_CHANGED")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def public_status(url):
    # Never follow a public-doc redirect or read a document body.
    request = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.build_opener(NoRedirect).open(request, timeout=10) as response:
            return response.status
    except urllib.error.HTTPError as error:
        status = error.code
        error.close()
        return status


class Release:
    def __init__(self, clients, cdk, *, account, region, revision, with_mcp=True,
                 now=time.time, sleep=time.sleep, probe=public_status):
        self.clients, self.cdk = clients, cdk
        self.account, self.region, self.revision = account, region, revision
        self.with_mcp, self.now, self.sleep, self.probe = with_mcp, now, sleep, probe
        self.phase = "inspect"
        self.state = None
        self.configs = {}
        self.alias = {}
        self.foundation_snapshot = None
        self.live_proof = None
        self.api_url = self.site_url = self.distribution = ""
        self.checks = {}
        self.recovered_expired_cutover = False

    def mark(self, phase):
        self.phase = phase
        print(json.dumps({"phase": phase}), flush=True)

    def outputs(self, stack):
        result = self.clients["cfn"].describe_stacks(StackName=stack)["Stacks"]
        require(len(result) == 1, "STACK_IDENTITY_UNAVAILABLE")
        require(result[0].get("StackStatus") in {
            "CREATE_COMPLETE", "UPDATE_COMPLETE", "UPDATE_ROLLBACK_COMPLETE",
            "IMPORT_COMPLETE", "IMPORT_ROLLBACK_COMPLETE",
        }, "STACK_NOT_STABLE")
        return {item["OutputKey"]: item["OutputValue"] for item in result[0].get("Outputs", [])}

    def configuration(self):
        names = ["ttobak-api:live", "ttobak-api", "ttobak-qa"]
        if self.with_mcp:
            names.append("ttobak-mcp")
        result = {}
        for name in names:
            value = self.clients["lambda"].get_function_configuration(FunctionName=name)
            require(value.get("State", "Active") == "Active" and
                    value.get("LastUpdateStatus", "Successful") == "Successful", "CONSUMER_UPDATE_IN_PROGRESS")
            base = name.split(":")[0]
            arn = f"arn:aws:lambda:{self.region}:{self.account}:function:{base}"
            require(value.get("FunctionName") == base and
                    (value.get("FunctionArn") == arn or value.get("FunctionArn", "").startswith(arn + ":")),
                    "CONSUMER_NAME_MISMATCH")
            require(all(isinstance(value.get(field), str) and value[field]
                        for field in ("Version", "RevisionId", "CodeSha256")), "CONSUMER_REVISION_MISSING")
            result[name] = value
        alias = self.clients["lambda"].get_alias(FunctionName="ttobak-api", Name="live")
        require(not alias.get("RoutingConfig", {}).get("AdditionalVersionWeights"), "WEIGHTED_ALIAS_UNSUPPORTED")
        require(alias.get("FunctionVersion") == result["ttobak-api:live"].get("Version"), "ALIAS_VERSION_CHANGED")
        require(bool(alias.get("RevisionId")), "ALIAS_REVISION_MISSING")
        self.alias = alias
        self.configs = result
        return result

    def environment(self, name):
        return self.configs[name].get("Environment", {}).get("Variables", {})

    def snapshot(self):
        self.configuration()
        return {
            "functions": {name: {key: value[key] for key in
                                 ("FunctionName", "FunctionArn", "Version", "RevisionId", "CodeSha256")}
                          for name, value in self.configs.items()},
            "alias": {key: self.alias[key] for key in ("FunctionVersion", "RevisionId")},
        }

    def guard(self):
        require(self.foundation_snapshot is not None and self.snapshot() == self.foundation_snapshot,
                "CONSUMER_CHANGED_AFTER_PREFLIGHT")
        if self.state:
            require(self.read().version == self.state.version, "CONCURRENT_SECRET_CHANGE")
            known = [self.state.document["current"], self.state.document["previous"]]
        else:
            known = [self.live_proof]
        actual = self.distribution_proof(deployed=True)
        require(any(equal(actual, value) for value in known if value), "ORIGIN_CHANGED_AFTER_PREFLIGHT")
        self.transition_window(actual)

    def transition_window(self, producer):
        if not self.state or self.state.document["phase"] != "rotating":
            return
        doc = self.state.document
        if equal(producer, doc["current"]) or all(
                self.environment(name).get("ORIGIN_VERIFY_SECRET_VERSION_ID") == self.state.version
                for name in self.configs):
            return  # Existing dual consumers already have this immutable deadline.
        remaining = doc["previousExpiresAt"] - self.now()
        require(remaining > 0, "EXPIRED_MIXED_CUTOVER_REQUIRES_OPERATOR")
        require(remaining >= MIN_TRANSITION_WINDOW, "INSUFFICIENT_ROTATION_WINDOW")

    @staticmethod
    def expected_status(name):
        return 404 if name.startswith("ttobak-api") else 405 if name == "ttobak-qa" else 200

    def verify_foundation(self):
        self.mark("verifying_installed_reader_foundation")
        before = self.snapshot()
        for name in self.configs:
            env = self.environment(name)
            candidates = [env.get("ORIGIN_VERIFY_SECRET")] if not env.get("ORIGIN_VERIFY_SECRET_ARN") else [
                self.state.document["current"], self.state.document["previous"]]
            accepted = False
            for proof in candidates:
                if CONTRACT.valid_proof(proof) and self.canary(name, proof) == (self.expected_status(name), "1"):
                    accepted = True
                    break
            require(accepted, "READER_FOUNDATION_NOT_DEPLOYED")
            require(self.canary(name, None) == (403, None), "FOUNDATION_MISSING_PROOF_NOT_REJECTED")
        after = self.snapshot()
        require(before == after, "CONSUMER_CHANGED_DURING_PREFLIGHT")
        self.foundation_snapshot = after

    def distribution_proof(self, deployed=False):
        value = self.clients["cloudfront"].get_distribution(Id=self.distribution)["Distribution"]
        require(value.get("DomainName") == urllib.parse.urlsplit(self.site_url).hostname,
                "CLOUDFRONT_DOMAIN_MISMATCH")
        if deployed:
            require(value.get("Status") == "Deployed", "CLOUDFRONT_STILL_DEPLOYING")
        domain = urllib.parse.urlsplit(self.api_url).hostname
        origins = [origin for origin in value["DistributionConfig"]["Origins"]["Items"]
                   if origin["DomainName"] == domain]
        require(len(origins) == 1, "HTTP_ORIGIN_IDENTITY_MISMATCH")
        headers = [header["HeaderValue"] for header in origins[0].get("CustomHeaders", {}).get("Items", [])
                   if header["HeaderName"].lower() == "x-origin-verify"]
        require(len(headers) == 1 and CONTRACT.valid_proof(headers[0]), "LIVE_ORIGIN_PROOF_INVALID")
        return headers[0]

    def inventory(self):
        gateway, frontend = self.outputs("TtobakGatewayStack"), self.outputs("TtobakFrontendStack")
        self.api_url = gateway["HttpApiUrl"].rstrip("/")
        self.site_url = "https://" + frontend["DistributionDomainName"]
        self.distribution = frontend["DistributionId"]
        endpoint = urllib.parse.urlsplit(self.api_url)
        require(endpoint.scheme == "https" and endpoint.hostname and
                endpoint.hostname.endswith(f".execute-api.{self.region}.amazonaws.com"), "API_ORIGIN_INVALID")
        api_id = endpoint.hostname.split(".")[0]
        integrations = {}
        for page in self.clients["gateway"].get_paginator("get_integrations").paginate(ApiId=api_id):
            for item in page.get("Items", []):
                integrations[item["IntegrationId"]] = item
        routes = []
        for page in self.clients["gateway"].get_paginator("get_routes").paginate(ApiId=api_id):
            routes.extend(page.get("Items", []))
            require(len(routes) <= 1000, "ROUTE_INVENTORY_TOO_LARGE")
        required = {"ANY /api/{proxy+}", "GET /api/public/docs/{token}", "POST /api/qa/ask"}
        public = {"GET /api/public/docs/{token}"}
        if self.with_mcp:
            required |= {"ANY /api/mcp", "GET " + DISCOVERY, "GET " + DISCOVERY + "/api/mcp"}
            public |= {"GET " + DISCOVERY, "GET " + DISCOVERY + "/api/mcp"}
        require(required <= {route["RouteKey"] for route in routes}, "REQUIRED_ROUTE_MISSING")
        for route in routes:
            key = route["RouteKey"]
            require(route.get("AuthorizationType", "NONE") != "NONE" or key in public,
                    "UNEXPECTED_PUBLIC_ROUTE")
            expected = "ttobak-api:live"
            if key.split(" ", 1)[-1].startswith("/api/qa/"):
                expected = "ttobak-qa"
            if key == "ANY /api/mcp" or key.startswith("GET /.well-known/"):
                expected = "ttobak-mcp"
            integration = integrations.get(route.get("Target", "").removeprefix("integrations/"), {})
            uri = integration.get("IntegrationUri", "")
            arn = f"arn:aws:lambda:{self.region}:{self.account}:function:{expected}"
            require(uri == arn or uri.endswith("/functions/" + arn + "/invocations"), "INTEGRATION_IDENTITY_MISMATCH")
            require(integration.get("PayloadFormatVersion") == ("1.0" if expected.endswith(":live") else "2.0"),
                    "INTEGRATION_FORMAT_MISMATCH")

    def decode(self, result):
        arn, version = result.get("ARN", ""), result.get("VersionId", "")
        prefix = f"arn:aws:secretsmanager:{self.region}:{self.account}:secret:{SECRET_NAME}-"
        require(arn.startswith(prefix) and re.fullmatch(r"[A-Za-z0-9]{6}", arn[len(prefix):]),
                "SECRET_IDENTITY_MISMATCH")
        require(bool(VERSION.fullmatch(version)), "SECRET_VERSION_INVALID")
        document = CONTRACT.parse_proofs(result.get("SecretString"))
        require(document is not None, "SECRET_DOCUMENT_INVALID")
        if document["phase"] != "seeded":
            require(bool(VERSION.fullmatch(document.get("retiredVersionId", ""))), "RETIREMENT_IDENTITY_MISSING")
        return State(arn, version, document, tuple(result.get("VersionStages", [])))

    def read(self, version=None, optional=False):
        arguments = {"SecretId": self.state.arn if self.state else SECRET_NAME}
        if version:
            arguments["VersionId"] = version
        try:
            return self.decode(self.clients["secrets"].get_secret_value(**arguments))
        except self.clients["secrets"].exceptions.ResourceNotFoundException:
            if optional:
                return None
            raise ReleaseError("SECRET_STATE_MISSING") from None

    def inspect(self):
        self.inventory()
        self.configuration()
        live = self.distribution_proof(deployed=True)
        self.live_proof = live
        self.state = self.read(optional=True)
        if self.state is None:
            for name in self.configs:
                env = self.environment(name)
                require(not env.get("ORIGIN_VERIFY_SECRET_ARN") and equal(env.get("ORIGIN_VERIFY_SECRET"), live),
                        "LIVE_PROOF_AGREEMENT_FAILED")
            return live
        doc = self.state.document
        known = [doc["current"]] + ([doc["previous"]] if doc["phase"] == "rotating" else [])
        require(any(equal(live, proof) for proof in known), "UNRECOGNIZED_CLOUDFRONT_PROOF")
        for name in self.configs:
            env = self.environment(name)
            if env.get("ORIGIN_VERIFY_SECRET_ARN"):
                require(env["ORIGIN_VERIFY_SECRET_ARN"] == self.state.arn and "ORIGIN_VERIFY_SECRET" not in env,
                        "CONSUMER_SECRET_IDENTITY_MISMATCH")
            else:
                require(doc["phase"] != "active" and any(equal(env.get("ORIGIN_VERIFY_SECRET"), proof) for proof in known),
                        "LEGACY_CONSUMER_STATE_MISMATCH")
        return live

    def bootstrap(self, live):
        if self.state:
            return
        self.mark("bootstrap_same_proof")
        self.guard()
        generation = uuid.uuid4().hex
        document = {"schemaVersion": 1, "current": live, "previous": "", "generation": generation,
                    "rotationStartedAt": 0, "previousExpiresAt": 0, "phase": "seeded"}
        try:
            self.clients["secrets"].create_secret(Name=SECRET_NAME, ClientRequestToken=generation,
                                                  SecretString=json.dumps(document),
                                                  Tags=[{"Key": "Project", "Value": "TTOBAK"}])
        except Exception:
            # A response can be lost after creation. Do not retry a mutation
            # blindly; re-read and require the same live proof before proceeding.
            recovered = self.read(optional=True)
            require(recovered is not None and recovered.document["phase"] == "seeded" and
                    equal(recovered.document["current"], live), "BOOTSTRAP_OUTCOME_UNKNOWN")
        self.state = self.read()
        require(self.state.document["phase"] == "seeded" and equal(self.state.document["current"], live),
                "BOOTSTRAP_STATE_CHANGED")

    def write(self, document):
        self.guard()
        expected = self.state.version
        require(self.read().version == expected, "CONCURRENT_SECRET_CHANGE")
        token = uuid.uuid4().hex
        self.clients["secrets"].put_secret_value(SecretId=self.state.arn, ClientRequestToken=token,
                                                SecretString=json.dumps(document), VersionStages=["AWSPENDING"])
        self.guard()
        try:
            self.clients["secrets"].update_secret_version_stage(
                SecretId=self.state.arn, VersionStage="AWSCURRENT",
                MoveToVersionId=token, RemoveFromVersionId=expected)
        except Exception:
            observed = self.read()
            require(observed.version == token, "SECRET_PROMOTION_OUTCOME_UNKNOWN")
        observed = self.read()
        require(observed.version == token and observed.document == document, "SECRET_PROMOTION_NOT_CONFIRMED")
        self.state = observed

    def retain_retirement_version(self):
        # Without a custom label, a second AWSCURRENT move removes AWSPREVIOUS
        # from the seeded version, allowing Secrets Manager to delete it.
        # Retain the original value for actual rejection probes on later releases.
        observed = self.read(version=self.state.version)
        if RETIRED_STAGE not in observed.stages:
            self.guard()
            self.clients["secrets"].update_secret_version_stage(
                SecretId=self.state.arn, VersionStage=RETIRED_STAGE,
                MoveToVersionId=self.state.version)
        require(RETIRED_STAGE in self.read(version=self.state.version).stages,
                "RETIREMENT_VERSION_NOT_RETAINED")

    def arn_only(self):
        self.configuration()
        for name in self.configs:
            env = self.environment(name)
            require(env.get("ORIGIN_VERIFY_SECRET_ARN") == self.state.arn and
                    env.get("ORIGIN_VERIFY_SECRET_VERSION_ID") == self.state.version and
                    "ORIGIN_VERIFY_SECRET" not in env,
                    "CONSUMER_CUTOVER_NOT_VERIFIED")

    def canary(self, name, proof):
        headers = {} if proof is None else {"x-origin-verify": proof}
        if name.startswith("ttobak-api"):
            event = {"version": "1.0", "resource": "/api/public/docs/{token}", "path": PUBLIC_DOC_PROBE,
                     "httpMethod": "GET", "headers": headers,
                     "requestContext": {"resourcePath": "/api/public/docs/{token}", "httpMethod": "GET", "path": PUBLIC_DOC_PROBE},
                     "body": None, "isBase64Encoded": False}
        else:
            event = {"version": "2.0", "rawPath": DISCOVERY if name == "ttobak-mcp" else "/api/qa/origin-probe",
                     "headers": headers, "requestContext": {"http": {"method": "GET"}}, "body": None}
        response = self.clients["lambda"].invoke(FunctionName=name, InvocationType="RequestResponse",
                                               Payload=json.dumps(event).encode())
        stream = response["Payload"]
        try:
            data = stream.read(65537)
        finally:
            stream.close()
        require(response.get("StatusCode") == 200 and len(data) <= 65536 and
                not response.get("FunctionError"), "CONSUMER_CANARY_FAILED")
        require(response.get("ExecutedVersion") == self.configs[name]["Version"], "INVOKED_VERSION_CHANGED")
        result = json.loads(data)
        single = result.get("headers") or {}
        multiple = result.get("multiValueHeaders") or {}
        require(isinstance(single, dict) and isinstance(multiple, dict), "CANARY_HEADERS_INVALID")
        values = []
        for source, is_multiple in ((single, False), (multiple, True)):
            for key, value in source.items():
                require(isinstance(key, str), "CANARY_HEADERS_INVALID")
                if key.lower() == "x-ttobak-origin-protocol":
                    if is_multiple:
                        require(isinstance(value, list) and all(isinstance(v, str) for v in value),
                                "CANARY_HEADERS_INVALID")
                        values.extend(value)
                    else:
                        require(isinstance(value, str), "CANARY_HEADERS_INVALID")
                        values.append(value)
        protocol = "1" if values and set(values) == {"1"} else None
        return result.get("statusCode"), protocol

    def verify_readers(self, retired=None, adopt=False, expected=None):
        self.arn_only()
        before = self.snapshot()
        if expected is not None:
            require(before == expected, "CONSUMER_CHANGED_AFTER_DEPLOY")
        if not adopt:
            require(before == self.foundation_snapshot, "CONSUMER_CHANGED_AFTER_PREFLIGHT")
        for name in self.configs:
            require(self.canary(name, self.state.document["current"]) == (self.expected_status(name), "1"),
                    "CURRENT_PROOF_NOT_ACCEPTED")
            require(self.canary(name, None) == (403, None), "MISSING_PROOF_NOT_REJECTED")
            if retired is not None:
                require(self.canary(name, retired) == (403, None), "RETIRED_PROOF_STILL_ACCEPTED")
        after = self.snapshot()
        require(before == after, "CONSUMER_CHANGED_DURING_VERIFICATION")
        if adopt:
            self.foundation_snapshot = after

    def wait_cache(self):
        self.mark("waiting_for_consumer_cache")
        # Always wait after observing the new version, including on resumed
        # releases; the version's creation time can precede its promotion.
        remaining = CACHE_WAIT
        while remaining:
            interval = min(15, remaining)
            self.sleep(interval)
            remaining -= interval

    def frontend(self):
        self.mark("deploying_frontend_proof_version")
        self.guard()
        self.cdk.deploy("TtobakFrontendStack", {"HttpOriginSecretVersionId": self.state.version})
        require(equal(self.distribution_proof(deployed=True), self.state.document["current"]),
                "CLOUDFRONT_PROOF_NOT_VERIFIED")

    def deploy_consumers(self):
        self.mark("deploying_consumer_proof_version")
        self.guard()
        self.cdk.deploy("TtobakGatewayStack", {
            "HttpOriginSecretArn": self.state.arn,
            "HttpOriginSecretVersionId": self.state.version,
        })
        self.accept_consumer_transition()

    def accept_consumer_transition(self):
        self.arn_only()
        staged = self.snapshot()
        self.wait_cache()
        self.verify_readers(adopt=True, expected=staged)

    def verify_public(self):
        path = DISCOVERY if self.with_mcp else PUBLIC_DOC_PROBE
        expected = 200 if self.with_mcp else 404
        require(self.probe(self.site_url + path) == expected, "CLOUDFRONT_PROBE_FAILED")
        require(self.probe(self.api_url + path) == 403, "DIRECT_ORIGIN_NOT_REJECTED")

    def finish_rotation(self):
        doc = self.state.document
        require(doc["phase"] == "rotating", "ROTATION_STATE_REQUIRED")
        self.mark("resuming_rotation" if self.now() < doc["previousExpiresAt"] else "repairing_expired_cutover")
        if self.now() >= doc["previousExpiresAt"] and not equal(self.distribution_proof(), doc["current"]):
            # An expired previous proof must not be restored or its deadline
            # extended. Avoid turning still-seeded consumers into a new outage.
            require(all(self.environment(name).get("ORIGIN_VERIFY_SECRET_VERSION_ID") == self.state.version
                        for name in self.configs), "EXPIRED_MIXED_CUTOVER_REQUIRES_OPERATOR")
        self.deploy_consumers()
        self.verify_readers()
        self.frontend()
        self.verify_public()
        self.mark("retiring_previous_proof")
        self.write(dict(doc, previous="", previousExpiresAt=0, phase="active"))
        self.deploy_consumers()

    def expired_cutover_interrupted(self, producer):
        # After the overlap, a producer or any consumer outside the rotating
        # version has already rejected traffic. Classify it before repairs.
        doc = self.state.document if self.state else None
        if not doc or doc["phase"] != "rotating" or self.now() < doc["previousExpiresAt"]:
            return False
        return not (equal(producer, doc["current"]) and all(
            self.environment(name).get("ORIGIN_VERIFY_SECRET_VERSION_ID") == self.state.version
            for name in self.configs))

    def run(self):
        live = self.inspect()
        self.verify_foundation()
        self.recovered_expired_cutover = self.expired_cutover_interrupted(live)
        self.mark("reviewing_stack_diff")
        self.cdk.diff()
        self.bootstrap(live)
        self.transition_window(live)
        for stack in STACKS[:-1]:
            self.mark("deploying_" + stack)
            if stack == "TtobakAiStack":
                try:
                    self.clients["iam"].get_role(RoleName="ttobak-agentcore-research-role")
                except self.clients["iam"].exceptions.NoSuchEntityException:
                    raise ReleaseError("RESEARCH_ROLE_MISSING") from None
                except Exception:
                    # Preserve the existing non-missing read-failure behavior:
                    # CloudFormation's real IAM operation decides the outcome.
                    print(json.dumps({"warning": "RESEARCH_ROLE_READ_UNAVAILABLE"}), flush=True)
            self.guard()
            self.cdk.deploy(stack, {"HttpOriginSecretArn": self.state.arn,
                                   "HttpOriginSecretVersionId": self.state.version}
                            if stack == "TtobakGatewayStack" else None)
            if stack == "TtobakGatewayStack":
                self.accept_consumer_transition()
        if self.state.document["phase"] == "rotating":
            self.finish_rotation()
        else:
            self.frontend()
            self.verify_public()
        if self.state.document["phase"] == "seeded":
            self.mark("publishing_bounded_rotation")
            self.retain_retirement_version()
            started = int(self.now()) - 5  # Small clock-skew allowance, still within the fixed maximum.
            self.write(dict(self.state.document, phase="rotating", current=secrets.token_urlsafe(48),
                            previous=self.state.document["current"], rotationStartedAt=started,
                            previousExpiresAt=started + OVERLAP, retiredVersionId=self.state.version,
                            generation=uuid.uuid4().hex))
            self.finish_rotation()
        elif self.state.document["phase"] == "active":
            self.wait_cache()
        retired_state = self.read(version=self.state.document["retiredVersionId"])
        require(RETIRED_STAGE in retired_state.stages, "RETIREMENT_VERSION_NOT_RETAINED")
        retired = retired_state.document["current"]
        require(not equal(retired, self.state.document["current"]), "RETIREMENT_IDENTITY_INVALID")
        self.mark("verifying_retirement")
        self.verify_readers(retired)
        self.verify_public()
        require(self.read().version == self.state.version and
                equal(self.distribution_proof(deployed=True), self.state.document["current"]),
                "FINAL_STATE_CHANGED")
        self.guard()
        self.mark("complete")
        return self.receipt("complete")

    def receipt(self, status, code=None):
        result = {"status": status, "phase": self.phase, "revision": self.revision,
                  "account": self.account, "region": self.region, "checkedAt": int(self.now()),
                  "recoveredExpiredCutover": status == "complete" and self.recovered_expired_cutover}
        if code:
            result["code"] = code
        if self.state:
            result.update(generation=self.state.document["generation"], secretVersionId=self.state.version,
                          secretPhase=self.state.document["phase"],
                          previousExpiresAt=self.state.document["previousExpiresAt"])
        if status == "complete":
            result["checks"] = {"arnOnlyConsumers": True, "cloudfrontDeployed": True,
                                "currentProofAccepted": True, "retiredProofRejected": True,
                                "missingProofRejected": True, "directOriginRejected": True}
            result["functions"] = {name: {"version": value.get("Version"), "codeSha256": value.get("CodeSha256"),
                                          "revisionId": value.get("RevisionId")}
                                   for name, value in self.configs.items()}
        return result


def save_receipt(path, value):
    destination = Path(path)
    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as output:
        json.dump(value, output, indent=2)
        output.write("\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("check", "release", "retighten"))
    parser.add_argument("--region", default="ap-northeast-2")
    parser.add_argument("--profile")
    parser.add_argument("--receipt", default=str(ROOT / "build/verification/http-origin-release.json"))
    args = parser.parse_args()
    release = None
    try:
        import boto3
        from botocore.config import Config
        session = boto3.Session(profile_name=args.profile, region_name=args.region)
        config = Config(connect_timeout=3, read_timeout=40, retries={"total_max_attempts": 1})
        account = session.client("sts", config=config).get_caller_identity()["Account"]
        require(bool(re.fullmatch(r"[0-9]{12}", account)), "ACCOUNT_IDENTITY_INVALID")
        revision = checkout_revision()
        if args.action == "retighten":
            # Repair only OAC, with the same account/revision and assembly guards.
            cdk = Cdk(args.region, account, args.profile)
            cdk.diff(("TtobakStorageStack",), activation=False)
            cdk.deploy("TtobakStorageStack")
            result = {"status": "retightened", "revision": revision, "account": account,
                      "region": args.region}
            save_receipt(args.receipt, result)
            print(json.dumps(result, sort_keys=True), flush=True)
            return 0
        context = json.loads((ROOT / "infra/cdk.json").read_text())["context"]
        clients = {name: session.client(service, config=config) for name, service in (
            ("cfn", "cloudformation"), ("lambda", "lambda"), ("cloudfront", "cloudfront"),
            ("gateway", "apigatewayv2"), ("secrets", "secretsmanager"), ("iam", "iam"))}
        with_mcp = bool(context.get("ttobak:mcpResourceAudience"))
        release = Release(clients, Cdk(args.region, account, args.profile, with_mcp=with_mcp), account=account,
                          region=args.region, revision=revision,
                          with_mcp=with_mcp)
        if args.action == "check":
            release.inspect()
            result = release.receipt("checked_only")
        else:
            result = release.run()
        save_receipt(args.receipt, result)
        print(json.dumps(result, sort_keys=True), flush=True)
        return 0
    except Exception as error:
        code = str(error) if isinstance(error, ReleaseError) else "OPERATION_FAILED_OR_OUTCOME_UNKNOWN"
        result = release.receipt("incomplete", code) if release else {"status": "incomplete", "code": code}
        try:
            save_receipt(args.receipt, result)
        except Exception:
            result["receiptPersistence"] = "failed"
        try:
            print(json.dumps(result, sort_keys=True), file=sys.stderr, flush=True)
        except Exception:
            pass
        return 1


if __name__ == "__main__":
    sys.exit(main())
