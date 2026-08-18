"""AWS Price List API lookups for the cost/sizing simulator (ADR-031).

Scoped to 또박's own stack (v1 service-range decision): Lambda, API Gateway,
DynamoDB, S3, CloudFront. Anything outside this allowlist is out of scope
for v1 and should fail loudly rather than guess at a SKU filter shape it
was never validated against -- a silently wrong SKU (wrong tenancy/OS/
purchase-option/region) is worse than an explicit "not supported".

The Price List Query API has no endpoint in ap-northeast-2 (confirmed via
`aws pricing describe-services --region ap-northeast-2`, which fails to
resolve the endpoint) -- it must be called against us-east-1 regardless of
which region the priced resources actually run in. This is a plain SDK
region argument, not a new cross-region SigV4 integration.
"""
import logging

import boto3

logger = logging.getLogger()

PRICING_REGION = "us-east-1"

# Each entry: service_code, filters (region baked in per-entry so callers
# don't need to know the Price List attribute name), and a short label
# surfaced in the report so a human can spot a mismatched SKU.
SERVICE_FILTERS = {
    "lambda": {
        "serviceCode": "AWSLambda",
        "label": "AWS Lambda (ARM, on-demand)",
        "filters": [
            {"Type": "TERM_MATCH", "Field": "group", "Value": "AWS-Lambda-Duration-ARM"},
        ],
    },
    "apigateway": {
        "serviceCode": "AmazonApiGateway",
        "label": "API Gateway (HTTP API)",
        "filters": [
            {"Type": "TERM_MATCH", "Field": "location", "Value": "Asia Pacific (Seoul)"},
        ],
    },
    "dynamodb": {
        "serviceCode": "AmazonDynamoDB",
        "label": "DynamoDB (on-demand)",
        "filters": [
            {"Type": "TERM_MATCH", "Field": "location", "Value": "Asia Pacific (Seoul)"},
            {"Type": "TERM_MATCH", "Field": "group", "Value": "DDB-ReadUnits"},
        ],
    },
    "s3": {
        "serviceCode": "AmazonS3",
        "label": "S3 Standard storage",
        "filters": [
            {"Type": "TERM_MATCH", "Field": "location", "Value": "Asia Pacific (Seoul)"},
            {"Type": "TERM_MATCH", "Field": "storageClass", "Value": "General Purpose"},
        ],
    },
    "cloudfront": {
        "serviceCode": "AmazonCloudFront",
        "label": "CloudFront data transfer out",
        "filters": [
            {"Type": "TERM_MATCH", "Field": "transferType", "Value": "CloudFront Outbound"},
        ],
    },
}

# Hard cap on GetProducts calls -- the API is slow and paginated; ten
# services is already generous for 또박's own stack.
MAX_LOOKUPS = 10


def _extract_usd_per_unit(price_item):
    """Pull the first On-Demand USD/unit rate out of one GetProducts price item."""
    terms = price_item.get("terms", {}).get("OnDemand", {})
    for term in terms.values():
        for dim in term.get("priceDimensions", {}).values():
            usd = dim.get("pricePerUnit", {}).get("USD")
            if usd is not None:
                try:
                    return float(usd), dim.get("unit", "")
                except (TypeError, ValueError):
                    continue
    return None, None


def normalize_price_response(price_list_json):
    """Parse one GetProducts response's `PriceList` (raw JSON strings) into
    {sku: {unit, usd, attributes}}. Raises on an empty/malformed PriceList --
    a silent {} would let a downstream simulation quietly run with no price
    for a service it thinks it queried."""
    import json

    price_list = price_list_json.get("PriceList")
    if not price_list:
        raise ValueError("empty PriceList in GetProducts response")

    out = {}
    for raw in price_list:
        item = json.loads(raw) if isinstance(raw, str) else raw
        product = item.get("product", {})
        sku = product.get("sku")
        if not sku:
            continue
        usd, unit = _extract_usd_per_unit(item)
        if usd is None:
            continue
        out[sku] = {
            "usd": usd,
            "unit": unit,
            "attributes": product.get("attributes", {}),
        }
    if not out:
        raise ValueError("PriceList contained no usable On-Demand price entries")
    return out


def fetch_unit_prices(client=None):
    """Queries SERVICE_FILTERS against the Price List API and returns a
    snapshot: {service_key: {label, prices: {sku: {...}}}, retrievedAt}.

    A per-service failure is recorded under that service's key rather than
    aborting the whole snapshot -- a run that only needs DynamoDB pricing
    shouldn't fail because an unrelated S3 filter drifted.
    """
    client = client or boto3.client("pricing", region_name=PRICING_REGION)
    snapshot = {"retrievedAt": None, "services": {}}

    for i, (key, cfg) in enumerate(SERVICE_FILTERS.items()):
        if i >= MAX_LOOKUPS:
            break
        try:
            resp = client.get_products(
                ServiceCode=cfg["serviceCode"],
                Filters=cfg["filters"],
                MaxResults=20,
            )
            snapshot["services"][key] = {
                "label": cfg["label"],
                "serviceCode": cfg["serviceCode"],
                "prices": normalize_price_response(resp),
            }
        except Exception as e:  # noqa: BLE001 -- one bad service must not sink the run
            logger.warning("pricing lookup failed for %s: %s", key, e)
            snapshot["services"][key] = {
                "label": cfg["label"],
                "serviceCode": cfg["serviceCode"],
                "error": str(e),
            }

    snapshot["retrievedAt"] = _iso_now()
    return snapshot


def _iso_now():
    from datetime import datetime, timezone

    return datetime.now(timezone.utc).isoformat()
