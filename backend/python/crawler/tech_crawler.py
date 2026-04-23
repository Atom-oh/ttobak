"""Tech Crawler Lambda — discovers and indexes AWS technical content.

Triggered by Step Functions with a source config containing awsServices.
Fetches from AWS What's New RSS, AWS Blog RSS, and direct documentation
pages. Generates summaries + auto-tags via Bedrock, stores in S3 + DynamoDB.

Dependencies: stdlib + boto3 only.
"""

import hashlib
import json
import logging
import os
import re
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
SUMMARIZE_MODEL_ID = os.environ.get('SUMMARIZE_MODEL_ID', 'global.anthropic.claude-sonnet-4-6')

dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)
s3 = boto3.client('s3')
bedrock = boto3.client('bedrock-runtime')

# AWS RSS sources
AWS_WHATS_NEW_RSS = 'https://aws.amazon.com/about-aws/whats-new/recent/feed/'
AWS_BLOG_RSS = 'https://aws.amazon.com/blogs/{category}/feed/'
AWS_BLOG_KR_RSS = 'https://aws.amazon.com/ko/blogs/{category}/feed/'

# Service → blog category mapping
SERVICE_BLOG_MAP = {
    'eks': ['containers'],
    'ecs': ['containers'],
    'fargate': ['containers'],
    'lambda': ['compute', 'aws-lambda'],
    's3': ['storage'],
    'dynamodb': ['database'],
    'rds': ['database'],
    'aurora': ['database'],
    'bedrock': ['machine-learning'],
    'sagemaker': ['machine-learning'],
    'opensearch': ['big-data'],
    'cloudfront': ['networking-and-content-delivery'],
    'vpc': ['networking-and-content-delivery'],
    'ec2': ['compute'],
    'iam': ['security'],
    'kms': ['security'],
    'cloudwatch': ['mt'],
    'step-functions': ['compute'],
    'api-gateway': ['compute'],
    'cognito': ['security'],
    'eventbridge': ['compute'],
    'kinesis': ['big-data'],
    'glue': ['big-data'],
    'athena': ['big-data'],
    'redshift': ['big-data'],
    'elasticache': ['database'],
}

MAX_ARTICLES_PER_SOURCE = 5
FETCH_TIMEOUT_SECONDS = 15
MAX_CONTENT_LENGTH = 50000


def _make_hash(url: str) -> str:
    return hashlib.sha256(f'tech:{url}'.encode('utf-8')).hexdigest()[:16]


# ---------------------------------------------------------------------------
# HTML text extraction (stdlib only)
# ---------------------------------------------------------------------------

class _TextExtractor(HTMLParser):
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
    if not url.startswith(('http://', 'https://')):
        raise ValueError(f'Unsupported URL scheme: {url[:30]}')
    req = Request(url, headers={
        'User-Agent': 'Mozilla/5.0 (compatible; TtobakCrawler/2.0)',
        'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
        'Accept-Language': 'ko-KR,ko;q=0.9,en;q=0.5',
    })
    with urlopen(req, timeout=timeout) as resp:
        charset = resp.headers.get_content_charset() or 'utf-8'
        return resp.read().decode(charset, errors='replace')


def _strip_html_tags(text: str) -> str:
    return re.sub(r'<[^>]+>', '', text).strip()


MAX_RSS_SIZE = 2_000_000

def _parse_rss(xml_text: str, max_items: int = MAX_ARTICLES_PER_SOURCE) -> list:
    articles = []
    if len(xml_text) > MAX_RSS_SIZE:
        logger.warning(f'RSS response too large ({len(xml_text)} chars), skipping')
        return []
    try:
        root = ET.fromstring(xml_text)

        # Try RSS 2.0 first
        channel = root.find('channel')
        if channel is not None:
            for item in channel.findall('item'):
                title_el = item.find('title')
                link_el = item.find('link')
                pub_el = item.find('pubDate')
                desc_el = item.find('description')
                # Some feeds use content:encoded
                content_el = item.find('{http://purl.org/rss/1.0/modules/content/}encoded')
                if title_el is not None and link_el is not None:
                    desc_text = ''
                    if content_el is not None and content_el.text:
                        desc_text = _strip_html_tags(content_el.text)[:500]
                    elif desc_el is not None and desc_el.text:
                        desc_text = _strip_html_tags(desc_el.text)[:500]
                    articles.append({
                        'title': _strip_html_tags(title_el.text or ''),
                        'url': link_el.text or '',
                        'pubDate': pub_el.text if pub_el is not None else '',
                        'description': desc_text,
                    })
                    if len(articles) >= max_items:
                        break
            return articles

        # Try Atom format
        ns = '{http://www.w3.org/2005/Atom}'
        for entry in root.findall(f'{ns}entry'):
            title_el = entry.find(f'{ns}title')
            link_el = entry.find(f'{ns}link')
            pub_el = entry.find(f'{ns}published') or entry.find(f'{ns}updated')
            summary_el = entry.find(f'{ns}summary') or entry.find(f'{ns}content')
            href = ''
            if link_el is not None:
                href = link_el.get('href', '') or (link_el.text or '')
            if title_el is not None and href:
                articles.append({
                    'title': _strip_html_tags(title_el.text or ''),
                    'url': href,
                    'pubDate': pub_el.text if pub_el is not None else '',
                    'description': _strip_html_tags(summary_el.text or '') if summary_el is not None else '',
                })
                if len(articles) >= max_items:
                    break
    except ET.ParseError as e:
        logger.warning(f'RSS parse error: {e}')
    return articles


# ---------------------------------------------------------------------------
# RSS source fetchers
# ---------------------------------------------------------------------------

def _fetch_whats_new(service: str) -> list:
    """Fetch AWS What's New RSS and filter by service keyword."""
    try:
        xml_text = _fetch_url(AWS_WHATS_NEW_RSS)
        all_articles = _parse_rss(xml_text, max_items=50)
        service_lower = service.lower().replace('-', ' ')
        aliases = _get_service_aliases(service)
        filtered = []
        for a in all_articles:
            text = (a.get('title', '') + ' ' + a.get('description', '')).lower()
            if any(alias in text for alias in aliases):
                filtered.append(a)
                if len(filtered) >= MAX_ARTICLES_PER_SOURCE:
                    break
        return filtered
    except Exception as e:
        logger.warning(f'What\'s New RSS failed: {e}')
        return []


def _fetch_blog_rss(service: str) -> list:
    """Fetch AWS Blog RSS for relevant categories."""
    categories = SERVICE_BLOG_MAP.get(service.lower(), [])
    if not categories:
        categories = ['aws']

    articles = []
    for cat in categories[:2]:
        for rss_template in [AWS_BLOG_RSS, AWS_BLOG_KR_RSS]:
            try:
                rss_url = rss_template.format(category=cat)
                xml_text = _fetch_url(rss_url)
                parsed = _parse_rss(xml_text, max_items=20)
                # Filter by service keyword
                aliases = _get_service_aliases(service)
                for a in parsed:
                    text = (a.get('title', '') + ' ' + a.get('description', '')).lower()
                    if any(alias in text for alias in aliases):
                        articles.append(a)
                if len(articles) >= MAX_ARTICLES_PER_SOURCE:
                    break
            except Exception as e:
                logger.warning(f'Blog RSS failed for {cat}: {e}')

        if len(articles) >= MAX_ARTICLES_PER_SOURCE:
            break

    return articles[:MAX_ARTICLES_PER_SOURCE]


def _get_service_aliases(service: str) -> list:
    """Get search aliases for an AWS service."""
    s = service.lower()
    aliases_map = {
        'eks': ['eks', 'elastic kubernetes', 'kubernetes'],
        'ecs': ['ecs', 'elastic container service'],
        'lambda': ['lambda', 'serverless'],
        's3': ['s3', 'simple storage'],
        'dynamodb': ['dynamodb', 'dynamo db'],
        'rds': ['rds', 'relational database'],
        'aurora': ['aurora'],
        'bedrock': ['bedrock', 'generative ai', 'foundation model'],
        'sagemaker': ['sagemaker', 'sage maker'],
        'opensearch': ['opensearch', 'open search'],
        'cloudfront': ['cloudfront', 'cloud front', 'cdn'],
        'ec2': ['ec2', 'elastic compute'],
        'iam': ['iam', 'identity'],
        'cognito': ['cognito'],
        'cloudwatch': ['cloudwatch', 'cloud watch', 'monitoring'],
        'step-functions': ['step functions', 'stepfunctions'],
        'api-gateway': ['api gateway', 'apigateway'],
        'eventbridge': ['eventbridge', 'event bridge'],
        'kinesis': ['kinesis'],
        'glue': ['glue', 'etl'],
        'athena': ['athena'],
        'redshift': ['redshift'],
        'elasticache': ['elasticache', 'redis', 'memcached'],
    }
    return aliases_map.get(s, [s, s.replace('-', ' ')])


# ---------------------------------------------------------------------------
# Bedrock summarization + auto-tagging
# ---------------------------------------------------------------------------

def _summarize_and_tag(title: str, text: str, service: str) -> tuple:
    """Generate summary + tags. Returns (summary, tags_list)."""
    truncated = text[:6000] if len(text) > 6000 else text
    prompt = (
        f'다음 AWS 기술 문서/블로그를 한국어로 분석하여 JSON으로 응답하세요:\n\n'
        f'{{"summary": "핵심 개념, 사용법, 주의사항을 포함한 간결한 요약", '
        f'"tags": ["태그1", "태그2", ...]}}\n\n'
        f'태그 규칙:\n'
        f'- AWS 서비스명 (예: Lambda, S3, Bedrock, EKS)\n'
        f'- 기술 카테고리 (예: 서버리스, 컨테이너, AI/ML, 데이터베이스, 보안, 네트워킹)\n'
        f'- 주제 키워드 (예: 성능최적화, 비용절감, 마이그레이션, 모니터링, 신규기능)\n'
        f'- 3-6개 태그 추출\n\n'
        f'관련 서비스: {service}\n'
        f'제목: {title}\n\n'
        f'내용:\n{truncated}'
    )
    try:
        resp = bedrock.converse(
            modelId=SUMMARIZE_MODEL_ID,
            messages=[{'role': 'user', 'content': [{'text': prompt}]}],
            inferenceConfig={'maxTokens': 1500, 'temperature': 0.2},
        )
        response_text = resp['output']['message']['content'][0]['text']

        start_idx = response_text.find('{')
        if start_idx >= 0:
            parsed, _ = json.JSONDecoder().raw_decode(response_text, start_idx)
            summary = parsed.get('summary', '')
            tags = parsed.get('tags', [])
            if isinstance(tags, list):
                tags = [str(t).strip() for t in tags if t][:10]
            else:
                tags = []
            if service and service not in [t.lower() for t in tags]:
                tags.insert(0, service.upper() if len(service) <= 3 else service.capitalize())
            return summary, tags

        return response_text, [service]
    except Exception as e:
        logger.warning(f'Bedrock summarize+tag failed for "{title}": {e}')
        return '', [service]


# ---------------------------------------------------------------------------
# DynamoDB dedup check
# ---------------------------------------------------------------------------

def _doc_exists(source_id: str, doc_hash: str) -> bool:
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
                 content: str, summary: str, tags: list) -> None:
    tag_line = f'**Tags:** {", ".join(tags)}\n' if tags else ''
    md = (
        f'# {title}\n\n'
        f'**Source:** {url}\n'
        f'**Service:** {service}\n'
        f'{tag_line}\n'
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
                    service: str, summary: str = '', tags: list = None,
                    pub_date: str = '') -> None:
    item = {
        'PK': f'CRAWLER#{source_id}',
        'SK': f'DOC#{doc_hash}',
        'url': url,
        'title': title,
        'service': service,
        'crawledAt': int(time.time()),
        'type': 'tech',
        's3Key': f'shared/aws-docs/{service}/{doc_hash}.md',
        'inKB': True,
    }
    if summary:
        item['summary'] = summary
    if tags:
        item['tags'] = tags
    if pub_date:
        item['pubDate'] = pub_date
    table.put_item(Item=item)


# ---------------------------------------------------------------------------
# Lambda handler
# ---------------------------------------------------------------------------

def handler(event, context):
    """Process AWS technical content for the given services.

    Expected event:
      {
        "sourceId": "wooribank",
        "awsServices": ["lambda", "s3", "bedrock", "eks"]
      }
    """
    source_id = event.get('sourceId', 'unknown')
    services = event.get('awsServices', [])
    logger.info(f'Tech crawler: sourceId={source_id}, services={services}')

    docs_added = 0
    docs_updated = 0
    errors = []
    seen_urls = set()

    for service in services:
        # Collect articles from multiple RSS sources
        all_articles = []

        # 1. AWS What's New
        whats_new = _fetch_whats_new(service)
        logger.info(f'{service} What\'s New: {len(whats_new)} article(s)')
        all_articles.extend(whats_new)

        # 2. AWS Blog RSS
        blog_articles = _fetch_blog_rss(service)
        logger.info(f'{service} Blog RSS: {len(blog_articles)} article(s)')
        all_articles.extend(blog_articles)

        # Deduplicate by URL within this run
        unique_articles = []
        for a in all_articles:
            url = a.get('url', '')
            if url and url not in seen_urls:
                seen_urls.add(url)
                unique_articles.append(a)

        logger.info(f'{service}: {len(unique_articles)} unique article(s) to process')

        for article in unique_articles[:MAX_ARTICLES_PER_SOURCE * 2]:
            url = article['url']
            title = article['title']
            doc_hash = _make_hash(url)

            try:
                if _doc_exists(source_id, doc_hash):
                    logger.debug(f'Skipping duplicate: {url}')
                    continue

                text = article.get('description', '')
                try:
                    html = _fetch_url(url)
                    extracted = extract_text_from_html(html)
                    if extracted and len(extracted) > len(text):
                        text = extracted
                except Exception as e:
                    logger.info(f'Could not fetch full page for {url}: {e}')

                if not text or len(text) < 50:
                    logger.info(f'Skipping low-content page: {url}')
                    continue

                summary, tags = _summarize_and_tag(title, text, service)
                _write_to_s3(service, doc_hash, title, url, text, summary, tags)
                _write_metadata(source_id, doc_hash, title, url, service, summary, tags,
                                article.get('pubDate', ''))
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
