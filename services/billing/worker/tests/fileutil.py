"""Open-and-close helpers so tests never leave a file handle for the GC (and its ResourceWarning) to find."""
import json


def rd(path, mode="r", encoding=None):
    with open(path, mode, encoding=encoding) as f:
        return f.read()


def wr(data, path, mode="w", encoding=None):
    with open(path, mode, encoding=encoding) as f:
        return f.write(data)


def jload(path, mode="r", encoding=None):
    with open(path, mode, encoding=encoding) as f:
        return json.load(f)


def jdump(obj, path, mode="w", encoding=None, **kw):
    with open(path, mode, encoding=encoding) as f:
        json.dump(obj, f, **kw)
