"""Unit tests for Ttobak Python Crawler Lambdas.

Uses stdlib unittest + unittest.mock only -- no external test frameworks.
"""

import io
import json
import os
import unittest
from unittest import mock

# Set env vars BEFORE importing modules (they read env at import time)
os.environ['TABLE_NAME'] = 'test-table'
os.environ['KB_BUCKET_NAME'] = 'test-bucket'
os.environ['KB_ID'] = 'test-kb'
os.environ['DATA_SOURCE_ID'] = 'test-ds'
os.environ['HAIKU_MODEL_ID'] = 'test-model'


# ---------------------------------------------------------------------------
# Patch boto3 at module level so imports don't hit real AWS
# ---------------------------------------------------------------------------
_mock_dynamodb_resource = mock.MagicMock()
_mock_s3_client = mock.MagicMock()
_mock_bedrock_client = mock.MagicMock()
_mock_bedrock_agent_client = mock.MagicMock()

_boto3_patcher = mock.patch('boto3.resource', return_value=_mock_dynamodb_resource)
_boto3_client_patcher = mock.patch('boto3.client', side_effect=lambda svc, **kw: {
    's3': _mock_s3_client,
    'bedrock-runtime': _mock_bedrock_client,
    'bedrock-agent': _mock_bedrock_agent_client,
}.get(svc, mock.MagicMock()))

_boto3_patcher.start()
_boto3_client_patcher.start()

# Now safe to import the modules
import orchestrator
import tech_crawler
import news_crawler
import ingest_trigger


# ---------------------------------------------------------------------------
# 1. orchestrator.handler
# ---------------------------------------------------------------------------

class TestOrchestrator(unittest.TestCase):
    """Test orchestrator.handler scans DynamoDB and returns sources."""

    def test_handler_returns_sources(self):
        """Mock DynamoDB scan, verify returns sources list."""
        mock_table = mock.MagicMock()
        mock_table.scan.return_value = {
            'Items': [
                {
                    'PK': 'CRAWLER#aws-docs',
                    'SK': 'CONFIG',
                    'status': 'active',
                    'type': 'tech',
                    'awsServices': ['lambda', 's3'],
                    'newsQueries': [],
                    'customUrls': [],
                },
                {
                    'PK': 'CRAWLER#tech-news',
                    'SK': 'CONFIG',
                    'status': 'active',
                    'type': 'news',
                    'awsServices': [],
                    'newsQueries': ['AWS cloud'],
                    'customUrls': [],
                },
            ],
            # No LastEvaluatedKey -- single page
        }

        with mock.patch.object(orchestrator, 'table', mock_table):
            result = orchestrator.handler({}, None)

        self.assertIn('sources', result)
        self.assertEqual(len(result['sources']), 2)
        self.assertEqual(result['sources'][0]['sourceId'], 'aws-docs')
        self.assertEqual(result['sources'][0]['type'], 'tech')
        self.assertEqual(result['sources'][1]['sourceId'], 'tech-news')
        self.assertEqual(result['sources'][1]['newsQueries'], ['AWS cloud'])

    def test_handler_paginates(self):
        """Verify orchestrator handles DynamoDB pagination."""
        mock_table = mock.MagicMock()
        mock_table.scan.side_effect = [
            {
                'Items': [{'PK': 'CRAWLER#src1', 'SK': 'CONFIG', 'status': 'active', 'type': 'tech'}],
                'LastEvaluatedKey': {'PK': 'CRAWLER#src1', 'SK': 'CONFIG'},
            },
            {
                'Items': [{'PK': 'CRAWLER#src2', 'SK': 'CONFIG', 'status': 'active', 'type': 'news'}],
            },
        ]

        with mock.patch.object(orchestrator, 'table', mock_table):
            result = orchestrator.handler({}, None)

        self.assertEqual(len(result['sources']), 2)
        self.assertEqual(mock_table.scan.call_count, 2)

    def test_handler_scan_error(self):
        """Verify orchestrator returns error on scan failure."""
        mock_table = mock.MagicMock()
        mock_table.scan.side_effect = Exception('DynamoDB boom')

        with mock.patch.object(orchestrator, 'table', mock_table):
            result = orchestrator.handler({}, None)

        self.assertEqual(result['sources'], [])
        self.assertIn('error', result)


# ---------------------------------------------------------------------------
# 2. tech_crawler.discover_docs (via _search_aws_docs)
# ---------------------------------------------------------------------------

class TestTechCrawlerDiscover(unittest.TestCase):
    """Test tech_crawler._search_aws_docs URL parsing."""

    @mock.patch.object(tech_crawler, '_fetch_url')
    def test_search_aws_docs_parses_results(self, mock_fetch):
        """Mock urlopen, verify URL parsing from AWS docs search API."""
        mock_fetch.return_value = json.dumps({
            'items': [
                {
                    'title': {'value': 'AWS Lambda Developer Guide'},
                    'url': 'https://docs.aws.amazon.com/lambda/latest/dg/welcome.html',
                },
                {
                    'title': {'value': 'Lambda Functions'},
                    'url': '/lambda/latest/dg/lambda-functions.html',
                },
                {
                    'title': 'Plain string title',
                    'url': 'https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted.html',
                },
            ],
        })

        results = tech_crawler._search_aws_docs('lambda')

        self.assertEqual(len(results), 3)
        self.assertEqual(results[0]['title'], 'AWS Lambda Developer Guide')
        self.assertIn('https://docs.aws.amazon.com', results[0]['url'])
        # Relative URL should be converted to absolute
        self.assertTrue(results[1]['url'].startswith('https://docs.aws.amazon.com'))
        # Plain string title should work too
        self.assertEqual(results[2]['title'], 'Plain string title')

    @mock.patch.object(tech_crawler, '_fetch_url')
    def test_search_aws_docs_empty_on_error(self, mock_fetch):
        """Verify empty list on search failure."""
        mock_fetch.side_effect = Exception('Network error')

        results = tech_crawler._search_aws_docs('lambda')
        self.assertEqual(results, [])


# ---------------------------------------------------------------------------
# 3. tech_crawler.process_doc -- dedup skip
# ---------------------------------------------------------------------------

class TestTechCrawlerDedupSkip(unittest.TestCase):
    """Test tech_crawler handler skips duplicate documents."""

    @mock.patch.object(tech_crawler, '_write_metadata')
    @mock.patch.object(tech_crawler, '_write_to_s3')
    @mock.patch.object(tech_crawler, '_fetch_url')
    @mock.patch.object(tech_crawler, '_doc_exists', return_value=True)
    @mock.patch.object(tech_crawler, '_search_aws_docs')
    def test_dedup_skip(self, mock_search, mock_exists, mock_fetch, mock_s3, mock_meta):
        """Mock DynamoDB get_item returns existing, verify no S3 write."""
        mock_search.return_value = [
            {'title': 'Existing Doc', 'url': 'https://docs.aws.amazon.com/existing'},
        ]

        result = tech_crawler.handler(
            {'sourceId': 'aws-docs', 'awsServices': ['lambda']}, None
        )

        mock_exists.assert_called_once()
        mock_fetch.assert_not_called()  # Should not fetch since dedup found existing
        mock_s3.assert_not_called()     # Should not write to S3
        mock_meta.assert_not_called()   # Should not write metadata
        self.assertEqual(result['docsAdded'], 0)


# ---------------------------------------------------------------------------
# 4. tech_crawler.process_doc -- new doc
# ---------------------------------------------------------------------------

class TestTechCrawlerNewDoc(unittest.TestCase):
    """Test tech_crawler handler processes new documents end-to-end."""

    @mock.patch.object(tech_crawler, '_write_metadata')
    @mock.patch.object(tech_crawler, '_write_to_s3')
    @mock.patch.object(tech_crawler, '_summarize', return_value='Test summary')
    @mock.patch.object(tech_crawler, '_fetch_url')
    @mock.patch.object(tech_crawler, '_doc_exists', return_value=False)
    @mock.patch.object(tech_crawler, '_search_aws_docs')
    def test_new_doc_writes_s3_and_dynamo(self, mock_search, mock_exists,
                                          mock_fetch, mock_summarize,
                                          mock_s3, mock_meta):
        """Mock get_item returns None, mock urlopen, verify S3 put and DDB put called."""
        mock_search.return_value = [
            {'title': 'New Lambda Guide', 'url': 'https://docs.aws.amazon.com/new-lambda'},
        ]
        # Return HTML with enough content (>100 chars)
        mock_fetch.return_value = (
            '<html><body>'
            '<p>' + 'A' * 200 + '</p>'
            '</body></html>'
        )

        result = tech_crawler.handler(
            {'sourceId': 'aws-docs', 'awsServices': ['lambda']}, None
        )

        mock_fetch.assert_called_once()
        mock_summarize.assert_called_once()
        mock_s3.assert_called_once()
        mock_meta.assert_called_once()
        self.assertEqual(result['docsAdded'], 1)

    @mock.patch.object(tech_crawler, '_write_metadata')
    @mock.patch.object(tech_crawler, '_write_to_s3')
    @mock.patch.object(tech_crawler, '_summarize')
    @mock.patch.object(tech_crawler, '_fetch_url')
    @mock.patch.object(tech_crawler, '_doc_exists', return_value=False)
    @mock.patch.object(tech_crawler, '_search_aws_docs')
    def test_low_content_skipped(self, mock_search, mock_exists,
                                  mock_fetch, mock_summarize,
                                  mock_s3, mock_meta):
        """Pages with too little text (<100 chars) should be skipped."""
        mock_search.return_value = [
            {'title': 'Empty Page', 'url': 'https://docs.aws.amazon.com/empty'},
        ]
        mock_fetch.return_value = '<html><body><p>Short</p></body></html>'

        result = tech_crawler.handler(
            {'sourceId': 'aws-docs', 'awsServices': ['lambda']}, None
        )

        mock_summarize.assert_not_called()
        mock_s3.assert_not_called()
        self.assertEqual(result['docsAdded'], 0)


# ---------------------------------------------------------------------------
# 5. news_crawler.fetch_rss (via _parse_rss)
# ---------------------------------------------------------------------------

class TestNewsCrawlerFetchRss(unittest.TestCase):
    """Test news_crawler._parse_rss with sample RSS XML."""

    def test_parse_rss_extracts_articles(self):
        """Provide sample RSS XML, verify article parsing."""
        sample_rss = '''<?xml version="1.0" encoding="UTF-8"?>
        <rss version="2.0">
          <channel>
            <title>Google News</title>
            <item>
              <title>AWS launches new service</title>
              <link>https://example.com/article1</link>
              <pubDate>Mon, 14 Apr 2026 10:00:00 GMT</pubDate>
            </item>
            <item>
              <title>Cloud computing trends 2026</title>
              <link>https://example.com/article2</link>
              <pubDate>Sun, 13 Apr 2026 08:00:00 GMT</pubDate>
            </item>
            <item>
              <title>No link article</title>
            </item>
          </channel>
        </rss>'''

        articles = news_crawler._parse_rss(sample_rss)

        # Should only get 2 articles (3rd has no link)
        self.assertEqual(len(articles), 2)
        self.assertEqual(articles[0]['title'], 'AWS launches new service')
        self.assertEqual(articles[0]['url'], 'https://example.com/article1')
        self.assertEqual(articles[0]['pubDate'], 'Mon, 14 Apr 2026 10:00:00 GMT')
        self.assertEqual(articles[1]['title'], 'Cloud computing trends 2026')

    def test_parse_rss_empty_on_invalid_xml(self):
        """Invalid XML should return empty list, not crash."""
        articles = news_crawler._parse_rss('not valid xml <<>>')
        self.assertEqual(articles, [])

    def test_parse_rss_respects_max_limit(self):
        """Verify MAX_ARTICLES_PER_QUERY limits results."""
        items = ''.join(
            f'<item><title>Article {i}</title><link>https://example.com/{i}</link></item>'
            for i in range(30)
        )
        rss = f'<?xml version="1.0"?><rss><channel>{items}</channel></rss>'

        articles = news_crawler._parse_rss(rss)
        self.assertLessEqual(len(articles), news_crawler.MAX_ARTICLES_PER_QUERY)


# ---------------------------------------------------------------------------
# 6. news_crawler.process_article -- dedup
# ---------------------------------------------------------------------------

class TestNewsCrawlerDedupSkip(unittest.TestCase):
    """Test news_crawler._process_article skips existing articles."""

    @mock.patch.object(news_crawler, '_write_metadata')
    @mock.patch.object(news_crawler, '_write_to_s3')
    @mock.patch.object(news_crawler, '_fetch_url')
    @mock.patch.object(news_crawler, '_doc_exists', return_value=True)
    def test_dedup_skip(self, mock_exists, mock_fetch, mock_s3, mock_meta):
        """Mock existing doc, verify skip."""
        result = news_crawler._process_article(
            'tech-news', 'Old Article', 'https://example.com/old', '2026-04-14'
        )

        self.assertFalse(result)
        mock_exists.assert_called_once()
        mock_fetch.assert_not_called()
        mock_s3.assert_not_called()
        mock_meta.assert_not_called()


# ---------------------------------------------------------------------------
# 7. news_crawler.process_article -- new article
# ---------------------------------------------------------------------------

class TestNewsCrawlerNewArticle(unittest.TestCase):
    """Test news_crawler._process_article writes S3 + DynamoDB for new articles."""

    @mock.patch.object(news_crawler, '_write_metadata')
    @mock.patch.object(news_crawler, '_write_to_s3')
    @mock.patch.object(news_crawler, '_summarize', return_value='Article summary')
    @mock.patch.object(news_crawler, '_fetch_url')
    @mock.patch.object(news_crawler, '_doc_exists', return_value=False)
    def test_new_article_writes_s3_and_dynamo(self, mock_exists, mock_fetch,
                                               mock_summarize, mock_s3, mock_meta):
        """Verify S3 + DynamoDB writes for a new article with sufficient content."""
        # Return HTML with enough paragraph text (>50 chars)
        mock_fetch.return_value = (
            '<html><body>'
            '<p>' + 'This is a long enough paragraph for testing. ' * 5 + '</p>'
            '</body></html>'
        )

        result = news_crawler._process_article(
            'tech-news', 'New AWS Article',
            'https://example.com/new-article', 'Mon, 14 Apr 2026 10:00:00 GMT'
        )

        self.assertTrue(result)
        mock_fetch.assert_called_once_with('https://example.com/new-article')
        mock_summarize.assert_called_once()
        mock_s3.assert_called_once()
        mock_meta.assert_called_once()

        # Verify S3 write args include source_id and title
        s3_call_args = mock_s3.call_args
        self.assertEqual(s3_call_args[0][0], 'tech-news')  # source_id
        self.assertEqual(s3_call_args[0][2], 'New AWS Article')  # title

    @mock.patch.object(news_crawler, '_write_metadata')
    @mock.patch.object(news_crawler, '_write_to_s3')
    @mock.patch.object(news_crawler, '_summarize')
    @mock.patch.object(news_crawler, '_fetch_url')
    @mock.patch.object(news_crawler, '_doc_exists', return_value=False)
    def test_low_content_article_skipped(self, mock_exists, mock_fetch,
                                          mock_summarize, mock_s3, mock_meta):
        """Articles with too little text (<50 chars) should be skipped."""
        mock_fetch.return_value = '<html><body><p>Short.</p></body></html>'

        result = news_crawler._process_article(
            'tech-news', 'Thin Article', 'https://example.com/thin', ''
        )

        self.assertFalse(result)
        mock_summarize.assert_not_called()
        mock_s3.assert_not_called()


# ---------------------------------------------------------------------------
# 8. ingest_trigger.handler -- success
# ---------------------------------------------------------------------------

class TestIngestTriggerSuccess(unittest.TestCase):
    """Test ingest_trigger.handler successfully starts ingestion."""

    @mock.patch.object(ingest_trigger, 'bedrock_agent')
    def test_handler_starts_ingestion(self, mock_agent):
        """Mock start_ingestion_job, verify STARTED response."""
        mock_agent.start_ingestion_job.return_value = {
            'ingestionJob': {
                'ingestionJobId': 'job-123',
                'status': 'STARTING',
            },
        }

        event = {
            'crawlerResults': [
                {'docsAdded': 3, 'docsUpdated': 0, 'errors': []},
                {'docsAdded': 2, 'docsUpdated': 0, 'errors': []},
            ],
        }

        result = ingest_trigger.handler(event, None)

        self.assertEqual(result['status'], 'STARTED')
        self.assertEqual(result['ingestionJobId'], 'job-123')
        self.assertEqual(result['totalDocsAdded'], 5)
        mock_agent.start_ingestion_job.assert_called_once_with(
            knowledgeBaseId='test-kb',
            dataSourceId='test-ds',
        )

    @mock.patch.object(ingest_trigger, 'bedrock_agent')
    def test_handler_skips_when_no_new_docs(self, mock_agent):
        """If no docs added/updated, skip ingestion."""
        event = {
            'crawlerResults': [
                {'docsAdded': 0, 'docsUpdated': 0, 'errors': []},
            ],
        }

        result = ingest_trigger.handler(event, None)

        self.assertEqual(result['status'], 'SKIPPED')
        self.assertIsNone(result['ingestionJobId'])
        mock_agent.start_ingestion_job.assert_not_called()

    @mock.patch.object(ingest_trigger, 'bedrock_agent')
    def test_handler_error_on_api_failure(self, mock_agent):
        """Verify ERROR status when start_ingestion_job raises."""
        mock_agent.start_ingestion_job.side_effect = Exception('Bedrock error')

        event = {
            'crawlerResults': [
                {'docsAdded': 1, 'docsUpdated': 0, 'errors': []},
            ],
        }

        result = ingest_trigger.handler(event, None)

        self.assertEqual(result['status'], 'ERROR')
        self.assertIn('Bedrock error', result['error'])


# ---------------------------------------------------------------------------
# 9. ingest_trigger.handler -- no KB config
# ---------------------------------------------------------------------------

class TestIngestTriggerNoKBConfig(unittest.TestCase):
    """Test ingest_trigger.handler when KB_ID is empty."""

    @mock.patch.object(ingest_trigger, 'bedrock_agent')
    def test_handler_no_kb_config(self, mock_agent):
        """KB_ID empty, verify skipped with ERROR."""
        original_kb_id = ingest_trigger.KB_ID
        original_ds_id = ingest_trigger.DATA_SOURCE_ID
        try:
            ingest_trigger.KB_ID = ''
            ingest_trigger.DATA_SOURCE_ID = ''

            event = {
                'crawlerResults': [
                    {'docsAdded': 5, 'docsUpdated': 0, 'errors': []},
                ],
            }

            result = ingest_trigger.handler(event, None)

            self.assertEqual(result['status'], 'ERROR')
            self.assertIn('not set', result['error'])
            mock_agent.start_ingestion_job.assert_not_called()
        finally:
            ingest_trigger.KB_ID = original_kb_id
            ingest_trigger.DATA_SOURCE_ID = original_ds_id


# ---------------------------------------------------------------------------
# Extra: HTML text extraction helpers
# ---------------------------------------------------------------------------

class TestTechCrawlerHTMLExtraction(unittest.TestCase):
    """Test tech_crawler.extract_text_from_html."""

    def test_extracts_paragraphs_and_headings(self):
        html = '<h1>Title</h1><p>Body text here.</p><li>List item</li>'
        text = tech_crawler.extract_text_from_html(html)
        self.assertIn('Title', text)
        self.assertIn('Body text here.', text)
        self.assertIn('List item', text)

    def test_skips_script_and_style(self):
        html = '<script>var x=1;</script><style>.a{}</style><p>Visible</p>'
        text = tech_crawler.extract_text_from_html(html)
        self.assertIn('Visible', text)
        self.assertNotIn('var x', text)
        self.assertNotIn('.a{}', text)


class TestNewsCrawlerHTMLExtraction(unittest.TestCase):
    """Test news_crawler.extract_paragraphs."""

    def test_extracts_paragraphs(self):
        html = '<p>This is a long enough paragraph for testing purposes only.</p><p>Short</p>'
        text = news_crawler.extract_paragraphs(html)
        # Only paragraphs > 20 chars are kept
        self.assertIn('long enough paragraph', text)
        self.assertNotIn('Short', text)

    def test_skips_nav_and_aside(self):
        html = '<nav><p>Nav text is long enough to pass the filter check.</p></nav><p>This is the actual main body content text.</p>'
        text = news_crawler.extract_paragraphs(html)
        self.assertNotIn('Nav text', text)
        self.assertIn('actual main body', text)


if __name__ == '__main__':
    unittest.main()
