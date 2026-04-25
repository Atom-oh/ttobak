"""Inline tools for the Deep Research Agent.

These run inside AgentCore Runtime — no separate Lambda needed.
Direct boto3 access for S3 and DynamoDB.
"""

import json
import os
import logging
import hashlib
import xml.etree.ElementTree as ET
from datetime import datetime
from html.parser import HTMLParser
from urllib.request import Request, urlopen
from urllib.parse import quote_plus
from urllib.error import URLError

from strands.tools import tool

logger = logging.getLogger(__name__)

TABLE_NAME = os.environ.get("TABLE_NAME", "ttobak-main")
KB_BUCKET = os.environ.get("KB_BUCKET_NAME", "ttobak-kb-180294183052")

# Lazy-init boto3 clients
_s3 = None
_table = None


def _get_s3():
    global _s3
    if _s3 is None:
        import boto3
        _s3 = boto3.client("s3")
    return _s3


def _get_table():
    global _table
    if _table is None:
        import boto3
        _table = boto3.resource("dynamodb").Table(TABLE_NAME)
    return _table


# ---------------------------------------------------------------------------
# HTML text extraction (stdlib only)
# ---------------------------------------------------------------------------

class _TextExtractor(HTMLParser):
    CONTENT_TAGS = {"p", "li", "h1", "h2", "h3", "h4", "h5", "h6", "td", "th", "blockquote"}
    SKIP_TAGS = {"script", "style", "nav", "footer", "header", "noscript"}

    def __init__(self):
        super().__init__()
        self._pieces: list[str] = []
        self._in_content = False
        self._skip_depth = 0
        self._current: list[str] = []

    def handle_starttag(self, tag, attrs):
        if tag in self.SKIP_TAGS:
            self._skip_depth += 1
        elif tag in self.CONTENT_TAGS and self._skip_depth == 0:
            self._in_content = True
            self._current = []

    def handle_endtag(self, tag):
        if tag in self.SKIP_TAGS:
            self._skip_depth = max(0, self._skip_depth - 1)
        elif tag in self.CONTENT_TAGS and self._in_content:
            text = " ".join(self._current).strip()
            if text:
                self._pieces.append(text)
            self._in_content = False

    def handle_data(self, data):
        if self._in_content and self._skip_depth == 0:
            self._current.append(data.strip())

    def get_text(self) -> str:
        return "\n\n".join(self._pieces)


# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------

@tool
def web_search(query: str, max_results: int = 10) -> str:
    """Search the web using Google News RSS. Returns article titles and URLs.

    Args:
        query: Search query (Korean or English)
        max_results: Maximum number of results to return (default 10)
    """
    encoded = quote_plus(query)
    rss_url = f"https://news.google.com/rss/search?q={encoded}&hl=ko&gl=KR&ceid=KR:ko"

    try:
        req = Request(rss_url, headers={"User-Agent": "TtobakResearch/1.0"})
        with urlopen(req, timeout=10) as resp:
            xml_data = resp.read()

        root = ET.fromstring(xml_data)
        articles = []
        channel = root.find("channel")
        if channel is not None:
            for item in channel.findall("item"):
                title = item.findtext("title", "")
                link = item.findtext("link", "")
                pub_date = item.findtext("pubDate", "")
                if title and link:
                    articles.append({"title": title, "url": link, "date": pub_date})
                    if len(articles) >= max_results:
                        break

        if not articles:
            return json.dumps({"results": [], "message": "No results found"})

        return json.dumps({"results": articles}, ensure_ascii=False)
    except Exception as e:
        logger.warning(f"Web search failed for '{query}': {e}")
        return json.dumps({"results": [], "error": str(e)})


@tool
def fetch_page(url: str) -> str:
    """Fetch and extract text content from a web page URL.

    Args:
        url: URL to fetch (must be http or https)
    """
    if not url.startswith(("http://", "https://")):
        return json.dumps({"error": "Only http/https URLs are supported"})

    try:
        req = Request(url, headers={"User-Agent": "TtobakResearch/1.0"})
        with urlopen(req, timeout=15) as resp:
            charset = resp.headers.get_content_charset() or "utf-8"
            html = resp.read().decode(charset, errors="replace")

        parser = _TextExtractor()
        parser.feed(html)
        text = parser.get_text()[:8000]

        # Extract title
        title_start = html.find("<title>")
        title_end = html.find("</title>")
        title = html[title_start + 7:title_end].strip() if title_start >= 0 and title_end > title_start else ""

        return json.dumps({"title": title, "content": text, "url": url}, ensure_ascii=False)
    except Exception as e:
        logger.warning(f"Fetch failed for {url}: {e}")
        return json.dumps({"error": str(e), "url": url})


def _split_sections(content: str) -> list[dict]:
    """Split markdown content into sections by h2 headings."""
    import re
    sections = []
    parts = re.split(r'^(## .+)$', content, flags=re.MULTILINE)

    preamble = parts[0].strip()
    if preamble:
        sections.append({"title": "Overview", "slug": "overview", "body": preamble})

    for i in range(1, len(parts), 2):
        heading = parts[i].lstrip("# ").strip()
        body = parts[i + 1].strip() if i + 1 < len(parts) else ""
        slug = re.sub(r'[^\w가-힣-]', '', heading.lower().replace(' ', '-'))[:60]
        slug = slug or f"section-{len(sections)}"
        sections.append({"title": heading, "slug": slug, "body": f"## {heading}\n\n{body}"})

    return sections


@tool
def save_report(research_id: str, content: str, summary: str, source_count: int, word_count: int) -> str:
    """Save the completed research report to S3 and update DynamoDB status to done.

    Args:
        research_id: The research job ID (provided in the user message)
        content: Full markdown report content
        summary: Executive summary (200-400 words)
        source_count: Number of sources cited in the report
        word_count: Total word count of the report
    """
    if not research_id or not content:
        return json.dumps({"error": "research_id and content are required"})

    s3 = _get_s3()
    table = _get_table()
    s3_key = f"shared/research/{research_id}.md"

    try:
        s3.put_object(
            Bucket=KB_BUCKET,
            Key=s3_key,
            Body=content.encode("utf-8"),
            ContentType="text/markdown; charset=utf-8",
        )
        logger.info(f"Saved full report to s3://{KB_BUCKET}/{s3_key}")

        sections = _split_sections(content)
        section_meta = []
        for idx, sec in enumerate(sections):
            sec_key = f"shared/research/{research_id}/{sec['slug']}.md"
            s3.put_object(
                Bucket=KB_BUCKET,
                Key=sec_key,
                Body=sec["body"].encode("utf-8"),
                ContentType="text/markdown; charset=utf-8",
            )
            section_meta.append({
                "index": idx,
                "title": sec["title"],
                "slug": sec["slug"],
                "s3Key": sec_key,
                "wordCount": len(sec["body"].split()),
            })

        logger.info(f"Saved {len(sections)} sections for research {research_id}")

        table.update_item(
            Key={"PK": f"RESEARCH#{research_id}", "SK": "CONFIG"},
            UpdateExpression="SET #s = :s, completedAt = :c, s3Key = :k, sourceCount = :sc, wordCount = :wc, summary = :sm, sections = :sec",
            ExpressionAttributeNames={"#s": "status"},
            ExpressionAttributeValues={
                ":s": "done",
                ":c": datetime.utcnow().isoformat() + "Z",
                ":k": s3_key,
                ":sc": source_count,
                ":wc": word_count,
                ":sm": summary[:1000],
                ":sec": section_meta,
            },
        )
        logger.info(f"Updated research {research_id} status to done ({len(sections)} sections)")

        return json.dumps({"status": "saved", "s3Key": s3_key, "sections": len(sections)})
    except Exception as e:
        logger.error(f"Save report failed: {e}", exc_info=True)
        return json.dumps({"error": str(e)})
