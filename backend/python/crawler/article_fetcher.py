"""Bounded article retrieval for news enrichment; never substitute search snippets.

Resolve each redirect independently, reject nonpublic answers, and connect to
the checked numeric address while retaining the hostname for TLS and HTTP.
This module is staged independently of the active legacy crawler.
"""

import codecs
import gzip
import http.client
import io
import ipaddress
import json
import os
import random
import re
import socket
import ssl
import subprocess
import sys
import time
import zlib
from dataclasses import dataclass
from html.parser import HTMLParser
from urllib.parse import quote, urljoin, urlsplit

from news_pipeline import canonicalize_url

MAX_REDIRECTS = 5
MAX_COMPRESSED_BYTES = 2 * 1024 * 1024
MAX_DECODED_BYTES = 4 * 1024 * 1024
MAX_BODY_CHARS = 30000
MIN_BODY_CHARS = 800
MIN_BLOCKS = 4
CONNECT_TIMEOUT_SECONDS = 10
ADDRESS_TIMEOUT_SECONDS = 3
DNS_TIMEOUT_SECONDS = 5
MAX_ATTEMPTS = 3
MAX_ATTEMPT_SECONDS = 40
_READ_CHUNK = 65536
ALLOWED_CONTENT_TYPES = {"text/html", "application/xhtml+xml", "text/plain"}
_REDIRECT_CODES = {301, 302, 303, 307, 308}
_HEADERS = {
    "User-Agent": "TtobakCrawler/2.0",
    "Accept": "text/html,application/xhtml+xml,text/plain",
    "Accept-Encoding": "gzip",
    "Accept-Language": "ko-KR,ko;q=0.9,en;q=0.5",
    "Connection": "close",
}
# No application imports, user code, shell, or inherited Python search path.
# The child emits at most 33 bounded getaddrinfo records. subprocess.run kills
# and reaps it on timeout, so stalled DNS cannot accumulate warm-worker threads.
_DNS_SCRIPT = """
import json, socket, sys
answers = socket.getaddrinfo(sys.argv[1], int(sys.argv[2]), type=socket.SOCK_STREAM)
print(json.dumps(answers[:33]))
"""


class FetchRejected(Exception):
    """Only a bounded reason code may leave the worker; no URLs or raw errors."""

    def __init__(self, reason):
        super().__init__(reason)
        self.reason = reason


@dataclass(frozen=True)
class ArticleBody:
    requested_url: str
    final_url: str
    canonical_url: str
    title: str
    published_at: str
    body: str
    blocks: tuple


def _resolve_addresses(host, port, deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError()
    try:
        result = subprocess.run(
            [sys.executable, "-I", "-S", "-c", _DNS_SCRIPT, host, str(port)],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            # Hosted Python and Lambda may dynamically link libpython outside
            # the system loader path. Keep that path, not application secrets
            # or PYTHONPATH/LD_PRELOAD hooks; -I -S also disables Python hooks.
            env={"LD_LIBRARY_PATH": os.environ.get("LD_LIBRARY_PATH", "")}, check=True,
            timeout=min(DNS_TIMEOUT_SECONDS, remaining))
    except subprocess.TimeoutExpired:
        raise TimeoutError() from None
    except subprocess.CalledProcessError:
        raise FetchRejected("DNS_ERROR") from None
    if len(result.stdout) > 32768:
        raise FetchRejected("DNS_ERROR")
    try:
        return json.loads(result.stdout)
    except (ValueError, UnicodeError):
        raise FetchRejected("DNS_ERROR") from None


def _target(url, deadline):
    try:
        normalized = canonicalize_url(url)
        parsed = urlsplit(url)._replace(netloc=urlsplit(normalized).netloc)
        port = parsed.port or (443 if parsed.scheme == "https" else 80)
        if port != (443 if parsed.scheme == "https" else 80):
            raise ValueError("Non-web port")
        answers = _resolve_addresses(parsed.hostname, port, deadline)
        if not answers or len(answers) > 32:
            raise ValueError("Missing or excessive DNS answers")
        for family, kind, _, _, address in answers:
            ip = ipaddress.ip_address(address[0])
            # IPv4-mapped IPv6 and transition ranges must not tunnel to a
            # destination different from the validated public address.
            if (family not in (socket.AF_INET, socket.AF_INET6)
                    or kind != socket.SOCK_STREAM or address[1] != port
                    or not ip.is_global or ip.is_multicast
                    or (ip.version == 6 and ip not in ipaddress.ip_network("2000::/3"))
                    or getattr(ip, "ipv4_mapped", None)
                    or getattr(ip, "sixtofour", None)
                    or getattr(ip, "teredo", None)):
                raise ValueError("Nonpublic address")
        return parsed, [(family, kind, protocol, name, tuple(address))
                        for family, kind, protocol, name, address in answers]
    except TimeoutError:
        raise
    except (ValueError, UnicodeError, OSError):
        raise FetchRejected("BLOCKED_HOST") from None


def validate_public_url(url):
    _target(url, time.monotonic() + DNS_TIMEOUT_SECONDS)


class _DeadlineReader(io.RawIOBase):
    """Apply one deadline to every socket receive, including HTTP headers."""

    def __init__(self, sock, deadline):
        self.sock = sock
        self.deadline = deadline

    def readable(self):
        return True

    def readinto(self, buffer):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError()
        self.sock.settimeout(min(CONNECT_TIMEOUT_SECONDS, remaining))
        return self.sock.recv_into(buffer)


class _ResponseSocket:
    def __init__(self, sock, deadline):
        self.sock = sock
        self.deadline = deadline

    def makefile(self, mode):
        return io.BufferedReader(_DeadlineReader(self.sock, self.deadline))


def _open_response(parsed, answers, deadline):
    """No proxy, second DNS lookup, credential forwarding, or automatic redirect."""
    for index, answer in enumerate(answers):
        try:
            return _open_address(parsed, answer, deadline)
        except (OSError, http.client.HTTPException):
            if time.monotonic() >= deadline:
                raise TimeoutError() from None
            if index + 1 == len(answers):
                raise
    raise FetchRejected("DNS_ERROR")


def _open_address(parsed, answer, deadline):
    sock = None
    connection = None
    try:
        family, kind, protocol, _, address = answer
        sock = socket.socket(family, kind, protocol)
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError()
        sock.settimeout(min(ADDRESS_TIMEOUT_SECONDS, remaining))
        sock.connect(address)
        if parsed.scheme == "https":
            context = ssl.create_default_context()
            context.minimum_version = ssl.TLSVersion.TLSv1_2
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError()
            sock.settimeout(min(ADDRESS_TIMEOUT_SECONDS, remaining))
            sock = context.wrap_socket(sock, server_hostname=parsed.hostname)
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError()
        sock.settimeout(min(CONNECT_TIMEOUT_SECONDS, remaining))
        connection = http.client.HTTPConnection(
            parsed.hostname, parsed.port or (443 if parsed.scheme == "https" else 80))
        connection.sock = sock
        path = quote(parsed.path or "/", safe="/%:@!$&'()*+,;=-._~")
        if parsed.query:
            path += "?" + quote(parsed.query, safe="/?%:@!$&'()*+,;=-._~")
        connection.request("GET", path, headers=_HEADERS)
        # HTTPResponse normally reads from a socket file without an overall
        # deadline. The adapter retains the stdlib header/chunk parser while
        # bounding slow headers and a body that arrives one byte at a time.
        response = http.client.HTTPResponse(_ResponseSocket(sock, deadline))
        response.begin()
        return connection, response
    except BaseException:
        if connection is not None:
            connection.close()
        elif sock is not None:
            sock.close()
        raise


def _read_bounded(response):
    header = response.headers.get("Content-Length")
    transfer = (response.headers.get("Transfer-Encoding") or "").lower()
    if (len(response.headers.get_all("Content-Length", [])) > 1
            or (header is not None and transfer)
            or transfer not in ("", "chunked")):
        raise FetchRejected("INVALID_LENGTH")
    expected = None
    if header is not None:
        try:
            if not header.isascii() or not header.isdigit() or len(header) > 10:
                raise ValueError()
            expected = int(header)
        except ValueError:
            raise FetchRejected("INVALID_LENGTH") from None
        if expected > MAX_COMPRESSED_BYTES:
            raise FetchRejected("BODY_TOO_LARGE")
    chunks = []
    total = 0
    while True:
        chunk = response.read1(_READ_CHUNK)
        if not chunk:
            break
        total += len(chunk)
        if total > MAX_COMPRESSED_BYTES:
            raise FetchRejected("BODY_TOO_LARGE")
        chunks.append(chunk)
    if expected is not None and total != expected:
        raise FetchRejected("INCOMPLETE_BODY")
    return b"".join(chunks)


class _CharsetExtractor(HTMLParser):
    def __init__(self):
        super().__init__()
        self.charsets = []

    def handle_starttag(self, tag, attrs):
        if tag != "meta":
            return
        attrs = dict(attrs)
        if attrs.get("charset"):
            self.charsets.append(attrs["charset"])
        if (attrs.get("http-equiv") or "").lower() == "content-type":
            match = re.search(r"(?:^|;)\s*charset\s*=\s*['\"]?([^;'\"\s]+)",
                              attrs.get("content", ""), re.I)
            if match:
                self.charsets.append(match.group(1))


def _body_charset(raw, headers):
    declarations = []
    parameters = headers.get_params(header="content-type", failobj=[]) or []
    http_charsets = [value for key, value in parameters[1:] if key.lower() == "charset"]
    if len(http_charsets) > 1 or any(not isinstance(value, str) or not value.strip()
                                   for value in http_charsets):
        raise FetchRejected("DECODE_ERROR")
    declarations.extend(http_charsets)
    if headers.get_content_type() != "text/plain":
        parser = _CharsetExtractor()
        parser.feed(raw[:4096].decode("ascii", errors="ignore"))
        declarations.extend(parser.charsets)
    if raw.startswith(codecs.BOM_UTF8):
        declarations.append("utf-8")
    normalized = {codecs.lookup(name).name for name in declarations}
    if len(normalized) > 1:
        raise FetchRejected("DECODE_ERROR")
    charset = next(iter(normalized), "utf-8")
    # Reject permissive single-byte decoders that turn Korean bytes into
    # plausible-length mojibake and hide paywall markers.
    if charset not in {"utf-8", "ascii", "euc_kr", "cp949"}:
        raise FetchRejected("UNSUPPORTED_CHARSET")
    return "utf-8-sig" if charset == "utf-8" else charset


def _decode_body(raw, headers):
    encoding = (headers.get("Content-Encoding") or "identity").lower().strip()
    if encoding not in ("identity", "gzip"):
        raise FetchRejected("UNSUPPORTED_ENCODING")
    try:
        if encoding == "gzip":
            with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:
                raw = stream.read(MAX_DECODED_BYTES + 1)
        if len(raw) > MAX_DECODED_BYTES:
            raise FetchRejected("BODY_TOO_LARGE")
        return raw.decode(_body_charset(raw, headers))
    except (OSError, EOFError, zlib.error, UnicodeError, LookupError):
        raise FetchRejected("DECODE_ERROR") from None


_SKIP_TAGS = {"script", "style", "nav", "header", "footer", "aside", "form", "noscript"}
_BLOCK_TAGS = {"p", "li", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote"}
_VISIBLE_BREAKS = _BLOCK_TAGS | {
    "br", "div", "article", "main", "section", "ul", "ol", "dl", "dt", "dd",
    "table", "tr", "td", "th", "hr", "pre",
}
_VOID_TAGS = {"area", "base", "br", "col", "embed", "hr", "img", "input",
              "link", "meta", "param", "source", "track", "wbr"}
_PAYWALL_MARKERS = (
    "구독 후 전체 기사", "구독자만 볼 수", "로그인 후 이용", "유료 구독자",
    "subscribe to continue reading", "this content is for subscribers",
    "subscribers only", "sign in to continue reading",
)


class _ArticleExtractor(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.stack = []  # (tag, hidden, article scope)
        self.blocks = []  # (article scope, text)
        self.text = []
        self.block_scope = False
        self.active_block = None
        self.has_scope = False
        self.has_script = False
        self.title = []
        self.canonical = ""
        self.published_at = ""
        self.visible_text = []

    def _flush(self):
        text = re.sub(r"\s+", " ", "".join(self.text)).strip()
        if text and self.active_block:
            self.blocks.append((self.block_scope, text))
            if len(self.blocks) > 2000:
                raise FetchRejected("BODY_TOO_LARGE")
        self.text = []
        self.active_block = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "script" and attrs.get("src"):
            self.has_script = True
        if tag == "link" and "canonical" in (attrs.get("rel") or "").lower().split():
            self.canonical = attrs.get("href") or ""
        if tag == "meta" and (attrs.get("property") or attrs.get("name")) in (
                "article:published_time", "datePublished"):
            self.published_at = attrs.get("content") or ""
        parent_hidden = bool(self.stack and self.stack[-1][1])
        marker = f"{attrs.get('class', '')} {attrs.get('id', '')}".lower()
        hidden = (parent_hidden or tag in _SKIP_TAGS or "cookie" in marker
                  or "consent" in marker or "hidden" in attrs
                  or attrs.get("aria-hidden") == "true")
        scope = bool(self.stack and self.stack[-1][2]) or tag in ("article", "main")
        if not hidden and tag in _VISIBLE_BREAKS:
            self.visible_text.append(" ")
        if tag in ("article", "main") and not hidden:
            self.has_scope = True
        if tag in _BLOCK_TAGS and not hidden:
            self._flush()
            self.active_block = tag
            self.block_scope = scope
        if tag == "br" and not hidden:
            self.text.append(" ")
        if tag not in _VOID_TAGS:
            if len(self.stack) >= 256:
                raise FetchRejected("HTML_TOO_DEEP")
            self.stack.append((tag, hidden, scope))

    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)
        if tag not in _VOID_TAGS:
            self.handle_endtag(tag)

    def handle_endtag(self, tag):
        for index in range(len(self.stack) - 1, -1, -1):
            if self.stack[index][0] == tag:
                closing = self.stack[index:]
                if any(t in _VISIBLE_BREAKS and not hidden for t, hidden, _ in closing):
                    self.visible_text.append(" ")
                if self.active_block and any(t == self.active_block for t, _, _ in closing):
                    self._flush()
                del self.stack[index:]
                if self.active_block is None:
                    for parent, hidden, scope in reversed(self.stack):
                        if parent in _BLOCK_TAGS and not hidden:
                            self.active_block = parent
                            self.block_scope = scope
                            break
                break

    def handle_data(self, data):
        if self.stack and not self.stack[-1][1]:
            self.visible_text.append(data)
            if self.stack[-1][0] == "title":
                self.title.append(data)
            if self.active_block:
                self.text.append(data)


def _quality_body(blocks):
    body = "\n\n".join(blocks)
    if any(marker in body.lower() for marker in _PAYWALL_MARKERS):
        raise FetchRejected("PAYWALL")
    if len(body) > MAX_BODY_CHARS:
        raise FetchRejected("BODY_TOO_LARGE")
    if len(body) < MIN_BODY_CHARS or len(blocks) < MIN_BLOCKS:
        raise FetchRejected("BODY_TOO_SHORT")
    return body


def normalize_html(html, url):
    if len(html.encode("utf-8")) > MAX_DECODED_BYTES:
        raise FetchRejected("BODY_TOO_LARGE")
    parser = _ArticleExtractor()
    parser.feed(html)
    parser.close()
    parser._flush()
    visible = re.sub(r"\s+", " ", "".join(parser.visible_text)).lower()
    if any(marker in visible for marker in _PAYWALL_MARKERS):
        raise FetchRejected("PAYWALL")
    # An empty article/main is not permission to summarize navigation chrome.
    blocks = tuple(text for scope, text in parser.blocks if scope or not parser.has_scope)
    body = "\n\n".join(blocks)
    if len(body) < 50 and parser.has_script:
        raise FetchRejected("JS_SHELL")
    body = _quality_body(blocks)
    canonical = canonicalize_url(url)
    try:
        proposed = canonicalize_url(urljoin(url, parser.canonical))
        # A page cannot claim another publisher's identity and overwrite it.
        if urlsplit(proposed).hostname == urlsplit(url).hostname:
            canonical = proposed
    except ValueError:
        pass
    return ArticleBody(url, url, canonical,
                       " ".join("".join(parser.title).split())[:500],
                       parser.published_at[:100], body, blocks)


def _fetch_once(url):
    deadline = time.monotonic() + MAX_ATTEMPT_SECONDS
    current = url
    for redirect in range(MAX_REDIRECTS + 1):
        parsed, answers = _target(current, deadline)
        connection, response = _open_response(parsed, answers, deadline)
        try:
            if response.status in _REDIRECT_CODES:
                location = response.headers.get("Location")
                if not location or redirect == MAX_REDIRECTS:
                    raise FetchRejected("REDIRECT_LIMIT")
                try:
                    current = urljoin(current, location)
                except ValueError:
                    raise FetchRejected("INVALID_REDIRECT") from None
                continue
            if response.status != 200:
                raise FetchRejected(f"HTTP_{response.status}")
            raw_types = response.headers.get_all("Content-Type", [])
            if (len(raw_types) != 1
                    or raw_types[0].split(";", 1)[0].strip().lower()
                    not in ALLOWED_CONTENT_TYPES):
                raise FetchRejected("UNSUPPORTED_CONTENT_TYPE")
            content_type = response.headers.get_content_type()
            if content_type not in ALLOWED_CONTENT_TYPES:
                raise FetchRejected("UNSUPPORTED_CONTENT_TYPE")
            text = _decode_body(_read_bounded(response), response.headers)
        finally:
            response.close()
            connection.close()
        if content_type == "text/plain":
            blocks = tuple(p.strip() for p in re.split(r"\n\s*\n", text) if p.strip())
            article = ArticleBody(current, current, canonicalize_url(current), "", "",
                                  _quality_body(blocks), blocks)
        else:
            article = normalize_html(text, current)
        return ArticleBody(url, current, article.canonical_url, article.title,
                           article.published_at, article.body, article.blocks)
    raise FetchRejected("REDIRECT_LIMIT")


def fetch_article(url):
    for attempt in range(MAX_ATTEMPTS):
        try:
            return _fetch_once(url)
        except FetchRejected as error:
            reason = error.reason
            retry = reason == "HTTP_429" or reason.startswith("HTTP_5")
        except (TimeoutError, socket.timeout):
            reason, retry = "TIMEOUT", True
        except (OSError, http.client.HTTPException):
            reason, retry = "FETCH_ERROR", True
        if not retry or attempt + 1 == MAX_ATTEMPTS:
            raise FetchRejected(reason) from None
        time.sleep(min(2 ** attempt, 8) + random.uniform(0, 0.5))
    raise FetchRejected("FETCH_ERROR")
