#!/usr/bin/env python3
"""Validate and package only the public user-guide assets for GitHub Pages."""

import argparse
import hashlib
from html.parser import HTMLParser
import io
import json
from pathlib import Path
import re
import shutil
from urllib.parse import urlsplit
from urllib.request import Request, urlopen
import zipfile

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / 'docs/user-guide'
# Keep this explicit: internal reference/review files and unreviewed screens
# must never enter the public artifact, even when present beside the guide.
LOCAL_ASSETS = ('index.html', 'style.css', 'guide.js', 'favicon.svg')
SCREENSHOT_NAMES = tuple(f'screen-{name}.webp' for name in (
    'home', 'assistant', 'meetings', 'record', 'upload', 'meeting',
    'meeting-followup', 'accounts', 'account-overview', 'contacts', 'people',
    'projects', 'project-create', 'project-detail', 'documents', 'library',
    'library-attachments', 'research', 'news', 'tech', 'dictionary', 'billing',
    'settings', 'settings-import', 'admin', 'mobile-home', 'mobile-record',
    'settings-connectors', 'sharing', 'simulation',
    'account-meetings', 'account-events', 'account-insights',
    'account-projects', 'account-docs', 'account-kb',
))
ASSETS = LOCAL_ASSETS + SCREENSHOT_NAMES
ARCHIVE_URL = 'https://api.github.com/repos/Atom-oh/ttobak/releases/assets/730636766'
MAX_ARCHIVE_BYTES = 10 * 1024 * 1024
MAX_IMAGE_BYTES = 1024 * 1024
CSP = ("default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; "
       "base-uri 'none'; form-action 'none'; connect-src 'none'")


class GuideParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.references = []
        self.lang = None
        self.policies = []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == 'html':
            self.lang = values.get('lang')
        if tag == 'meta' and values.get('http-equiv', '').lower() == 'content-security-policy':
            self.policies.append(values.get('content'))
        if 'id' in values:
            identifier = values['id']
            if identifier in self.ids:
                raise ValueError(f'duplicate id: {identifier}')
            self.ids.add(identifier)
        for attribute in ('href', 'src'):
            if attribute in values:
                self.references.append(values[attribute])


def checked_file(path):
    if path.is_symlink() or not path.is_file() or not path.stat().st_size:
        raise ValueError(f'missing, empty or symlinked asset: {path.name}')


def checked_digest(value):
    if not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{64}', value):
        raise ValueError('media must have a SHA-256 identity')
    return value


def checked_size(value, maximum):
    if type(value) is not int or not 0 < value <= maximum:
        raise ValueError('media size is outside the allowed bounds')
    return value


def read_screenshots(archive_path=None):
    """Verify exact archive/entry identities before producing any public output."""
    receipt_path = SOURCE / 'screenshots.json'
    checked_file(receipt_path)
    if receipt_path.stat().st_size > 64 * 1024:
        raise ValueError('oversized screenshot receipt')
    receipt = json.loads(receipt_path.read_text(encoding='utf-8'))
    archive = receipt['archive']
    if archive['url'] != ARCHIVE_URL:
        raise ValueError('screenshot archive URL is not approved')
    archive_size = checked_size(archive['bytes'], MAX_ARCHIVE_BYTES)
    archive_hash = checked_digest(archive['sha256'])
    screens = receipt['screens']
    if not isinstance(screens, list) or len(screens) != len(SCREENSHOT_NAMES):
        raise ValueError('unexpected screenshot inventory')
    expected = {}
    for screen in screens:
        name = screen['file']
        if name not in SCREENSHOT_NAMES or name in expected:
            raise ValueError('unexpected or duplicate screenshot')
        checked_size(screen['bytes'], MAX_IMAGE_BYTES)
        checked_size(screen['width'], 3840)
        checked_size(screen['height'], 2160)
        checked_digest(screen['sha256'])
        expected[name] = screen
    if set(expected) != set(SCREENSHOT_NAMES):
        raise ValueError('incomplete screenshot inventory')
    if archive_path is not None:
        checked_file(archive_path)
        if archive_path.stat().st_size != archive_size:
            raise ValueError('screenshot archive size changed')
        raw = archive_path.read_bytes()
    else:
        request = Request(ARCHIVE_URL, headers={
            'User-Agent': 'TTOBAK-Guide-Packager', 'Accept-Encoding': 'identity',
            'Accept': 'application/octet-stream'})
        with urlopen(request, timeout=30) as response:
            raw = response.read(archive_size + 1)
    if len(raw) != archive_size or hashlib.sha256(raw).hexdigest() != archive_hash:
        raise ValueError('screenshot archive identity changed')
    media = {}
    with zipfile.ZipFile(io.BytesIO(raw)) as bundle:
        entries = bundle.infolist()
        if len(entries) != len(expected) or {entry.filename for entry in entries} != set(expected):
            raise ValueError('unexpected or duplicate archive entries')
        for entry in entries:
            screen = expected[entry.filename]
            if (entry.compress_type != zipfile.ZIP_STORED or entry.flag_bits & 1
                    or entry.file_size != screen['bytes']):
                raise ValueError('screenshot entry encoding or size changed')
            image = bundle.read(entry)
            if hashlib.sha256(image).hexdigest() != screen['sha256']:
                raise ValueError('screenshot image identity changed')
            # Lossless WebP: exact RIFF size, VP8L signature and bounded dimensions.
            if (len(image) < 25 or image[:4] != b'RIFF' or image[8:16] != b'WEBPVP8L'
                    or int.from_bytes(image[4:8], 'little') + 8 != len(image)
                    or image[20] != 0x2f):
                raise ValueError('screenshot must be a lossless WebP image')
            dimensions = int.from_bytes(image[21:25], 'little')
            width = (dimensions & 0x3fff) + 1
            height = ((dimensions >> 14) & 0x3fff) + 1
            if (width, height) != (screen['width'], screen['height']):
                raise ValueError('screenshot dimensions changed')
            media[entry.filename] = image
    return media


def build(destination, screenshot_archive=None):
    destination = destination.resolve()
    # Refuse existing trees before any network access or publication writes.
    if destination.exists():
        raise ValueError(f'output directory already exists: {destination}')
    parser = GuideParser()
    checked_file(SOURCE / 'index.html')
    parser.feed((SOURCE / 'index.html').read_text(encoding='utf-8'))
    parser.close()
    if parser.lang != 'ko':
        raise ValueError('the public guide must declare Korean')
    if parser.policies != [CSP]:
        raise ValueError('the public guide must retain its local-only content security policy')
    for reference in parser.references:
        url = urlsplit(reference)
        if url.scheme or url.netloc:
            raise ValueError(f'public guide must use local assets/links: {reference}')
        if url.path and url.path not in ASSETS:
            raise ValueError(f'unpublished path: {reference}')
        if url.fragment and url.fragment not in parser.ids:
            raise ValueError(f'missing anchor: {reference}')
    for name in ASSETS:
        if name not in SCREENSHOT_NAMES:
            checked_file(SOURCE / name)
    media = read_screenshots(screenshot_archive)
    destination.mkdir(parents=True)
    for name in ASSETS:
        if name in media:
            (destination / name).write_bytes(media[name])
        else:
            shutil.copyfile(SOURCE / name, destination / name)
    print(f'Validated {len(parser.ids)} anchors; published {len(ASSETS)} assets to {destination}')


if __name__ == '__main__':
    args = argparse.ArgumentParser(description=__doc__)
    args.add_argument('--output', required=True, type=Path)
    args.add_argument('--screenshot-archive', type=Path,
                      help='optional offline cache; identical hash checks apply')
    options = args.parse_args()
    build(options.output, options.screenshot_archive)
