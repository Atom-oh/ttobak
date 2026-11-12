"""Manual "collect now" starter for one account-owned news source (ADR-004).

Claims the API-claimed request on the CONFIG row and starts the versioned
publication workflow once as manual-{requestId}. It never searches, fetches
or calls a model; the child run owns that and its run and monthly budgets.
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
from news_store import StateRejected

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


def _daily_running(sfn):
    # Errors propagate: no claim, no start; the failure queue records it.
    response = sfn.list_executions(stateMachineArn=os.environ['DAILY_STATE_MACHINE_ARN'],
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


def handler(event, context):
    request = parse_request(event)
    if request is None:
        return _result('REFUSED', 'INVALID_REQUEST')
    source_id, account_id, request_id = request
    # The versioned handlers' own activation and pricing gates, before any SDK
    # call: a request they would reject is refused here and not consumed.
    try:
        _enabled(source_id)
        _policy(int(time.time()))
    except StateRejected as error:
        return _result('REFUSED', error.reason, request_id)
    sfn = _sfn()
    # Backstop for the API's daily window: an overlapping daily run would fail
    # the later reservation of a shared article. Refused before the claim, so
    # nothing is consumed.
    if _daily_running(sfn):
        return _result('REFUSED', 'DAILY_RUNNING', request_id)
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
            # queue records it and the API's hourly claim expires normally.
            logger.error('manual collect: outcome=START_FAILED requestId=%s', request_id)
            raise
        return _result('STARTED', 'ALREADY_STARTED', request_id)
    return _result('STARTED', '', request_id)
