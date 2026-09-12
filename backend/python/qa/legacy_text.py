"""Source-pinned legacy text excerpts and continuation without indexed-text trust."""
import re

from source_revision import HEX_REVISION, legacy_text_key, legacy_text_revision

PAGE_CHARACTERS = 6000


def read_legacy_text(reader, user_id, uri, expected_revision=None):
    key = legacy_text_key(uri, reader.kb_bucket, user_id)
    if key is None:
        return None
    original = reader.head(key, reader.kb_bucket)
    if original.get('missing'):
        return None
    revision = legacy_text_revision(uri, original)
    if expected_revision is not None and expected_revision != revision:
        raise ValueError('Legacy source changed; restart reading')
    text = reader.bytes(original, reader.kb_bucket).decode('utf-8', errors='strict')
    current = reader.head(key, reader.kb_bucket)
    if current.get('missing') or legacy_text_revision(uri, current) != revision:
        raise ValueError('Legacy source changed while reading')
    return {'uri': uri, 'key': key, 'title': key.rsplit('/', 1)[-1],
            'sourceRevision': revision, 'text': text}


def coverage(start, end, total):
    result = {'startCharacter': start, 'includedCharacters': end - start,
              'totalCharacters': total, 'partial': start > 0 or end < total}
    if end < total:
        result['nextOffset'] = end
    return result


def provenance(source, part, matched=False):
    return {'uri': source['uri'], 'resourceKind': 'legacyText', 'title': source['title'],
            'contentSource': 'current_legacy_text', 'sourceRevision': source['sourceRevision'],
            'partial': part['partial'], 'matchedIndexedText': matched}


def source_excerpt(source, question, candidates):
    text, start, matched = source['text'], 0, False
    ranked = sorted(candidates, key=lambda item: item.get('score', 0), reverse=True)
    score = ranked[0].get('score', 0)
    for item in ranked:
        indexed = item.get('_provider', {}).get('content', {}).get('text')
        if isinstance(indexed, str) and indexed.strip():
            position = text.find(indexed)
            if position >= 0:
                start, matched, score = position, True, item.get('score', 0)
                break
    if not matched:
        # Search current bytes when the stale/normalized indexed excerpt cannot
        # be verified. A longer literal term is a better anchor than a common
        # short question word; this is only an excerpt locator, not NLP recall.
        terms = sorted(set(re.findall(r'[\w-]+', question, flags=re.UNICODE)), key=lambda term: (-len(term), term))
        for term in terms[:20]:
            found = re.search(re.escape(term), text, flags=re.IGNORECASE)
            if found:
                start = max(0, found.start() - 200)
                break
    end = min(len(text), start + PAGE_CHARACTERS)
    part = coverage(start, end, len(text))
    return {'uri': source['uri'], 'score': score,
            'text': text[start:end], 'coverage': part,
            'dependency': {'legacyURI': source['uri'], 'sourceRevision': source['sourceRevision']},
            'provenance': provenance(source, part, matched)}


def legacy_page(reader, user_id, uri, offset=0, expected_revision=None):
    if type(offset) is not int or offset < 0:
        raise ValueError('Invalid legacy text offset')
    if expected_revision is not None and (not isinstance(expected_revision, str)
                                         or not HEX_REVISION.fullmatch(expected_revision)):
        raise ValueError('Invalid legacy source revision')
    if offset and expected_revision is None:
        raise ValueError('Continuation requires the previous source revision')
    source = read_legacy_text(reader, user_id, uri, expected_revision)
    if source is None:
        return None
    if offset > len(source['text']):
        raise ValueError('Legacy text offset exceeds the document')
    end = min(len(source['text']), offset + PAGE_CHARACTERS)
    part = coverage(offset, end, len(source['text']))
    return {'uri': uri, 'title': source['title'], 'sourceRevision': source['sourceRevision'],
            'text': source['text'][offset:end], **part,
            'dependency': {'legacyURI': uri, 'sourceRevision': source['sourceRevision']},
            'provenance': provenance(source, part)}
