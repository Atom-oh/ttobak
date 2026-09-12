"""Source reader contract tests; independent of the QA handler integration."""
import copy
import io
import json
from pathlib import Path
import unittest
from unittest import mock

import test_handler as helpers
from source_context import SourceReader

handler = helpers.handler


class _SourceFixture:
    def setUp(self):
        self.table = helpers.RetrievalTable()
        self.s3 = mock.Mock()
        self.patches = [
            mock.patch.object(handler, 'table', self.table),
            mock.patch.object(handler, 's3_client', self.s3),
            mock.patch.object(handler, 'BUCKET_NAME', 'assets'),
            mock.patch.object(handler, 'KB_BUCKET_NAME', 'knowledge', create=True),
            mock.patch('socket.socket', side_effect=AssertionError('live network forbidden')),
        ]
        for patcher in self.patches:
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = mock.patch.object(handler, 'bedrock_agent_runtime')
        self.runtime = patcher.start()
        self.addCleanup(patcher.stop)
        self.runtime.retrieve.return_value = {'retrievalResults': []}
        self.source_reader = SourceReader(self.table, self.s3, 'assets', 'knowledge', handler._has_meeting_access)

    def doc(self, pk='USER#owner', content='current note', **extra):
        row = {'PK': pk, 'SK': 'DOC#doc', 'docId': 'doc',
               'sourceUserId': 'owner', 'entityType': 'USER_DOC',
               'title': 'Document', 'content': content, 'updatedAt': 'same-time'}
        if pk.startswith('ACCOUNT#'):
            row.update(accountId=pk[8:], entityType='ACCOUNT_DOC')
        row.update(extra)
        self.table.put_item(Item=row)
        return self.table.items[(pk, 'DOC#doc')]

    def grant(self):
        self.table.put_item(Item={
            'PK': 'USER#reader', 'SK': 'SHAREDDOC#doc', 'entityType': 'DOC_SHARE',
            'meetingId': 'doc', 'ownerId': 'owner', 'sharedToId': 'reader', 'permission': 'read',
        })

    def indexed(self, pk='USER#owner', sk='DOC#doc', filename='document.md', text='STALE_INDEX'):
        snapshot = self.source_reader.read('owner' if pk.startswith('USER') else 'reader', pk, sk)
        identity = snapshot['identity']
        run = '00000000-0000-4000-8000-000000000001'
        uri = (f"s3://knowledge/canonical/v1/{identity['resourceKind']}/"
               f"{identity['resourceHash']}/{run}/{filename}")
        return {
            'score': 0.9, 'content': {'text': text}, 'location': {'s3Location': {'uri': uri}},
            'metadata': dict(identity, indexSchema='canonical-v1', indexRunId=run,
                             sourceRevision=snapshot['revision'], sourceObjects=json.dumps(snapshot['objects'])),
        }


class TestSourceRevision(unittest.TestCase):
    def test_worker_vectors_match_without_normalizing_raw_fields(self):
        from source_revision import source_revision, resource_identity
        path = Path(__file__).resolve().parent / 'testdata/index-revisions.json'
        vectors = json.loads(path.read_text())
        go_vectors = Path(__file__).resolve().parents[2] / 'internal/service/testdata/index-revisions.json'
        if go_vectors.exists():
            self.assertEqual(vectors, json.loads(go_vectors.read_text()), 'worker/QA vectors drifted')
        for vector in vectors:
            with self.subTest(vector=vector['name']):
                identity = resource_identity(vector['pk'], vector['sk'])
                self.assertEqual(identity['resourceHash'], vector['resourceHash'])
                self.assertEqual(source_revision(identity, vector['fields'], vector['objects'],
                                                 vector['outcome']), vector['revision'])
                changed = dict(vector['fields'], content='')
                if changed != vector['fields']:
                    self.assertNotEqual(source_revision(identity, changed, vector['objects'],
                                                        vector['outcome']), vector['revision'])

    def test_projection_uri_pins_bucket_identity_kind_run_and_filename(self):
        from source_revision import resource_identity, projection_identity
        identity = resource_identity('USER#owner', 'DOC#doc')
        run = '00000000-0000-4000-8000-000000000001'
        uri = f"s3://knowledge/canonical/v1/personalDocument/{identity['resourceHash']}/{run}/file.pdf"
        metadata = dict(identity, indexSchema='canonical-v1', sourceRevision='a' * 64,
                        indexRunId=run, sourceObjects='[]')
        self.assertEqual(projection_identity(uri, metadata, 'knowledge')['sourcePK'], 'USER#owner')
        for bad in (uri.replace('knowledge/', 'other/'), uri + '?query=1', uri + '#fragment',
                    uri.replace('file.pdf', '../file.pdf'), uri.replace('file.pdf', '%66ile.pdf'),
                    uri.replace('personalDocument', 'meeting'), uri.replace(run, 'other-run')):
            with self.subTest(uri=bad):
                self.assertIsNone(projection_identity(bad, metadata, 'knowledge'))
        for field, bad in (('sourcePK', 'USER#other'), ('sourceSK', 'DOC#other'),
                           ('resourceKind', 'accountDocument'), ('indexSchema', 'old'),
                           ('resourceId', 'other'), ('sourceRevision', 'x' * 64)):
            altered = dict(metadata, **{field: bad})
            self.assertIsNone(projection_identity(uri, altered, 'knowledge'), field)

class TestSourceReads(_SourceFixture, unittest.TestCase):
    def test_document_auth_precedes_content_and_s3_and_raw_fields_survive(self):
        self.doc(content=None, fileKey='docs/owner/file.pdf')
        self.assertIsNone(self.source_reader.read('reader', 'USER#owner', 'DOC#doc'))
        self.s3.head_object.assert_not_called()
        self.assertNotIn(('USER#owner', 'DOC#doc'), [key for key, _ in self.table.reads])
        self.grant()
        self.s3.head_object.return_value = {'ETag': '"etag"', 'VersionId': 'v1', 'ContentLength': 4}
        current = self.source_reader.read('reader', 'USER#owner', 'DOC#doc')
        self.assertIsNone(current['fields']['content'])
        self.assertNotIn('docType', current['fields'])
        self.assertEqual(current['objects'][0]['versionId'], 'v1')
        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
        self.s3.reset_mock()
        self.assertIsNone(self.source_reader.read('reader', 'USER#owner', 'DOC#doc'))
        self.s3.head_object.assert_not_called()

    def test_meeting_denial_reads_only_authorization_metadata(self):
        self.table.put_item(Item={
            'PK': 'USER#owner', 'SK': 'MEETING#m', 'meetingId': 'm', 'userId': 'owner',
            'content': 'PRIVATE', 'notes': 'PRIVATE',
            'transcriptB': 's3://assets/transcripts/m/transcriptB.txt',
        })
        self.assertIsNone(self.source_reader.read('reader', 'USER#owner', 'MEETING#m'))
        self.s3.head_object.assert_not_called()
        for key, options in self.table.reads:
            if key == ('USER#owner', 'MEETING#m'):
                self.assertIn('ProjectionExpression', options)
                self.assertNotIn('content', options['ProjectionExpression'])
                self.assertNotIn('notes', options['ProjectionExpression'])
