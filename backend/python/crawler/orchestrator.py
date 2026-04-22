"""Orchestrator Lambda — scans DynamoDB for all active crawler sources.

Triggered by Step Functions as the first step in the crawler pipeline.
Returns a list of source configs for the Map state to fan out.
"""

import json
import logging
import os

import boto3
from boto3.dynamodb.conditions import Key, Attr

logger = logging.getLogger()
logger.setLevel(logging.INFO)

TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')

dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)


def handler(event, context):
    """Scan for all active crawler source configs.

    DynamoDB schema:
      PK = CRAWLER#{sourceId}
      SK = CONFIG
      status = "active" | "disabled"
      + source-specific fields (type, awsServices, newsQueries, etc.)
    """
    logger.info('Orchestrator: scanning for active crawler sources')

    sources = []
    scan_kwargs = {
        'FilterExpression': (
            Attr('PK').begins_with('CRAWLER#')
            & Attr('SK').eq('CONFIG')
            & Attr('status').ne('disabled')
        ),
    }

    try:
        while True:
            resp = table.scan(**scan_kwargs)
            items = resp.get('Items', [])
            for item in items:
                # Extract sourceId from PK (CRAWLER#{sourceId})
                pk = item.get('PK', '')
                source_id = pk.split('#', 1)[1] if '#' in pk else pk
                sources.append({
                    'sourceId': source_id,
                    'sourceName': item.get('sourceName', ''),
                    'type': item.get('type', 'unknown'),
                    'status': item.get('status', 'active'),
                    'awsServices': item.get('awsServices', []),
                    'newsQueries': item.get('newsQueries', []),
                    'customUrls': item.get('customUrls', []),
                    'newsSources': item.get('newsSources', ['google']),
                    'config': {
                        k: v for k, v in item.items()
                        if k not in ('PK', 'SK', 'status', 'type',
                                     'awsServices', 'newsQueries', 'customUrls',
                                     'sourceName', 'newsSources')
                    },
                })

            # Handle pagination
            last_key = resp.get('LastEvaluatedKey')
            if not last_key:
                break
            scan_kwargs['ExclusiveStartKey'] = last_key

    except Exception as e:
        logger.error(f'Failed to scan crawler sources: {e}', exc_info=True)
        return {'sources': [], 'error': str(e)}

    logger.info(f'Orchestrator: found {len(sources)} active source(s)')
    return {'sources': sources}
