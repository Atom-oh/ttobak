"""Current document authority for indexed candidates, independent of index grants."""
import re

from source_revision import resource_identity, source_fields

_IDENTIFIER = re.compile(r'[A-Za-z0-9][A-Za-z0-9_-]{0,127}')


def _id(value):
    return isinstance(value, str) and _IDENTIFIER.fullmatch(value) is not None


def _document_access(table, requester_id, source_pk, document_id):
    prefix, _, scope_id = source_pk.partition('#')
    if prefix == 'USER':
        if requester_id == scope_id:
            return True
        grant = table.get_item(
            Key={'PK': f'USER#{requester_id}', 'SK': f'SHAREDDOC#{document_id}'},
            ConsistentRead=True,
        ).get('Item') or {}
        return (
            grant.get('entityType') == 'DOC_SHARE' and grant.get('meetingId') == document_id
            and grant.get('ownerId') == scope_id and grant.get('sharedToId') == requester_id
            and grant.get('permission') == 'read'
        )
    member = table.get_item(
        Key={'PK': source_pk, 'SK': f'MEMBER#{requester_id}'}, ConsistentRead=True,
    ).get('Item') or {}
    return member.get('accountId') == scope_id and member.get('userId') == requester_id


def read_current_document(table, requester_id, source_pk, document_id, *, raw=False):
    """Read current content only after checking canonical identity and live access.

    None means missing or inaccessible. Storage failures propagate so callers
    cannot substitute cached text or falsely report an empty successful search.
    Internal file metadata is returned for revision checks; public-share tokens
    and email addresses are deliberately excluded.
    """
    if not _id(requester_id) or not _id(document_id) or not isinstance(source_pk, str):
        raise ValueError('Invalid document identity')
    prefix, separator, scope_id = source_pk.partition('#')
    if not separator or prefix not in ('USER', 'ACCOUNT') or not _id(scope_id):
        raise ValueError('Invalid document scope')
    if not _document_access(table, requester_id, source_pk, document_id):
        return None
    key = {'PK': source_pk, 'SK': f'DOC#{document_id}'}
    item = table.get_item(Key=key, ConsistentRead=True).get('Item')
    if not item:
        return None
    expected_type = 'USER_DOC' if prefix == 'USER' else 'ACCOUNT_DOC'
    if (item.get('PK') != source_pk or item.get('SK') != key['SK']
            or item.get('docId') != document_id or item.get('entityType') != expected_type):
        return None
    if prefix == 'USER' and item.get('sourceUserId') != scope_id:
        return None
    if prefix == 'ACCOUNT' and item.get('accountId') != scope_id:
        return None
    if not _document_access(table, requester_id, source_pk, document_id):
        return None
    if raw:
        # Internal revision input only: retain present/null fields; never return
        # tokens, emails or unrelated metadata alongside source content.
        return source_fields(resource_identity(source_pk, key['SK']), item)
    content = item.get('content') or ''
    if not isinstance(content, str):
        raise ValueError('Document content is not text')
    return {
        'sourcePK': source_pk,
        'docId': document_id,
        'scope': 'personal' if prefix == 'USER' else 'account',
        'scopeId': scope_id,
        'title': item.get('title') or '',
        'content': content,
        'updatedAt': item.get('updatedAt') or '',
        'fileKey': item.get('fileKey') or '',
        'fileName': item.get('fileName') or '',
        'mimeType': item.get('mimeType') or '',
    }
