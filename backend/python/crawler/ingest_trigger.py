"""Ingest Trigger Lambda — starts a Bedrock Knowledge Base ingestion job.

Triggered by Step Functions after crawlers have written new documents to S3.
Kicks off the KB sync so newly crawled content becomes searchable.
"""

import json
import logging
import os

import boto3

logger = logging.getLogger()
logger.setLevel(logging.INFO)

KB_ID = os.environ.get('KB_ID', '')
DATA_SOURCE_ID = os.environ.get('DATA_SOURCE_ID', '')

bedrock_agent = boto3.client('bedrock-agent')


def handler(event, context):
    """Trigger Bedrock KB ingestion job.

    Expected event (from Step Functions aggregation of crawler results):
      {
        "crawlerResults": [
          {"docsAdded": 5, "docsUpdated": 0, "errors": []},
          ...
        ]
      }

    Returns:
      {
        "status": "STARTED" | "SKIPPED" | "ERROR",
        "ingestionJobId": "...",
        "totalDocsAdded": N,
        "totalErrors": N
      }
    """
    # Aggregate crawler results to decide if ingestion is needed
    crawler_results = event.get('crawlerResults', [])
    total_added = sum(r.get('docsAdded', 0) for r in crawler_results)
    total_updated = sum(r.get('docsUpdated', 0) for r in crawler_results)
    total_errors = sum(len(r.get('errors', [])) for r in crawler_results)

    logger.info(f'Ingest trigger: {total_added} added, {total_updated} updated, '
                f'{total_errors} errors across {len(crawler_results)} crawler(s)')

    # Skip ingestion if no new documents were added or updated
    if total_added == 0 and total_updated == 0:
        logger.info('No new documents — skipping ingestion')
        return {
            'status': 'SKIPPED',
            'ingestionJobId': None,
            'totalDocsAdded': 0,
            'totalErrors': total_errors,
        }

    if not KB_ID or not DATA_SOURCE_ID:
        logger.error('KB_ID or DATA_SOURCE_ID not configured')
        return {
            'status': 'ERROR',
            'ingestionJobId': None,
            'error': 'KB_ID or DATA_SOURCE_ID environment variable not set',
            'totalDocsAdded': total_added,
            'totalErrors': total_errors,
        }

    try:
        resp = bedrock_agent.start_ingestion_job(
            knowledgeBaseId=KB_ID,
            dataSourceId=DATA_SOURCE_ID,
        )
        job = resp.get('ingestionJob', {})
        job_id = job.get('ingestionJobId', 'unknown')
        status = job.get('status', 'UNKNOWN')

        logger.info(f'Ingestion job started: id={job_id}, status={status}')
        return {
            'status': 'STARTED',
            'ingestionJobId': job_id,
            'ingestionStatus': status,
            'totalDocsAdded': total_added,
            'totalErrors': total_errors,
        }
    except Exception as e:
        logger.error(f'Failed to start ingestion job: {e}', exc_info=True)
        return {
            'status': 'ERROR',
            'ingestionJobId': None,
            'error': str(e),
            'totalDocsAdded': total_added,
            'totalErrors': total_errors,
        }
