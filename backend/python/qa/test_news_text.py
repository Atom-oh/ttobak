"""Publication-aware news reads, continuation and replay; no live AWS calls."""
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from decimal import Decimal
from unittest import mock

from test_source_contract import _SourceFixture
from attachment_context import AttachmentReader
from indexed_retrieval import discovery_filters, hydrate_candidates
from legacy_text import legacy_page
from news_text import current_news_source, news_identity, news_source_revision, read_news_text
from session_provenance import new_source_state, restore_sources, valid_dependency
from source_access import SourceAccess
from source_tools import execute_source_tool, format_source_results
from tool_context import build_tool_context
from tool_history import ToolHistory
import test_handler as helpers
import history_details


class NewsTextTests(_SourceFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.config = {'PK': 'CRAWLER#우리은행', 'SK': 'CONFIG', 'sourceId': '우리은행',
                       'sourceName': '우리은행', 'aliases': ['Woori Bank'], 'newsQueries': ['AI'],
                       'customUrls': [], 'status': 'idle', 'ownerId': 'owner'}
        self.key = 'news/v1/우리은행/' + 'a' * 16 + '/' + 'b' * 32 + '.md'
        self.uri = 's3://knowledge/' + self.key
        self.doc = {
            'PK': self.config['PK'], 'SK': 'DOC#' + 'a' * 16, 'sourceId': '우리은행',
            'docHash': 'a' * 16, 'attemptToken': 'b' * 32, 'schemaVersion': Decimal(1),
            'qualityStatus': 'PUBLISHED', 'sourceRevision': news_source_revision(self.config),
            's3Key': self.key, 'artifactKey': 'knowledge-artifacts/' + self.key[:-3] + '.json',
            'artifactSha256': 'c' * 64, 'title': 'Verified public news'}
        self.body('CURRENT_APPROVED_NEWS\n' + 'paragraph ' * 900 + 'NEWS_END')
        self.table.put_item(Item=self.config)
        self.table.put_item(Item=self.doc)

    def body(self, text):
        data = text.encode()
        self.doc['markdownSha256'] = hashlib.sha256(data).hexdigest()
        self.headers = {'ETag': '"one"', 'VersionId': 'v1', 'ContentLength': len(data)}
        self.s3.head_object.return_value = self.headers
        self.s3.get_object.side_effect = lambda **kwargs: {
            **self.headers, 'Body': io.BytesIO(data)}

    def access(self):
        return SourceAccess(self.source_reader, AttachmentReader(self.source_reader, helpers.handler._query_all),
                            helpers.handler._list_shared_meetings)

    def hit(self, text='STALE_INDEXED_NEWS'):
        return {'uri': self.uri, 'score': .9, 'metadata': {'qualityStatus': 'PUBLISHED'},
                '_provider': {'content': {'text': text}}}

    def test_published_current_bytes_replace_untrusted_index_text(self):
        found = hydrate_candidates(self.source_reader, 'reader', 'CURRENT_APPROVED_NEWS',
                                   [self.hit()], {}, 5)
        self.assertEqual(len(found), 1)
        self.assertEqual(found[0]['provenance']['resourceKind'], 'publishedNews')
        self.assertEqual(found[0]['dependency']['newsURI'], self.uri)
        rendered = format_source_results(found)
        self.assertIn('CURRENT_APPROVED_NEWS', rendered)
        self.assertNotIn('STALE_INDEXED_NEWS', rendered)
        self.assertIn('get_legacy_text_detail', rendered)
        self.s3.get_object.assert_called_once_with(
            Bucket='knowledge', Key=self.key, IfMatch='"one"', VersionId='v1')

    def test_missing_unpublished_or_mismatched_canonical_rows_never_read_s3(self):
        original = copy.deepcopy(self.doc)
        variants = [dict(original, qualityStatus='FAILED'), dict(original, schemaVersion=True),
                    dict(original, schemaVersion='1'), dict(original, attemptToken='c' * 32),
                    dict(original, s3Key=self.key + '.extra'),
                    dict(original, artifactKey='private/other.json'),
                    dict(original, markdownSha256='invalid'), {}]
        for row in variants:
            self.table.items[(original['PK'], original['SK'])] = row
            with self.subTest(row=row):
                self.assertEqual(hydrate_candidates(
                    self.source_reader, 'reader', 'query', [self.hit()], {}, 5), [])
        self.s3.head_object.assert_not_called()
        self.s3.get_object.assert_not_called()

    def test_search_source_details_match_the_model_excerpt_coverage(self):
        access = self.access()
        for size, partial in ((2400, False), (2401, True), (3001, True)):
            with self.subTest(size=size):
                self.body('x' * size)
                self.table.put_item(Item=self.doc)
                results = hydrate_candidates(
                    self.source_reader, 'reader', 'query', [self.hit()], {}, 5)
                self.assertEqual(len(results), 1)
                state, details = new_source_state(), []
                context = build_tool_context(
                    'reader', '', state, details, source_access=access,
                    history=ToolHistory('reader', {}),
                    create_research=lambda *args: None,
                    check_research_limit=lambda *args: True,
                    check_web_search_limit=lambda *args: True)
                with mock.patch.object(access, 'retrieve_from_kb', return_value=results):
                    rendered = format_source_results(context['retrieve_from_kb']('query'))
                self.assertEqual(details[0]['partial'], partial)
                self.assertIn('"partial": ' + str(partial).lower(), rendered)
                self.assertEqual(state['dependencies'][0]['newsURI'], self.uri)

    def test_deleted_disabled_or_reconfigured_source_never_reads_objects(self):
        key = (self.config['PK'], 'CONFIG')
        for row in ({}, dict(self.config, status='disabled'),
                    dict(self.config, aliases=['changed']), dict(self.config, newsQueries=['changed'])):
            self.table.items[key] = row
            with self.subTest(row=row):
                self.assertIsNone(read_news_text(self.source_reader, 'reader', self.uri))
        self.s3.head_object.assert_not_called()

    def test_malformed_foreign_and_private_artifact_uris_never_read_storage(self):
        values = [self.uri.replace('knowledge/', 'foreign/'), self.uri + '?x=1', self.uri + '#x',
                  self.uri.replace('우리은행', '__reserved'), self.uri.replace('/v1/', '/v2/'),
                  self.uri.replace('우리은행', '%2e%2e'), self.uri.replace('우리은행', '..'),
                  self.uri.replace('.md', '.md/extra'), 's3://knowledge/' + self.doc['artifactKey']]
        for uri in values:
            with self.subTest(uri=uri):
                self.assertIsNone(news_identity(uri, 'knowledge'))
                self.assertIsNone(read_news_text(self.source_reader, 'reader', uri))
        self.assertIsNone(read_news_text(self.source_reader, '', self.uri))
        self.assertEqual(self.table.reads, [])
        self.s3.head_object.assert_not_called()

    def test_publication_changed_during_head_is_rejected_before_bytes(self):
        def head(**kwargs):
            self.table.items[(self.doc['PK'], self.doc['SK'])]['qualityStatus'] = 'FAILED'
            return self.headers
        self.s3.head_object.side_effect = head
        self.assertIsNone(read_news_text(self.source_reader, 'reader', self.uri))
        self.s3.get_object.assert_not_called()

    def test_publication_changed_during_content_read_never_returns_text(self):
        original = self.s3.get_object.side_effect
        def changed(**kwargs):
            response = original(**kwargs)
            self.table.items[(self.config['PK'], 'CONFIG')]['newsQueries'] = ['changed']
            return response
        self.s3.get_object.side_effect = changed
        with self.assertRaisesRegex(ValueError, 'changed while reading'):
            read_news_text(self.source_reader, 'reader', self.uri)

    def test_hash_mismatch_and_oversized_objects_fail_closed(self):
        self.table.items[(self.doc['PK'], self.doc['SK'])]['markdownSha256'] = 'd' * 64
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            read_news_text(self.source_reader, 'reader', self.uri)
        self.s3.get_object.reset_mock()
        self.headers['ContentLength'] = 64001
        self.assertIsNone(read_news_text(self.source_reader, 'reader', self.uri))
        self.s3.get_object.assert_not_called()

    def test_continuation_and_replay_bind_publication_and_current_source(self):
        access, state, details = self.access(), new_source_state(), []
        context = {'user_id': 'reader', 'load_legacy_text':
                   lambda uid, uri, offset, revision: access.load_legacy_text(
                       uid, uri, offset, revision, source_state=state, source_details=details)}
        first, _ = execute_source_tool('get_legacy_text_detail', {'uri': self.uri}, context)
        page = json.loads(first.split('\n', 1)[1])
        second, _ = execute_source_tool('get_legacy_text_detail', {
            'uri': self.uri, 'offset': page['nextOffset'], 'sourceRevision': page['sourceRevision']}, context)
        self.assertIn('NEWS_END', second)
        self.assertEqual(details[0]['resourceKind'], 'publishedNews')
        self.assertTrue(valid_dependency(state['dependencies'][0]))
        history = {'sourceProvenanceVersion': 1, 'sourceReplayable': True,
                   'sourceDependencies': state['dependencies']}
        current = lambda dep: access._source_is_current('reader', dep)
        self.assertTrue(restore_sources(history, new_source_state(), current))
        self.table.items[(self.config['PK'], 'CONFIG')]['status'] = 'disabled'
        self.assertFalse(restore_sources(history, new_source_state(), current))
        self.assertIsNone(access.load_legacy_text('reader', self.uri))

    def test_changed_object_revision_rejects_continuation_before_reading_bytes(self):
        first = legacy_page(self.source_reader, 'reader', self.uri)
        self.headers['VersionId'] = 'v2'
        self.s3.get_object.reset_mock()
        with self.assertRaisesRegex(ValueError, 'restart reading'):
            legacy_page(self.source_reader, 'reader', self.uri, first['nextOffset'], first['sourceRevision'])
        self.s3.get_object.assert_not_called()

    def test_saved_and_fallback_history_keep_distinct_news_provenance(self):
        result = hydrate_candidates(self.source_reader, 'reader', 'query', [self.hit()], {}, 5)[0]
        detail, dependency = result['provenance'], result['dependency']
        payload = history_details.pack([detail], [dependency], 'knowledge', 'assets')
        restored = history_details.restore([dependency], payload, 'knowledge', 'assets')
        self.assertEqual(len(restored), 1)
        self.assertEqual(restored[0]['resourceKind'], 'publishedNews')
        self.assertEqual(restored[0]['uri'], self.uri)
        self.assertEqual(restored[0]['title'], self.doc['title'])
        self.assertEqual(restored[0]['provenanceScope'], 'validated_history')
        fallback = history_details.restore([dependency], None, 'knowledge', 'assets')
        self.assertEqual(fallback[0]['resourceKind'], 'publishedNews')
        self.assertEqual(fallback[0]['uri'], self.uri)
        self.assertEqual(fallback[0]['contentSource'], 'validated_history_identity')
        for forged in (dict(detail, uri=self.uri + '.other'),
                       dict(detail, resourceKind='legacyText'),
                       dict(detail, sourceRevision='wrong')):
            self.assertEqual(history_details.pack(
                [forged], [dependency], 'knowledge', 'assets'), '[]')

    def test_news_discovery_filter_is_not_an_access_grant(self):
        filters = discovery_filters('reader', 'knowledge', {}, set(), [])
        self.assertIn('s3://knowledge/news/v1/', json.dumps(filters))
        self.table.items.clear()
        self.assertEqual(hydrate_candidates(
            self.source_reader, 'reader', 'query', [self.hit()], {}, 5), [])
        self.s3.head_object.assert_not_called()

    def test_dynamodb_errors_propagate_without_falling_back_to_index_text(self):
        with mock.patch.object(self.table, 'get_item', side_effect=RuntimeError('synthetic outage')):
            with self.assertRaises(RuntimeError):
                hydrate_candidates(self.source_reader, 'reader', 'query', [self.hit()], {}, 5)
        self.s3.head_object.assert_not_called()

    def test_crawler_and_qa_source_fingerprints_match(self):
        crawler = Path(__file__).resolve().parents[1] / 'crawler'
        configs = [dict(self.config, sourceId='source'), dict(self.config, sourceId='source',
                   aliases=[' Woori ', 'Woori'], newsQueries=[' AI ', 'AI'], status='active')]
        configs += [dict(self.config, sourceId=value) for value in
                    ('우리은행', 'école-١', '가', '가', '𠀀' * 128)]
        script = ('import json,sys;sys.path.insert(0,sys.argv[1]);'
                  'from news_discovery import source_revision;'
                  'print(json.dumps([source_revision(v) for v in json.load(sys.stdin)]))')
        result = subprocess.run([sys.executable, '-I', '-c', script, str(crawler)],
                                input=json.dumps(configs, ensure_ascii=False), text=True,
                                capture_output=True, timeout=5, check=True,
                                env={'LD_LIBRARY_PATH': os.environ.get('LD_LIBRARY_PATH', '')})
        self.assertEqual(json.loads(result.stdout), [news_source_revision(value) for value in configs])
