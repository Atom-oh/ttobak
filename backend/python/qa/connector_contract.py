"""Connector policy shared by private QA delegation and typed consumers."""
from dataclasses import dataclass
from typing import FrozenSet, Optional, Tuple
from connector_operations import OPERATION_RULES

PROTOCOL = "ttobak.connector"
SIGNING_ALGORITHM = "RSASSA_PSS_SHA_256"
MAX_WIRE_BYTES = 8192
MAX_PAYLOAD_BYTES = 4096
MAX_ARGUMENT_BYTES = 512 * 1024
MAX_LIFETIME_SECONDS = 60
MAX_SAFE_INTEGER = 9007199254740991


class ConnectorError(Exception):
    """Closed diagnostic; never retains provider errors, arguments or credentials."""
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def valid_capabilities(value):
    return (isinstance(value, (list, tuple)) and 1 <= len(value) <= 4
            and all(type(cap) is str and cap in ("read", "write", "share", "delete") for cap in value)
            and "read" in value and list(value) == sorted(set(value)))


def required_capabilities(operation, destination):
    rule = OPERATION_RULES.get(operation)
    if rule is None or not rule[2] or rule[0] != destination:
        raise ConnectorError("CAPABILITY_DENIED")
    return tuple(rule[1].split(","))


@dataclass(frozen=True)
class Approval:
    user_id: str
    client_id: str
    generation: int
    capabilities: Tuple[str, ...]
    expires_at: int
    revoked: bool = False


@dataclass(frozen=True)
class Grants:
    user_id: str
    client_id: str
    client_enabled: bool
    client_maximum: Tuple[str, ...]
    approval: Optional[Approval] = None


@dataclass(frozen=True)
class Principal:
    user_id: str
    client_id: str
    capabilities: FrozenSet[str]
    approval_generation: int
    operation: str
    request_id: str
    expires_at: int

    def require(self, operation, destination, now):
        if (type(now) is not int or not 0 < now <= MAX_SAFE_INTEGER
                or not self.user_id or operation != self.operation or self.expires_at <= now
                or not set(required_capabilities(operation, destination)) <= self.capabilities):
            raise ConnectorError("CAPABILITY_DENIED")


def effective_capabilities(payload, grants, now):
    if (not isinstance(grants, Grants) or grants.client_enabled is not True
            or grants.user_id != payload["sub"] or grants.client_id != payload["clientId"]
            or not valid_capabilities(grants.client_maximum)):
        raise ConnectorError("INVALID_GRANT")
    approved = ("read",)
    generation = payload["approvalGeneration"]
    if generation != 0:
        a = grants.approval
        if (not isinstance(a, Approval) or a.revoked is not False or a.user_id != payload["sub"]
                or a.client_id != payload["clientId"] or type(a.generation) is not int or a.generation != generation
                or type(a.expires_at) is not int or not 0 < a.expires_at <= MAX_SAFE_INTEGER or a.expires_at <= now
                or a.expires_at < payload["expiresAt"] or not valid_capabilities(a.capabilities)):
            raise ConnectorError("INVALID_GRANT")
        approved = a.capabilities
    elif payload["capabilities"] != ["read"]:
        raise ConnectorError("INVALID_GRANT")
    effective = frozenset(payload["capabilities"]) & frozenset(grants.client_maximum) & frozenset(approved)
    if len(effective) != len(payload["capabilities"]):
        raise ConnectorError("CAPABILITY_DENIED")
    return effective
