"""Synthetic binary source and immutable-snapshot fixtures shared by QA tests."""
import hashlib
import io
import json
from pathlib import Path
from unittest import mock
import zipfile

from botocore.exceptions import ClientError
import test_handler as helpers

handler = helpers.handler


def binary_fixture(extension, marker):
    if extension == '.docx':
        output = io.BytesIO()
        with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_STORED) as archive:
            archive.writestr('[Content_Types].xml', '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>')
            archive.writestr('_rels/.rels', '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>')
            archive.writestr('word/document.xml', '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>' + marker + '</w:t></w:r></w:p></w:body></w:document>')
        return output.getvalue()
    stream = ('BT /F1 12 Tf 72 72 Td (' + marker + ') Tj ET').encode()
    objects = [
        b'<< /Type /Catalog /Pages 2 0 R >>',
        b'<< /Type /Pages /Count 1 /Kids [3 0 R] >>',
        b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>',
        b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',
        b'<< /Length ' + str(len(stream)).encode() + b' >>\nstream\n' + stream + b'\nendstream',
    ]
    body, offsets = b'%PDF-1.4\n', [0]
    for index, obj in enumerate(objects, 1):
        offsets.append(len(body))
        body += f'{index} 0 obj\n'.encode() + obj + b'\nendobj\n'
    xref = len(body)
    body += b'xref\n0 6\n0000000000 65535 f \n'
    body += b''.join(f'{offset:010d} 00000 n \n'.encode() for offset in offsets[1:])
    return body + f'trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n'.encode()


class _KBFixture:
    def setUp(self):
        self.table = helpers.RetrievalTable()
        self.s3, self.runtime = mock.Mock(), mock.Mock()
        self.key, self.body, self.version = 'kb/owner/file.pdf', binary_fixture('.pdf', 'CURRENT_V1'), 'version-1'
        self.snapshots = []
        for patcher in (
            mock.patch.object(handler, 'table', self.table),
            mock.patch.object(handler, 's3_client', self.s3),
            mock.patch.object(handler, 'KB_BUCKET_NAME', 'knowledge', create=True),
            mock.patch.object(handler, 'BUCKET_NAME', 'assets'),
            mock.patch.object(handler, 'bedrock_agent_runtime', self.runtime),
            mock.patch('socket.socket', side_effect=AssertionError('live network forbidden')),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.s3.head_object.side_effect = self.head
        self.runtime.retrieve.side_effect = self.retrieve

    def head(self, **kwargs):
        self.assertEqual(kwargs, {'Bucket': 'knowledge', 'Key': self.key})
        if self.body is None:
            raise ClientError({'Error': {'Code': '404'}}, 'HeadObject')
        return {'ETag': '"' + hashlib.md5(self.body).hexdigest() + '"',
                'VersionId': self.version, 'ContentLength': len(self.body)}

    def old_hit(self):
        return {'score': 0.9, 'location': {'s3Location': {'uri': 's3://knowledge/' + self.key}},
                'content': {'text': 'OLD_UNBOUND_PRIVATE_CHUNK'}}

    @staticmethod
    def matches(condition, hit):
        if 'orAll' in condition:
            return any(_KBFixture.matches(child, hit) for child in condition['orAll'])
        if 'andAll' in condition:
            return all(_KBFixture.matches(child, hit) for child in condition['andAll'])
        operation, data = next(iter(condition.items()))
        actual = (hit['location']['s3Location']['uri'] if data['key'] == 'x-amz-bedrock-kb-source-uri'
                  else hit.get('metadata', {}).get(data['key']))
        return isinstance(actual, str) and actual.startswith(data['value']) if operation == 'startsWith' else actual == data['value']

    def retrieve(self, **kwargs):
        filters = kwargs['retrievalConfiguration']['vectorSearchConfiguration']['filter']
        return {'retrievalResults': [hit for hit in [self.old_hit(), *self.snapshots] if self.matches(filters, hit)]}

    def snapshot_fixture(self, marker, run='00000000-0000-4000-8000-000000000001'):
        """Represent ingestion of a NEW immutable copy, never tag the old key."""
        from manual_kb import manual_revision
        head = self.head(Bucket='knowledge', Key=self.key)
        copied_bytes = bytes(self.body)
        self.assertIn(marker.encode(), copied_bytes)
        resource_id = hashlib.sha256(self.key.encode()).hexdigest()
        revision = manual_revision('knowledge', self.key, head['ETag'], self.version, len(copied_bytes))
        extension = Path(self.key).suffix.lower()
        uri = f's3://knowledge/manual-kb/v1/owner/{resource_id}/{revision}/{run}/document{extension}'
        self.assertNotEqual(uri, self.old_hit()['location']['s3Location']['uri'])
        return {'score': 0.95, 'location': {'s3Location': {'uri': uri}}, 'content': {'text': marker},
                'metadata': {'indexSchema': 'manual-kb-v1', 'resourceKind': 'manualKbDocument',
                             'ownerId': 'owner', 'resourceId': resource_id, 'sourceRevision': revision,
                             'indexRunId': run, 'sourceBucket': 'knowledge', 'sourceKey': self.key,
                             'sourceETag': head['ETag'], 'sourceVersionId': self.version,
                             'sourceSize': len(copied_bytes)}}

class _SharedKBFixture(_KBFixture):
    def setUp(self):
        super().setUp()
        self.key = 'shared/reference/file.pdf'

    def shared_snapshot(self, marker='CURRENT_V1', run='00000000-0000-4000-8000-000000000001'):
        from manual_kb import binary_revision
        obj = self.head(Bucket='knowledge', Key=self.key)
        resource_id = hashlib.sha256(self.key.encode()).hexdigest()
        revision = binary_revision('shared-kb-v1', 'knowledge', self.key, obj['ETag'],
                                   self.version, len(self.body))
        uri = (f's3://knowledge/shared-kb/v1/{resource_id}/{revision}/{run}/'
               f'document{Path(self.key).suffix.lower()}')
        return {
            'score': 0.95, 'location': {'s3Location': {'uri': uri}}, 'content': {'text': marker},
            'metadata': {'indexSchema': 'shared-kb-v1', 'resourceKind': 'sharedKbDocument',
                         'visibility': 'authenticated-shared', 'resourceId': resource_id,
                         'sourceRevision': revision, 'indexRunId': run, 'sourceBucket': 'knowledge',
                         'sourceKey': self.key, 'sourceETag': obj['ETag'],
                         'sourceVersionId': self.version, 'sourceSize': len(self.body)},
        }


class _QAConversationFixture:
    """Synthetic Converse replies and the real REST/WebSocket entrypoints."""

    def set_up_transport(self, table):
        self.table = table
        for patch in (mock.patch.object(handler, 'table', self.table),
                      mock.patch('socket.socket', side_effect=AssertionError('live network forbidden')),
                      mock.patch.object(handler, '_apigw_client'),
                      mock.patch.object(handler, '_post_ws', return_value=True)):
            patch.start()
            self.addCleanup(patch.stop)
        patch = mock.patch.object(handler, 'bedrock_runtime')
        self.model = patch.start()
        self.addCleanup(patch.stop)

    def replies(self, transport, name, arguments):
        tool = {'toolUseId': 'tool1', 'name': name, 'input': arguments}
        messages = [[{'toolUse': tool}], [{'text': 'PRIVATE_CHOICE'}],
                    [{'text': 'stable followup'}], [{'text': 'fresh answer'}]]
        if transport == 'rest':
            self.model.converse.side_effect = [
                {'stopReason': 'tool_use' if i == 0 else 'end_turn',
                 'output': {'message': {'role': 'assistant', 'content': content}}}
                for i, content in enumerate(messages)]
            return self.model.converse
        streams = []
        for i, content in enumerate(messages):
            if i == 0:
                start, delta = {'toolUse': {k: tool[k] for k in ('toolUseId', 'name')}}, {
                    'toolUse': {'input': json.dumps(arguments)}}
            else:
                start, delta = {}, content[0]
            streams.append({'stream': [
                {'contentBlockStart': {'start': start}}, {'contentBlockDelta': {'delta': delta}},
                {'contentBlockStop': {}}, {'messageStop': {'stopReason': 'tool_use' if i == 0 else 'end_turn'}},
            ]})
        self.model.converse_stream.side_effect = streams
        return self.model.converse_stream

    def ask(self, transport, question, meeting_id=None):
        if transport == 'rest':
            result = handler.handle_ask(question, user_id='reader', session_id='chat-readonly', meeting_id=meeting_id)
            self.assertEqual(result['statusCode'], 200, result)
        else:
            result = handler.handle_ask_stream({
                'question': question, 'userId': 'reader', 'sessionId': 'chat-readonly',
                'meetingId': meeting_id,
                'connectionId': 'c', 'endpoint': 'https://synthetic.invalid',
            })
            self.assertEqual(result['status'], 'ok', result)
        return result
