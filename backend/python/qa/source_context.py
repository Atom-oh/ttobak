"""Fresh canonical authorization, source snapshots and bounded pinned S3 reads."""
import json
from pathlib import PurePosixPath

from botocore.exceptions import ClientError
from document_context import read_current_document
from source_revision import (
    IDENTIFIER, FILE_EXTENSIONS, resource_identity, source_fields, source_revision,
)
from transcript_storage import validate_transcript_ref

MAX_SOURCE_BYTES = 50 * 1024 * 1024


class SourceReader:
    def __init__(self, table, s3, assets_bucket, kb_bucket, meeting_access):
        self.table, self.s3 = table, s3
        self.assets_bucket, self.kb_bucket = assets_bucket, kb_bucket
        self.meeting_access = meeting_access

    def record(self, user_id, pk, sk):
        identity = resource_identity(pk, sk)
        if not isinstance(user_id, str) or not IDENTIFIER.fullmatch(user_id):
            raise ValueError('Authenticated user is required')
        if identity['resourceKind'] != 'meeting':
            return read_current_document(self.table, user_id, pk, identity['resourceId'], raw=True)
        owner, meeting_id = pk[5:], identity['resourceId']
        key = {'PK': pk, 'SK': sk}
        # Identity and authorization metadata only, before notes/transcripts.
        metadata = self.table.get_item(
            Key=key, ConsistentRead=True,
            ProjectionExpression='PK, SK, meetingId, userId, accountId, sharedToAccount',
        ).get('Item')
        if not self._meeting_identity(metadata, key, owner, meeting_id):
            return None
        if not self.meeting_access(user_id, owner, meeting_id, metadata):
            return None
        record = self.table.get_item(Key=key, ConsistentRead=True).get('Item')
        if not self._meeting_identity(record, key, owner, meeting_id):
            return None
        if not self.meeting_access(user_id, owner, meeting_id, record):
            return None
        return source_fields(identity, record)

    @staticmethod
    def _meeting_identity(record, key, owner, meeting_id):
        return bool(record and record.get('PK') == key['PK'] and record.get('SK') == key['SK']
                    and record.get('meetingId') == meeting_id and record.get('userId') == owner)

    def head(self, key, bucket=None):
        bucket = self.assets_bucket if bucket is None else bucket
        if not bucket:
            raise ValueError('Source bucket is not configured')
        try:
            response = self.s3.head_object(Bucket=bucket, Key=key)
        except ClientError as exc:
            # HeadObject represents absence as generic HTTP 404, not a modeled
            # NoSuchKey exception. Never turn AccessDenied/transport errors into absence.
            if exc.response.get('Error', {}).get('Code') in ('404', 'NoSuchKey', 'NotFound'):
                return {'key': key, 'etag': '', 'size': 0, 'missing': True}
            raise
        etag, size = response.get('ETag'), response.get('ContentLength')
        if not isinstance(etag, str) or not etag or isinstance(size, bool) or not isinstance(size, int) or size < 0:
            raise ValueError('Invalid source object headers')
        metadata = response.get('Metadata') or {}
        binding = {name: metadata[name] for name in ('source-etag', 'source-version-id')
                   if metadata.get(name)}
        obj = {'key': key, 'etag': etag, 'size': size}
        if response.get('VersionId'):
            obj['versionId'] = response['VersionId']
        if binding:
            obj['metadata'] = binding
        return obj

    def bytes(self, obj, bucket=None):
        if obj.get('missing') or obj['size'] > MAX_SOURCE_BYTES:
            raise ValueError('Source object unavailable or too large')
        request = {'Bucket': bucket or self.assets_bucket, 'Key': obj['key'], 'IfMatch': obj['etag']}
        if obj.get('versionId'):
            request['VersionId'] = obj['versionId']
        response = self.s3.get_object(**request)
        body = response['Body']
        try:
            if (response.get('ETag') != obj['etag']
                    or obj.get('versionId') and response.get('VersionId') != obj['versionId']):
                raise ValueError('Source object changed')
            data = body.read(obj['size'] + 1)
            if len(data) != obj['size']:
                raise ValueError('Source object size changed')
            return data
        finally:
            body.close()

    def read(self, user_id, pk, sk):
        identity = resource_identity(pk, sk)
        fields = self.record(user_id, pk, sk)
        if fields is None:
            return None
        return self.snapshot(identity, fields)

    def snapshot(self, identity, fields):
        """Called only with an authorized canonical record; never index metadata."""
        objects, filenames = [], []
        outcome, error = 'INDEXED', ''
        if identity['resourceKind'] == 'meeting':
            for field in ('transcriptA', 'transcriptB'):
                value = fields.get(field) or ''
                if value.startswith('s3://'):
                    key = validate_transcript_ref(value, bucket_name=self.assets_bucket,
                                                  meeting_id=identity['resourceId'], field=field)
                    objects.append(self.head(key))
            if fields.get('actionItems'):
                json.loads(fields['actionItems'])
            filenames = ['meeting.md']
        else:
            content, key = fields.get('content') or '', fields.get('fileKey') or ''
            if content:
                filenames.append('document.md')
            if key:
                parts = key.split('/')
                if (len(parts) < 3 or parts[0] != 'docs' or not IDENTIFIER.fullmatch(parts[1])
                        or any(part in ('', '.', '..') for part in parts)
                        or identity['resourceKind'] == 'personalDocument' and identity['sourcePK'] != 'USER#' + parts[1]):
                    raise ValueError('Invalid document source key')
                original = self.head(key)
                objects.append(original)
                obj, extension = original, PurePosixPath(key).suffix.lower()
                if original.get('missing'):
                    outcome, error = 'WAITING_SOURCE', 'FILE_MISSING'
                if extension in ('.ppt', '.pptx'):
                    preview = self.head('docs-pdf/' + key[5:] + '.pdf')
                    objects.append(preview)
                    binding = preview.get('metadata', {})
                    if (original.get('missing') or preview.get('missing') or not binding.get('source-etag')
                            or binding['source-etag'].strip('"') != original['etag'].strip('"')
                            or original.get('versionId') and binding.get('source-version-id') != original['versionId']):
                        outcome, error = 'WAITING_SOURCE', 'PREVIEW_PENDING'
                    obj, extension = preview, '.pdf'
                if extension not in FILE_EXTENSIONS:
                    outcome, error = 'WAITING_SOURCE', 'UNSUPPORTED_FILE'
                elif outcome == 'INDEXED' and obj['size'] == 0:
                    outcome, error = 'WAITING_SOURCE', 'EMPTY_FILE'
                if outcome == 'INDEXED':
                    filenames.append('file' + extension)
            elif not content.strip():
                outcome, error = 'WAITING_SOURCE', 'EMPTY_DOCUMENT'
        if outcome != 'INDEXED':
            filenames = []
        revision = source_revision(identity, fields, objects, outcome)
        return {'identity': identity, 'fields': fields, 'objects': objects, 'revision': revision,
                'outcome': outcome, 'error': error, 'filenames': filenames}

    def meeting_text(self, snapshot, include_notes=True):
        fields, identity = snapshot['fields'], snapshot['identity']
        parts = []
        if fields.get('title'):
            parts.append('제목: ' + fields['title'])
        for name, title in (('notes', '사용자 메모'), ('content', '저장된 요약'), ('actionItems', '저장된 작업 항목')):
            if name == 'notes' and not include_notes:
                continue
            if fields.get(name):
                parts.append(f'## {title}\n{fields[name]}')
        variants = {name: fields.get(name) or '' for name in ('transcriptA', 'transcriptB')}
        for _ in range(2):
            a, b = variants['transcriptA'], variants['transcriptB']
            selected = 'transcriptB' if b.strip() and (fields.get('selectedTranscript') == 'B' or not a.strip()) else 'transcriptA'
            text = variants[selected]
            if text.startswith('s3://'):
                key = validate_transcript_ref(text, bucket_name=self.assets_bucket,
                                              meeting_id=identity['resourceId'], field=selected)
                obj = next(obj for obj in snapshot['objects'] if obj['key'] == key)
                variants[selected] = self.bytes(obj).decode('utf-8', errors='strict')
                if not variants[selected].strip():
                    continue
                text = variants[selected]
            if text.strip():
                parts.append('## 트랜스크립트\n' + text)
            break
        return '\n\n'.join(parts)
