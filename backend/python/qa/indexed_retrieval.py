"""Unified discovery with live authorization and current-source hydration."""
import re

from boto3.dynamodb.conditions import Key
from source_revision import (
    IDENTIFIER, resource_identity, projection_identity, legacy_meeting_identity, normalize_bindings,
)
from manual_kb import discovery_filter as manual_discovery_filter, hydrate_manual_candidates
from legacy_text import read_legacy_text, source_excerpt


def discover_sources(reader, user_id, query_all, shared_meetings):
    """Only identities are enumerated; each content read authorizes afresh."""
    identities = {}

    def add(pk, sk):
        try:
            identity = resource_identity(pk, sk)
            identities[(pk, sk)] = identity
        except ValueError:
            pass  # unrelated/malformed discovery rows cannot become source keys

    own_pk = 'USER#' + user_id
    for prefix in ('MEETING#', 'DOC#'):
        for row in query_all(
            KeyConditionExpression=Key('PK').eq(own_pk) & Key('SK').begins_with(prefix),
            ProjectionExpression='PK, SK', ConsistentRead=True,
        ):
            add(row.get('PK'), row.get('SK'))
    for shared in shared_meetings:
        add('USER#' + shared['ownerId'], 'MEETING#' + shared['meetingId'])
    for share in query_all(
        KeyConditionExpression=Key('PK').eq(own_pk) & Key('SK').begins_with('SHAREDDOC#'),
        ProjectionExpression='SK, meetingId, ownerId, sharedToId, #perm, entityType',
        ExpressionAttributeNames={'#perm': 'permission'},
        ConsistentRead=True,
    ):
        doc_id, owner = share.get('meetingId'), share.get('ownerId')
        if (share.get('entityType') == 'DOC_SHARE' and share.get('sharedToId') == user_id
                and share.get('permission') == 'read' and isinstance(doc_id, str)
                and share.get('SK') == 'SHAREDDOC#' + doc_id and isinstance(owner, str)):
            add('USER#' + owner, 'DOC#' + doc_id)
    accounts = set()
    memberships = query_all(
        IndexName='GSI1',
        KeyConditionExpression=Key('GSI1PK').eq(own_pk) & Key('GSI1SK').begins_with('ACCOUNT#'),
        ProjectionExpression='accountId',
    )
    for account_id in sorted({row['accountId'] for row in memberships
                              if isinstance(row.get('accountId'), str) and IDENTIFIER.fullmatch(row['accountId'])}):
        member = reader.table.get_item(
            Key={'PK': 'ACCOUNT#' + account_id, 'SK': 'MEMBER#' + user_id},
            ConsistentRead=True,
        ).get('Item') or {}
        if member.get('accountId') != account_id or member.get('userId') != user_id:
            continue
        accounts.add('ACCOUNT#' + account_id)
        for row in query_all(
            KeyConditionExpression=Key('PK').eq('ACCOUNT#' + account_id) & Key('SK').begins_with('DOC#'),
            ProjectionExpression='PK, SK', ConsistentRead=True,
        ):
            add(row.get('PK'), row.get('SK'))
    return identities, accounts


def discovery_filters(user_id, bucket, identities, account_pks, shared_meetings):
    partitions = {'USER#' + user_id} | account_pks
    filters = [{'equals': {'key': 'sourcePK', 'value': pk}} for pk in sorted(partitions)]
    for pk, sk in sorted(identities):
        if pk not in partitions:
            filters.append({'andAll': [
                {'equals': {'key': 'sourcePK', 'value': pk}},
                {'equals': {'key': 'sourceSK', 'value': sk}},
            ]})
    for prefix in (f'kb/{user_id}/', f'meetings/{user_id}/', 'shared/'):
        filters.append({'startsWith': {'key': 'x-amz-bedrock-kb-source-uri',
                                       'value': f's3://{bucket}/{prefix}'}})
    for shared in shared_meetings:
        filters.append({'equals': {'key': 'x-amz-bedrock-kb-source-uri',
                                  'value': f"s3://{bucket}/meetings/{shared['ownerId']}/{shared['meetingId']}.md"}})
    filters.append(manual_discovery_filter(user_id))
    filters.append(manual_discovery_filter(user_id, shared=True))
    # Keep filter groups small. Each group is queried, then current resources
    # are deduplicated/ranked; a long share list cannot truncate discovery.
    return [batch[0] if len(batch) == 1 else {'orAll': batch}
            for start in range(0, len(filters), 5) if (batch := filters[start:start + 5])]


def _current_result(snapshot, uri, score, content_source):
    identity, fields = snapshot['identity'], snapshot['fields']
    provenance = {key: identity[key] for key in ('resourceKind', 'resourceId', 'sourcePK', 'sourceSK')}
    provenance.update(uri=uri, title=fields.get('title') or '', sourceRevision=snapshot['revision'],
                      contentSource=content_source)
    result = {'uri': uri, 'score': score, 'provenance': provenance,
              'dependency': {'sourcePK': identity['sourcePK'], 'sourceSK': identity['sourceSK'],
                             'sourceRevision': snapshot['revision']}}
    if identity['resourceKind'] == 'meeting':
        result['meeting'] = {
            'meetingId': identity['resourceId'], 'title': fields.get('title') or '',
            'updatedAt': fields.get('updatedAt'), 'notes': fields.get('notes') or '',
            'content': fields.get('content') or '', 'actionItems': fields.get('actionItems') or '',
        }
    else:
        result['document'] = {
            'docId': identity['resourceId'], 'title': fields.get('title') or '',
            'updatedAt': fields.get('updatedAt'), 'content': fields.get('content') or '',
        }
        if fields.get('fileKey'):
            result['document']['filePending'] = True
            result['provenance']['filePending'] = True
    return result


def _legacy_text(reader, user_id, uri, question, candidates):
    source = read_legacy_text(reader, user_id, uri)
    return source_excerpt(source, question, candidates) if source else None


def hydrate_candidates(reader, user_id, question, candidates, identities, limit, manual_lookup=None):
    results, snapshots = {}, {}
    grouped = {}
    for candidate in candidates:
        grouped.setdefault(candidate.get('uri', ''), []).append(candidate)
    for candidate in candidates:
        uri, score = candidate.get('uri', ''), candidate.get('score', 0)
        identity = projection_identity(uri, candidate.get('metadata'), reader.kb_bucket)
        legacy = legacy_meeting_identity(uri, reader.kb_bucket)
        if identity is None and legacy is None:
            if uri not in results:
                current = _legacy_text(reader, user_id, uri, question, grouped[uri])
                if current:
                    results[uri] = current
            continue
        source = identity or legacy
        key = source['sourcePK'], source['sourceSK']
        if key not in snapshots:
            snapshots[key] = reader.read(user_id, *key)
        snapshot = snapshots[key]
        if snapshot is None:
            continue
        result = results.get(key)
        if result is None:
            result = _current_result(snapshot, uri, score, 'current_saved')
            results[key] = result
        result['score'] = max(result['score'], score)
        if source['resourceKind'] == 'meeting':
            continue  # never reuse indexed/cached meeting text
        if identity and identity['filename'].startswith('file.'):
            verified = (snapshot['outcome'] == 'INDEXED' and identity['filename'] in snapshot['filenames']
                        and identity['indexRevision'] == snapshot['revision']
                        and identity['bindings'] == normalize_bindings(snapshot['objects']))
            if verified:
                text = candidate.get('_provider', {}).get('content', {}).get('text')
                if isinstance(text, str) and text:
                    previous = result.get('text', '')
                    if text not in previous:
                        result['text'] = (previous + '\n' + text).strip()[:6000]
                    result['provenance']['contentSource'] = 'verified_indexed_file'
                    result['provenance']['partial'] = True
                    result['document'].pop('filePending', None)
                    result['provenance'].pop('filePending', None)
            elif not result.get('text') and snapshot['fields'].get('fileKey'):
                result['document']['filePending'] = True
                result['provenance']['filePending'] = True
    # Fresh saved text complements semantic discovery while ingestion is pending.
    # No S3 reads/heads are needed for resources whose saved text does not match.
    terms = [word for word in re.split(r'\s+', question.casefold().strip()) if word][:20]
    keyword_ready, keyword_pending = [], []
    for key, identity in sorted(identities.items()):
        if key in results:
            continue
        fields = reader.record(user_id, *key)
        if fields is None:
            continue
        searchable = '\n'.join(fields.get(name) or '' for name in ('title', 'notes', 'content', 'actionItems')).casefold()
        if terms and all(term in searchable for term in terms):
            snapshot = reader.snapshot(identity, fields)
            result = _current_result(
                snapshot, 'ttobak://source/' + identity['resourceHash'], 0.0,
                'current_saved_keyword_match')
            body = '\n'.join(fields.get(name) or '' for name in ('notes', 'content', 'actionItems')).casefold()
            (keyword_ready if all(term in body for term in terms) else keyword_pending).append(result)
    manual = hydrate_manual_candidates(reader, user_id, candidates, manual_lookup)
    ranked = sorted([*results.values(), *manual], key=lambda result: result.get('score', 0), reverse=True)
    def has_body(result):
        fields = result.get('meeting') or result.get('document') or {}
        return bool(result.get('text') or any(fields.get(name) for name in ('notes', 'content', 'actionItems')))
    ready = [result for result in ranked if has_body(result)]
    pending = [result for result in ranked if not has_body(result)]
    if not keyword_ready or (limit == 1 and ready):
        return (ready + keyword_pending + pending)[:limit]
    # For multiple results, reserve a slot for new saved text while keeping
    # semantic body evidence in the other slots. A keyword score is
    # not a provider relevance score; metadata-only pending files come last.
    count = min(len(ready), max(0, limit - 1))
    selected = ready[:count] + keyword_ready[:limit - count]
    return (selected + ready[count:] + keyword_pending + pending)[:limit]
