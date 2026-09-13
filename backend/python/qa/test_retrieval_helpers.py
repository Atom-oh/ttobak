"""Direct source/search/history contracts, independent of the live QA handler."""
import copy
from decimal import Decimal
import hashlib
import io
import json
import unittest
from unittest import mock

import test_handler as helpers
from test_source_contract import _SourceFixture
from boto3.dynamodb.types import TypeDeserializer, TypeSerializer

from indexed_retrieval import discover_sources, discovery_filters, hydrate_candidates
from manual_kb import (
    binary_revision, hydrate_manual_candidates, snapshot_identity,
    SCHEMA, SHARED_SCHEMA,
)
from session_provenance import new_source_state, remember_source, restore_sources, validate_sources


def candidate(hit):
    return {'uri': hit['location']['s3Location']['uri'], 'score': hit['score'],
            'metadata': hit.get('metadata', {}), '_provider': hit}


class TestCurrentCandidates(_SourceFixture, unittest.TestCase):
    def discover(self, user):
        return discover_sources(self.source_reader, user, helpers.handler._query_all, [])

    def test_share_discovery_aliases_reserved_attributes(self):
        self.doc(content='SHARED_NOTE')
        self.grant()
        def query_all(**kwargs):
            # DynamoDB rejects these bare names before evaluating any rows.
            for name in kwargs.get('ProjectionExpression', '').split(','):
                if name.strip().upper() in {'PERMISSION', 'PERMISSIONS'}:
                    raise ValueError('reserved DynamoDB projection attribute')
            return helpers.handler._query_all(**kwargs)
        identities, _ = discover_sources(self.source_reader, 'reader', query_all, [])
        self.assertIn(('USER#owner', 'DOC#doc'), identities)

    def test_discovery_consumes_all_pages_and_live_membership(self):
        for i in range(4):
            self.table.put_item(Item={'PK': 'USER#reader', 'SK': f'DOC#doc{i}'})
        self.doc(pk='ACCOUNT#child', content='CHILD_NOTE')
        member = {'PK': 'ACCOUNT#child', 'SK': 'MEMBER#reader', 'accountId': 'child',
                  'userId': 'reader', 'GSI1PK': 'USER#reader', 'GSI1SK': 'ACCOUNT#child'}
        self.table.put_item(Item=member)
        identities, accounts = self.discover('reader')
        self.assertEqual(len(identities), 5)
        self.assertEqual(accounts, {'ACCOUNT#child'})
        self.table.index_rows = copy.deepcopy(list(self.table.items.values()))
        del self.table.items[('ACCOUNT#child', 'MEMBER#reader')]
        identities, accounts = self.discover('reader')
        self.assertEqual(len(identities), 4)
        self.assertEqual(accounts, set())
        self.assertTrue(any('ExclusiveStartKey' in call for call in self.table.queries))

    def test_revocation_after_discovery_removes_saved_text_and_provenance(self):
        self.doc(content='PRIVATE_TERM')
        self.grant()
        identities, _ = self.discover('reader')
        self.assertIn(('USER#owner', 'DOC#doc'), identities)
        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
        result = hydrate_candidates(self.source_reader, 'reader', 'PRIVATE_TERM', [], identities, 5)
        self.assertEqual(result, [])
        self.s3.head_object.assert_not_called()

    def test_saved_edit_is_searchable_before_indexing_and_deletion_removes_it(self):
        row = self.doc(content='OLD_TERM')
        identities, _ = self.discover('owner')
        self.assertEqual(hydrate_candidates(self.source_reader, 'owner', 'NEW_TERM', [], identities, 5), [])
        row['content'] = 'NEW_TERM'
        results = hydrate_candidates(self.source_reader, 'owner', 'NEW_TERM', [], identities, 5)
        self.assertEqual(results[0]['document']['content'], 'NEW_TERM')
        self.assertEqual(results[0]['provenance']['contentSource'], 'current_saved_keyword_match')
        del self.table.items[('USER#owner', 'DOC#doc')]
        self.assertEqual(hydrate_candidates(self.source_reader, 'owner', 'NEW_TERM', [], identities, 5), [])

    def test_old_meeting_export_is_only_an_identity(self):
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#m1', 'meetingId': 'm1',
                                 'userId': 'owner', 'notes': 'CURRENT_NOTE', 'content': 'CURRENT_SUMMARY'})
        hit = {'uri': 's3://knowledge/meetings/owner/m1.md', 'score': .9,
               '_provider': {'content': {'text': 'STALE_PRIVATE_TEXT'}}}
        results = hydrate_candidates(self.source_reader, 'owner', 'note', [hit], {}, 5)
        self.assertEqual(results[0]['meeting']['notes'], 'CURRENT_NOTE')
        self.assertNotIn('STALE_PRIVATE_TEXT', json.dumps(results))

    def test_file_bytes_must_match_current_binding(self):
        self.doc(content='', fileKey='docs/owner/file.pdf')
        self.s3.head_object.return_value = {'ETag': '"one"', 'VersionId': 'v1', 'ContentLength': 10}
        hit = candidate(self.indexed(filename='file.pdf', text='OLD_BYTES'))
        current = hydrate_candidates(self.source_reader, 'owner', 'file', [hit], {}, 5)
        self.assertEqual(current[0]['text'], 'OLD_BYTES')
        self.s3.head_object.return_value = {'ETag': '"two"', 'VersionId': 'v2', 'ContentLength': 10}
        changed = hydrate_candidates(self.source_reader, 'owner', 'file', [hit], {}, 5)
        self.assertNotIn('OLD_BYTES', json.dumps(changed))
        self.assertTrue(changed[0]['document']['filePending'])

    def test_duplicate_candidates_keep_the_best_score_independent_of_group_order(self):
        self.doc(content='CURRENT')
        hit = candidate(self.indexed())
        lower, higher = dict(hit, score=.5), dict(hit, score=.9)
        for candidates in ([lower, higher], [higher, lower]):
            result = hydrate_candidates(self.source_reader, 'owner', 'query', candidates, {}, 5)
            self.assertEqual(result[0]['score'], .9)

    def test_old_file_hit_does_not_mark_current_markdown_as_file_pending(self):
        row = self.doc(content='', fileKey='docs/owner/file.pdf')
        self.s3.head_object.return_value = {'ETag': '"one"', 'VersionId': 'v1', 'ContentLength': 10}
        hit = candidate(self.indexed(filename='file.pdf', text='OLD_FILE'))
        row.pop('fileKey')
        row['content'] = 'CURRENT_MARKDOWN'
        result = hydrate_candidates(self.source_reader, 'owner', 'query', [hit], {}, 5)
        self.assertEqual(result[0]['document']['content'], 'CURRENT_MARKDOWN')
        self.assertNotIn('filePending', result[0]['document'])
        self.assertNotIn('filePending', result[0]['provenance'])

    def test_keyword_fallback_retains_new_saved_text_without_crowding_out_file_evidence(self):
        self.s3.head_object.return_value = {'ETag': '"one"', 'VersionId': 'v1', 'ContentLength': 10}
        hits = []
        for i in range(2):
            row = dict(self.doc(content='', fileKey='docs/owner/file.pdf'),
                       SK=f'DOC#file{i}', docId=f'file{i}')
            self.table.put_item(Item=row)
            hits.append(candidate(self.indexed(sk=f'DOC#file{i}', filename='file.pdf', text=f'FILE_FACT_{i}')))
        for i in range(7):
            self.table.put_item(Item=dict(self.doc(content='NEW_TERM'), SK=f'DOC#note{i}', docId=f'note{i}'))
        identities, _ = self.discover('owner')
        result = hydrate_candidates(self.source_reader, 'owner', 'NEW_TERM', hits, identities, 3)
        self.assertEqual([entry.get('text') for entry in result[:2]], ['FILE_FACT_0', 'FILE_FACT_1'])
        self.assertEqual(result[2]['document']['content'], 'NEW_TERM')

    def test_single_result_keeps_verified_file_ahead_of_literal_fallback(self):
        row = self.doc(content='', fileKey='docs/owner/file.pdf')
        self.s3.head_object.return_value = {'ETag': '"one"', 'VersionId': 'v1', 'ContentLength': 10}
        hit = candidate(self.indexed(filename='file.pdf', text='VERIFIED_FILE'))
        self.table.put_item(Item=dict(row, SK='DOC#new', docId='new', content='NEW_TERM', fileKey=''))
        identities, _ = self.discover('owner')
        result = hydrate_candidates(self.source_reader, 'owner', 'NEW_TERM', [hit], identities, 1)
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0].get('text'), 'VERIFIED_FILE')

    def test_title_only_fallback_cannot_displace_verified_body(self):
        row = self.doc(content='', fileKey='docs/owner/file.pdf')
        self.s3.head_object.return_value = {'ETag': '"one"', 'VersionId': 'v1', 'ContentLength': 10}
        self.table.put_item(Item=dict(row, SK='DOC#second', docId='second'))
        hits = [candidate(self.indexed(sk=sk, filename='file.pdf', text=body))
                for sk, body in [('DOC#doc', 'FIRST_FILE'), ('DOC#second', 'SECOND_FILE')]]
        for body in ('', 'UNRELATED_BODY'):
            with self.subTest(body=body):
                self.table.put_item(Item=dict(row, SK='DOC#title', docId='title',
                                              title='NEW_TERM', content=body, fileKey=''))
                identities, _ = self.discover('owner')
                result = hydrate_candidates(self.source_reader, 'owner', 'NEW_TERM', hits, identities, 2)
                self.assertEqual([entry.get('text') for entry in result], ['FIRST_FILE', 'SECOND_FILE'])

    def test_legacy_text_excerpt_is_bounded_and_marked_partial(self):
        body = b'a' * 9000
        self.s3.head_object.return_value = {'ETag': '"one"', 'ContentLength': len(body)}
        self.s3.get_object.return_value = {'ETag': '"one"', 'Body': io.BytesIO(body)}
        result = hydrate_candidates(self.source_reader, 'owner', 'query', [
            {'uri': 's3://knowledge/kb/owner/large.md', 'score': .8}], {}, 5)
        self.assertEqual(len(result[0]['text']), 6000)
        self.assertTrue(result[0]['provenance']['partial'])

    def test_filter_groups_preserve_every_foreign_grant(self):
        identities = {('USER#owner', f'DOC#d{i}'): {} for i in range(23)}
        filters = discovery_filters('reader', 'knowledge', identities, set(), [])
        def leaves(condition):
            if 'orAll' in condition:
                return [leaf for child in condition['orAll'] for leaf in leaves(child)]
            return [condition]
        actual = [leaf for group in filters for leaf in leaves(group)]
        granted_ids = {
            part['equals']['value']
            for item in actual for part in item.get('andAll', [])
            if part.get('equals', {}).get('key') == 'sourceSK'
        }
        self.assertEqual(granted_ids, {f'DOC#d{i}' for i in range(23)})
        self.assertTrue(all(len(group.get('orAll', [group])) <= 5 for group in filters))


class TestBinaryCandidates(unittest.TestCase):
    def setUp(self):
        self.reader = mock.Mock(kb_bucket='knowledge')
        self.reader.head.return_value = {'key': 'kb/owner/file.pdf', 'etag': '"one"',
                                         'versionId': 'v1', 'size': 10}

    def fixture(self, shared=False):
        key = 'shared/team/file.pdf' if shared else 'kb/owner/file.pdf'
        schema = SHARED_SCHEMA if shared else SCHEMA
        resource = hashlib.sha256(key.encode()).hexdigest()
        revision = binary_revision(schema, 'knowledge', key, '"one"', 'v1', 10)
        run = '00000000-0000-4000-8000-000000000001'
        prefix = 'shared-kb/v1' if shared else 'manual-kb/v1/owner'
        metadata = {'indexSchema': schema, 'resourceKind': 'sharedKbDocument' if shared else 'manualKbDocument',
                    'sourceBucket': 'knowledge', 'sourceKey': key, 'sourceETag': '"one"',
                    'sourceVersionId': 'v1', 'sourceSize': 10, 'resourceId': resource,
                    'sourceRevision': revision, 'indexRunId': run}
        metadata.update({'visibility': 'authenticated-shared'} if shared else {'ownerId': 'owner'})
        return {'uri': f's3://knowledge/{prefix}/{resource}/{revision}/{run}/document.pdf',
                'metadata': metadata, 'score': .9, '_provider': {'content': {'text': 'VERIFIED_FILE'}}}

    def test_private_and_shared_snapshots_reject_old_bytes_and_deleted_original(self):
        for shared in (False, True):
            with self.subTest(shared=shared):
                self.setUp()
                hit = self.fixture(shared)
                user = 'reader' if shared else 'owner'
                ready = hydrate_manual_candidates(self.reader, user, [hit])
                self.assertEqual(ready[0]['text'], 'VERIFIED_FILE')
                self.reader.head.return_value['etag'] = '"two"'
                changed = hydrate_manual_candidates(self.reader, user, [hit])
                self.assertEqual(changed[0]['manualFile']['status'], 'pending')
                self.assertNotIn('VERIFIED_FILE', json.dumps(changed))
                self.reader.head.return_value = {'missing': True}
                self.assertEqual(hydrate_manual_candidates(self.reader, user, [hit]), [])

    def test_private_scope_and_shared_auth_are_checked_before_source_head(self):
        self.assertEqual(hydrate_manual_candidates(self.reader, 'reader', [self.fixture()]), [])
        self.assertEqual(hydrate_manual_candidates(self.reader, None, [self.fixture(True)]), [])
        forged = self.fixture()
        forged['metadata'].update(indexSchema=SHARED_SCHEMA, resourceKind='sharedKbDocument',
                                  visibility='authenticated-shared')
        self.assertIsNone(snapshot_identity(forged['uri'], forged['metadata'], 'knowledge', 'reader'))
        self.reader.head.assert_not_called()

    def test_attaching_new_metadata_to_legacy_uri_does_not_make_old_chunk_current(self):
        hit = self.fixture()
        hit['uri'] = 's3://knowledge/kb/owner/file.pdf'
        result = hydrate_manual_candidates(self.reader, 'owner', [hit])
        self.assertEqual(result[0]['manualFile']['status'], 'pending')
        self.assertNotIn('VERIFIED_FILE', json.dumps(result))

    def test_source_unavailability_is_not_empty_success(self):
        self.reader.head.side_effect = RuntimeError('synthetic source unavailable')
        with self.assertRaisesRegex(RuntimeError, 'unavailable'):
            hydrate_manual_candidates(self.reader, 'owner', [self.fixture()])


class TestSessionDependencies(unittest.TestCase):
    dependency = {'sourcePK': 'USER#owner', 'sourceSK': 'DOC#doc', 'sourceRevision': 'a' * 64}

    def stored(self):
        return {'sourceProvenanceVersion': 1, 'sourceReplayable': True,
                'sourceDependencies': [dict(self.dependency)]}

    def test_real_dynamodb_round_trip_preserves_replay_of_current_sources(self):
        item = self.stored()
        serializer, deserializer = TypeSerializer(), TypeDeserializer()
        stored = {name: deserializer.deserialize(serializer.serialize(value)) for name, value in item.items()}
        state = new_source_state()
        self.assertTrue(restore_sources(stored, state, lambda dependency: dependency == self.dependency))
        self.assertEqual(state['dependencies'], [self.dependency])

    def test_untracked_malformed_or_changed_history_is_not_replayed(self):
        for version in (None, True, '1', 2, 1.0, Decimal('NaN')):
            with self.subTest(version=version):
                item = dict(self.stored(), sourceProvenanceVersion=version)
                self.assertFalse(restore_sources(item, new_source_state(), lambda _: True))
        self.assertFalse(restore_sources(self.stored(), new_source_state(), lambda _: False))
        self.assertFalse(restore_sources({}, new_source_state(), lambda _: True))

    def test_revision_change_inside_answer_fails_before_reusing_history(self):
        state = new_source_state()
        remember_source(state, self.dependency)
        remember_source(state, self.dependency)
        self.assertEqual(len(state['dependencies']), 1)
        with self.assertRaisesRegex(RuntimeError, 'changed'):
            remember_source(state, dict(self.dependency, sourceRevision='b' * 64))
        with self.assertRaisesRegex(RuntimeError, 'changed'):
            validate_sources(state, lambda _: False)
