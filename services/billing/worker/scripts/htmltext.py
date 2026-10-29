"""Linear, size-capped text of an untrusted COT360 page, shared by every reader of it (build_data, cot360_check).
html2json.py reads the embedded tables with its own linear scanner and the same cap."""
import os

MAX_HTML = 64 * 2 ** 20  # as html2json.py: a larger page yields no figures (spend_check reports it)


def strip_blocks(h):
    """The page without <script>/<style> elements, in one linear pass (the page is untrusted). The next opener of
    each kind is searched once and reused until passed. An unclosed element drops the rest of the page."""
    low, out, i = h.lower(), [], 0
    nxt = {"<script": low.find("<script"), "<style": low.find("<style")}
    while True:
        for k in nxt:
            if 0 <= nxt[k] < i:
                nxt[k] = low.find(k, i)
        hits = [p for p in nxt.values() if p >= 0]
        if not hits:
            out.append(h[i:])
            return "".join(out)
        s = min(hits)
        out.append(h[i:s])
        close = "</script>" if low.startswith("<script", s) else "</style>"
        c = low.find(close, s)
        if c < 0:
            return "".join(out)
        i = c + len(close)


def strip_tags(t, joiner=" "):
    """Markup replaced by joiner, in one linear pass: a tag is "<" up to the next ">" (no ">" left: plain text)."""
    out, i = [], 0
    while True:
        lt = t.find("<", i)
        if lt < 0:
            out.append(t[i:])
            return "".join(out)
        gt = t.find(">", lt + 1)
        if gt < 0:
            out.append(t[i:])
            return "".join(out)
        out.append(t[i:lt])
        out.append(joiner)
        i = gt + 1


def page_text(path, joiner=" "):
    """The page's text without scripts, styles and markup, or None when the page exceeds MAX_HTML."""
    if os.path.getsize(path) > MAX_HTML:
        return None
    with open(path, encoding="utf-8") as fh:
        return strip_tags(strip_blocks(fh.read()), joiner)
