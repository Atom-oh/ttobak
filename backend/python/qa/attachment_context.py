"""Authorized, bounded consumers of the attachment extraction worker contract."""
import hashlib
import json
import re
import time
from decimal import Decimal
from pathlib import PurePosixPath

from boto3.dynamodb.conditions import Key
from source_revision import IDENTIFIER, resource_identity

RESULT_LIMIT = 1024 * 1024
ATTACH_FIELDS = ('attachmentId', 'meetingId', 'userId', 'originalKey', 'fileName')
STATE_FIELDS = ('runId', 'status', 'leaseUntil', 'sourceKey', 'ownerId', 'uploaderId',
                'sourceETag', 'resultKey', 'errorCode', 'unitCount', 'complete', 'updatedAt')


def _decimal(value):
    if isinstance(value, Decimal) and value.is_finite() and value == value.to_integral_value():
        return int(value)
    raise ValueError('Invalid extraction state value')


def _encode(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, allow_nan=False, default=_decimal).encode()


def _fingerprint(value):
    return hashlib.sha256(b'attachment-text-v1\0' + _encode(value)).hexdigest()


def _unique_object(pairs):
    obj = {}
    for key, value in pairs:
        if key in obj:
            raise ValueError('Duplicate result property')
        obj[key] = value
    return obj


def _invalid_number(_):
    raise ValueError('Invalid JSON number')


def _location(fmt, value):
    allowed = {
        'pdf': {'kind', 'page'}, 'md': {'kind', 'paragraph', 'startLine', 'endLine'},
        'docx': {'kind', 'paragraph', 'part', 'table', 'row', 'cell'},
        'pptx': {'kind', 'slide', 'paragraph', 'part', 'table', 'row', 'cell', 'hidden'},
    }
    required = {'pdf': ('page',), 'md': ('paragraph', 'startLine', 'endLine'),
                'docx': ('paragraph',), 'pptx': ('slide', 'paragraph')}[fmt]
    kind = {'pdf': 'page', 'pptx': 'slide'}.get(fmt, 'paragraph')
    if not isinstance(value, dict) or value.get('kind') != kind or set(value) - allowed[fmt]:
        raise ValueError('Invalid document location')
    if any(key not in value for key in required):
        raise ValueError('Missing document location')
    for key, data in value.items():
        if key == 'kind':
            continue
        if key == 'part':
            valid = isinstance(data, str) and 0 < len(data.encode()) <= 1024
        elif key == 'hidden':
            valid = isinstance(data, bool)
        else:
            valid = type(data) is int and 1 <= data <= 10000000
        if not valid:
            raise ValueError('Invalid document location value')
    if fmt == 'md' and value['endLine'] < value['startLine']:
        raise ValueError('Invalid document line range')
    if len(_encode(value)) > 2048:
        raise ValueError('Document location too large')


class AttachmentReader:
    def __init__(self, reader, query_all):
        self.reader, self.query_all = reader, query_all

    def _authorized(self, user_id, pk, meeting_id):
        identity = resource_identity(pk, 'MEETING#' + meeting_id)
        if identity['resourceKind'] != 'meeting':
            raise ValueError('Invalid parent meeting')
        return identity if self.reader.record(user_id, pk, identity['sourceSK']) is not None else None

    def metadata(self, user_id, pk, meeting_id, attachment_id, *, heads=True):
        identity = self._authorized(user_id, pk, meeting_id)
        if identity is None:
            return None
        if not isinstance(attachment_id, str) or not IDENTIFIER.fullmatch(attachment_id):
            raise ValueError('Invalid attachment identity')
        key = {'PK': 'MEETING#' + meeting_id, 'SK': 'ATTACH#' + attachment_id}
        attachment = self.reader.table.get_item(Key=key, ConsistentRead=True).get('Item')
        if not attachment:
            return None
        uploader, source = attachment.get('userId'), attachment.get('originalKey')
        if (attachment.get('PK') != key['PK'] or attachment.get('SK') != key['SK']
                or attachment.get('attachmentId') != attachment_id or attachment.get('meetingId') != meeting_id
                or not isinstance(uploader, str) or not IDENTIFIER.fullmatch(uploader)
                or not isinstance(source, str) or len(source.encode()) > 1024):
            raise ValueError('Invalid canonical attachment')
        parts = source.split('/')
        if (len(parts) != 4 or parts[:3] != ['files', uploader, meeting_id] or parts[3] in ('', '.', '..')
                or any(ord(char) < 32 or ord(char) == 127 or char == '\\' for char in source)):
            raise ValueError('Invalid attachment source key')
        state = self.reader.table.get_item(
            Key={'PK': key['PK'], 'SK': 'ATTEXT#' + attachment_id}, ConsistentRead=True,
        ).get('Item')
        if state is None:
            state = {'status': 'unknown'}
        elif (state.get('ownerId') != pk[5:] or state.get('uploaderId') != uploader
              or state.get('sourceKey') != source):
            raise ValueError('Extraction source changed')
        attempt = {name: state[name] for name in ('status', 'runId', 'errorCode') if name in state}
        if attempt.get('status') not in ('unknown', 'queued', 'running', 'succeeded', 'partial', 'failed'):
            raise ValueError('Invalid extraction status')
        if attempt['status'] in ('queued', 'running') and state.get('leaseUntil', 0) <= int(time.time() * 1000):
            attempt.update(status='failed', errorCode='INTERRUPTED')
        result_key = state.get('resultKey') or ''
        run, objects = '', []
        if result_key:
            prefix = f'files/{uploader}/{meeting_id}/text/{attachment_id}/'
            match = re.fullmatch(re.escape(prefix) + r'([A-Za-z0-9][A-Za-z0-9_-]{0,127})\.json', result_key)
            if not match or not isinstance(state.get('sourceETag'), str) or not state['sourceETag']:
                raise ValueError('Invalid extraction result key')
            run = match[1]
            if heads:
                original = self.reader.head(source)
                if original.get('missing') or original['etag'] != state['sourceETag']:
                    raise ValueError('Attachment source bytes changed')
                result = self.reader.head(result_key)
                if result.get('missing') or not 0 < result['size'] <= RESULT_LIMIT:
                    raise ValueError('Extraction result unavailable or oversized')
                objects = [original, result]
        metadata = {
            'identity': identity, 'attachment': {name: attachment[name] for name in ATTACH_FIELDS if name in attachment},
            'state': {name: state[name] for name in STATE_FIELDS if name in state},
            'attempt': attempt, 'resultRun': run, 'objects': objects,
        }
        metadata['revision'] = _fingerprint(metadata)
        return metadata

    def inventory(self, user_id, pk, meeting_id):
        identity = self._authorized(user_id, pk, meeting_id)
        if identity is None:
            return None
        entries = []
        for row in self.query_all(
            KeyConditionExpression=Key('PK').eq('MEETING#' + meeting_id) & Key('SK').begins_with('ATTACH#'),
            ProjectionExpression='attachmentId, originalKey', ConsistentRead=True,
        ):
            if not isinstance(row.get('originalKey'), str) or not row['originalKey'].startswith('files/'):
                continue
            metadata = self.metadata(user_id, pk, meeting_id, row.get('attachmentId'), heads=False)
            if metadata:
                entries.append(metadata)
        entries.sort(key=lambda entry: entry['attachment']['attachmentId'])
        revision = _fingerprint([entry['revision'] for entry in entries])
        return {'entries': entries, 'dependency': {
            'sourcePK': pk, 'sourceSK': 'MEETING#' + meeting_id, 'attachmentId': '*',
            'sourceRevision': revision,
        }}

    def is_current(self, user_id, dependency):
        pk, meeting_id = dependency['sourcePK'], dependency['sourceSK'][8:]
        if dependency.get('attachmentId') == '*':
            inventory = self.inventory(user_id, pk, meeting_id)
            return inventory is not None and inventory['dependency']['sourceRevision'] == dependency['sourceRevision']
        metadata = self.metadata(user_id, pk, meeting_id, dependency.get('attachmentId'))
        return metadata is not None and metadata['revision'] == dependency['sourceRevision']

    def overview(self, user_id, pk, meeting_id, *, offset=0, include_text=True):
        if type(offset) is not int or offset < 0:
            raise ValueError('Invalid attachment offset')
        inventory = self.inventory(user_id, pk, meeting_id)
        if inventory is None:
            return None
        entries = inventory['entries']
        view = {'attachments': [], 'totalAttachments': len(entries),
                'dependencies': [inventory['dependency']], 'sourceDetails': [], 'replayable': True}
        if offset + 5 < len(entries):
            view['nextAttachmentOffset'] = offset + 5
        for index, metadata in enumerate(entries[offset:offset + 5]):
            attachment = metadata['attachment']
            item = {'attachmentId': attachment['attachmentId'],
                    'fileName': (attachment.get('fileName') or '')[:200],
                    'attempt': metadata['attempt'], 'hasStoredResult': bool(metadata['state'].get('resultKey')),
                    'available': False}
            if include_text and index < 2 and item['hasStoredResult']:
                try:
                    page = self.read(user_id, pk, meeting_id, attachment['attachmentId'], max_bytes=4000)
                    if page is None:
                        return None
                    view['dependencies'].append(page['dependency'])
                    item.update({key: value for key, value in page.items() if key != 'dependency'})
                    if page['available']:
                        view['sourceDetails'].append(self.provenance(pk, meeting_id, page))
                except Exception:
                    # Preserve the meeting view, but expose inability to verify
                    # document text. This turn cannot later replay the notice as
                    # a current source fact without another read.
                    item['errorCode'] = 'ATTACHMENT_TEXT_UNAVAILABLE'
                    view['replayable'] = False
            view['attachments'].append(item)
        return view

    def provenance(self, pk, meeting_id, page):
        return {
            'uri': 's3://' + page['source']['bucket'] + '/' + page['source']['key'],
            'resourceKind': 'meetingAttachment', 'resourceId': page['attachmentId'],
            'sourcePK': pk, 'sourceSK': 'MEETING#' + meeting_id, 'meetingId': meeting_id,
            'title': page['fileName'], 'sourceRevision': page['dependency']['sourceRevision'],
            'contentSource': 'verified_attachment_text', 'attempt': page['attempt'],
            'result': page.get('result'), 'usingPreviousResult': page.get('usingPreviousResult', False),
            'partial': page.get('coverage', {}).get('partial', False),
            'locations': [unit['location'] for unit in page['units'][:20]],
        }

    def _validate_result(self, data, metadata):
        if len(data) > RESULT_LIMIT:
            raise ValueError('Extraction result oversized')
        result = json.loads(data.decode('utf-8', errors='strict'), object_pairs_hook=_unique_object,
                            parse_constant=_invalid_number)
        attachment, state, identity = metadata['attachment'], metadata['state'], metadata['identity']
        expected = {
            'bucket': self.reader.assets_bucket, 'key': attachment['originalKey'], 'eTag': state['sourceETag'],
            'meetingId': identity['resourceId'], 'ownerId': identity['sourcePK'][5:],
            'uploaderId': attachment['userId'], 'attachmentId': attachment['attachmentId'],
            'runId': metadata['resultRun'],
        }
        fmt = PurePosixPath(attachment['originalKey']).suffix.lower()[1:]
        if (not isinstance(result, dict) or type(result.get('schemaVersion')) is not int
                or result['schemaVersion'] != 1 or fmt not in ('pdf', 'pptx', 'docx', 'md')
                or result.get('format') != fmt or result.get('source') != expected
                or result.get('status') not in ('succeeded', 'partial')
                or result.get('complete') is not (result.get('status') == 'succeeded')
                or not isinstance(result.get('scope'), str) or not 0 < len(result['scope']) <= 1024):
            raise ValueError('Invalid extraction result provenance')
        units = result.get('units')
        if not isinstance(units, list) or not 0 < len(units) <= 4000:
            raise ValueError('Extraction result has no verified text')
        size = 0
        for unit in units:
            if not isinstance(unit, dict) or set(unit) != {'text', 'location'} or not isinstance(unit['text'], str) or not unit['text'].strip():
                raise ValueError('Invalid extracted text unit')
            size += len(unit['text'].encode('utf-8', errors='strict'))
            _location(fmt, unit['location'])
        metrics = result.get('metrics') or {}
        if size > 256 * 1024 or metrics.get('units') != len(units) or metrics.get('textBytes') != size:
            raise ValueError('Invalid extraction text metrics')
        if state['status'] in ('succeeded', 'partial'):
            if (state.get('runId') != metadata['resultRun'] or state.get('unitCount') != len(units)
                    or state.get('complete') is not result['complete'] or state['status'] != result['status']):
                raise ValueError('Extraction state/result mismatch')
        warnings = result.get('warnings') or []
        if not isinstance(warnings, list) or len(warnings) > 400:
            raise ValueError('Invalid extraction warnings')
        return result

    def read(self, user_id, pk, meeting_id, attachment_id, *, unit_offset=0, text_offset=0,
             expected_revision=None, max_bytes=16000):
        if (type(unit_offset) is not int or type(text_offset) is not int or unit_offset < 0 or text_offset < 0
                or type(max_bytes) is not int or not 1024 <= max_bytes <= 16000):
            raise ValueError('Invalid attachment page')
        metadata = self.metadata(user_id, pk, meeting_id, attachment_id)
        if metadata is None:
            return None
        if ((unit_offset or text_offset) and expected_revision is None
                or expected_revision is not None and expected_revision != metadata['revision']):
            raise ValueError('Attachment revision changed or is missing; restart from the first unit')
        state, attachment = metadata['state'], metadata['attachment']
        page = {
            'attachmentId': attachment_id, 'meetingId': meeting_id, 'fileName': attachment.get('fileName') or '',
            'sourceRevision': metadata['revision'],
            'attempt': metadata['attempt'], 'available': False, 'units': [],
            'dependency': {'sourcePK': pk, 'sourceSK': 'MEETING#' + meeting_id,
                           'attachmentId': attachment_id, 'sourceRevision': metadata['revision']},
        }
        if not state.get('resultKey'):
            if len(_encode(page)) > max_bytes:
                raise ValueError('Attachment metadata exceeds response bound')
            return page
        data = self.reader.bytes(metadata['objects'][1])
        result = self._validate_result(data, metadata)
        # Revalidate auth, canonical attachment, state and both object bindings
        # after the body read. Deleted/revoked/changed sources never reach tools.
        current = self.metadata(user_id, pk, meeting_id, attachment_id)
        if current is None:
            return None
        if current['revision'] != metadata['revision']:
            raise ValueError('Attachment changed while reading')
        page.update(available=True, usingPreviousResult=state.get('runId') != metadata['resultRun'],
                    result={name: result[name] for name in ('status', 'complete', 'scope', 'format')},
                    warnings=result.get('warnings', [])[:20], source=result['source'])
        page['totalWarnings'] = len(result.get('warnings') or [])
        page['warningsTruncated'] = page['totalWarnings'] > len(page['warnings'])
        page['result']['runId'] = metadata['resultRun']
        units = result['units']
        if unit_offset > len(units) or unit_offset == len(units) and text_offset:
            raise ValueError('Invalid attachment unit offset')
        if unit_offset < len(units) and text_offset >= len(units[unit_offset]['text']) and text_offset:
            raise ValueError('Invalid attachment text offset')
        page['coverage'] = {'totalUnits': len(units), 'partial': unit_offset > 0 or text_offset > 0}
        while unit_offset < len(units):
            unit = units[unit_offset]
            remaining = unit['text'][text_offset:]
            page['nextUnitOffset'], page['nextTextOffset'] = unit_offset, text_offset
            entry = {'text': '', 'location': unit['location'], 'textOffset': text_offset}
            page['units'].append(entry)
            low, high = 0, len(remaining)
            while low < high:
                middle = (low + high + 1) // 2
                entry['text'] = remaining[:middle]
                if len(_encode(page)) + 48 <= max_bytes:
                    low = middle
                else:
                    high = middle - 1
            entry['text'] = remaining[:low]
            if not low:
                page['units'].pop()
                if not page['units']:
                    raise ValueError('Attachment metadata exceeds response bound')
                break
            text_offset += low
            if text_offset == len(unit['text']):
                unit_offset, text_offset = unit_offset + 1, 0
            page['nextUnitOffset'], page['nextTextOffset'] = unit_offset, text_offset
            if low < len(remaining):
                break
        if unit_offset == len(units):
            page.pop('nextUnitOffset', None)
            page.pop('nextTextOffset', None)
        else:
            page['coverage']['partial'] = True
        if len(_encode(page)) > max_bytes:
            raise ValueError('Attachment response exceeds bound')
        return page
