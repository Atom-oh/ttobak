"""News Crawler Lambda — fetches and indexes Korean tech news articles.

Triggered by Step Functions with a source config containing newsQueries
and/or customUrls. Searches Google News RSS and Naver News RSS, fetches
articles, extracts text, generates summaries + auto-tags via Bedrock,
and stores in S3 + DynamoDB.

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
from urllib.parse import quote_plus, urlencode
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

# RSS sources
GOOGLE_NEWS_RSS = 'https://news.google.com/rss/search?q={query}&hl=ko&gl=KR&ceid=KR:ko'
NAVER_NEWS_RSS = 'https://news.search.naver.com/search.naver?where=rss&query={query}'

# Site-specific RSS feeds (Korean tech/business news)
SITE_RSS_FEEDS = {
    'zdnet': 'https://zdnet.co.kr/rss/newsall.xml',
    'etnews': 'https://rss.etnews.com/Section901.xml',
    'itchosun': 'https://it.chosun.com/rss/it_all_rss.xml',
    'bloter': 'https://www.bloter.net/feed',
}

MAX_ARTICLES_PER_QUERY = 5
MAX_ARTICLES_PER_FEED = 3
FETCH_TIMEOUT_SECONDS = 10
MAX_CONTENT_LENGTH = 30000

BLOCKED_URL_PATTERNS = [
    'contents.premium.naver.com',
    'premium.chosun.com',
    'www.chosun.com/premium',
    'paywalled.',
]
MIN_BODY_LENGTH = 100


def _make_hash(url: str) -> str:
    return hashlib.sha256(f'news:{url}'.encode('utf-8')).hexdigest()[:16]


# ---------------------------------------------------------------------------
# HTML paragraph extraction (stdlib only)
# ---------------------------------------------------------------------------

class _ParagraphExtractor(HTMLParser):
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
            if text and len(text) > 20:
                self._paragraphs.append(text)
            self._in_p = False
            self._current = []

    def handle_data(self, data):
        if self._in_p and self._skip_depth == 0:
            self._current.append(data)

    def get_text(self) -> str:
        return '\n\n'.join(self._paragraphs)


def extract_paragraphs(html: str) -> str:
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

def _parse_rss(xml_text: str, max_items: int = MAX_ARTICLES_PER_QUERY) -> list:
    articles = []
    if len(xml_text) > MAX_RSS_SIZE:
        logger.warning(f'RSS response too large ({len(xml_text)} chars), skipping')
        return []
    try:
        root = ET.fromstring(xml_text)
        channel = root.find('channel')
        if channel is None:
            for ns_prefix in ['', '{http://www.w3.org/2005/Atom}']:
                entries = root.findall(f'{ns_prefix}entry')
                for entry in entries:
                    title_el = entry.find(f'{ns_prefix}title')
                    link_el = entry.find(f'{ns_prefix}link')
                    pub_el = entry.find(f'{ns_prefix}published') or entry.find(f'{ns_prefix}updated')
                    summary_el = entry.find(f'{ns_prefix}summary') or entry.find(f'{ns_prefix}content')
                    href = ''
                    if link_el is not None:
                        href = link_el.get('href', '') or (link_el.text or '')
                    if title_el is not None and href:
                        articles.append({
                            'title': _strip_html_tags(title_el.text or ''),
                            'url': href,
                            'pubDate': (pub_el.text if pub_el is not None else ''),
                            'description': _strip_html_tags(summary_el.text or '') if summary_el is not None else '',
                        })
                        if len(articles) >= max_items:
                            break
            return articles

        for item in channel.findall('item'):
            title_el = item.find('title')
            link_el = item.find('link')
            pub_el = item.find('pubDate')
            desc_el = item.find('description')
            if title_el is not None and link_el is not None:
                articles.append({
                    'title': _strip_html_tags(title_el.text or ''),
                    'url': link_el.text or '',
                    'pubDate': pub_el.text if pub_el is not None else '',
                    'description': _strip_html_tags(desc_el.text or '') if desc_el is not None else '',
                })
                if len(articles) >= max_items:
                    break
    except ET.ParseError as e:
        logger.warning(f'RSS parse error: {e}')
    return articles


def _search_google_news(query: str) -> list:
    encoded = quote_plus(query)
    rss_url = GOOGLE_NEWS_RSS.format(query=encoded)
    try:
        xml_text = _fetch_url(rss_url)
        return _parse_rss(xml_text)
    except Exception as e:
        logger.warning(f'Google News RSS failed for "{query}": {e}')
        return []


def _search_naver_news(query: str) -> list:
    encoded = quote_plus(query)
    rss_url = NAVER_NEWS_RSS.format(query=encoded)
    try:
        xml_text = _fetch_url(rss_url)
        return _parse_rss(xml_text, max_items=MAX_ARTICLES_PER_QUERY)
    except Exception as e:
        logger.warning(f'Naver News RSS failed for "{query}": {e}')
        return []


def _fetch_site_rss(feed_url: str, keyword_filter: str = '') -> list:
    try:
        xml_text = _fetch_url(feed_url, timeout=15)
        articles = _parse_rss(xml_text, max_items=20)
        if keyword_filter:
            kw_lower = keyword_filter.lower()
            articles = [a for a in articles
                        if kw_lower in a.get('title', '').lower()
                        or kw_lower in a.get('description', '').lower()]
        return articles[:MAX_ARTICLES_PER_FEED]
    except Exception as e:
        logger.warning(f'Site RSS failed for {feed_url}: {e}')
        return []


def _generate_search_queries(source_name: str, keywords: list) -> list:
    """Generate search queries by combining source name with keywords.

    If keywords are provided, each becomes "{source_name} {keyword}".
    The bare source name is always included as the first query.
    Without keywords, default topics are appended automatically.
    """
    queries = []

    if source_name:
        queries.append(source_name)

        if keywords:
            for kw in keywords:
                combined = f'{source_name} {kw}'
                if combined not in queries:
                    queries.append(combined)
        else:
            for topic in ['IT', '클라우드', 'AI', '디지털전환']:
                queries.append(f'{source_name} {topic}')

    return queries


# ---------------------------------------------------------------------------
# Bedrock summarization + auto-tagging
# ---------------------------------------------------------------------------

def _summarize_and_tag(title: str, text: str, source_name: str = '') -> tuple:
    """Generate SA briefing + auto-tags. Returns (summary, tags_list)."""
    content = text[:4000] if len(text) > 4000 else text
    source_hint = f'\n고객사: {source_name}' if source_name else ''
    prompt = (
        f'당신은 AWS Solutions Architect를 위한 고객사 뉴스 브리핑을 작성합니다.{source_hint}\n\n'
        f'아래 뉴스 기사를 분석하여 한국어로 다음 형식의 JSON으로 응답하세요:\n\n'
        f'{{"summary": "브리핑 내용 (핵심요약 3-5문장 + 비즈니스 시사점 + AWS 관련성)", '
        f'"tags": ["태그1", "태그2", ...]}}\n\n'
        f'태그 규칙:\n'
        f'- 기사 내용에서 핵심 주제/키워드를 3-8개 추출\n'
        f'- 회사명 (예: 우리은행, 삼성전자, SK텔레콤)\n'
        f'- 산업분야 (예: 금융, 통신, 제조, 유통, 공공)\n'
        f'- 기술 키워드 (예: AI, GPU, 클라우드, 반도체, LLM, 데이터, 보안, SaaS)\n'
        f'- 비즈니스 주제 (예: 디지털전환, M&A, 투자, 경제, 규제, ESG)\n'
        f'- 모두 한국어로 작성 (영문 약어는 그대로: AI, GPU, LLM, SaaS 등)\n\n'
        f'기사 제목: {title}\n\n'
        f'기사 내용:\n{content if content and len(content) > 30 else "(본문 없음 — 제목 기반으로 분석해주세요)"}'
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
            return summary, tags

        return response_text, []
    except Exception as e:
        logger.warning(f'Bedrock summarize+tag failed for "{title}": {e}')
        return '', []


# ---------------------------------------------------------------------------
# DynamoDB dedup
# ---------------------------------------------------------------------------

def _doc_exists(source_id: str, doc_hash: str) -> bool:
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
                 content: str, summary: str, pub_date: str, tags: list) -> None:
    tag_line = f'**Tags:** {", ".join(tags)}\n' if tags else ''
    md = (
        f'# {title}\n\n'
        f'**Published:** {pub_date}\n'
        f'**Source:** {url}\n'
        f'{tag_line}\n'
        f'---\n\n'
        f'{summary}\n'
    )
    if content and len(content) > 50:
        md += f'\n---\n\n## 원문 발췌\n\n{content[:MAX_CONTENT_LENGTH]}\n'
    key = f'shared/news/{source_id}/{doc_hash}.md'
    s3.put_object(
        Bucket=KB_BUCKET_NAME,
        Key=key,
        Body=md.encode('utf-8'),
        ContentType='text/markdown; charset=utf-8',
    )
    logger.info(f'Wrote s3://{KB_BUCKET_NAME}/{key}')


def _write_metadata(source_id: str, doc_hash: str, title: str, url: str,
                    pub_date: str, summary: str = '', source_name: str = '',
                    tags: list = None) -> None:
    crawled_at = int(time.time())
    item = {
        'PK': f'CRAWLER#{source_id}',
        'SK': f'DOC#{doc_hash}',
        'docHash': doc_hash,
        'url': url,
        'title': title,
        'pubDate': pub_date,
        'crawledAt': crawled_at,
        'type': 'news',
        's3Key': f'shared/news/{source_id}/{doc_hash}.md',
        'inKB': True,
        'GSI4PK': 'DOC#news',
        'GSI4SK': crawled_at,
    }
    item['sourceId'] = source_id
    if summary:
        item['summary'] = summary
    if source_name:
        item['source'] = source_name
        item['sourceName'] = source_name
    if tags:
        item['tags'] = tags
    table.put_item(Item=item)


# ---------------------------------------------------------------------------
# Lambda handler
# ---------------------------------------------------------------------------

def _is_blocked_url(url: str) -> bool:
    url_lower = url.lower()
    return any(pattern in url_lower for pattern in BLOCKED_URL_PATTERNS)


def _process_article(source_id: str, title: str, url: str,
                     pub_date: str, description: str = '',
                     crawler_source_name: str = '') -> bool:
    if _is_blocked_url(url):
        logger.info(f'Skipping paywalled/premium URL: {url}')
        return False

    doc_hash = _make_hash(url)

    if _doc_exists(source_id, doc_hash):
        logger.debug(f'Skipping duplicate: {url}')
        return False

    text = ''
    try:
        html = _fetch_url(url)
        text = extract_paragraphs(html)
    except Exception as e:
        logger.info(f'Could not fetch article body: {e}')

    if not text or len(text) < MIN_BODY_LENGTH:
        logger.info(f'Skipping article with insufficient body ({len(text or "")} chars): {title[:60]}')
        return False

    summary, tags = _summarize_and_tag(title, text, crawler_source_name)
    source_name = _extract_source_name(title)
    _write_to_s3(source_id, doc_hash, title, url, text, summary, pub_date, tags)
    _write_metadata(source_id, doc_hash, title, url, pub_date, summary, source_name, tags)
    return True


def _extract_source_name(title: str) -> str:
    if ' - ' in title:
        return title.rsplit(' - ', 1)[-1].strip()
    return ''


def handler(event, context):
    """Process news articles — automatically searches all aggregators.

    Expected event:
      {
        "sourceId": "wooribank",
        "sourceName": "우리은행",
        "newsQueries": ["AI", "클라우드", "디지털전환"],
        "customUrls": [{"url": "https://...", "title": "..."}]
      }

    The crawler always searches Google News + Naver News (aggregators that
    cover all Korean outlets). newsQueries are interest keywords that get
    combined with sourceName to form search queries.
    """
    source_id = event.get('sourceId', 'unknown')
    source_name = event.get('sourceName', '')
    keywords = event.get('newsQueries') or []
    custom_urls = event.get('customUrls') or []

    all_queries = _generate_search_queries(source_name, keywords)
    logger.info(f'News crawler: sourceId={source_id}, sourceName={source_name}, '
                f'keywords={keywords}, queries={all_queries}, customUrls={len(custom_urls)}')

    docs_added = 0
    docs_updated = 0
    errors = []
    seen_urls = set()

    def _try_process(title, url, pub_date='', description=''):
        nonlocal docs_added
        if url in seen_urls:
            return
        seen_urls.add(url)
        try:
            if _process_article(source_id, title, url, pub_date, description, source_name):
                docs_added += 1
        except Exception as e:
            error_msg = f'{url}: {e}'
            logger.error(f'Article error: {error_msg}', exc_info=True)
            errors.append(error_msg)

    # 1. Google News (covers all Korean outlets: 조선일보, 중앙일보, ZDNet, etc.)
    for query in all_queries:
        articles = _search_google_news(query)
        logger.info(f'Google News "{query}": {len(articles)} article(s)')
        for article in articles:
            _try_process(article['title'], article['url'],
                         article.get('pubDate', ''), article.get('description', ''))

    # 2. Naver News (largest Korean news aggregator)
    for query in all_queries:
        articles = _search_naver_news(query)
        logger.info(f'Naver News "{query}": {len(articles)} article(s)')
        for article in articles:
            _try_process(article['title'], article['url'],
                         article.get('pubDate', ''), article.get('description', ''))

    # 3. Site-specific RSS feeds (supplementary — catches articles aggregators may miss)
    if source_name:
        for site_key, feed_url in SITE_RSS_FEEDS.items():
            articles = _fetch_site_rss(feed_url, source_name)
            if articles:
                logger.info(f'Site RSS {site_key} (filter="{source_name}"): {len(articles)} article(s)')
                for article in articles:
                    _try_process(article['title'], article['url'],
                                 article.get('pubDate', ''), article.get('description', ''))

    # 4. Custom URLs
    for entry in custom_urls:
        url = entry.get('url', '')
        title = entry.get('title', url)
        if url:
            _try_process(title, url)

    result = {
        'docsAdded': docs_added,
        'docsUpdated': docs_updated,
        'errors': errors,
    }
    logger.info(f'News crawler complete: {json.dumps(result)}')
    return result
