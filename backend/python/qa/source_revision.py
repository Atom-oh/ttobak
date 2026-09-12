"""Canonical-v1 wire identity and revision contract shared with the Go worker."""
import hashlib
import json
import re

IDENTIFIER = re.compile(r'[A-Za-z0-9][A-Za-z0-9_-]{0,127}')
HEX_REVISION = re.compile(r'[0-9a-f]{64}')
RUN_ID = re.compile(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}')
FILE_EXTENSIONS = ('.pdf', '.txt', '.md', '.html', '.doc', '.docx', '.csv', '.xls', '.xlsx')
MEETING_FIELDS = (
    'meetingId', 'userId', 'title', 'date', 'status', 'notes', 'content',
    'selectedTranscript', 'transcriptA', 'transcriptB', 'actionItems', 'updatedAt',
)
DOCUMENT_FIELDS = (
    'docId', 'accountId', 'sourceUserId', 'title', 'docType', 'content',
    'fileKey', 'fileName', 'mimeType', 'updatedAt',
)


def resource_identity(pk, sk):
    if not isinstance(pk, str) or not isinstance(sk, str):
        raise ValueError('Invalid canonical source identity')
    partition, _, owner = pk.partition('#')
    prefix, _, resource_id = sk.partition('#')
    kinds = {('USER', 'MEETING'): 'meeting', ('USER', 'DOC'): 'personalDocument',
             ('ACCOUNT', 'DOC'): 'accountDocument'}
    kind = kinds.get((partition, prefix))
    if not kind or not IDENTIFIER.fullmatch(owner) or not IDENTIFIER.fullmatch(resource_id):
        raise ValueError('Invalid canonical source identity')
    return {'sourcePK': pk, 'sourceSK': sk, 'resourceKind': kind, 'resourceId': resource_id,
            'resourceHash': hashlib.sha256((pk + '\0' + sk).encode()).hexdigest()}


def source_fields(identity, record):
    names = MEETING_FIELDS if identity['resourceKind'] == 'meeting' else DOCUMENT_FIELDS
    fields = {name: record[name] for name in names if name in record}
    for value in fields.values():
        if value is not None:
            if not isinstance(value, str):
                raise ValueError('Canonical source field is not text')
            value.encode('utf-8', errors='strict')
    return fields


def normalize_bindings(objects):
    if not isinstance(objects, list) or len(objects) > 2:
        raise ValueError('Invalid source object bindings')
    result, seen = [], set()
    for obj in objects:
        if not isinstance(obj, dict) or set(obj) - {'key', 'etag', 'versionId', 'size', 'missing', 'metadata'}:
            raise ValueError('Invalid source object binding')
        key = obj.get('key')
        if not isinstance(key, str) or not key or key in seen:
            raise ValueError('Invalid source object key')
        seen.add(key)
        size = obj.get('size')
        if isinstance(size, bool) or not isinstance(size, int) or size < 0:
            raise ValueError('Invalid source object size')
        meta = obj.get('metadata', {})
        if not isinstance(meta, dict) or set(meta) - {'source-etag', 'source-version-id'}:
            raise ValueError('Invalid source binding metadata')
        missing = obj.get('missing', False)
        if not isinstance(missing, bool):
            raise ValueError('Invalid missing object flag')
        values = [obj.get('etag', ''), obj.get('versionId', ''),
                  meta.get('source-etag', ''), meta.get('source-version-id', '')]
        if not all(isinstance(value, str) for value in values):
            raise ValueError('Invalid source binding value')
        if missing and (size or any(values)):
            raise ValueError('Invalid missing object binding')
        result.append({'key': key, 'etag': values[0], 'versionId': values[1],
                       'size': size, 'missing': missing,
                       'metadata': {'source-etag': values[2], 'source-version-id': values[3]}})
    return sorted(result, key=lambda obj: obj['key'])


def source_revision(identity, fields, objects, outcome='INDEXED'):
    values = ['canonical-v1', identity['sourcePK'], identity['sourceSK'], outcome]
    for name, value in sorted(fields.items()):
        if value is not None and not isinstance(value, str):
            raise ValueError('Invalid canonical source field')
        values.extend(('field', name, 'null' if value is None else 'string',
                       '' if value is None else value))
    for obj in normalize_bindings(objects):
        values.extend(('object', obj['key'], obj['etag'], obj['versionId'], str(obj['size']),
                       'true' if obj['missing'] else 'false',
                       obj['metadata']['source-etag'], obj['metadata']['source-version-id']))
    digest = hashlib.sha256()
    for value in values:
        encoded = value.encode('utf-8', errors='strict')
        digest.update(str(len(encoded)).encode() + b':' + encoded)
    return digest.hexdigest()


def projection_identity(uri, metadata, bucket):
    """Metadata discovers an identity; this function never authorizes it."""
    if not bucket or not isinstance(uri, str) or not isinstance(metadata, dict):
        return None
    try:
        identity = resource_identity(metadata.get('sourcePK'), metadata.get('sourceSK'))
        if any(metadata.get(name) != identity[name] for name in ('resourceKind', 'resourceId')):
            return None
        revision, run = metadata.get('sourceRevision'), metadata.get('indexRunId')
        if (metadata.get('indexSchema') != 'canonical-v1' or not isinstance(revision, str)
                or not HEX_REVISION.fullmatch(revision) or not isinstance(run, str)
                or not RUN_ID.fullmatch(run)):
            return None
        prefix = (f"s3://{bucket}/canonical/v1/{identity['resourceKind']}/"
                  f"{identity['resourceHash']}/{run}/")
        if not uri.startswith(prefix):
            return None
        filename = uri[len(prefix):]
        allowed = ('meeting.md',) if identity['resourceKind'] == 'meeting' else (
            'document.md', *('file' + ext for ext in FILE_EXTENSIONS))
        if filename not in allowed:
            return None
        objects = json.loads(metadata['sourceObjects'])
        bindings = normalize_bindings(objects)
        return dict(identity, filename=filename, indexRevision=revision, bindings=bindings)
    except (ValueError, TypeError, KeyError):
        return None


def legacy_meeting_identity(uri, bucket):
    if not bucket or not isinstance(uri, str):
        return None
    match = re.fullmatch(re.escape(f's3://{bucket}/meetings/') +
                         r'([A-Za-z0-9][A-Za-z0-9_-]{0,127})/([A-Za-z0-9][A-Za-z0-9_-]{0,127})\.md', uri)
    if not match:
        return None
    return resource_identity('USER#' + match[1], 'MEETING#' + match[2])


def legacy_text_key(uri, bucket, user_id):
    if (not bucket or not isinstance(uri, str) or not isinstance(user_id, str)
            or not IDENTIFIER.fullmatch(user_id) or not uri.startswith('s3://' + bucket + '/')):
        return None
    key = uri[len('s3://' + bucket + '/'):]
    if (not (key.startswith('kb/' + user_id + '/') or key.startswith('shared/'))
            or any(part in ('', '.', '..') for part in key.split('/'))
            or any(char in key for char in ('?', '#', '%', '\\'))
            or any(ord(char) < 32 or ord(char) == 127 for char in key)
            or not key.lower().endswith(('.md', '.txt', '.html', '.csv'))):
        return None
    return key


def legacy_text_revision(uri, obj):
    binding = json.dumps(normalize_bindings([obj]), sort_keys=True, separators=(',', ':'), ensure_ascii=False)
    return hashlib.sha256(('legacy-text-v1\0' + uri + '\0' + binding).encode()).hexdigest()
