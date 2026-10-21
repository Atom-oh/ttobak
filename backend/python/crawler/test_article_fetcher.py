"""Network, completeness, and extraction boundaries for the staged news reader."""

import gzip
import io
import socket
import subprocess
import tempfile
from pathlib import Path
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


def public_resolver(host, port, deadline):
    return [(socket.AF_INET, socket.SOCK_STREAM, socket.IPPROTO_TCP, "", ("93.184.216.34", port))]


class TestBoundedDNS(unittest.TestCase):
    def test_temporary_resolver_failure_is_distinct_and_retryable(self):
        with mock.patch.object(fetcher.subprocess, "run",
                               side_effect=subprocess.CalledProcessError(75, ["python"])):
            with self.assertRaisesRegex(fetcher.FetchRejected, "DNS_TEMPORARY"):
                fetcher._resolve_addresses("example.com", 443, fetcher.time.monotonic() + 5)
        article = mock.Mock()
        with mock.patch.object(fetcher, "_fetch_once",
                               side_effect=[fetcher.FetchRejected("DNS_TEMPORARY"), article]) as once, \
                mock.patch.object(fetcher.time, "sleep"):
            self.assertIs(fetcher.fetch_article("https://example.com/a"), article)
        self.assertEqual(once.call_count, 2)

    def test_child_retains_only_required_dynamic_loader_path(self):
        environment = {"LD_LIBRARY_PATH": "/runtime/lib", "AWS_SECRET_ACCESS_KEY": "fixture",
                       "PYTHONPATH": "/untrusted", "LD_PRELOAD": "/untrusted.so"}
        with mock.patch.dict(fetcher.os.environ, environment, clear=True), \
                mock.patch.object(fetcher.subprocess, "run",
                                  return_value=mock.Mock(stdout=b"[]")) as run:
            fetcher._resolve_addresses("example.com", 443, fetcher.time.monotonic() + 5)
        self.assertEqual(run.call_args.kwargs["env"], {"LD_LIBRARY_PATH": "/runtime/lib"})
        self.assertEqual(run.call_args.args[0][1:3], ["-I", "-S"])

    def test_numeric_resolution_in_isolated_child_returns_socket_addresses(self):
        answers = fetcher._resolve_addresses("93.184.216.34", 443,
                                             fetcher.time.monotonic() + 5)
        self.assertTrue(answers)
        self.assertTrue(all(answer[4][:2] == ["93.184.216.34", 443] for answer in answers))

    def test_stalled_resolver_child_is_killed_and_reaped_before_return(self):
        with tempfile.TemporaryDirectory() as folder:
            pid_file = Path(folder) / "resolver.pid"
            script = ("import os, time\n"
                      f"open({str(pid_file)!r}, 'w').write(str(os.getpid()))\n"
                      "time.sleep(60)\n")
            with mock.patch.object(fetcher, "_DNS_SCRIPT", script), \
                    mock.patch.object(fetcher, "DNS_TIMEOUT_SECONDS", 0.5):
                start = fetcher.time.monotonic()
                with self.assertRaises(TimeoutError):
                    fetcher._resolve_addresses("example.com", 443, start + 10)
            self.assertLess(fetcher.time.monotonic() - start, 3)
            pid = int(pid_file.read_text())
            with self.assertRaises(ChildProcessError):
                fetcher.subprocess.os.waitpid(pid, fetcher.subprocess.os.WNOHANG)
            with self.assertRaises(ProcessLookupError):
                fetcher.subprocess.os.kill(pid, 0)

    def test_exhausted_budget_never_starts_resolver(self):
        with mock.patch.object(fetcher.subprocess, "run") as run:
            with self.assertRaises(TimeoutError):
                fetcher._resolve_addresses("example.com", 443, 0)
        run.assert_not_called()

    def test_dns_failure_has_bounded_reason(self):
        with mock.patch.object(fetcher.subprocess, "run",
                               side_effect=subprocess.CalledProcessError(1, "resolver")):
            with self.assertRaisesRegex(fetcher.FetchRejected, "^DNS_ERROR$"):
                fetcher._resolve_addresses("example.com", 443, fetcher.time.monotonic() + 5)


class TestArticleFetch(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch.object(fetcher, "_resolve_addresses", side_effect=public_resolver)
        self.resolve = patcher.start()
        self.addCleanup(patcher.stop)

    def fetch_with(self, *responses, url="https://example.com/a"):
        pairs = [(mock.Mock(), item) for item in responses]
        with mock.patch.object(fetcher, "_open_response", side_effect=pairs) as opened:
            article = fetcher.fetch_article(url)
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

        def resolve(host, port, deadline):
            ip = "127.0.0.1" if host == "127.0.0.1" else "93.184.216.34"
            return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (ip, port))]

        connection = mock.Mock()
        self.resolve.side_effect = resolve
        with mock.patch.object(fetcher, "_open_response", return_value=(connection, first)) as opened:
            with self.assertRaisesRegex(fetcher.FetchRejected, "BLOCKED_HOST"):
                fetcher.fetch_article("https://example.com/a")
        opened.assert_called_once()
        connection.close.assert_called_once()

    def test_https_only_rejects_downgrade_before_connecting_to_redirect(self):
        first = response(status=302, Location="http://example.com/insecure")
        connection = mock.Mock()
        with mock.patch.object(fetcher, "_open_response", return_value=(connection, first)) as opened:
            with self.assertRaisesRegex(fetcher.FetchRejected, "INSECURE_REDIRECT"):
                fetcher.fetch_article("https://example.com/a", https_only=True)
        opened.assert_called_once()
        connection.close.assert_called_once()
        self.resolve.assert_called_once()

    def test_rejects_private_mixed_empty_and_transition_dns_answers(self):
        for addresses in ([], ["127.0.0.1"], ["169.254.169.254"], ["10.1.1.1"],
                          ["100.64.0.1"], ["224.0.0.1"], ["93.184.216.34", "10.0.0.1"],
                          ["::1"], ["64:ff9b::a00:1"], ["::ffff:127.0.0.1"],
                          ["2002:7f00:1::"], ["2001:0000:4136:e378:8000:63bf:3fff:fdd2"]):
            def resolve(host, port, deadline):
                return [(socket.AF_INET6 if ":" in ip else socket.AF_INET,
                         socket.SOCK_STREAM, 6, "", (ip, port)) for ip in addresses]
            self.resolve.side_effect = resolve
            with self.subTest(addresses=addresses), self.assertRaisesRegex(
                    fetcher.FetchRejected, "BLOCKED_HOST"):
                fetcher.validate_public_url("https://example.com/a")

    def test_rejects_credential_urls_non_web_ports_and_bad_schemes(self):
        for url in ("ftp://example.com/a", "https://user@example.com/a",
                    "http://example.com:8080/a", "https://example.com/\r\nheader"):
            with self.subTest(url=url), self.assertRaisesRegex(fetcher.FetchRejected, "BLOCKED_HOST"):
                fetcher.validate_public_url(url)

    def test_numeric_connection_is_pinned_and_tls_uses_hostname(self):
        parsed, addresses = fetcher._target("https://example.com/a", fetcher.time.monotonic() + 40)
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
        self.assertIs(returned.sock.sock, tls_socket)
        self.assertEqual(context.minimum_version, fetcher.ssl.TLSVersion.TLSv1_2)

    def test_tls_failure_closes_socket_without_retrying_an_unchecked_host(self):
        parsed, addresses = fetcher._target("https://example.com/a", fetcher.time.monotonic() + 40)
        sock, context = mock.Mock(), mock.Mock()
        context.wrap_socket.side_effect = OSError("certificate failure")
        with mock.patch.object(fetcher.socket, "socket", return_value=sock), \
                mock.patch.object(fetcher.ssl, "create_default_context", return_value=context):
            with self.assertRaises(OSError):
                fetcher._open_response(parsed, addresses, fetcher.time.monotonic() + 40)
        sock.close.assert_called_once()

    def test_unreachable_ipv6_falls_back_to_validated_ipv4(self):
        addresses = [
            (socket.AF_INET6, socket.SOCK_STREAM, 6, "", ("2606:4700:4700::1111", 443, 0, 0)),
            public_resolver("example.com", 443, 0)[0],
        ]
        self.resolve.side_effect = None
        self.resolve.return_value = addresses
        parsed, checked = fetcher._target("https://example.com/a", fetcher.time.monotonic() + 40)
        first, second, tls, connection, http_response = (mock.Mock() for _ in range(5))
        first.connect.side_effect = OSError("IPv6 unreachable")
        context = mock.Mock()
        context.wrap_socket.return_value = tls
        with mock.patch.object(fetcher.socket, "socket", side_effect=[first, second]), \
                mock.patch.object(fetcher.ssl, "create_default_context", return_value=context), \
                mock.patch.object(fetcher.http.client, "HTTPConnection", return_value=connection), \
                mock.patch.object(fetcher.http.client, "HTTPResponse", return_value=http_response):
            result, _ = fetcher._open_response(parsed, checked, fetcher.time.monotonic() + 40)
        first.connect.assert_called_once_with(addresses[0][4])
        first.close.assert_called_once()
        second.connect.assert_called_once_with(addresses[1][4])
        self.assertIs(result.sock.sock, tls)
        context.wrap_socket.assert_called_once_with(second, server_hostname="example.com")

    def test_address_fallback_stops_at_overall_deadline(self):
        parsed, addresses = fetcher._target("https://example.com/a", fetcher.time.monotonic() + 40)
        with mock.patch.object(fetcher, "_open_address", side_effect=OSError("unreachable")) as opened, \
                mock.patch.object(fetcher.time, "monotonic", return_value=11):
            with self.assertRaises(TimeoutError):
                fetcher._open_response(parsed, addresses * 3, deadline=10)
        opened.assert_called_once()

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

    def test_valueless_meta_content_and_duplicate_transfer_encoding(self):
        raw = article_html(head='<meta http-equiv="content-type" content>')
        article, _ = self.fetch_with(response(raw))
        self.assertGreater(len(article.body), 800)
        item = response()
        item.headers["Transfer-Encoding"] = "chunked"
        item.headers["Transfer-Encoding"] = "gzip"
        with self.assertRaisesRegex(fetcher.FetchRejected, "INVALID_LENGTH"):
            self.fetch_with(item)

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

    def test_missing_malformed_or_duplicate_mime_is_rejected(self):
        items = [response(), response(**{"Content-Type": "broken"})]
        del items[0].headers["Content-Type"]
        duplicate = response()
        duplicate.headers["Content-Type"] = "text/plain"
        for item in items + [duplicate]:
            with self.assertRaisesRegex(fetcher.FetchRejected, "UNSUPPORTED_CONTENT_TYPE"):
                self.fetch_with(item)

    def test_korean_meta_charset_is_respected_and_conflicts_are_rejected(self):
        for charset in ("euc-kr", "cp949"):
            raw = article_html(head=f'<meta charset="{charset}">').decode().encode(charset)
            article, _ = self.fetch_with(response(raw, **{"Content-Type": "text/html"}))
            self.assertIn("사실과 근거", article.body)
            with self.assertRaisesRegex(fetcher.FetchRejected, "DECODE_ERROR"):
                self.fetch_with(response(raw))  # UTF-8 header conflicts with meta.
        with self.assertRaisesRegex(fetcher.FetchRejected, "UNSUPPORTED_CHARSET"):
            self.fetch_with(response(article_html(),
                                     **{"Content-Type": "text/html; charset=iso-8859-1"}))

    def test_plain_text_identity_drops_tracking_and_fragment(self):
        raw = "\n\n".join(["내용입니다. " * 60] * 4).encode()
        article, _ = self.fetch_with(response(raw, **{"Content-Type": "text/plain"}),
                                    url="https://example.com?utm_source=a#top")
        self.assertEqual(article.canonical_url, "https://example.com/")

    def test_duplicate_http_charset_parameters_are_rejected(self):
        for value in ("utf-8; charset=euc-kr", "euc-kr; charset=utf-8",
                      "utf-8; charset=utf-8", ""):
            with self.subTest(value=value), self.assertRaisesRegex(fetcher.FetchRejected, "DECODE_ERROR"):
                self.fetch_with(response(**{"Content-Type": f"text/html; charset={value}"}))

    def test_malformed_redirect_has_bounded_rejection(self):
        with self.assertRaisesRegex(fetcher.FetchRejected, "^INVALID_REDIRECT$"):
            self.fetch_with(response(status=302, Location="https://[invalid/a"))

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
    def test_wrapped_plain_text_paywall_markers_are_rejected(self):
        for separator in ("\n", "\t", "   ", "\n\n"):
            blocks = ["A complete article paragraph " * 15] * 4
            blocks.append("subscribe" + separator + "to continue reading")
            with self.subTest(separator=separator), self.assertRaisesRegex(
                    fetcher.FetchRejected, "PAYWALL"):
                fetcher._quality_body(blocks)

    def test_duplicate_content_encoding_is_rejected(self):
        for encodings in (("identity", "gzip"), ("gzip", "identity"), ("identity", "identity")):
            headers = Message()
            for encoding in encodings:
                headers["Content-Encoding"] = encoding
            with self.assertRaisesRegex(fetcher.FetchRejected, "UNSUPPORTED_ENCODING"):
                fetcher._decode_body(b"content", headers)

    def test_canonical_metadata_requires_same_normalized_origin(self):
        for fetched, href, expected in (
                ("https://bücher.example/a", "https://xn--bcher-kva.example/b",
                 "https://xn--bcher-kva.example/b"),
                ("https://example.com./a", "https://example.com/b", "https://example.com/b"),
                ("https://example.com/a", "http://example.com/b", "https://example.com/a"),
                ("https://example.com/a", "https://example.com:444/b", "https://example.com/a")):
            html = article_html(head=f'<link rel="canonical" href="{href}">').decode()
            with self.subTest(fetched=fetched, href=href):
                self.assertEqual(fetcher.normalize_html(html, fetched).canonical_url, expected)

    def test_visible_paywall_notice_outside_blocks_rejects_full_excerpt(self):
        for extra in ("<div>subscribe to continue reading</div>",
                      "<p>subscribe to continue reading</p>",
                      "<div>subscribe <b>to continue</b> reading</div>",
                      "<div>subscribe</div><div>to continue reading</div>",
                      "<div>subscribe<br>to continue reading</div>",
                      "<div><span>sub</span><span>scribe</span> to continue reading</div>"):
            with self.subTest(extra=extra), self.assertRaisesRegex(fetcher.FetchRejected, "PAYWALL"):
                fetcher.normalize_html(article_html(extra=extra).decode(), "https://example.com/a")
        html = article_html(extra="<div hidden>subscribe to continue reading</div>").decode()
        self.assertIn("기사 3", fetcher.normalize_html(html, "https://example.com/a").body)

    def test_nested_list_restores_outer_trailing_correction(self):
        nested = ("<ul><li>Initial claim<ul><li>Supporting detail</li></ul>"
                  "Trailing correction</li></ul>")
        html = article_html().decode().replace("</article>", nested + "</article>")
        body = fetcher.normalize_html(html, "https://example.com/a").body
        self.assertIn("Initial claim\n\nSupporting detail\n\nTrailing correction", body)

    def test_boilerplate_containers_still_participate_in_paywall_detection(self):
        for tag in ("form", "aside", "header", "footer", "nav"):
            for notice in ("subscribe to continue reading", "로그인 후 이용"):
                with self.subTest(tag=tag, notice=notice):
                    extra = f"<{tag}><div>{notice}</div></{tag}>"
                    with self.assertRaisesRegex(fetcher.FetchRejected, "PAYWALL"):
                        fetcher.normalize_html(article_html(extra=extra).decode(),
                                               "https://example.com/a")
        for attr in ("hidden", 'aria-hidden="true"', 'class="cookie-banner"'):
            extra = f"<form {attr}><div>subscribe to continue reading</div></form>"
            self.assertIn("기사 3", fetcher.normalize_html(
                article_html(extra=extra).decode(), "https://example.com/a").body)

    def test_direct_scoped_text_and_container_corrections_are_preserved(self):
        for container in ("div", "section", "td", "figcaption", "pre"):
            correction = (f"Direct introduction<{container}>Correction before "
                          "<span>inline detail</span><p>Nested paragraph</p>"
                          f"Correction after</{container}>Direct closing statement")
            html = article_html().decode().replace("</article>", correction + "</article>")
            with self.subTest(container=container):
                body = fetcher.normalize_html(html, "https://example.com/a").body
                for text in ("Direct introduction", "Correction before inline detail",
                             "Nested paragraph", "Correction after", "Direct closing statement"):
                    self.assertIn(text, body)
                positions = [body.index(text) for text in ("Direct introduction",
                             "Correction before", "Nested paragraph",
                             "Correction after", "Direct closing statement")]
                self.assertEqual(positions, sorted(positions))

    def test_invalid_canonical_keeps_normalized_fallback_identity(self):
        for href in ("https://other.example/a", "javascript:alert(1)", "https://[invalid"):
            html = article_html(head=f'<link href="{href}" rel="canonical">').decode()
            article = fetcher.normalize_html(html, "https://example.com?utm_source=a#top")
            self.assertEqual(article.canonical_url, "https://example.com/")

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
