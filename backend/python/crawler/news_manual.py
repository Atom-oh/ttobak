"""Manual "collect now" starter for one account-owned news source (ADR-004).

Claims the API-claimed request on the CONFIG row and starts the versioned
publication workflow once as manual-{requestId}. A refusal before the claim
releases the API's hourly claim and records a COLLECTREFUSAL# receipt. It never
searches, fetches or calls a model; the child run owns that and its budgets.
"""

import json
import logging
import os
import re
import time
from datetime import datetime, timezone

import boto3
from botocore.exceptions import ClientError

from news_runtime import SDK_CONFIG, _enabled, _policy
from news_store import ACCOUNTING_RETENTION_SECONDS, StateRejected

logger = logging.getLogger()
logger.setLevel(logging.INFO)

MANUAL_COLLECT_KEY = 'manualCollect'
EXECUTION_PREFIX = 'manual-'
# Same grammar the legacy manual branch accepted. manual- plus 64 characters
# stays within the 80-character Step Functions name and the run-ID grammar.
_ID_RE = re.compile(r'[A-Za-z0-9-]{1,64}')
_SOURCE_SUFFIX_RE = re.compile(r'[0-9a-f]{8}')

# The API claims a request by setting lastCollectRequestId (and
# lastCollectRequestedAt) on the CONFIG row; this starter marks it started with
# lastCollectStartedRequestId/lastCollectStartedAt. These attributes are
# outside the source fingerprint (news_discovery.source_snapshot and Go
# newsSourceRevision hash only an allowlist) and news_store.CONFIG_FIELDS.
CLAIM_CONDITION = (
    'attribute_exists(PK) AND accountId = :acc '
    'AND (attribute_not_exists(#status) OR #status <> :disabled) '
    'AND attribute_not_exists(globalNewsSource) '
    'AND lastCollectRequestId = :rid '
    'AND (attribute_not_exists(lastCollectStartedRequestId) '
    'OR lastCollectStartedRequestId <> :rid)'
)
CLAIM_UPDATE = 'SET lastCollectStartedRequestId = :rid, lastCollectStartedAt = :now'
# Release of the API's hourly claim after a refusal that is not the request's
# fault, so the member can ask again. Like Go ReleaseAccountNewsCollect it
# removes only this request's claim; it also requires the row/account and a
# request this starter has not started.
RELEASE_CONDITION = (
    'attribute_exists(PK) AND accountId = :acc AND lastCollectRequestId = :rid '
    'AND (attribute_not_exists(lastCollectStartedRequestId) '
    'OR lastCollectStartedRequestId <> :rid)'
)
RELEASE_UPDATE = 'REMOVE lastCollectRequestedAt, lastCollectRequestId'
# Refusal receipt: same partition, an SK no history/document reader matches
# (they query HISTORY#, DOC#, CONFIG or NEWS* prefixes), ordered by time:
# COLLECTREFUSAL#{UTC second}#{requestId}. At most one per source per
# RECEIPT_INTERVAL_SECONDS; releases are not rate-limited.
REFUSAL_PREFIX = 'COLLECTREFUSAL#'
RECEIPT_INTERVAL_SECONDS = 300
_RECEIPT_TIME = '%Y-%m-%dT%H:%M:%SZ'


def _table():
    return boto3.resource('dynamodb', config=SDK_CONFIG).Table(os.environ['TABLE_NAME'])


def _sfn():
    return boto3.client('stepfunctions', config=SDK_CONFIG)


def _result(status, code='', request_id=''):
    # Codes and the request UUID only; never names, queries or URLs.
    logger.info(f'manual collect: outcome={status} code={code or "-"} '
                f'requestId={request_id or "-"}')
    result = {'status': status}
    if code:
        result['code'] = code
    if request_id:
        result['requestId'] = request_id
    return result


def parse_request(event):
    """Validated (sourceId, accountId, requestId), or None."""
    if not isinstance(event, dict) or set(event) != {MANUAL_COLLECT_KEY}:
        return None
    request = event[MANUAL_COLLECT_KEY]
    if not isinstance(request, dict) or set(request) != {'sourceId', 'accountId', 'requestId'}:
        return None
    source_id, account_id, request_id = (request[k] for k in ('sourceId', 'accountId', 'requestId'))
    if not all(isinstance(v, str) for v in (source_id, account_id, request_id)):
        return None
    if not _ID_RE.fullmatch(account_id) or not _ID_RE.fullmatch(request_id):
        return None
    prefix = f'acct_{account_id}_'
    if not source_id.startswith(prefix) or not _SOURCE_SUFFIX_RE.fullmatch(source_id[len(prefix):]):
        return None
    return source_id, account_id, request_id


def daily_window(now):
    """True from the lead (the child timeout) before the daily schedule until
    the grace after it, which covers a delayed daily start; wraps at midnight.

    Raises ValueError on missing/invalid configuration, which the caller
    refuses: all four values come from the CDK schedule and constants.
    """
    values = [os.environ.get(k, '') for k in ('NEWS_DAILY_HOUR_UTC', 'NEWS_DAILY_MINUTE_UTC',
                                              'NEWS_DAILY_LEAD_SECONDS', 'NEWS_DAILY_GRACE_SECONDS')]
    if not all(re.fullmatch(r'[0-9]{1,5}', v) for v in values):
        raise ValueError('INVALID_CONFIGURATION')
    hour, minute, lead, grace = (int(v) for v in values)
    if hour > 23 or minute > 59 or not 0 < lead < 43200 or not 0 < grace < 43200:
        raise ValueError('INVALID_CONFIGURATION')
    # Signed seconds since the nearest schedule time, in [-43200, 43200).
    offset = (int(now) - hour * 3600 - minute * 60 + 43200) % 86400 - 43200
    return -lead <= offset < grace


def _running(sfn, variable):
    # Errors propagate: no claim, no start; the failure queue records it.
    response = sfn.list_executions(stateMachineArn=os.environ[variable],
                                   statusFilter='RUNNING', maxResults=1)
    return bool(response.get('executions'))


def claim(table, source_id, account_id, request_id):
    """Atomically mark one API-claimed request started; False if refused."""
    try:
        table.update_item(
            Key={'PK': f'CRAWLER#{source_id}', 'SK': 'CONFIG'},
            ConditionExpression=CLAIM_CONDITION,
            UpdateExpression=CLAIM_UPDATE,
            ExpressionAttributeNames={'#status': 'status'},
            ExpressionAttributeValues={
                ':acc': account_id, ':rid': request_id, ':disabled': 'disabled',
                ':now': datetime.now(timezone.utc).isoformat(),
            },
        )
        return True
    except ClientError as e:
        if e.response.get('Error', {}).get('Code') != 'ConditionalCheckFailedException':
            raise
        return False


def refusal_code(table, source_id, account_id, request_id):
    """Classify a failed claim for the result/log only; never starts work."""
    try:
        item = table.get_item(Key={'PK': f'CRAWLER#{source_id}', 'SK': 'CONFIG'},
                              ConsistentRead=True).get('Item')
    except Exception:
        return 'CLAIM_REJECTED'
    if not item:
        return 'NOT_FOUND'
    if item.get('lastCollectStartedRequestId') == request_id:
        return 'DUPLICATE_REQUEST'
    if item.get('lastCollectRequestId') != request_id:
        return 'NOT_CLAIMED'
    if item.get('accountId') != account_id:
        return 'ACCOUNT_MISMATCH'
    if item.get('status') == 'disabled':
        return 'SOURCE_DISABLED'
    if 'globalNewsSource' in item:
        return 'OPERATOR_SOURCE'
    return 'CLAIM_REJECTED'


def release(table, source_id, account_id, request_id, code):
    """Release the API's claim and record a refusal receipt; never raises."""
    key = {'PK': f'CRAWLER#{source_id}', 'SK': 'CONFIG'}
    try:
        table.update_item(Key=key, ConditionExpression=RELEASE_CONDITION,
                          UpdateExpression=RELEASE_UPDATE,
                          ExpressionAttributeValues={':acc': account_id, ':rid': request_id})
    except ClientError as e:
        reason = e.response.get('Error', {}).get('Code', 'ERROR')
        logger.warning(f'manual collect: release={reason} requestId={request_id}')
        if reason == 'ConditionalCheckFailedException':
            # Not this request's live claim (already started, replaced or
            # gone): a receipt would misdescribe it.
            return
    except Exception as e:  # The claim then expires after the hour.
        logger.warning(f'manual collect: release={type(e).__name__} requestId={request_id}')
    now = int(time.time())
    pk = f'CRAWLER#{source_id}'
    try:
        # A long refusal condition must not write a 400-day receipt per request.
        latest = table.query(
            KeyConditionExpression='PK = :pk AND begins_with(SK, :prefix)',
            ExpressionAttributeValues={':pk': pk, ':prefix': REFUSAL_PREFIX},
            ScanIndexForward=False, Limit=1, ConsistentRead=True).get('Items') or []
        if latest:
            last = datetime.strptime(latest[0]['SK'][len(REFUSAL_PREFIX):][:20], _RECEIPT_TIME)
            if now - last.replace(tzinfo=timezone.utc).timestamp() < RECEIPT_INTERVAL_SECONDS:
                logger.info(f'manual collect: receipt=RECENT requestId={request_id}')
                return
        stamp = datetime.fromtimestamp(now, timezone.utc).strftime(_RECEIPT_TIME)
        table.put_item(Item={
            'PK': pk, 'SK': f'{REFUSAL_PREFIX}{stamp}#{request_id}',
            'requestId': request_id, 'code': code, 'refusedAt': stamp,
            'pendingShareExpiresAt': now + ACCOUNTING_RETENTION_SECONDS,
        }, ConditionExpression='attribute_not_exists(PK)')
    except Exception as e:
        logger.warning(f'manual collect: receipt={type(e).__name__} requestId={request_id}')


def _refuse(source_id, account_id, request_id, code):
    release(_table(), source_id, account_id, request_id, code)
    return _result('REFUSED', code, request_id)


def handler(event, context):
    request = parse_request(event)
    if request is None:
        # No trustworthy IDs: nothing is released or recorded.
        return _result('REFUSED', 'INVALID_REQUEST')
    source_id, account_id, request_id = request
    # Every refusal before the claim below is not the request's fault: it
    # releases the API's hourly claim and records a receipt (_refuse).
    # The versioned handlers' own activation and pricing gates first.
    try:
        _enabled(source_id)
        _policy(int(time.time()))
    except StateRejected as error:
        return _refuse(source_id, account_id, request_id, error.reason)
    # Discovery has reserved concurrency 1; a throttled discovery waits for the
    # slot (bounded retry). A manual run near the daily schedule (from the
    # child timeout before until the grace after, for a delayed start) would
    # delay the daily batch, so it is refused.
    try:
        in_window = daily_window(time.time())
    except ValueError:
        return _refuse(source_id, account_id, request_id, 'INVALID_CONFIGURATION')
    if in_window:
        return _refuse(source_id, account_id, request_id, 'DAILY_WINDOW')
    sfn = _sfn()
    if _running(sfn, 'DAILY_STATE_MACHINE_ARN'):
        return _refuse(source_id, account_id, request_id, 'DAILY_RUNNING')
    # Cost and fairness guards, not correctness: a manual run would otherwise
    # queue for discovery behind or inside an active run. These are
    # check-then-start; a run beginning after them only makes the later
    # discovery wait for the slot.
    if _running(sfn, 'PUBLICATION_STATE_MACHINE_ARN'):
        return _refuse(source_id, account_id, request_id, 'RUN_IN_PROGRESS')
    table = _table()
    if not claim(table, source_id, account_id, request_id):
        return _result('REFUSED', refusal_code(table, source_id, account_id, request_id), request_id)
    # The child ignores runId and binds its run to the execution name, exactly
    # as for the daily parent's per-source StartExecution.
    name = EXECUTION_PREFIX + request_id
    try:
        sfn.start_execution(stateMachineArn=os.environ['PUBLICATION_STATE_MACHINE_ARN'], name=name,
                            input=json.dumps({'sourceId': source_id, 'runId': name}))
    except ClientError as e:
        if e.response.get('Error', {}).get('Code') != 'ExecutionAlreadyExists':
            # Do not undo the started marker: a duplicate delivery must not
            # create a second paid run. The request is spent; the failure
            # queue records it and the hourly claim expires normally.
            logger.error('manual collect: outcome=START_FAILED requestId=%s', request_id)
            raise
        return _result('STARTED', 'ALREADY_STARTED', request_id)
    return _result('STARTED', '', request_id)
