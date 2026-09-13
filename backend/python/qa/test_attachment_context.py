"""Attachment source-contract tests without QA handler integration dependencies."""
import copy
import io
import json
import unittest
from unittest import mock

import test_handler as helpers
from source_context import SourceReader

handler = helpers.handler


class _AttachmentFixture:
    def setUp(self):
        self.table = helpers.RetrievalTable()
        self.s3 = mock.Mock()
        for patcher in (
            mock.patch.object(handler, 'table', self.table),
            mock.patch.object(handler, 's3_client', self.s3),
            mock.patch.object(handler, 'BUCKET_NAME', 'assets'),
            mock.patch('socket.socket', side_effect=AssertionError('live network forbidden')),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#m',
                                 'meetingId': 'm', 'userId': 'owner', 'content': 'summary'})
        self.table.put_item(Item={'PK': 'USER#reader', 'SK': 'SHARED#m',
                                 'meetingId': 'm', 'ownerId': 'owner'})
        self.attachment = {
            'PK': 'MEETING#m', 'SK': 'ATTACH#a', 'attachmentId': 'a', 'meetingId': 'm',
            'userId': 'editor', 'originalKey': 'files/editor/m/upload.pdf', 'fileName': '자료.pdf',
        }
        self.state = {
            'PK': 'MEETING#m', 'SK': 'ATTEXT#a', 'ownerId': 'owner', 'uploaderId': 'editor',
            'sourceKey': self.attachment['originalKey'], 'sourceETag': '"source"',
            'runId': 'run-1', 'resultKey': 'files/editor/m/text/a/run-1.json',
            'status': 'succeeded', 'complete': True, 'unitCount': 1, 'leaseUntil': 0,
        }
        self.result = {
            'schemaVersion': 1, 'format': 'pdf', 'status': 'succeeded', 'complete': True,
            'scope': 'native_page_text', 'units': [{'text': '현재 파일 사실', 'location': {'kind': 'page', 'page': 3}}],
            'warnings': [], 'metrics': {'units': 1, 'textBytes': len('현재 파일 사실'.encode())},
            'source': {'bucket': 'assets', 'key': self.attachment['originalKey'], 'eTag': '"source"',
                       'meetingId': 'm', 'ownerId': 'owner', 'uploaderId': 'editor',
                       'attachmentId': 'a', 'runId': 'run-1'},
        }
        self.table.items[('MEETING#m', 'ATTACH#a')] = self.attachment
        self.table.items[('MEETING#m', 'ATTEXT#a')] = self.state
        self.source_etag = '"source"'
        self.bodies = []
        self.s3.head_object.side_effect = self.head
        self.s3.get_object.side_effect = self.get

    def encoded(self):
        return json.dumps(self.result, ensure_ascii=False).encode()

    def head(self, **kwargs):
        self.assertEqual(kwargs['Bucket'], 'assets')
        if kwargs['Key'] == self.attachment['originalKey']:
            return {'ETag': self.source_etag, 'ContentLength': 100}
        self.assertEqual(kwargs['Key'], 'files/editor/m/text/a/run-1.json')
        return {'ETag': '"result"', 'ContentLength': len(self.encoded())}

    def get(self, **kwargs):
        self.assertEqual(kwargs, {'Bucket': 'assets', 'Key': 'files/editor/m/text/a/run-1.json',
                                  'IfMatch': '"result"'})
        body = io.BytesIO(self.encoded())
        self.bodies.append(body)
        return {'ETag': '"result"', 'Body': body}

    def reader(self):
        from attachment_context import AttachmentReader
        return AttachmentReader(SourceReader(self.table, self.s3, 'assets', 'knowledge', handler._has_meeting_access), handler._query_all)

    def read(self, **kwargs):
        return self.reader().read('reader', 'USER#owner', 'm', 'a', **kwargs)


class AttachmentContextTests(_AttachmentFixture, unittest.TestCase):
    def test_auth_precedes_attachment_and_s3_reads_and_owner_differs_from_uploader(self):
        del self.table.items[('USER#reader', 'SHARED#m')]
        self.assertIsNone(self.read())
        self.assertFalse(any(key[0] == 'MEETING#m' for key, _ in self.table.reads))
        self.s3.head_object.assert_not_called()
        self.table.put_item(Item={'PK': 'USER#reader', 'SK': 'SHARED#m', 'ownerId': 'owner'})
        page = self.read()
        self.assertEqual(page['units'][0]['location'], {'kind': 'page', 'page': 3})
        self.assertEqual(page['units'][0]['text'], '현재 파일 사실')
        self.assertTrue(all(body.closed for body in self.bodies))
        self.assertNotIn('timestamp', json.dumps(page))

    def test_wrong_result_key_and_json_source_identity_never_supply_text(self):
        for key in ('files/owner/m/text/a/run-1.json', 'files/editor/other/text/a/run-1.json',
                    'files/editor/m/text/other/run-1.json', 'files/editor/m/text/a/../run-1.json',
                    'files/editor/m/text/a/run-1.json?x=1', 'files/editor/m/text/a/%72un-1.json'):
            with self.subTest(key=key):
                self.state['resultKey'] = key
                self.s3.reset_mock()
                with self.assertRaises(ValueError):
                    self.read()
                self.s3.head_object.assert_not_called()
        self.state['resultKey'] = 'files/editor/m/text/a/run-1.json'
        for field in self.result['source']:
            with self.subTest(field=field):
                old = self.result['source'][field]
                self.result['source'][field] = 'wrong'
                with self.assertRaises(ValueError):
                    self.read()
                self.result['source'][field] = old

    def test_changed_original_and_state_revoke_result_even_when_old_object_remains(self):
        self.assertTrue(self.read()['available'])
        self.source_etag = '"changed"'
        with self.assertRaises(ValueError):
            self.read()
        self.s3.get_object.reset_mock()
        self.source_etag = '"source"'
        original_get = self.get

        def revoked_during_read(**kwargs):
            response = original_get(**kwargs)
            del self.table.items[('USER#reader', 'SHARED#m')]
            return response
        self.s3.get_object.side_effect = revoked_during_read
        self.assertIsNone(self.read())
        self.assertTrue(all(body.closed for body in self.bodies))

    def test_failed_retry_retains_verified_prior_result_without_claiming_current_success(self):
        self.state.update(status='failed', runId='new-run', errorCode='TIMEOUT')
        page = self.read()
        self.assertEqual(page['attempt']['status'], 'failed')
        self.assertEqual(page['attempt']['errorCode'], 'TIMEOUT')
        self.assertTrue(page['usingPreviousResult'])
        self.assertEqual(page['result']['status'], 'succeeded')
        self.assertTrue(page['result']['complete'])
        self.state.update(resultKey='', sourceETag='')
        unavailable = self.read()
        self.assertFalse(unavailable['available'])
        self.assertEqual(unavailable['units'], [])
        self.assertNotIn('현재 파일 사실', json.dumps(unavailable))

    def test_partial_text_has_unit_continuation_and_no_audio_locations(self):
        text = '긴 파일 내용 ' * 4000
        self.result.update(status='partial', complete=False)
        self.result['units'] = [{'text': text, 'location': {'kind': 'page', 'page': 3}}]
        self.result['metrics'] = {'units': 1, 'textBytes': len(text.encode())}
        self.state.update(status='partial', complete=False)
        recovered, unit_offset, text_offset, revision = '', 0, 0, None
        for _ in range(100):
            page = self.read(unit_offset=unit_offset, text_offset=text_offset, max_bytes=4000,
                             expected_revision=revision)
            revision = page['sourceRevision']
            self.assertLessEqual(len(json.dumps(page, ensure_ascii=False).encode()), 4000)
            recovered += ''.join(unit['text'] for unit in page['units'])
            if 'nextUnitOffset' not in page:
                break
            unit_offset, text_offset = page['nextUnitOffset'], page['nextTextOffset']
        self.assertEqual(recovered, text)
        self.result['units'][0]['location']['startTime'] = 30
        with self.assertRaises(ValueError):
            self.read()

    def test_actual_result_bytes_are_bounded_and_stream_closed(self):
        class HugeBody(io.BytesIO):
            def __init__(self):
                super().__init__(b'x' * (1024 * 1024 + 1))
                self.read_limit = None
            def read(self, size=-1):
                self.read_limit = size
                return super().read(size)
        body = HugeBody()
        self.s3.head_object.side_effect = lambda **kw: (
            {'ETag': '"source"', 'ContentLength': 100} if kw['Key'] == self.attachment['originalKey']
            else {'ETag': '"result"', 'ContentLength': 1024 * 1024})
        self.s3.get_object.side_effect = lambda **kw: {'ETag': '"result"', 'Body': body}
        with self.assertRaises(ValueError):
            self.read()
        self.assertTrue(body.closed)
        self.assertLessEqual(body.read_limit, 1024 * 1024 + 1)

    def test_continuation_is_bound_to_the_extraction_revision(self):
        page = self.read()
        self.assertIn('sourceRevision', page)
        self.state['updatedAt'] = 'changed-attempt-metadata'
        self.s3.get_object.reset_mock()
        with self.assertRaises(ValueError):
            self.read(expected_revision=page['sourceRevision'])
        self.s3.get_object.assert_not_called()
