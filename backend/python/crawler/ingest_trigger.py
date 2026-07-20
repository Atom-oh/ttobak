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

    Expected event -- the Step Functions ParallelCrawl branch outputs,
    unwrapped: [techResult, [newsResult, ...]] (a bare list; techResult and
    each newsResult are {"docsAdded", "docsUpdated", "errors"} dicts). A
    dict shaped {"crawlerResults": [...]} is also accepted for direct/test
    invocation.

    Returns on success:
      {
        "status": "STARTED" | "SKIPPED",
        "ingestionJobId": "...",
        "totalDocsAdded": N,
        "totalErrors": N
      }

    Raises (rather than returning an "ERROR" status) if KB_ID/DATA_SOURCE_ID
    is unset or start_ingestion_job fails, so the failure surfaces as a
    FAILED Step Functions execution instead of a silently-successful one.
    """
    # Validate config before the SKIPPED short-circuit below -- otherwise a
    # KB_ID/DATA_SOURCE_ID regression goes unnoticed on any night with zero
    # new/updated docs, since the config check would never run and the
    # execution reports SUCCEEDED regardless. 'PENDING' is checked
    # explicitly (not just falsiness): it's the actual placeholder
    # knowledge-stack.ts hardcodes before the real KB/DataSource are wired
    # up (see ADR-021) -- a truthy, non-empty string that `not KB_ID` alone
    # would let straight through.
    if not KB_ID or not DATA_SOURCE_ID or 'PENDING' in (KB_ID, DATA_SOURCE_ID):
        raise RuntimeError(f'KB_ID/DATA_SOURCE_ID not configured (KB_ID={KB_ID!r}, DATA_SOURCE_ID={DATA_SOURCE_ID!r})')

    # Step Functions ParallelCrawl outputs a bare list, one entry per branch
    # (techResult, then the news Map's own list of per-source results) --
    # extend() flattens that one level of list nesting into crawler_results.
    raw = event if isinstance(event, list) else event.get('crawlerResults', [])
    crawler_results = []
    for item in raw:
        if isinstance(item, list):
            crawler_results.extend(item)
        elif isinstance(item, dict):
            crawler_results.append(item)
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

    # Raise (rather than return an "ERROR" result) so a start_ingestion_job
    # failure surfaces as a FAILED Step Functions execution instead of a
    # silently-successful one -- for 7 weeks this returned {"status":
    # "ERROR"} and the workflow kept reporting SUCCEEDED every night while
    # the KB never got the day's crawled docs.

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
