"""Pinned screenshot media must not widen the public publication boundary."""

import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import build_user_guide as guide


class ScreenshotMediaTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / 'source'
        self.source.mkdir()
        self.output = self.root / 'public'
        self.image = b'RIFF\x16\x00\x00\x00WEBPVP8L\x09\x00\x00\x00\x2f\x00\x00\x00\x00\x00\x00\x00\x00\x00'
        self.name = 'screen-home.webp'
        self.url = 'https://api.github.com/repos/Atom-oh/ttobak/releases/assets/730636766'
        self.archive = self.make_archive([(self.name, self.image)])
        self.receipt = {
            'archive': {'url': self.url, 'bytes': len(self.archive),
                        'sha256': hashlib.sha256(self.archive).hexdigest()},
            'screens': [{'file': self.name, 'width': 1, 'height': 1,
                         'bytes': len(self.image),
                         'sha256': hashlib.sha256(self.image).hexdigest()}],
        }
        self.html = (
            '<html lang="ko"><head><meta http-equiv="Content-Security-Policy" '
            f'content="{guide.CSP}"></head><body id="start">'
            f'<img src="{self.name}" alt="Actual UI with example data">'
            '<script src="guide.js"></script></body></html>'
        )
        (self.source / 'index.html').write_text(self.html)
        for name in ('style.css', 'guide.js', 'favicon.svg'):
            (self.source / name).write_bytes(b'reviewed local asset')
        (self.source / 'private.md').write_text('private')
        self.save_receipt()
        for target, value in [('SOURCE', self.source), ('SCREENSHOT_NAMES', (self.name,)),
                              ('ASSETS', ('index.html', 'style.css', 'guide.js', 'favicon.svg', self.name))]:
            context = patch.object(guide, target, value, create=True)
            context.start()
            self.addCleanup(context.stop)

    def make_archive(self, entries):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, 'w', compression=zipfile.ZIP_STORED) as archive:
            for name, body in entries:
                archive.writestr(name, body)
        return stream.getvalue()

    def save_receipt(self):
        (self.source / 'screenshots.json').write_text(json.dumps(self.receipt))

    def save_archive(self, raw=None):
        path = self.root / 'guide-ui.zip'
        path.write_bytes(self.archive if raw is None else raw)
        return path

    def test_verified_archive_publishes_only_reviewed_media_and_local_files(self):
        guide.build(self.output, self.save_archive())
        self.assertEqual({p.name for p in self.output.iterdir()}, set(guide.ASSETS))
        self.assertEqual((self.output / self.name).read_bytes(), self.image)
        self.assertFalse((self.output / 'screenshots.json').exists())
        self.assertFalse((self.output / 'private.md').exists())

    def test_automatic_fetch_is_bounded_and_identifies_the_reviewed_url(self):
        with patch.object(guide, 'urlopen', return_value=io.BytesIO(self.archive)) as fetch:
            guide.build(self.output)
        request = fetch.call_args.args[0]
        self.assertEqual(request.full_url, self.url)
        self.assertEqual(request.get_header('Accept'), 'application/octet-stream')
        self.assertEqual(fetch.call_args.kwargs['timeout'], 30)

    def test_changed_archive_or_image_hash_never_creates_output(self):
        for changed in ('archive', 'image'):
            with self.subTest(changed=changed):
                receipt = json.loads(json.dumps(self.receipt))
                target = receipt['archive'] if changed == 'archive' else receipt['screens'][0]
                target['sha256'] = '0' * 64
                (self.source / 'screenshots.json').write_text(json.dumps(receipt))
                with self.assertRaises(ValueError):
                    guide.build(self.output, self.save_archive())
                self.assertFalse(self.output.exists())

    def test_duplicate_unexpected_and_traversal_archive_entries_are_rejected(self):
        cases = [[(self.name, self.image), (self.name, self.image)],
                 [(self.name, self.image), ('private.md', b'private')],
                 [('../screen-home.webp', self.image)]]
        for entries in cases:
            with self.subTest(entries=[name for name, _ in entries]):
                import warnings
                with warnings.catch_warnings():
                    warnings.simplefilter('ignore', UserWarning)
                    raw = self.make_archive(entries)
                self.receipt['archive'].update(bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest())
                self.save_receipt()
                with self.assertRaises(ValueError):
                    guide.build(self.output, self.save_archive(raw))
                self.assertFalse(self.output.exists())

    def test_unapproved_url_and_oversized_receipt_are_rejected_before_fetch(self):
        cases = [
            ('url', 'https://example.com/guide-ui.zip'),
            ('url', self.url + '?token=example'),
            ('bytes', 20 * 1024 * 1024),
        ]
        for key, value in cases:
            with self.subTest(key=key, value=value):
                receipt = json.loads(json.dumps(self.receipt))
                receipt['archive'][key] = value
                (self.source / 'screenshots.json').write_text(json.dumps(receipt))
                with patch.object(guide, 'urlopen') as fetch:
                    with self.assertRaises(ValueError):
                        guide.build(self.output)
                    fetch.assert_not_called()
                self.assertFalse(self.output.exists())

    def test_wrong_dimensions_are_rejected(self):
        self.receipt['screens'][0]['width'] = 2
        self.save_receipt()
        with self.assertRaises(ValueError):
            guide.build(self.output, self.save_archive())
        self.assertFalse(self.output.exists())

    def test_invalid_image_header_is_rejected_even_with_matching_hashes(self):
        raw_image = b'not a WebP image'
        raw = self.make_archive([(self.name, raw_image)])
        self.receipt['archive'].update(bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest())
        self.receipt['screens'][0].update(bytes=len(raw_image),
                                        sha256=hashlib.sha256(raw_image).hexdigest())
        self.save_receipt()
        with self.assertRaises(ValueError):
            guide.build(self.output, self.save_archive(raw))
        self.assertFalse(self.output.exists())

    def test_symlinked_receipt_and_cache_are_rejected(self):
        receipt = self.source / 'screenshots.json'
        actual = self.source / 'receipt.backup'
        receipt.rename(actual)
        receipt.symlink_to(actual)
        with self.assertRaises(ValueError):
            guide.build(self.output, self.save_archive())
        self.assertFalse(self.output.exists())
        receipt.unlink()
        actual.rename(receipt)
        linked = self.root / 'linked.zip'
        linked.symlink_to(self.save_archive())
        with self.assertRaises(ValueError):
            guide.build(self.output, linked)
        self.assertFalse(self.output.exists())

    def test_existing_output_never_fetches_or_overwrites(self):
        self.output.mkdir()
        sentinel = self.output / 'keep'
        sentinel.write_bytes(b'keep')
        with patch.object(guide, 'urlopen') as fetch:
            with self.assertRaises(ValueError):
                guide.build(self.output)
            fetch.assert_not_called()
        self.assertEqual(sentinel.read_bytes(), b'keep')


if __name__ == '__main__':
    unittest.main()
