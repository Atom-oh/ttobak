"""Tech Crawler Lambda — discovers and indexes AWS documentation.

Triggered by Step Functions with a source config containing awsServices.
Uses the AWS public documentation search API, fetches HTML, extracts text,
generates summaries via Bedrock Haiku, and stores results in S3 + DynamoDB.

Dependencies: stdlib + boto3 only.
"""

import hashlib
import json
import logging
import os
import time
from html.parser import HTMLParser
from urllib.error import URLError
from urllib.parse import quote_plus
from urllib.request import Request, urlopen

import boto3

logger = logging.getLogger()
logger.setLevel(logging.INFO)

TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
KB_BUCKET_NAME = os.environ.get('KB_BUCKET_NAME', 'ttobak-kb')
HAIKU_MODEL_ID = os.environ.get('HAIKU_MODEL_ID', 'anthropic.claude-haiku-3-v1:0')

dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)
s3 = boto3.client('s3')
bedrock = boto3.client('bedrock-runtime')

# AWS docs search endpoint
AWS_DOCS_SEARCH_URL = 'https://proxy.search.docs.aws.com/search'

# Limits
MAX_DOCS_PER_SERVICE = 20
FETCH_TIMEOUT_SECONDS = 10
MAX_CONTENT_LENGTH = 50000  # chars, to avoid processing huge pages


def _make_hash(url: str) -> str:
    """Generate a 16-char hex hash for dedup: sha256('tech:{url}')."""
    return hashlib.sha256(f'tech:{url}'.encode('utf-8')).hexdigest()[:16]


# ---------------------------------------------------------------------------
# HTML text extraction (stdlib only)
# ---------------------------------------------------------------------------

class _TextExtractor(HTMLParser):
    """Simple HTML parser that extracts visible text from <p>, <li>, <h1-h6>, <td>, <th> tags."""

    _CONTENT_TAGS = {'p', 'li', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'td', 'th'}
    _SKIP_TAGS = {'script', 'style', 'nav', 'footer', 'header', 'noscript'}

    def __init__(self):
        super().__init__()
        self._pieces = []
        self._in_content = False
        self._skip_depth = 0
        self._current = []

    def handle_starttag(self, tag, attrs):
        tag_lower = tag.lower()
        if tag_lower in self._SKIP_TAGS:
            self._skip_depth += 1
        elif tag_lower in self._CONTENT_TAGS and self._skip_depth == 0:
            self._in_content = True
            self._current = []

    def handle_endtag(self, tag):
        tag_lower = tag.lower()
        if tag_lower in self._SKIP_TAGS:
            self._skip_depth = max(0, self._skip_depth - 1)
        elif tag_lower in self._CONTENT_TAGS and self._in_content:
            text = ''.join(self._current).strip()
            if text:
                self._pieces.append(text)
            self._in_content = False
            self._current = []

    def handle_data(self, data):
        if self._in_content and self._skip_depth == 0:
            self._current.append(data)

    def get_text(self) -> str:
        return '\n'.join(self._pieces)


def extract_text_from_html(html: str) -> str:
    """Extract visible text content from HTML using stdlib parser."""
    parser = _TextExtractor()
    try:
        parser.feed(html)
    except Exception:
        pass
    return parser.get_text()


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------

def _fetch_url(url: str, timeout: int = FETCH_TIMEOUT_SECONDS) -> str:
    """Fetch a URL and return the response body as text."""
    req = Request(url, headers={'User-Agent': 'TtobakCrawler/1.0'})
    with urlopen(req, timeout=timeout) as resp:
        charset = resp.headers.get_content_charset() or 'utf-8'
        return resp.read().decode(charset, errors='replace')


def _search_aws_docs(service: str) -> list:
    """Search AWS docs for a given service via POST JSON API (same as QA Lambda)."""
    try:
        payload = json.dumps({
            'textQuery': {'input': f'AWS {service} best practices getting started'},
            'contextAttributes': [],
            'locales': ['en_us', 'ko_kr'],
        }).encode('utf-8')
        req = Request(
            AWS_DOCS_SEARCH_URL,
            data=payload,
            headers={'Content-Type': 'application/json', 'User-Agent': 'TtobakCrawler/1.0'},
            method='POST',
        )
        with urlopen(req, timeout=FETCH_TIMEOUT_SECONDS) as resp:
            data = json.loads(resp.read().decode('utf-8'))
        results = []
        for suggestion in data.get('suggestions', []):
            excerpt = suggestion.get('textExcerptSuggestion', {})
            url = excerpt.get('link', '')
            title = excerpt.get('title', '')
            if url and title:
                results.append({'title': title, 'url': url})
                if len(results) >= MAX_DOCS_PER_SERVICE:
                    break
        return results
    except Exception as e:
        logger.warning(f'AWS docs search failed for {service}: {e}')
        return []


# ---------------------------------------------------------------------------
# Bedrock summarization
# ---------------------------------------------------------------------------

def _summarize(title: str, text: str) -> str:
    """Generate a concise summary of a document using Bedrock Haiku."""
    # Truncate to keep prompt manageable
    truncated = text[:8000] if len(text) > 8000 else text
    prompt = (
        f'다음 AWS 문서를 한국어로 간결하게 요약해주세요. '
        f'핵심 개념, 사용법, 주의사항을 포함하세요.\n\n'
        f'제목: {title}\n\n'
        f'내용:\n{truncated}'
    )
    try:
        resp = bedrock.invoke_model(
            modelId=HAIKU_MODEL_ID,
            contentType='application/json',
            accept='application/json',
            body=json.dumps({
                'anthropic_version': 'bedrock-2023-05-31',
                'max_tokens': 1024,
                'temperature': 0.2,
                'messages': [{'role': 'user', 'content': prompt}],
            }),
        )
        result = json.loads(resp['body'].read())
        return result.get('content', [{}])[0].get('text', '')
    except Exception as e:
        logger.warning(f'Bedrock summarize failed for "{title}": {e}')
        return ''


# ---------------------------------------------------------------------------
# DynamoDB dedup check
# ---------------------------------------------------------------------------

def _doc_exists(source_id: str, doc_hash: str) -> bool:
    """Check if DOC#{hash} already exists in DynamoDB for this source."""
    try:
        resp = table.get_item(
            Key={'PK': f'CRAWLER#{source_id}', 'SK': f'DOC#{doc_hash}'},
            ProjectionExpression='PK',
        )
        return 'Item' in resp
    except Exception as e:
        logger.warning(f'DynamoDB dedup check failed for {doc_hash}: {e}')
        return False


# ---------------------------------------------------------------------------
# Storage
# ---------------------------------------------------------------------------

def _write_to_s3(service: str, doc_hash: str, title: str, url: str,
                 content: str, summary: str) -> None:
    """Write document markdown to S3."""
    md = (
        f'# {title}\n\n'
        f'**Source:** {url}\n\n'
        f'## Summary\n\n{summary}\n\n'
        f'## Content\n\n{content[:MAX_CONTENT_LENGTH]}\n'
    )
    key = f'shared/aws-docs/{service}/{doc_hash}.md'
    s3.put_object(
        Bucket=KB_BUCKET_NAME,
        Key=key,
        Body=md.encode('utf-8'),
        ContentType='text/markdown; charset=utf-8',
    )
    logger.info(f'Wrote s3://{KB_BUCKET_NAME}/{key}')


def _write_metadata(source_id: str, doc_hash: str, title: str, url: str,
                    service: str) -> None:
    """Write document metadata to DynamoDB."""
    table.put_item(Item={
        'PK': f'CRAWLER#{source_id}',
        'SK': f'DOC#{doc_hash}',
        'url': url,
        'title': title,
        'service': service,
        'crawledAt': int(time.time()),
        'type': 'tech',
    })


# ---------------------------------------------------------------------------
# Lambda handler
# ---------------------------------------------------------------------------

def handler(event, context):
    """Process AWS documentation for the given services.

    Expected event:
      {
        "sourceId": "aws-docs",
        "awsServices": ["lambda", "s3", "bedrock"]
      }
    """
    source_id = event.get('sourceId', 'unknown')
    services = event.get('awsServices', [])
    logger.info(f'Tech crawler: sourceId={source_id}, services={services}')

    docs_added = 0
    docs_updated = 0
    errors = []

    for service in services:
        search_results = _search_aws_docs(service)
        logger.info(f'Service {service}: found {len(search_results)} doc(s)')

        for doc in search_results:
            url = doc['url']
            title = doc['title']
            doc_hash = _make_hash(url)

            try:
                # Dedup check
                if _doc_exists(source_id, doc_hash):
                    logger.debug(f'Skipping duplicate: {url}')
                    continue

                # Fetch and extract
                html = _fetch_url(url)
                text = extract_text_from_html(html)
                if not text or len(text) < 100:
                    logger.info(f'Skipping low-content page: {url}')
                    continue

                # Summarize
                summary = _summarize(title, text)

                # Store
                _write_to_s3(service, doc_hash, title, url, text, summary)
                _write_metadata(source_id, doc_hash, title, url, service)
                docs_added += 1

            except Exception as e:
                error_msg = f'{service}/{url}: {e}'
                logger.error(f'Doc processing error: {error_msg}', exc_info=True)
                errors.append(error_msg)

    result = {
        'docsAdded': docs_added,
        'docsUpdated': docs_updated,
        'errors': errors,
    }
    logger.info(f'Tech crawler complete: {json.dumps(result)}')
    return result
