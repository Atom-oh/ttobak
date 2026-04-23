"""Orchestrator Lambda — scans DynamoDB for all active crawler sources.

Triggered by Step Functions as the first step in the crawler pipeline.
Returns:
  - newsSources: per-customer source configs for news crawling (Map fan-out)
  - techConfig: merged AWS services from all sources for a single global tech crawl
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

TECH_SOURCE_ID = '__tech__'


def handler(event, context):
    """Scan for all active crawler source configs.

    Returns two structures:
      - newsSources: list of per-customer configs for news crawling
      - techConfig: single config with merged awsServices for global tech crawling
    """
    logger.info('Orchestrator: scanning for active crawler sources')

    news_sources = []
    all_aws_services = set()

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
                pk = item.get('PK', '')
                source_id = pk.split('#', 1)[1] if '#' in pk else pk

                aws_services = item.get('awsServices', [])
                if aws_services:
                    all_aws_services.update(aws_services)

                news_sources.append({
                    'sourceId': source_id,
                    'sourceName': item.get('sourceName', ''),
                    'newsQueries': item.get('newsQueries', []),
                    'customUrls': item.get('customUrls', []),
                    'newsSources': item.get('newsSources', ['google']),
                })

            last_key = resp.get('LastEvaluatedKey')
            if not last_key:
                break
            scan_kwargs['ExclusiveStartKey'] = last_key

    except Exception as e:
        logger.error(f'Failed to scan crawler sources: {e}', exc_info=True)
        return {'newsSources': [], 'techConfig': {'sourceId': TECH_SOURCE_ID, 'awsServices': []}, 'error': str(e)}

    tech_config = {
        'sourceId': TECH_SOURCE_ID,
        'awsServices': sorted(all_aws_services),
    }

    logger.info(f'Orchestrator: {len(news_sources)} news source(s), '
                f'{len(all_aws_services)} unique AWS service(s) for tech')
    return {
        'newsSources': news_sources,
        'techConfig': tech_config,
    }
