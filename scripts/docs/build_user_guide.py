#!/usr/bin/env python3
"""Validate and package only the public user-guide assets for GitHub Pages."""

import argparse
from html.parser import HTMLParser
from pathlib import Path
import shutil
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / 'docs/user-guide'
ASSETS = ('index.html', 'style.css', 'guide.js')
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


def build(destination):
    parser = GuideParser()
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
        asset = SOURCE / name
        if asset.is_symlink() or not asset.is_file() or not asset.stat().st_size:
            raise ValueError(f'missing, empty or symlinked asset: {name}')
    destination = destination.resolve()
    # Never delete/overwrite an existing tree, especially a repository directory.
    if destination.exists():
        raise ValueError(f'output directory already exists: {destination}')
    destination.mkdir(parents=True)
    for name in ASSETS:
        shutil.copyfile(SOURCE / name, destination / name)
    print(f'Validated {len(parser.ids)} anchors; published {len(ASSETS)} assets to {destination}')


if __name__ == '__main__':
    args = argparse.ArgumentParser(description=__doc__)
    args.add_argument('--output', required=True, type=Path)
    build(args.parse_args().output)
