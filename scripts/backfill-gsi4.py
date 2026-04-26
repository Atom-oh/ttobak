"""One-time backfill: add GSI4PK and GSI4SK to existing crawled documents."""
import boto3

TABLE = "ttobak-main"
dynamodb = boto3.resource("dynamodb")
table = dynamodb.Table(TABLE)

# Scan for all crawled documents (PK begins with CRAWLER#, SK begins with DOC#)
scan_kwargs = {
    "FilterExpression": "begins_with(PK, :pk) AND begins_with(SK, :sk)",
    "ExpressionAttributeValues": {":pk": "CRAWLER#", ":sk": "DOC#"},
}

updated = 0
while True:
    resp = table.scan(**scan_kwargs)
    for item in resp.get("Items", []):
        doc_type = item.get("type", "news")
        crawled_at = item.get("crawledAt")
        if not crawled_at:
            continue
        # Skip if already has GSI4
        if item.get("GSI4PK"):
            continue
        table.update_item(
            Key={"PK": item["PK"], "SK": item["SK"]},
            UpdateExpression="SET GSI4PK = :gsi4pk, GSI4SK = :gsi4sk",
            ExpressionAttributeValues={
                ":gsi4pk": f"DOC#{doc_type}",
                ":gsi4sk": int(crawled_at) if isinstance(crawled_at, (int, float)) else int(crawled_at),
            },
        )
        updated += 1

    if "LastEvaluatedKey" not in resp:
        break
    scan_kwargs["ExclusiveStartKey"] = resp["LastEvaluatedKey"]

print(f"Backfilled {updated} documents with GSI4PK/GSI4SK")
