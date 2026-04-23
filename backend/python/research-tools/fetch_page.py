"""Fetch Page tool Lambda for Bedrock Agent action group.

Fetches a URL and extracts text content using stdlib HTMLParser.
Only allows http/https URLs (security: no file:// SSRF).
Returns content + title in action group response format.
"""

import json
import logging
from html.parser import HTMLParser
from urllib.request import Request, urlopen

logger = logging.getLogger()
logger.setLevel(logging.INFO)

FETCH_TIMEOUT_SECONDS = 15
MAX_CONTENT_LENGTH = 30000  # chars


# ---------------------------------------------------------------------------
# HTML text extraction (stdlib only)
# ---------------------------------------------------------------------------

class _TextExtractor(HTMLParser):
    """HTML parser that extracts paragraph text and the page title."""

    _SKIP_TAGS = {'script', 'style', 'nav', 'footer', 'header', 'noscript', 'aside'}

    def __init__(self):
        super().__init__()
        self._paragraphs = []
        self._in_p = False
        self._skip_depth = 0
        self._current = []
        self._in_title = False
        self._title_parts = []
        self.title = ''

    def handle_starttag(self, tag, attrs):
        tag_lower = tag.lower()
        if tag_lower in self._SKIP_TAGS:
            self._skip_depth += 1
        elif tag_lower == 'title' and not self.title:
            self._in_title = True
            self._title_parts = []
        elif tag_lower == 'p' and self._skip_depth == 0:
            self._in_p = True
            self._current = []

    def handle_endtag(self, tag):
        tag_lower = tag.lower()
        if tag_lower in self._SKIP_TAGS:
            self._skip_depth = max(0, self._skip_depth - 1)
        elif tag_lower == 'title' and self._in_title:
            self.title = ''.join(self._title_parts).strip()
            self._in_title = False
        elif tag_lower == 'p' and self._in_p:
            text = ''.join(self._current).strip()
            if text and len(text) > 20:
                self._paragraphs.append(text)
            self._in_p = False
            self._current = []

    def handle_data(self, data):
        if self._in_title:
            self._title_parts.append(data)
        if self._in_p and self._skip_depth == 0:
            self._current.append(data)

    def get_text(self) -> str:
        return '\n\n'.join(self._paragraphs)


def _extract_content(html: str) -> tuple:
    """Extract title and paragraph text from HTML. Returns (title, text)."""
    parser = _TextExtractor()
    try:
        parser.feed(html)
    except Exception:
        pass
    return parser.title, parser.get_text()


def _fetch_url(url: str) -> str:
    """Fetch a URL and return response body as text. Only http/https allowed."""
    if not url.startswith(('http://', 'https://')):
        raise ValueError(f'Unsupported URL scheme — only http/https allowed: {url[:40]}')
    req = Request(url, headers={'User-Agent': 'TtobakResearchAgent/1.0'})
    with urlopen(req, timeout=FETCH_TIMEOUT_SECONDS) as resp:
        charset = resp.headers.get_content_charset() or 'utf-8'
        return resp.read().decode(charset, errors='replace')


# ---------------------------------------------------------------------------
# Lambda handler — Bedrock Agent action group
# ---------------------------------------------------------------------------

def handler(event, context):
    parameters = {p['name']: p['value'] for p in event.get('parameters', [])}
    url = parameters.get('url', '').strip()

    if not url:
        return action_response(event, 'error', 'url parameter is required')

    if not url.startswith(('http://', 'https://')):
        return action_response(event, 'error', 'Only http/https URLs are allowed')

    try:
        html = _fetch_url(url)
        title, text = _extract_content(html)

        if not text or len(text) < 30:
            return action_response(event, 'empty', f'Could not extract meaningful content from {url}')

        # Truncate to stay within response limits
        truncated = text[:MAX_CONTENT_LENGTH]
        result = json.dumps({
            'status': 'ok',
            'title': title or '',
            'url': url,
            'contentLength': len(truncated),
            'content': truncated,
        })
        return action_response_raw(event, result)

    except ValueError as e:
        return action_response(event, 'error', str(e))
    except Exception as e:
        logger.warning(f'Failed to fetch {url}: {e}')
        return action_response(event, 'error', f'Failed to fetch URL: {e}')


def action_response(event, status, message):
    return action_response_raw(event, json.dumps({'status': status, 'message': message}))


def action_response_raw(event, body):
    return {
        'messageVersion': '1.0',
        'response': {
            'actionGroup': event.get('actionGroup', ''),
            'function': event.get('function', ''),
            'functionResponse': {
                'responseBody': {
                    'TEXT': {
                        'body': body,
                    }
                }
            }
        }
    }
