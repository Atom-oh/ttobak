"""News Crawler Lambda — fetches and indexes Korean tech news articles.

Triggered by Step Functions with a source config containing newsQueries
and/or customUrls. Searches Google News RSS, fetches articles, extracts
text, generates summaries via Bedrock Haiku, and stores in S3 + DynamoDB.

Dependencies: stdlib + boto3 only.
"""

import hashlib
import json
import logging
import os
import time
import xml.etree.ElementTree as ET
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

# Google News RSS base
GOOGLE_NEWS_RSS = 'https://news.google.com/rss/search?q={query}&hl=ko&gl=KR&ceid=KR:ko'

# Limits
MAX_ARTICLES_PER_QUERY = 15
FETCH_TIMEOUT_SECONDS = 10
MAX_CONTENT_LENGTH = 30000  # chars


def _make_hash(url: str) -> str:
    """Generate a 16-char hex hash for dedup: sha256('news:{url}')."""
    return hashlib.sha256(f'news:{url}'.encode('utf-8')).hexdigest()[:16]


# ---------------------------------------------------------------------------
# HTML paragraph extraction (stdlib only)
# ---------------------------------------------------------------------------

class _ParagraphExtractor(HTMLParser):
    """Simple HTML parser that extracts text from <p> tags."""

    _SKIP_TAGS = {'script', 'style', 'nav', 'footer', 'header', 'noscript', 'aside'}

    def __init__(self):
        super().__init__()
        self._paragraphs = []
        self._in_p = False
        self._skip_depth = 0
        self._current = []

    def handle_starttag(self, tag, attrs):
        tag_lower = tag.lower()
        if tag_lower in self._SKIP_TAGS:
            self._skip_depth += 1
        elif tag_lower == 'p' and self._skip_depth == 0:
            self._in_p = True
            self._current = []

    def handle_endtag(self, tag):
        tag_lower = tag.lower()
        if tag_lower in self._SKIP_TAGS:
            self._skip_depth = max(0, self._skip_depth - 1)
        elif tag_lower == 'p' and self._in_p:
            text = ''.join(self._current).strip()
            if text and len(text) > 20:  # skip very short fragments
                self._paragraphs.append(text)
            self._in_p = False
            self._current = []

    def handle_data(self, data):
        if self._in_p and self._skip_depth == 0:
            self._current.append(data)

    def get_text(self) -> str:
        return '\n\n'.join(self._paragraphs)


def extract_paragraphs(html: str) -> str:
    """Extract paragraph text from HTML."""
    parser = _ParagraphExtractor()
    try:
        parser.feed(html)
    except Exception:
        pass
    return parser.get_text()


# ---------------------------------------------------------------------------
# HTTP + RSS helpers
# ---------------------------------------------------------------------------

def _fetch_url(url: str, timeout: int = FETCH_TIMEOUT_SECONDS) -> str:
    """Fetch a URL and return response body as text."""
    req = Request(url, headers={'User-Agent': 'TtobakCrawler/1.0'})
    with urlopen(req, timeout=timeout) as resp:
        charset = resp.headers.get_content_charset() or 'utf-8'
        return resp.read().decode(charset, errors='replace')


def _parse_rss(xml_text: str) -> list:
    """Parse Google News RSS XML, return list of {title, url, pubDate}."""
    articles = []
    try:
        root = ET.fromstring(xml_text)
        # RSS 2.0 structure: <rss><channel><item>...</item></channel></rss>
        channel = root.find('channel')
        if channel is None:
            return articles
        for item in channel.findall('item'):
            title_el = item.find('title')
            link_el = item.find('link')
            pub_el = item.find('pubDate')
            if title_el is not None and link_el is not None:
                articles.append({
                    'title': title_el.text or '',
                    'url': link_el.text or '',
                    'pubDate': pub_el.text if pub_el is not None else '',
                })
    except ET.ParseError as e:
        logger.warning(f'RSS parse error: {e}')
    return articles[:MAX_ARTICLES_PER_QUERY]


def _search_google_news(query: str) -> list:
    """Search Google News RSS for articles matching query."""
    encoded = quote_plus(query)
    rss_url = GOOGLE_NEWS_RSS.format(query=encoded)
    try:
        xml_text = _fetch_url(rss_url)
        return _parse_rss(xml_text)
    except Exception as e:
        logger.warning(f'Google News RSS fetch failed for "{query}": {e}')
        return []


# ---------------------------------------------------------------------------
# Bedrock summarization
# ---------------------------------------------------------------------------

def _summarize(title: str, text: str) -> str:
    """Generate a concise Korean summary using Bedrock Haiku."""
    truncated = text[:6000] if len(text) > 6000 else text
    prompt = (
        f'다음 뉴스 기사를 한국어로 3-5문장으로 요약해주세요. '
        f'핵심 내용과 시사점을 포함하세요.\n\n'
        f'제목: {title}\n\n'
        f'본문:\n{truncated}'
    )
    try:
        resp = bedrock.invoke_model(
            modelId=HAIKU_MODEL_ID,
            contentType='application/json',
            accept='application/json',
            body=json.dumps({
                'anthropic_version': 'bedrock-2023-05-31',
                'max_tokens': 512,
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
# DynamoDB dedup
# ---------------------------------------------------------------------------

def _doc_exists(source_id: str, doc_hash: str) -> bool:
    """Check if DOC#{hash} already exists for this source."""
    try:
        resp = table.get_item(
            Key={'PK': f'CRAWLER#{source_id}', 'SK': f'DOC#{doc_hash}'},
            ProjectionExpression='PK',
        )
        return 'Item' in resp
    except Exception as e:
        logger.warning(f'Dedup check failed for {doc_hash}: {e}')
        return False


# ---------------------------------------------------------------------------
# Storage
# ---------------------------------------------------------------------------

def _write_to_s3(source_id: str, doc_hash: str, title: str, url: str,
                 content: str, summary: str, pub_date: str) -> None:
    """Write article markdown to S3."""
    md = (
        f'# {title}\n\n'
        f'**Source:** {url}\n'
        f'**Published:** {pub_date}\n\n'
        f'## Summary\n\n{summary}\n\n'
        f'## Content\n\n{content[:MAX_CONTENT_LENGTH]}\n'
    )
    key = f'shared/news/{source_id}/{doc_hash}.md'
    s3.put_object(
        Bucket=KB_BUCKET_NAME,
        Key=key,
        Body=md.encode('utf-8'),
        ContentType='text/markdown; charset=utf-8',
    )
    logger.info(f'Wrote s3://{KB_BUCKET_NAME}/{key}')


def _write_metadata(source_id: str, doc_hash: str, title: str, url: str,
                    pub_date: str) -> None:
    """Write article metadata to DynamoDB."""
    table.put_item(Item={
        'PK': f'CRAWLER#{source_id}',
        'SK': f'DOC#{doc_hash}',
        'url': url,
        'title': title,
        'pubDate': pub_date,
        'crawledAt': int(time.time()),
        'type': 'news',
    })


# ---------------------------------------------------------------------------
# Lambda handler
# ---------------------------------------------------------------------------

def _process_article(source_id: str, title: str, url: str,
                     pub_date: str) -> bool:
    """Process a single article. Returns True if added, False if skipped."""
    doc_hash = _make_hash(url)

    if _doc_exists(source_id, doc_hash):
        logger.debug(f'Skipping duplicate: {url}')
        return False

    html = _fetch_url(url)
    text = extract_paragraphs(html)
    if not text or len(text) < 50:
        logger.info(f'Skipping low-content article: {url}')
        return False

    summary = _summarize(title, text)
    _write_to_s3(source_id, doc_hash, title, url, text, summary, pub_date)
    _write_metadata(source_id, doc_hash, title, url, pub_date)
    return True


def handler(event, context):
    """Process news articles from Google News RSS and custom URLs.

    Expected event:
      {
        "sourceId": "tech-news",
        "newsQueries": ["AWS 클라우드", "AI 인공지능"],
        "customUrls": [
          {"url": "https://example.com/article", "title": "Custom Article"}
        ]
      }
    """
    source_id = event.get('sourceId', 'unknown')
    queries = event.get('newsQueries', [])
    custom_urls = event.get('customUrls', [])
    logger.info(f'News crawler: sourceId={source_id}, queries={queries}, '
                f'customUrls={len(custom_urls)}')

    docs_added = 0
    docs_updated = 0
    errors = []

    # Process Google News RSS queries
    for query in queries:
        articles = _search_google_news(query)
        logger.info(f'Query "{query}": found {len(articles)} article(s)')

        for article in articles:
            try:
                if _process_article(source_id, article['title'],
                                    article['url'], article.get('pubDate', '')):
                    docs_added += 1
            except Exception as e:
                error_msg = f'news/{query}/{article.get("url", "?")}: {e}'
                logger.error(f'Article error: {error_msg}', exc_info=True)
                errors.append(error_msg)

    # Process custom URLs
    for entry in custom_urls:
        url = entry.get('url', '')
        title = entry.get('title', url)
        if not url:
            continue
        try:
            if _process_article(source_id, title, url, ''):
                docs_added += 1
        except Exception as e:
            error_msg = f'custom/{url}: {e}'
            logger.error(f'Custom URL error: {error_msg}', exc_info=True)
            errors.append(error_msg)

    result = {
        'docsAdded': docs_added,
        'docsUpdated': docs_updated,
        'errors': errors,
    }
    logger.info(f'News crawler complete: {json.dumps(result)}')
    return result
