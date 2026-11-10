"""Public packaging must reject accidental publication outside the guide."""

import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import build_user_guide as guide


class PublicGuideTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / 'source'
        self.source.mkdir()
        self.output = self.root / 'public'
        self.html = (
            '<html lang="ko"><head><meta http-equiv="Content-Security-Policy" '
            f'content="{guide.CSP}"><link href="style.css"></head>'
            '<body id="start"><a href="#start">Start</a>'
            '<script src="guide.js"></script></body></html>'
        )
        (self.source / 'index.html').write_text(self.html)
        (self.source / 'style.css').write_text('body { color: black; }')
        (self.source / 'guide.js').write_text('"use strict";')
        (self.source / 'favicon.svg').write_text('<svg xmlns="http://www.w3.org/2000/svg"/>')
        (self.source / 'private.md').write_text('Must never be published.')
        self.media = {name: b'Verified screenshot fixture' for name in guide.SCREENSHOT_NAMES}
        self.media_patch = patch.object(guide, 'read_screenshots', return_value=self.media)
        self.media_patch.start()
        self.addCleanup(self.media_patch.stop)
        (self.source / 'unreviewed.webp').write_bytes(b'Unreviewed image.')
        self.source_patch = patch.object(guide, 'SOURCE', self.source)
        self.source_patch.start()
        self.addCleanup(self.source_patch.stop)

    def test_only_allowlisted_assets_are_copied_byte_for_byte(self):
        guide.build(self.output)
        self.assertEqual({p.name for p in self.output.iterdir()}, set(guide.ASSETS))
        for name in guide.ASSETS:
            expected = self.media[name] if name in self.media else (self.source / name).read_bytes()
            self.assertEqual(expected, (self.output / name).read_bytes())

    def test_invalid_documents_never_create_public_output(self):
        bad_documents = [
            self.html.replace('style.css', 'https://example.com/style.css'),
            self.html.replace('style.css', '//example.com/style.css'),
            self.html.replace('style.css', '../private.md'),
            self.html.replace('#start', '#missing'),
            self.html.replace('<a href=', '<a id="start" href='),
            self.html.replace('lang="ko"', 'lang="en"'),
            self.html.replace(guide.CSP, "default-src *"),
            self.html.replace('Content-Security-Policy', 'Description'),
        ]
        for document in bad_documents:
            with self.subTest(document=document):
                (self.source / 'index.html').write_text(document)
                with self.assertRaises(ValueError):
                    guide.build(self.output)
                self.assertFalse(self.output.exists())

    def test_symlinked_asset_is_not_published(self):
        asset = self.source / 'guide.js'
        asset.unlink()
        asset.symlink_to(self.source / 'private.md')
        with self.assertRaises(ValueError):
            guide.build(self.output)
        self.assertFalse(self.output.exists())

    def test_reviewed_screen_reference_is_published(self):
        (self.source / 'index.html').write_text(
            self.html.replace('</body>', '<img src="screen-home.webp" alt="Actual UI with example data"></body>')
        )
        guide.build(self.output)
        self.assertEqual(
            self.media['screen-home.webp'],
            (self.output / 'screen-home.webp').read_bytes(),
        )

    def test_unreviewed_image_reference_is_rejected(self):
        (self.source / 'index.html').write_text(
            self.html.replace('</body>', '<img src="unreviewed.webp" alt="Unreviewed"></body>')
        )
        with self.assertRaises(ValueError):
            guide.build(self.output)
        self.assertFalse(self.output.exists())

    def test_local_image_override_cannot_replace_verified_media(self):
        asset = self.source / 'screen-home.webp'
        asset.symlink_to(self.source / 'private.md')
        guide.build(self.output)
        self.assertEqual((self.output / 'screen-home.webp').read_bytes(),
                         self.media['screen-home.webp'])

    def test_existing_destination_is_preserved(self):
        self.output.mkdir()
        sentinel = self.output / 'keep.txt'
        sentinel.write_bytes(b'existing content')
        with self.assertRaises(ValueError):
            guide.build(self.output)
        self.assertEqual(sentinel.read_bytes(), b'existing content')
        self.assertEqual(list(self.output.iterdir()), [sentinel])


if __name__ == '__main__':
    unittest.main()
