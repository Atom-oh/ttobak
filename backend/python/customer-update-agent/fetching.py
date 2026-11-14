"""The one import point for backend/python/crawler/article_fetcher.py.

The image copies article_fetcher.py (and its news_pipeline.py import) next to
these modules, so the plain import succeeds there. Local and CI tests fall back
to the sibling crawler directory, appended so it never shadows this package's
own modules. Do not fork the fetcher.
"""

import os
import sys

_module = None


def module():
    global _module
    if _module is None:
        try:
            import article_fetcher
        except ImportError:
            crawler = os.path.normpath(
                os.path.join(os.path.dirname(os.path.abspath(__file__)), os.pardir, "crawler"))
            if crawler not in sys.path:
                sys.path.append(crawler)
            import article_fetcher
        _module = article_fetcher
    return _module
