"""Network, completeness, and extraction boundaries for the staged news reader."""

import gzip
import io
import socket
import unittest
from email.message import Message
from unittest import mock

import article_fetcher as fetcher


def article_html(extra="", head="", paragraphs=None):
    paragraphs = paragraphs or [f"기사 {n}: " + "사실과 근거를 설명하는 문장입니다. " * 15 for n in range(4)]
    return ("<html><head><title>기사 &amp; 제목</title>" + head + "</head><body>"
            + extra + "<article>" + "".join(f"<p>{p}</p>" for p in paragraphs)
            + "</article></body></html>").encode()


def response(body=None, status=200, **headers):
    value = mock.Mock()
    value.status = status
    value.headers = Message()
    value.headers["Content-Type"] = headers.pop("Content-Type", "text/html; charset=utf-8")
    for key, content in headers.items():
        value.headers[key] = content
    value.read1 = io.BytesIO(body if body is not None else article_html()).read
    return value


def public_resolver(host, port, **kwargs):
    return [(socket.AF_INET, socket.SOCK_STREAM, socket.IPPROTO_TCP, "", ("93.184.216.34", port))]


class TestArticleFetch(unittest.TestCase):
    def fetch_with(self, *responses, url="https://example.com/a"):
        pairs = [(mock.Mock(), item) for item in responses]
        with mock.patch.object(fetcher, "_open_response", side_effect=pairs) as opened:
            article = fetcher.fetch_article(url, resolver=public_resolver)
        for connection, item in pairs:
            connection.close.assert_called_once()
            item.close.assert_called_once()
        return article, opened

    def test_redirects_are_revalidated_and_original_query_is_preserved(self):
        article, opened = self.fetch_with(
            response(status=302, Location="/b?utm_source=keep&z=2&a=1"), response())
        self.assertEqual(article.requested_url, "https://example.com/a")
        self.assertEqual(article.final_url, "https://example.com/b?utm_source=keep&z=2&a=1")
        self.assertEqual(opened.call_args[0][0].query, "utm_source=keep&z=2&a=1")

    def test_blocked_redirect_never_opens_second_connection(self):
        first = response(status=302, Location="http://127.0.0.1/private")

        def resolve(host, port, **kwargs):
            ip = "127.0.0.1" if host == "127.0.0.1" else "93.184.216.34"
            return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, port))]

        connection = mock.Mock()
        with mock.patch.object(fetcher, "_open_response", return_value=(connection, first)) as opened:
            with self.assertRaisesRegex(fetcher.FetchRejected, "BLOCKED_HOST"):
                fetcher.fetch_article("https://example.com/a", resolver=resolve)
        opened.assert_called_once()
        connection.close.assert_called_once()

    def test_rejects_private_mixed_empty_and_transition_dns_answers(self):
        for addresses in ([], ["127.0.0.1"], ["169.254.169.254"], ["10.1.1.1"],
                          ["100.64.0.1"], ["224.0.0.1"], ["93.184.216.34", "10.0.0.1"],
                          ["::1"], ["64:ff9b::a00:1"], ["::ffff:127.0.0.1"],
                          ["2002:7f00:1::"], ["2001:0000:4136:e378:8000:63bf:3fff:fdd2"]):
            def resolve(host, port, **kwargs):
                return [(socket.AF_INET6 if ":" in ip else socket.AF_INET,
                         socket.SOCK_STREAM, 6, "", (ip, port)) for ip in addresses]
            with self.subTest(addresses=addresses), self.assertRaisesRegex(
                    fetcher.FetchRejected, "BLOCKED_HOST"):
                fetcher.validate_public_url("https://example.com/a", resolver=resolve)

    def test_rejects_credential_urls_non_web_ports_and_bad_schemes(self):
        for url in ("ftp://example.com/a", "https://user@example.com/a",
                    "http://example.com:8080/a", "https://example.com/\r\nheader"):
            with self.subTest(url=url), self.assertRaisesRegex(fetcher.FetchRejected, "BLOCKED_HOST"):
                fetcher.validate_public_url(url, resolver=public_resolver)

    def test_numeric_connection_is_pinned_and_tls_uses_hostname(self):
        parsed, addresses = fetcher._target("https://example.com/a", public_resolver)
        raw_socket, tls_socket, connection, http_response = (mock.Mock() for _ in range(4))
        context = mock.Mock()
        context.wrap_socket.return_value = tls_socket
        with mock.patch.object(fetcher.socket, "socket", return_value=raw_socket), \
                mock.patch.object(fetcher.socket, "getaddrinfo", side_effect=AssertionError("DNS rebound")), \
                mock.patch.object(fetcher.ssl, "create_default_context", return_value=context), \
                mock.patch.object(fetcher.http.client, "HTTPConnection", return_value=connection), \
                mock.patch.object(fetcher.http.client, "HTTPResponse", return_value=http_response):
            returned, _ = fetcher._open_response(parsed, addresses, fetcher.time.monotonic() + 40)
        raw_socket.connect.assert_called_once_with(("93.184.216.34", 443))
        context.wrap_socket.assert_called_once_with(raw_socket, server_hostname="example.com")
        self.assertIs(returned.sock, tls_socket)

    def test_tls_failure_closes_socket_without_retrying_an_unchecked_host(self):
        parsed, addresses = fetcher._target("https://example.com/a", public_resolver)
        sock, context = mock.Mock(), mock.Mock()
        context.wrap_socket.side_effect = OSError("certificate failure")
        with mock.patch.object(fetcher.socket, "socket", return_value=sock), \
                mock.patch.object(fetcher.ssl, "create_default_context", return_value=context):
            with self.assertRaises(OSError):
                fetcher._open_response(parsed, addresses, fetcher.time.monotonic() + 40)
        sock.close.assert_called_once()

    def test_slow_header_or_body_cannot_extend_receive_deadline(self):
        sock = mock.Mock()
        reader = fetcher._DeadlineReader(sock, deadline=10)
        with mock.patch.object(fetcher.time, "monotonic", return_value=11):
            with self.assertRaises(TimeoutError):
                reader.readinto(bytearray(10))
        sock.recv_into.assert_not_called()

    def test_short_network_reads_do_not_truncate_article(self):
        raw = article_html()
        item = response()
        item.read1 = mock.Mock(side_effect=[raw[:17], raw[17:53], raw[53:], b""])
        article, _ = self.fetch_with(item)
        self.assertEqual(len(article.blocks), 4)
        self.assertIn("기사 3", article.body)

    def test_incomplete_or_ambiguous_http_body_is_rejected(self):
        for headers in ({"Content-Length": "99999"}, {"Content-Length": "-1"},
                        {"Content-Length": "5", "Transfer-Encoding": "chunked"},
                        {"Transfer-Encoding": "identity"}):
            with self.subTest(headers=headers), self.assertRaisesRegex(
                    fetcher.FetchRejected, "INCOMPLETE_BODY|INVALID_LENGTH"):
                self.fetch_with(response(**headers))

    def test_content_encoding_decoding_and_size_fail_closed(self):
        cases = [
            (response(**{"Content-Type": "application/pdf"}), "UNSUPPORTED_CONTENT_TYPE"),
            (response(**{"Content-Encoding": "br"}), "UNSUPPORTED_ENCODING"),
            (response(b"\xff"), "DECODE_ERROR"),
            (response(**{"Content-Type": "text/html; charset=not-a-codec"}), "DECODE_ERROR"),
            (response(b"broken", **{"Content-Encoding": "gzip"}), "DECODE_ERROR"),
            (response(gzip.compress(b"a" * (fetcher.MAX_DECODED_BYTES + 1)),
                      **{"Content-Encoding": "gzip"}), "BODY_TOO_LARGE"),
            (response(**{"Content-Length": str(fetcher.MAX_COMPRESSED_BYTES + 1)}), "BODY_TOO_LARGE"),
            (response(b"a" * (fetcher.MAX_COMPRESSED_BYTES + 1)), "BODY_TOO_LARGE"),
        ]
        for item, reason in cases:
            with self.subTest(reason=reason), self.assertRaisesRegex(fetcher.FetchRejected, reason):
                self.fetch_with(item)

    def test_gzip_and_plain_text_keep_complete_body(self):
        article, _ = self.fetch_with(response(gzip.compress(article_html()), **{"Content-Encoding": "gzip"}))
        plain = article.body
        result, _ = self.fetch_with(response(plain.encode(), **{"Content-Type": "text/plain"}))
        self.assertEqual(result.body, plain)

    def test_retry_only_transient_statuses_and_timeout(self):
        for status in (429, 503):
            with mock.patch.object(fetcher.time, "sleep"):
                _, opened = self.fetch_with(response(status=status), response())
            self.assertEqual(opened.call_count, 2)
        with mock.patch.object(fetcher, "_fetch_once", side_effect=TimeoutError()) as once, \
                mock.patch.object(fetcher.time, "sleep"):
            with self.assertRaisesRegex(fetcher.FetchRejected, "TIMEOUT"):
                fetcher.fetch_article("https://example.com/a")
        self.assertEqual(once.call_count, 3)
        for status in (206, 401, 404):
            with mock.patch.object(fetcher, "_fetch_once",
                                   side_effect=fetcher.FetchRejected(f"HTTP_{status}")) as once:
                with self.assertRaisesRegex(fetcher.FetchRejected, f"HTTP_{status}"):
                    fetcher.fetch_article("https://example.com/a")
            once.assert_called_once()

    def test_redirect_loop_is_bounded(self):
        with self.assertRaisesRegex(fetcher.FetchRejected, "REDIRECT_LIMIT"):
            self.fetch_with(*(response(status=302, Location="/a") for _ in range(6)))


class TestArticleNormalization(unittest.TestCase):
    def test_cookie_container_void_tags_and_inline_markup_do_not_hide_article(self):
        html = article_html(extra="<div class='cookie-banner'><img src='x'><p>consent</p></div>")
        article = fetcher.normalize_html(html.decode(), "https://example.com/a")
        self.assertEqual(len(article.blocks), 4)
        self.assertNotIn("consent", article.body)
        self.assertEqual(article.title, "기사 & 제목")

    def test_oversized_body_is_rejected_instead_of_silently_shortened(self):
        with self.assertRaisesRegex(fetcher.FetchRejected, "BODY_TOO_LARGE"):
            fetcher.normalize_html(article_html(paragraphs=["a" * 10000] * 4).decode(),
                                   "https://example.com/a")

    def test_paywall_js_shell_short_or_empty_article_is_rejected(self):
        cases = [
            ("<article><p>subscribe to continue reading</p></article>", "PAYWALL"),
            ("<main></main><script src='/app.js'></script>", "JS_SHELL"),
            ("<article><p>short</p></article>", "BODY_TOO_SHORT"),
            ("<main></main>" + article_html().decode().replace("article", "div"), "BODY_TOO_SHORT"),
        ]
        for html, reason in cases:
            with self.subTest(reason=reason), self.assertRaisesRegex(fetcher.FetchRejected, reason):
                fetcher.normalize_html(html, "https://example.com/a")

    def test_hostile_html_nesting_and_block_count_are_bounded(self):
        for html, reason in (("<div>" * 257, "HTML_TOO_DEEP"),
                             ("<p>small</p>" * 2001, "BODY_TOO_LARGE")):
            with self.subTest(reason=reason), self.assertRaisesRegex(fetcher.FetchRejected, reason):
                fetcher.normalize_html(html, "https://example.com/a")

    def test_canonical_metadata_cannot_impersonate_another_publisher(self):
        for href, expected in (("/canonical", "https://example.com/canonical"),
                               ("https://other.example/a", "https://example.com/a"),
                               ("javascript:alert(1)", "https://example.com/a")):
            html = article_html(head=f'<link href="{href}" rel="canonical">').decode()
            article = fetcher.normalize_html(html, "https://example.com/a")
            self.assertEqual(article.canonical_url, expected)
