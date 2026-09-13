"""User-bound asynchronous QA jobs; execution claims are never taken over."""
from contextlib import contextmanager
from decimal import Decimal
import hashlib
import json
import logging
import re
import signal
import time
import uuid

from boto3.dynamodb.conditions import Attr

from source_revision import IDENTIFIER
from session_provenance import SourceValidationError

logger = logging.getLogger(__name__)
JOB_ID = re.compile(r"(\d{13})-[0-9a-f]{32}")
INPUT_LIMIT = 256 * 1024
RESULT_LIMIT = 320 * 1024
PROOF_LIMIT = 128 * 1024
RETENTION_SECONDS = 3600
TOTAL_SECONDS = 600
RUN_SECONDS = 240
DISPATCH_SECONDS = 10
API_SECONDS = 15
VERSION = 1


class JobError(Exception):
    def __init__(self, code, message, status=503):
        super().__init__(message)
        self.code, self.message, self.status = code, message, status


class JobDeadline(BaseException):
    """Bypass legacy catch-all tool/model fallbacks when the job must stop."""


@contextmanager
def deadline(seconds):
    def expired(*_):
        raise JobDeadline()
    previous = signal.signal(signal.SIGALRM, expired)
    timer = signal.setitimer(signal.ITIMER_REAL, max(0.001, seconds))
    started = time.monotonic()
    try:
        yield
    finally:
        remaining = max(0.001, timer[0] - (time.monotonic() - started)) if timer[0] else 0
        signal.setitimer(signal.ITIMER_REAL, remaining, timer[1])
        signal.signal(signal.SIGALRM, previous)


def _decimal(value):
    if isinstance(value, Decimal) and value.is_finite() and value == value.to_integral_value():
        return int(value)
    raise TypeError('Unsupported JSON value')


def encode(value, limit):
    raw = json.dumps(value, ensure_ascii=False, separators=(',', ':'), sort_keys=True,
                     allow_nan=False, default=_decimal)
    if len(raw.encode()) > limit:
        raise JobError('QA_PAYLOAD_TOO_LARGE', 'QA data exceeds the asynchronous storage limit.', 413)
    return raw


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def identity(user_id, job_id):
    if (type(user_id) is not str or not IDENTIFIER.fullmatch(user_id)
            or type(job_id) is not str or not JOB_ID.fullmatch(job_id)):
        raise JobError('INVALID_JOB_ID', 'Invalid QA job identity.', 400)
    return {'PK': 'USER#' + user_id, 'SK': 'QA_JOB#' + job_id}


def request_data(body):
    if type(body) is not dict or set(body) - {'requestId', 'mode', 'question', 'context', 'meetingId', 'sessionId'}:
        raise JobError('INVALID_JOB_REQUEST', 'Invalid asynchronous QA request.', 400)
    request = {key: body.get(key) for key in ('question', 'context', 'meetingId', 'sessionId')}
    request['mode'] = body.get('mode', 'ask')
    if type(request['question']) is not str or not request['question'].strip():
        raise JobError('INVALID_JOB_REQUEST', 'question is required.', 400)
    request['question'] = request['question'].strip()
    for key in ('meetingId', 'sessionId'):
        if request[key] == '':
            request[key] = None
        if request[key] is not None and (type(request[key]) is not str or not IDENTIFIER.fullmatch(request[key])):
            raise JobError('INVALID_JOB_REQUEST', 'Invalid meeting or session identifier.', 400)
    if request['context'] is not None and type(request['context']) is not str:
        raise JobError('INVALID_JOB_REQUEST', 'context must be text.', 400)
    if (request['mode'] not in ('ask', 'meeting') or request['mode'] == 'meeting'
            and (not request['meetingId'] or request['context'] is not None)):
        raise JobError('INVALID_JOB_REQUEST', 'Invalid meeting QA request.', 400)
    return request


def proof_data(state):
    # A later GET cannot safely release an answer with untracked private reads.
    if state.get('replayable') is not True:
        raise JobError('QA_RESULT_UNVERIFIABLE', 'The answer lacks complete source proof. Check this job before submitting again.')
    return {'dependencies': state['dependencies'], 'replayable': True,
            'requestMeetingId': state.get('requestMeetingId')}


class QAJobs:
    def __init__(self, table, queue, queue_url, *, clock=time.time):
        self.table, self.queue, self.queue_url, self.clock = table, queue, queue_url, clock

    def now(self):
        return int(self.clock())

    def _get(self, key):
        return self.table.get_item(Key=key, ConsistentRead=True).get('Item')

    def _update(self, key, fields, condition):
        names = {f'#j{i}': name for i, name in enumerate(fields)}
        values = {f':j{i}': value for i, value in enumerate(fields.values())}
        return self.table.update_item(
            Key=key, UpdateExpression='SET ' + ', '.join(f'#j{i} = :j{i}' for i in range(len(fields))),
            ExpressionAttributeNames=names, ExpressionAttributeValues=values, ConditionExpression=condition)

    def _owned(self, user_id, job_id):
        key = identity(user_id, job_id)
        job = self._get(key)
        if not job or job.get('entityType') != 'QA_JOB' or job.get('userId') != user_id or job.get('jobId') != job_id:
            raise JobError('QA_JOB_NOT_FOUND', 'QA job not found.', 404)
        if job.get('version') != VERSION:
            raise JobError('QA_JOB_UNAVAILABLE', 'QA job version is unavailable.')
        if int(job['pendingShareExpiresAt']) <= self.now():
            raise JobError('QA_JOB_EXPIRED', 'QA job expired. It will not be executed again.', 410)
        return job

    @staticmethod
    def ticket(job):
        result = {'jobId': job['jobId'], 'status': job['status'].lower(),
                  'deadlineAt': int(job['deadlineAt']), 'expiresAt': int(job['pendingShareExpiresAt']),
                  'pollAfterMs': 1000}
        if job.get('errorCode'):
            result['error'] = {'code': job['errorCode'], 'message': job['errorMessage']}
        return result

    def _fail(self, job, code, message, *, running=False):
        condition = Attr('status').eq('RUNNING' if running else 'QUEUED')
        if running:
            condition &= Attr('runId').eq(job['runId'])
        try:
            self._update(identity(job['userId'], job['jobId']),
                         {'status': 'FAILED', 'errorCode': code, 'errorMessage': message},
                         condition)
        except self.table.meta.client.exceptions.ConditionalCheckFailedException:
            pass  # A successful publication must not be overwritten by a late failure.

    def _expire(self, job):
        now = self.now()
        if job['status'] == 'QUEUED' and int(job['deadlineAt']) <= now:
            self._fail(job, 'QA_JOB_NOT_STARTED', 'The QA job expired before execution.')
            return self._owned(job['userId'], job['jobId'])
        if job['status'] == 'RUNNING' and min(int(job['runUntil']), int(job['deadlineAt'])) <= now:
            self._fail(job, 'QA_JOB_INTERRUPTED',
                       'QA execution was interrupted; actions may have completed. Do not automatically resubmit.',
                       running=True)
            return self._owned(job['userId'], job['jobId'])
        return job

    def _dispatch(self, job):
        now = self.now()
        if job['status'] != 'QUEUED' or int(job['deadlineAt']) <= now:
            return
        condition = (Attr('status').eq('QUEUED') & Attr('deadlineAt').gt(now)
                     & (Attr('dispatchAfter').not_exists() | Attr('dispatchAfter').lte(now)))
        try:
            self._update(identity(job['userId'], job['jobId']), {'dispatchAfter': now + DISPATCH_SECONDS}, condition)
        except self.table.meta.client.exceptions.ConditionalCheckFailedException:
            return
        try:
            self.queue.send_message(QueueUrl=self.queue_url,
                MessageBody=encode({'version': VERSION, 'userId': job['userId'], 'jobId': job['jobId']}, 2048))
        except Exception as error:
            # The durable row remains QUEUED. A poll/repeated identical submit
            # can republish the pointer; duplicate delivery cannot reclaim RUNNING.
            logger.warning('QA queue publication not confirmed (%s)', type(error).__name__)

    def submit(self, user_id, body):
        job_id = body.get('requestId') if type(body) is dict else None
        key = identity(user_id, job_id)
        request_json = encode(request_data(body), INPUT_LIMIT)
        request_hash = digest(request_json)
        existing = self._get(key)
        if existing:
            job = self._owned(user_id, job_id)
            if job['requestHash'] != request_hash:
                raise JobError('QA_JOB_CONFLICT', 'This request ID is already bound to another request.', 409)
        else:
            now = self.now()
            issued = int(JOB_ID.fullmatch(job_id).group(1)) // 1000
            if not -60 <= now - issued <= 300:
                raise JobError('QA_JOB_EXPIRED', 'An expired request ID cannot create another execution.', 410)
            job = {**key, 'entityType': 'QA_JOB', 'version': VERSION, 'userId': user_id, 'jobId': job_id,
                   'requestJson': request_json, 'requestHash': request_hash, 'status': 'QUEUED',
                   'createdAt': now, 'deadlineAt': now + TOTAL_SECONDS,
                   'pendingShareExpiresAt': now + RETENTION_SECONDS}
            try:
                self.table.put_item(Item=job, ConditionExpression=Attr('PK').not_exists())
            except self.table.meta.client.exceptions.ConditionalCheckFailedException:
                job = self._owned(user_id, job_id)
                if job['requestHash'] != request_hash:
                    raise JobError('QA_JOB_CONFLICT', 'This request ID is already bound to another request.', 409)
        job = self._expire(job)
        self._dispatch(job)
        return self.ticket(job)

    def _artifact(self, job, kind, raw):
        item = {'PK': 'USER#' + job['userId'], 'SK': f'QA_{kind}#' + job['jobId'],
                'userId': job['userId'], 'jobId': job['jobId'], 'runId': job['runId'],
                'payload': raw, 'sha256': digest(raw), 'pendingShareExpiresAt': job['pendingShareExpiresAt']}
        try:
            self.table.put_item(Item=item, ConditionExpression=Attr('PK').not_exists())
        except Exception:
            # Confirm a possibly committed write; never regenerate the answer.
            found = self._get({'PK': item['PK'], 'SK': item['SK']})
            if found != item:
                raise
        return item['sha256']

    def _read_artifact(self, job, kind, expected_hash, limit):
        item = self._get({'PK': 'USER#' + job['userId'], 'SK': f'QA_{kind}#' + job['jobId']})
        if (not item or item.get('userId') != job['userId'] or item.get('jobId') != job['jobId']
                or item.get('runId') != job['runId'] or int(item.get('pendingShareExpiresAt', 0)) <= self.now()):
            raise JobError('QA_RESULT_UNAVAILABLE', 'QA result is unavailable.')
        raw = item.get('payload')
        if type(raw) is not str or len(raw.encode()) > limit or digest(raw) != expected_hash or item.get('sha256') != expected_hash:
            raise JobError('QA_RESULT_UNAVAILABLE', 'QA result integrity could not be verified.')
        return json.loads(raw)

    def poll(self, user_id, job_id, validate):
        job = self._expire(self._owned(user_id, job_id))
        self._dispatch(job)
        ticket = self.ticket(job)
        if job['status'] == 'SUCCEEDED':
            state = self._read_artifact(job, 'PROOF', job['proofHash'], PROOF_LIMIT)
            if state.get('replayable') is not True:
                raise JobError('QA_RESULT_UNVERIFIABLE', 'Current source proof is unavailable.')
            validate(user_id, state)  # Auth/source checks precede loading the cached body.
            result = self._read_artifact(job, 'RESULT', job['resultHash'], RESULT_LIMIT)
            validate(user_id, state)
            ticket['result'] = result
        return ticket

    def work(self, user_id, job_id, execute, validate, *, remaining_seconds=RUN_SECONDS + 10):
        try:
            job = self._expire(self._owned(user_id, job_id))
        except JobError as error:
            if error.status in (404, 410):
                return  # A delayed event cannot resurrect an expired job.
            raise
        if job['status'] != 'QUEUED':
            return
        now, run_id = self.now(), uuid.uuid4().hex
        run_until = min(int(job['deadlineAt']), now + RUN_SECONDS, now + max(0, int(remaining_seconds) - 10))
        if run_until <= now:
            self._fail(job, 'QA_JOB_NOT_STARTED', 'No execution time remained for this QA job.')
            return
        changes = {'status': 'RUNNING', 'runId': run_id, 'runUntil': run_until}
        try:
            self._update(identity(user_id, job_id), changes,
                         Attr('status').eq('QUEUED') & Attr('deadlineAt').gt(now))
        except self.table.meta.client.exceptions.ConditionalCheckFailedException:
            return
        except Exception:
            confirmed = self._owned(user_id, job_id)
            if confirmed.get('status') != 'RUNNING' or confirmed.get('runId') != run_id:
                raise
            # This invocation has not called the model/tool executor yet.
        job.update(changes)
        try:
            with deadline(run_until - self.clock()):
                if self.clock() >= run_until:
                    raise JobDeadline()
                raw = job['requestJson']
                if len(raw.encode()) > INPUT_LIMIT or digest(raw) != job['requestHash']:
                    raise JobError('QA_REQUEST_UNAVAILABLE', 'Stored QA request integrity failed.')
                state = {'dependencies': [], 'replayable': True}
                result = execute(user_id, json.loads(raw), state)
                validate(user_id, state)
                result_json = encode(result, RESULT_LIMIT)
                proof_json = encode(proof_data(state), PROOF_LIMIT)
                result_hash = self._artifact(job, 'RESULT', result_json)
                proof_hash = self._artifact(job, 'PROOF', proof_json)
                validate(user_id, state)
                self._update(identity(user_id, job_id),
                    {'status': 'SUCCEEDED', 'resultHash': result_hash, 'proofHash': proof_hash},
                    Attr('status').eq('RUNNING') & Attr('runId').eq(run_id)
                    & Attr('runUntil').gt(self.now()) & Attr('deadlineAt').gt(self.now()))
        except JobDeadline:
            self._fail(job, 'QA_JOB_INTERRUPTED',
                       'QA execution exceeded its deadline; actions may have completed. Do not automatically resubmit.',
                       running=True)
        except Exception as error:
            # Storage may have accepted final publication before its response
            # was lost. Failure CAS cannot overwrite SUCCEEDED or restart work.
            safe = isinstance(error, (JobError, SourceValidationError))
            code = error.code if safe else 'QA_EXECUTION_FAILED'
            message = (error.message if safe else
                       'QA execution failed; actions may have completed. Check this job before submitting again.')
            self._fail(job, code, message, running=True)
            logger.warning('QA job execution ended without confirmed completion (%s)', type(error).__name__)
