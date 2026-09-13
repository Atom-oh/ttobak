"""Bounded provenance for source-validated conversation history, never raw bodies."""
from decimal import Decimal
import hashlib
import json
import re

from source_revision import resource_identity, legacy_meeting_identity, RUN_ID, IDENTIFIER, FILE_EXTENSIONS
from attachment_context import _location

MAX_SAVED_BYTES = 64 * 1024
MAX_RESTORED_BYTES = 320 * 1024
FIELDS = frozenset((
    'uri', 'resourceKind', 'resourceId', 'sourcePK', 'sourceSK', 'sourceRevision',
    'title', 'titleTruncated', 'contentSource', 'sourceBucket', 'sourceKey', 'ownerId',
    'visibility', 'partial', 'filePending', 'migrationStatus', 'meetingId', 'attachmentId',
    'locations', 'attempt', 'result', 'usingPreviousResult',
))
BOOL_FIELDS = frozenset(('titleTruncated', 'partial', 'filePending', 'usingPreviousResult'))
NESTED_FIELDS = frozenset(('locations', 'attempt', 'result'))


def _number(value):
    if isinstance(value, Decimal) and value.is_finite() and value == value.to_integral_value():
        return int(value)
    raise TypeError('Unsupported provenance value')


def encoded(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':'),
                      allow_nan=False, default=_number)


def binding(user_id, session_id, messages, dependencies, payload):
    return hashlib.sha256(encoded(
        ['history-details-v1', user_id, session_id, messages, dependencies, payload]).encode()).hexdigest()


def identity_detail(dependency, kb_bucket):
    if dependency.get('attachmentId') == '*':
        # An inventory detects added/removed files, but contains no cited body.
        return None
    revision = dependency.get('sourceRevision')
    detail = {'sourceRevision': revision, 'contentSource': 'validated_history_identity',
              'provenanceScope': 'legacy_identity'}
    if 'manualKey' in dependency or 'sharedKey' in dependency:
        shared = 'sharedKey' in dependency
        key = dependency['sharedKey' if shared else 'manualKey']
        detail.update(resourceKind='sharedKbDocument' if shared else 'manualKbDocument',
                      resourceId=hashlib.sha256(key.encode()).hexdigest(),
                      uri='s3://' + kb_bucket + '/' + key, title=key.rsplit('/', 1)[-1][:256])
    elif 'legacyURI' in dependency:
        detail.update(resourceKind='legacyText', uri=dependency['legacyURI'])
    elif 'readOnlyTool' in dependency:
        detail.update(resourceKind='historyToolResult', tool=dependency['readOnlyTool'])
    elif 'researchReceipt' in dependency:
        detail.update(resourceKind='researchReceipt', resourceId=dependency['researchReceipt']['researchId'],
                      contentSource='creation_receipt', provenanceScope='history_receipt')
    elif 'sourcePK' in dependency:
        identity = resource_identity(dependency['sourcePK'], dependency['sourceSK'])
        detail.update({field: identity[field] for field in ('resourceKind', 'resourceId', 'sourcePK', 'sourceSK')})
        detail['uri'] = 'ttobak://source/' + identity['resourceHash']
        if dependency.get('attachmentId'):
            attachment_id = dependency['attachmentId']
            detail.update(resourceKind='meetingAttachment', resourceId=attachment_id,
                          meetingId=identity['resourceId'], attachmentId=attachment_id)
            detail['uri'] += '/attachments/' + attachment_id
    else:
        return None  # Client-input and empty-search receipts are not file citations.
    return detail


def _matches(detail, dep, kb_bucket, assets_bucket):
    if type(detail) is not dict or detail.get('sourceRevision') != dep.get('sourceRevision'):
        return False
    uri = detail.get('uri')
    if type(uri) is not str or len(uri.encode()) > 8192:
        return False
    if 'manualKey' in dep or 'sharedKey' in dep:
        shared = 'sharedKey' in dep
        key = dep['sharedKey' if shared else 'manualKey']
        rid = hashlib.sha256(key.encode()).hexdigest()
        owner = key.split('/')[1] if not shared else None
        prefix = f's3://{kb_bucket}/shared-kb/v1/' if shared else f's3://{kb_bucket}/manual-kb/v1/{owner}/'
        suffix = key.rsplit('.', 1)[-1].lower()
        snapshot = prefix + rid + '/' + dep['sourceRevision'] + '/'
        valid_uri = uri == f's3://{kb_bucket}/{key}' or bool(re.fullmatch(
            re.escape(snapshot) + RUN_ID.pattern + re.escape('/document.' + suffix), uri))
        return (valid_uri and detail.get('resourceKind') == ('sharedKbDocument' if shared else 'manualKbDocument')
                and detail.get('resourceId') == rid and detail.get('sourceKey') == key
                and detail.get('sourceBucket') == kb_bucket
                and (detail.get('visibility') == 'authenticated-shared' and 'ownerId' not in detail if shared
                     else detail.get('ownerId') == owner and 'visibility' not in detail))
    if 'legacyURI' in dep:
        return detail.get('resourceKind') == 'legacyText' and uri == dep['legacyURI']
    if 'sourcePK' not in dep:
        return False
    identity = resource_identity(dep['sourcePK'], dep['sourceSK'])
    if detail.get('sourcePK') != dep['sourcePK'] or detail.get('sourceSK') != dep['sourceSK']:
        return False
    attachment = dep.get('attachmentId')
    if attachment:
        if attachment == '*' or detail.get('resourceKind') != 'meetingAttachment':
            return False
        prefix = 's3://' + assets_bucket + '/files/'
        key = uri[len(prefix):] if uri.startswith(prefix) else ''
        parts = key.split('/')
        return (len(parts) == 3 and IDENTIFIER.fullmatch(parts[0]) is not None
                and parts[1] == identity['resourceId'] and parts[2] not in ('', '.', '..')
                and '\\' not in key and detail.get('resourceId') == attachment
                and detail.get('meetingId') == identity['resourceId'])
    if detail.get('resourceKind') != identity['resourceKind'] or detail.get('resourceId') != identity['resourceId']:
        return False
    if uri == 'ttobak://source/' + identity['resourceHash']:
        return True
    legacy = legacy_meeting_identity(uri, kb_bucket)
    if legacy is not None:
        return legacy == identity
    prefix = f"s3://{kb_bucket}/canonical/v1/{identity['resourceKind']}/{identity['resourceHash']}/"
    tail = uri[len(prefix):] if uri.startswith(prefix) else ''
    run, _, name = tail.partition('/')
    allowed = ('meeting.md',) if identity['resourceKind'] == 'meeting' else (
        'document.md', *('file' + ext for ext in FILE_EXTENSIONS))
    return RUN_ID.fullmatch(run) is not None and name in allowed


def _record(value, strings, booleans=(), integers=()):
    if type(value) is not dict:
        raise ValueError('Invalid detail metadata')
    clean = {}
    for key, data in value.items():
        if key in strings:
            if type(data) is not str or len(data.encode()) > 4096:
                raise ValueError('Invalid detail string')
        elif key in booleans:
            if type(data) is not bool:
                raise ValueError('Invalid detail flag')
        elif key in integers:
            if type(data) is not int:
                raise ValueError('Invalid detail position')
        else:
            continue
        clean[key] = data
    return clean


def _safe_detail(detail, dep, kb_bucket, assets_bucket):
    if not _matches(detail, dep, kb_bucket, assets_bucket):
        return None
    title = detail.get('title')
    if type(title) is str and len(title) > 256:
        detail = dict(detail, title=title[:256], titleTruncated=True)
    clean = _record(detail, FIELDS - BOOL_FIELDS - NESTED_FIELDS, BOOL_FIELDS)
    if 'attempt' in detail:
        clean['attempt'] = _record(detail['attempt'], ('status', 'runId', 'errorCode'))
    if detail.get('result') is not None:
        clean['result'] = _record(detail['result'], ('status', 'runId', 'scope', 'format'), ('complete',))
    if 'locations' in detail:
        if type(detail['locations']) is not list or len(detail['locations']) > 20:
            return None
        clean['locations'] = []
        for location in detail['locations']:
            location = _record(location, ('kind', 'part'), ('hidden',),
                               ('page', 'paragraph', 'startLine', 'endLine', 'slide', 'table', 'row', 'cell'))
            _location(clean.get('result', {}).get('format'), location)
            clean['locations'].append(location)
    # Invalid/oversized metadata falls back to identities, without losing dialogue.
    raw = encoded(clean)
    if len(raw.encode()) > 8192:
        return None
    clean['provenanceScope'] = (
        'legacy_identity' if clean.get('contentSource') == 'validated_history_identity'
        else 'validated_history')
    return clean


def safe_detail(detail, dep, kb_bucket, assets_bucket):
    try:
        return _safe_detail(detail, dep, kb_bucket, assets_bucket)
    except (ValueError, TypeError, KeyError, UnicodeError, RecursionError):
        return None


def pack(details, dependencies, kb_bucket, assets_bucket):
    output = []
    for detail in details:
        for dep in dependencies:
            clean = safe_detail(detail, dep, kb_bucket, assets_bucket)
            if clean:
                if clean not in output:
                    output.append(clean)
                break
    payload = encoded(output)
    return payload if len(payload.encode()) <= MAX_SAVED_BYTES else None


def restore(dependencies, payload, kb_bucket, assets_bucket):
    saved = []
    if type(payload) is str and len(payload.encode()) <= MAX_SAVED_BYTES:
        try:
            value = json.loads(payload)
            if type(value) is list:
                saved = value
        except (ValueError, TypeError, RecursionError):
            pass
    output = []
    for dep in dependencies:
        matches = [clean for item in saved if (clean := safe_detail(item, dep, kb_bucket, assets_bucket))]
        candidates = matches or [identity_detail(dep, kb_bucket)]
        for detail in candidates:
            if detail and detail not in output:
                output.append(detail)
    if len(encoded(output).encode()) > MAX_RESTORED_BYTES:
        return [{'resourceKind': 'history', 'provenanceScope': 'legacy_identity_unavailable',
                 'complete': False, 'reason': 'DETAIL_LIMIT'}]
    return output
