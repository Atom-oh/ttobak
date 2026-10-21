"""Read only current, conditionally published news Markdown (ADR-052).

The crawler and QA are separate deployment artifacts. The source fingerprint
contract is deliberately repeated here and checked against crawler test vectors.
Provider metadata and indexed text never authorize an object.
"""
import hashlib
import json
import re
from decimal import Decimal

from source_revision import HEX_REVISION, normalize_bindings

MAX_NEWS_BYTES = 64000
_HASH = re.compile(r'[0-9a-f]{16}')
_FILE = re.compile(r'([0-9a-f]{32})\.md')


def _source_id(value):
    return (isinstance(value, str) and 1 <= len(value) <= 128
            and not value.startswith('__')
            and all(c.isalpha() or c.isdecimal() or c in '_-' for c in value))


def news_identity(uri, bucket):
    if (not bucket or not isinstance(uri, str) or len(uri) > 2048
            or not uri.startswith(f's3://{bucket}/news/v1/')):
        return None
    key = uri[len(f's3://{bucket}/'):]
    parts = key.split('/')
    if len(parts) != 5 or not _source_id(parts[2]) or not _HASH.fullmatch(parts[3]):
        return None
    filename = _FILE.fullmatch(parts[4])
    if filename is None:
        return None
    return {'key': key, 'sourceId': parts[2], 'docHash': parts[3],
            'attemptToken': filename.group(1)}


def _text(value, maximum, empty=True):
    if (not isinstance(value, str) or len(value) > maximum
            or not empty and not value.strip()
            or any(ord(c) < 32 or ord(c) == 127 for c in value)):
        raise ValueError('Invalid news source configuration')
    value.encode('utf-8', errors='strict')
    return value.strip()


def _terms(value):
    if not isinstance(value, list) or len(value) > 32:
        raise ValueError('Invalid news source configuration')
    return list(dict.fromkeys(_text(item, 200, False) for item in value))


def news_source_revision(source):
    """Match crawler.source_revision's seven-field, compact UTF-8 contract."""
    if not isinstance(source, dict) or not _source_id(source.get('sourceId')):
        raise ValueError('Invalid news source configuration')
    urls = source.get('customUrls') or []
    if not isinstance(urls, list) or len(urls) > 20:
        raise ValueError('Invalid news source configuration')
    fields = {
        'sourceId': source['sourceId'], 'sourceName': _text(source.get('sourceName', ''), 200),
        'aliases': _terms(source.get('aliases') or []),
        'newsQueries': _terms(source.get('newsQueries') or []),
        'customUrls': [_text(url, 4096, False) for url in urls],
        'status': _text(source.get('status', 'active'), 32, False),
        'ownerId': _text(source.get('ownerId', ''), 128),
    }
    if len(fields['aliases']) + bool(fields['sourceName']) > 32:
        raise ValueError('Invalid news source configuration')
    return hashlib.sha256(json.dumps(fields, ensure_ascii=False, sort_keys=True,
                                     separators=(',', ':')).encode()).hexdigest()


def _publication(reader, identity):
    pk, sk = 'CRAWLER#' + identity['sourceId'], 'DOC#' + identity['docHash']
    row = reader.table.get_item(Key={'PK': pk, 'SK': sk}, ConsistentRead=True).get('Item') or {}
    version = row.get('schemaVersion')
    if (isinstance(version, bool) or not isinstance(version, (int, Decimal)) or version != 1
            or row.get('qualityStatus') != 'PUBLISHED'
            or row.get('PK') != pk or row.get('SK') != sk
            or any(row.get(key) != identity[key] for key in ('sourceId', 'docHash', 'attemptToken'))
            or row.get('s3Key') != identity['key']):
        return None
    expected_artifact = (f"knowledge-artifacts/news/v1/{identity['sourceId']}/"
                         f"{identity['docHash']}/{identity['attemptToken']}.json")
    if row.get('artifactKey') != expected_artifact:
        return None
    for key in ('sourceRevision', 'markdownSha256', 'artifactSha256'):
        if not isinstance(row.get(key), str) or not HEX_REVISION.fullmatch(row[key]):
            return None
    title = row.get('title')
    if not isinstance(title, str) or not 1 <= len(title.strip()) <= 300:
        return None
    title.encode('utf-8', errors='strict')
    source = reader.table.get_item(Key={'PK': pk, 'SK': 'CONFIG'}, ConsistentRead=True).get('Item') or {}
    if (source.get('PK') != pk or source.get('SK') != 'CONFIG'
            or source.get('sourceId') != identity['sourceId']
            or source.get('status') == 'disabled'):
        return None
    try:
        if news_source_revision(source) != row['sourceRevision']:
            return None
    except (ValueError, TypeError, UnicodeError):
        return None
    return {key: row[key] for key in (
        'PK', 'SK', 'sourceId', 'docHash', 'attemptToken', 'sourceRevision',
        's3Key', 'artifactKey', 'markdownSha256', 'artifactSha256', 'title')}


def current_news_source(reader, user_id, uri):
    """Bind present canonical state around HEAD without reading content."""
    if not isinstance(user_id, str) or not user_id:
        return None
    identity = news_identity(uri, reader.kb_bucket)
    if identity is None:
        return None
    publication = _publication(reader, identity)
    if publication is None:
        return None
    obj = reader.head(identity['key'], reader.kb_bucket)
    if obj.get('missing') or not 0 < obj['size'] <= MAX_NEWS_BYTES:
        return None
    if _publication(reader, identity) != publication:
        return None
    fingerprint = {'contract': 'news-v1', 'uri': uri, 'publication': publication,
                   'objects': normalize_bindings([obj])}
    revision = hashlib.sha256(json.dumps(fingerprint, ensure_ascii=False, sort_keys=True,
                                        separators=(',', ':')).encode()).hexdigest()
    return {'uri': uri, 'key': identity['key'], 'title': publication['title'],
            'sourceRevision': revision, 'object': obj,
            'markdownSha256': publication['markdownSha256']}


def read_news_text(reader, user_id, uri, expected_revision=None):
    source = current_news_source(reader, user_id, uri)
    if source is None:
        return None
    if expected_revision is not None and source['sourceRevision'] != expected_revision:
        raise ValueError('News source changed; restart reading')
    data = reader.bytes(source['object'], reader.kb_bucket)
    if len(data) > MAX_NEWS_BYTES or hashlib.sha256(data).hexdigest() != source['markdownSha256']:
        raise ValueError('Published news content hash mismatch')
    text = data.decode('utf-8', errors='strict')
    current = current_news_source(reader, user_id, uri)
    if current is None or current['sourceRevision'] != source['sourceRevision']:
        raise ValueError('News publication changed while reading')
    return {key: source[key] for key in ('uri', 'key', 'title', 'sourceRevision')} | {
        'text': text, 'resourceKind': 'publishedNews', 'contentSource': 'current_published_news',
        'dependencyKey': 'newsURI'}
