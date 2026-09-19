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
        (self.source / 'private.md').write_text('Must never be published.')
        self.source_patch = patch.object(guide, 'SOURCE', self.source)
        self.source_patch.start()
        self.addCleanup(self.source_patch.stop)

    def test_only_allowlisted_assets_are_copied_byte_for_byte(self):
        guide.build(self.output)
        self.assertEqual({p.name for p in self.output.iterdir()}, set(guide.ASSETS))
        for name in guide.ASSETS:
            self.assertEqual((self.source / name).read_bytes(), (self.output / name).read_bytes())

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
