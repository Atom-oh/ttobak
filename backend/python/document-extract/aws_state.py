"""Small transactional adapter for the Go ATTACH#/ATTEXT# contract."""
from datetime import datetime, timezone

from boto3.dynamodb.conditions import Attr, ConditionExpressionBuilder
from boto3.dynamodb.types import TypeDeserializer, TypeSerializer
from botocore.exceptions import BotoCoreError, ClientError


class ConditionRejected(Exception):
    pass


def wire(values):
    return {key: TypeSerializer().serialize(value) for key, value in values.items()}


def timestamp(now_ms):
    # Go's time.Time decoder expects RFC3339, never a DynamoDB number.
    return datetime.fromtimestamp(now_ms / 1000, timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def matches(fields):
    condition = Attr("PK").exists()
    for name, value in fields.items():
        condition &= Attr(name).eq(value)
    return condition


def operation(table, key, condition, updates=None):
    # Transactions require a client; retain the SDK condition builder and type
    # serializer rather than interpolating identifiers or values into expressions.
    built = ConditionExpressionBuilder().build_expression(condition)
    result = {
        "TableName": table, "Key": wire(key), "ConditionExpression": built.condition_expression,
        "ExpressionAttributeNames": built.attribute_name_placeholders,
        "ExpressionAttributeValues": wire(built.attribute_value_placeholders),
    }
    if updates:
        parts = []
        for i, (name, value) in enumerate(updates.items()):
            name_alias, value_alias = f"#u{i}", f":u{i}"
            result["ExpressionAttributeNames"][name_alias] = name
            result["ExpressionAttributeValues"][value_alias] = TypeSerializer().serialize(value)
            parts.append(f"{name_alias} = {value_alias}")
        result["UpdateExpression"] = "SET " + ", ".join(parts)
    return result


class StateStore:
    def __init__(self, client, table, job):
        self.client, self.table, self.job = client, table, job
        self.state_key = {"PK": "MEETING#" + job["meetingId"], "SK": "ATTEXT#" + job["attachmentId"]}
        self.parent_key = {"PK": "USER#" + job["ownerId"], "SK": "MEETING#" + job["meetingId"]}
        self.attachment_key = {"PK": "MEETING#" + job["meetingId"], "SK": "ATTACH#" + job["attachmentId"]}

    def get(self, key):
        # Metadata only: neither the meeting's large inline text nor any S3
        # reference is needed to authorize this extraction.
        fields = ("PK", "SK", "runId", "status", "leaseUntil", "sourceKey", "ownerId", "uploaderId",
                  "meetingId", "userId", "originalKey", "attachmentId")
        aliases = {f"#p{i}": field for i, field in enumerate(fields)}
        try:
            result = self.client.get_item(TableName=self.table, Key=wire(key), ConsistentRead=True,
                                          ProjectionExpression=", ".join(aliases), ExpressionAttributeNames=aliases)
        except (BotoCoreError, ClientError):
            raise RuntimeError("STATE_READ_FAILED") from None
        return {key: TypeDeserializer().deserialize(value) for key, value in result.get("Item", {}).items()}

    def source_checks(self, source_key):
        job = self.job
        parent = matches({"meetingId": job["meetingId"], "userId": job["ownerId"]})
        attachment = matches({"meetingId": job["meetingId"], "userId": job["userId"],
                              "attachmentId": job["attachmentId"], "originalKey": source_key})
        return [{"ConditionCheck": operation(self.table, self.parent_key, parent)},
                {"ConditionCheck": operation(self.table, self.attachment_key, attachment)}]

    def state_condition(self, state):
        return matches({key: state[key] for key in
                        ("runId", "status", "leaseUntil", "sourceKey", "ownerId", "uploaderId")})

    def transact(self, state, updates, now_ms):
        condition = self.state_condition(state) & Attr("leaseUntil").gt(now_ms)
        items = self.source_checks(state["sourceKey"])
        items.append({"Update": operation(self.table, self.state_key, condition, updates)})
        try:
            self.client.transact_write_items(TransactItems=items)
        except self.client.exceptions.TransactionCanceledException as error:
            codes = [reason.get("Code") for reason in error.response.get("CancellationReasons", [])]
            if "ConditionalCheckFailed" in codes and all(code in ("None", "ConditionalCheckFailed") for code in codes):
                raise ConditionRejected() from None
            raise RuntimeError("STATE_WRITE_FAILED") from None
        except (BotoCoreError, ClientError):
            # The commit may have succeeded. Never delete result objects or
            # blindly overwrite the run on an ambiguous transport response.
            raise RuntimeError("STATE_WRITE_FAILED") from None

    def fail(self, state, code, now_ms):
        updates = {"status": "failed", "errorCode": code, "leaseUntil": 0, "updatedAt": timestamp(now_ms)}
        try:
            self.client.update_item(**operation(self.table, self.state_key, self.state_condition(state), updates))
            return {"status": "failed", "errorCode": code}
        except self.client.exceptions.ConditionalCheckFailedException:
            return {"status": "ignored"}
        except (BotoCoreError, ClientError):
            raise RuntimeError("STATE_WRITE_FAILED") from None
