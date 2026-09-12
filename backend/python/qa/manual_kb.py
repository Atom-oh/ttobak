"""Source-bound private/shared KB snapshots with separate visibility contracts."""
import hashlib
import math
from pathlib import PurePosixPath

from source_revision import IDENTIFIER, RUN_ID

SCHEMA = 'manual-kb-v1'
KIND = 'manualKbDocument'
SHARED_SCHEMA = 'shared-kb-v1'
SHARED_KIND = 'sharedKbDocument'
SHARED_VISIBILITY = 'authenticated-shared'
# Preserve the manual uploader's binary formats. Provider support/failure is a
# migration-worker concern; none of these unbound old chunks becomes evidence.
BINARY_EXTENSIONS = frozenset(('.pdf', '.doc', '.docx', '.ppt', '.pptx', '.xls', '.xlsx'))
SNAPSHOT_EXTENSIONS = frozenset(('.pdf', '.doc', '.docx', '.xls', '.xlsx'))
MAX_SOURCE_BYTES = 50 * 1024 * 1024


def owned_manual_key(key, user_id):
    if (not isinstance(key, str) or not isinstance(user_id, str)
            or not IDENTIFIER.fullmatch(user_id) or len(key.encode('utf-8')) > 1024):
        return False
    parts = key.split('/')
    return (len(parts) == 3 and parts[:2] == ['kb', user_id] and parts[2] not in ('', '.', '..')
            and not key.lower().endswith('.metadata.json')
            and not any(ord(char) < 32 or ord(char) == 127 or char == '\\' for char in key))


def shared_source_key(key):
    if (not isinstance(key, str) or len(key.encode('utf-8')) > 1024
            or not key.startswith('shared/') or key.lower().endswith('.metadata.json')):
        return False
    return (all(part not in ('', '.', '..') for part in key.split('/'))
            and not any(ord(char) < 32 or ord(char) == 127 or char == '\\' for char in key))


def _authenticated(user_id):
    return isinstance(user_id, str) and IDENTIFIER.fullmatch(user_id) is not None


def legacy_binary_key(uri, bucket, user_id):
    if (not bucket or not _authenticated(user_id) or not isinstance(uri, str)
            or not uri.startswith('s3://' + bucket + '/')):
        return None
    # S3 key data, not an HTTP URL: never decode percent escapes, strip query
    # strings, or normalize path segments supplied by the index.
    key = uri[len('s3://' + bucket + '/'):]
    if (not (owned_manual_key(key, user_id) or shared_source_key(key))
            or PurePosixPath(key).suffix.lower() not in BINARY_EXTENSIONS):
        return None
    return key


def binary_revision(schema, bucket, key, etag, version, size):
    digest = hashlib.sha256()
    for value in (schema, bucket, key, etag, version, str(size)):
        encoded = value.encode('utf-8', errors='strict')
        digest.update(str(len(encoded)).encode() + b':' + encoded)
    return digest.hexdigest()


def manual_revision(bucket, key, etag, version, size):
    return binary_revision(SCHEMA, bucket, key, etag, version, size)


def current_source(reader, user_id, key):
    if not owned_manual_key(key, user_id):
        return None
    return _current_source(reader, key, shared=False)


def current_shared_source(reader, user_id, key):
    if not _authenticated(user_id) or not shared_source_key(key):
        return None
    return _current_source(reader, key, shared=True)


def _current_source(reader, key, shared):
    obj = reader.head(key, reader.kb_bucket)
    if obj.get('missing'):
        return None
    schema = SHARED_SCHEMA if shared else SCHEMA
    revision = binary_revision(schema, reader.kb_bucket, key, obj['etag'], obj.get('versionId', ''), obj['size'])
    return {'key': key, 'object': obj, 'revision': revision, 'shared': shared,
            'resourceId': hashlib.sha256(key.encode('utf-8')).hexdigest()}


def snapshot_identity(uri, metadata, bucket, user_id):
    if (not isinstance(metadata, dict) or not bucket or not isinstance(uri, str)
            or not _authenticated(user_id)):
        return None
    if metadata.get('sourceBucket') != bucket:
        return None
    key, etag, version = metadata.get('sourceKey'), metadata.get('sourceETag'), metadata.get('sourceVersionId')
    shared = shared_source_key(key)
    if shared:
        if (metadata.get('indexSchema') != SHARED_SCHEMA or metadata.get('resourceKind') != SHARED_KIND
                or metadata.get('visibility') != SHARED_VISIBILITY or 'ownerId' in metadata):
            return None
        schema = SHARED_SCHEMA
    else:
        if (not owned_manual_key(key, user_id) or metadata.get('indexSchema') != SCHEMA
                or metadata.get('resourceKind') != KIND or metadata.get('ownerId') != user_id
                or 'visibility' in metadata):
            return None
        schema = SCHEMA
    if (PurePosixPath(key).suffix.lower() not in SNAPSHOT_EXTENSIONS
            or not isinstance(etag, str) or not etag or not isinstance(version, str)):
        return None
    size = metadata.get('sourceSize')
    if (isinstance(size, bool) or not isinstance(size, (int, float))
            or not 0 < size <= MAX_SOURCE_BYTES or not math.isfinite(size) or int(size) != size):
        return None
    size = int(size)
    resource_id = hashlib.sha256(key.encode('utf-8')).hexdigest()
    revision = binary_revision(schema, bucket, key, etag, version, size)
    run = metadata.get('indexRunId')
    if (metadata.get('resourceId') != resource_id or metadata.get('sourceRevision') != revision
            or not isinstance(run, str) or not RUN_ID.fullmatch(run)):
        return None
    prefix = f's3://{bucket}/shared-kb/v1/' if shared else f's3://{bucket}/manual-kb/v1/{user_id}/'
    expected = prefix + f'{resource_id}/{revision}/{run}/document{PurePosixPath(key).suffix.lower()}'
    if uri != expected:
        return None
    return {'key': key, 'revision': revision, 'resourceId': resource_id, 'shared': shared}


def discovery_filter(user_id, resource_id=None, revision=None, *, shared=False):
    fields = ([('indexSchema', SHARED_SCHEMA), ('visibility', SHARED_VISIBILITY)] if shared
              else [('indexSchema', SCHEMA), ('ownerId', user_id)])
    if resource_id is not None:
        fields.append(('resourceId', resource_id))
    if revision is not None:
        fields.append(('sourceRevision', revision))
    return {'andAll': [{'equals': {'key': name, 'value': value}} for name, value in fields]}


def _result(reader, user_id, current, uri, score):
    key, size = current['key'], current['object']['size']
    status = 'pending'
    reason = 'VERIFIED_SNAPSHOT_UNAVAILABLE'
    if PurePosixPath(key).suffix.lower() not in SNAPSHOT_EXTENSIONS:
        status, reason = 'failed', 'UNSUPPORTED_FILE'
    elif size == 0:
        status, reason = 'empty', 'EMPTY_FILE'
    elif size > MAX_SOURCE_BYTES:
        status, reason = 'failed', 'SOURCE_TOO_LARGE'
    shared = current['shared']
    dependency_key = 'sharedKey' if shared else 'manualKey'
    result = {
        'uri': uri, 'score': score, 'volatile': True,
        'manualFile': {'fileName': key.rsplit('/', 1)[-1], 'status': status, 'reason': reason},
        'dependency': {dependency_key: key, 'sourceRevision': current['revision']},
        'provenance': {'uri': uri, 'resourceKind': SHARED_KIND if shared else KIND, 'resourceId': current['resourceId'],
                       'sourceBucket': reader.kb_bucket, 'sourceKey': key,
                       'title': key.rsplit('/', 1)[-1],
                       'sourceRevision': current['revision'],
                       'contentSource': 'shared_kb_migration_pending' if shared else 'manual_kb_migration_pending',
                       'filePending': True, 'migrationStatus': status},
    }
    if shared:
        result['provenance']['visibility'] = SHARED_VISIBILITY
    else:
        result['provenance']['ownerId'] = user_id
    return result


def hydrate_manual_candidates(reader, user_id, candidates, lookup=None):
    """Never consume unbound kb/ or shared/ text from a binary's old index URI."""
    results, current_by_key = {}, {}
    for candidate in candidates:
        uri = candidate.get('uri', '')
        identity = snapshot_identity(uri, candidate.get('metadata'), reader.kb_bucket, user_id)
        key = identity['key'] if identity else legacy_binary_key(uri, reader.kb_bucket, user_id)
        if key is None:
            continue
        if key not in current_by_key:
            read = current_shared_source if shared_source_key(key) else current_source
            current_by_key[key] = read(reader, user_id, key)
        current = current_by_key[key]
        if current is None:
            continue
        result = results.get(key)
        if result is None:
            result = _result(reader, user_id, current, 's3://' + reader.kb_bucket + '/' + key,
                             candidate.get('score', 0))
            results[key] = result
        if identity is not None and identity['revision'] == current['revision']:
            text = candidate.get('_provider', {}).get('content', {}).get('text')
            if not isinstance(text, str) or not text.strip():
                continue
            previous = result.get('text', '')
            if text not in previous:
                result['text'] = (previous + '\n' + text).strip()[:6000]
            result['uri'] = uri
            result['manualFile'].update(status='ready', reason='')
            result.pop('volatile', None)
            result['score'] = max(result['score'], candidate.get('score', 0))
            result['provenance'].update(uri=uri,
                                        contentSource='verified_shared_kb_file' if current['shared'] else 'verified_manual_kb_file',
                                        filePending=False, migrationStatus='ready', partial=True)
    if lookup is not None:
        # Old and new copies may compete for the same top-k slots. An exact
        # current-revision lookup prevents old hits from starving ready copies.
        for key, result in list(results.items()):
            if result['manualFile']['status'] != 'pending':
                continue
            current = current_by_key[key]
            discovered = lookup(discovery_filter(user_id, current['resourceId'], current['revision'],
                                                   shared=current['shared']))
            refreshed = hydrate_manual_candidates(reader, user_id, discovered)
            verified = next((item for item in refreshed if item['manualFile']['status'] == 'ready'
                             and item['dependency'] == result['dependency']), None)
            if verified is not None:
                results[key] = verified
    return list(results.values())
