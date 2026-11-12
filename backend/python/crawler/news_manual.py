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
# (they query HISTORY#, DOC#, CONFIG or NEWS* prefixes). One per request.
REFUSAL_PREFIX = 'COLLECTREFUSAL#'


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
    """True from the lead (the child timeout) before the daily schedule until it.

    Raises ValueError on missing/invalid configuration, which the caller
    refuses: hour, minute and lead come from the CDK schedule and timeout.
    """
    values = [os.environ.get(k, '') for k in
              ('NEWS_DAILY_HOUR_UTC', 'NEWS_DAILY_MINUTE_UTC', 'NEWS_DAILY_LEAD_SECONDS')]
    if not all(re.fullmatch(r'[0-9]{1,5}', v) for v in values):
        raise ValueError('INVALID_CONFIGURATION')
    hour, minute, lead = (int(v) for v in values)
    if hour > 23 or minute > 59 or not 0 < lead < 86400:
        raise ValueError('INVALID_CONFIGURATION')
    until_schedule = (hour * 3600 + minute * 60 - int(now) % 86400) % 86400
    return 0 < until_schedule <= lead


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
    try:
        table.put_item(Item={
            'PK': f'CRAWLER#{source_id}', 'SK': REFUSAL_PREFIX + request_id,
            'requestId': request_id, 'code': code,
            'refusedAt': datetime.fromtimestamp(now, timezone.utc).isoformat(),
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
    # Discovery has reserved concurrency 1 and no invoke retries. A run
    # started within the child timeout before the daily schedule would hold
    # that slot when the daily parent's children begin and throttle them.
    try:
        in_window = daily_window(time.time())
    except ValueError:
        return _refuse(source_id, account_id, request_id, 'INVALID_CONFIGURATION')
    if in_window:
        return _refuse(source_id, account_id, request_id, 'DAILY_WINDOW')
    sfn = _sfn()
    if _running(sfn, 'DAILY_STATE_MACHINE_ARN'):
        return _refuse(source_id, account_id, request_id, 'DAILY_RUNNING')
    # A run started beside any discovering child would be throttled after the
    # claim. This is check-then-start: two starters cannot interleave
    # (reserved concurrency 1), but a daily child or another run can still
    # begin between these checks and this run's discovery. That residual
    # window is UNMITIGATED: the later discovery is throttled. If it is this
    # run, the request is spent with no run and no receipt; if it is a daily
    # child, that source fails for the day.
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
