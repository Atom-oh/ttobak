"""Current connector policy/approval snapshots; metadata only, never cached."""
import re
from decimal import Decimal
from urllib.parse import urlsplit

from connector_contract import Approval, ConnectorError, Grants, valid_capabilities

_ID = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")
_CLIENT = re.compile(r"[A-Za-z0-9]{1,128}\Z")
_FIELDS = ("PK", "SK", "userId", "clientId", "enabled", "maximum", "audience", "issuer",
           "generation", "capabilities", "expiresAt", "revoked", "blocked")


def _integer(value):
    if (type(value) not in (int, Decimal) or not re.fullmatch(r"[1-9][0-9]{0,15}", str(value))
            or not 0 < value <= 9007199254740991):
        raise ConnectorError("INVALID_GRANT")
    return int(value)


def _caps(value):
    if not isinstance(value, set) or not all(isinstance(cap, str) for cap in value):
        raise ConnectorError("INVALID_GRANT")
    result = tuple(sorted(value))
    if not valid_capabilities(result):
        raise ConnectorError("INVALID_GRANT")
    return result


def _binding(audience, issuer):
    try:
        parsed = [urlsplit(value) for value in (audience, issuer)]
        if any(p.scheme != "https" or not p.hostname or p.username or p.password
               or p.query or p.fragment for p in parsed):
            return False
        return (parsed[0].path == "/api/mcp" and parsed[1].netloc.startswith("cognito-idp.")
                and parsed[1].netloc.endswith(".amazonaws.com")
                and bool(_ID.fullmatch(parsed[1].path.removeprefix("/"))))
    except (ValueError, TypeError):
        return False


def current_grants(table, user, client, audience, issuer):
    """table is a trusted boto3 DynamoDB Table resource, never caller data."""
    if (not isinstance(user, str) or not _ID.fullmatch(user)
            or not isinstance(client, str) or not _CLIENT.fullmatch(client)
            or not _binding(audience, issuer)):
        raise ConnectorError("INVALID_GRANT")
    names = {f"#f{i}": field for i, field in enumerate(_FIELDS)}
    def get(pk, sk):
        return {"Get": {"TableName": table.name, "Key": {"PK": pk, "SK": sk},
                        "ProjectionExpression": ",".join(names), "ExpressionAttributeNames": names}}
    try:
        response = table.meta.client.transact_get_items(TransactItems=[
            get("CONNECTOR_CLIENT#" + client, "POLICY"),
            get("USER#" + user, "CONNECTOR_APPROVAL#" + client),
        ])
        rows = response["Responses"]
        if not isinstance(rows, list) or len(rows) != 2:
            raise ValueError()
        policy, approval = (row.get("Item", {}) for row in rows)
    except Exception:
        raise ConnectorError("POLICY_UNAVAILABLE") from None
    if (not isinstance(policy, dict) or policy.get("PK") != "CONNECTOR_CLIENT#" + client
            or policy.get("SK") != "POLICY" or policy.get("enabled") is not True
            or policy.get("clientId") != client or policy.get("audience") != audience
            or policy.get("issuer") != issuer):
        raise ConnectorError("INVALID_GRANT")
    _integer(policy.get("generation"))
    maximum = _caps(policy.get("maximum"))
    if not isinstance(approval, dict):
        raise ConnectorError("INVALID_GRANT")
    if approval:
        if (approval.get("PK") != "USER#" + user or approval.get("SK") != "CONNECTOR_APPROVAL#" + client
                or approval.get("userId") != user or approval.get("clientId") != client
                or type(approval.get("revoked")) is not bool or type(approval.get("blocked")) is not bool):
            raise ConnectorError("INVALID_GRANT")
        return Grants(user, client, not approval["blocked"], maximum, Approval(
            user, client, _integer(approval.get("generation")), _caps(approval.get("capabilities")),
            _integer(approval.get("expiresAt")), approval["revoked"]))
    return Grants(user, client, True, maximum)
