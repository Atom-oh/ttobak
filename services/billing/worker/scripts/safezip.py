"""Extract an untrusted zip safely: entry checks first, then streamed writes with running byte limits.

extract(zip_path, dest, allowed_ext, ...) -> list of written basenames (flat, no subdirs).
Raises ZipRejected(reason) for: an absolute path, a '..' segment, a symlink, an encrypted entry, too many
entries, too large in total, a compression ratio over the limit, a disallowed extension, a duplicate basename (unless
flatten_dups) or a compressed stream that does not decompress.
__MACOSX/ and ._* entries are skipped. Never calls extractall; sizes in the headers are not trusted, the
decompressed stream is counted while it is written.
"""
import errno, lzma, os, stat, unicodedata, zipfile, zlib

CHUNK = 1 << 20
SUPPORTED = {zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED, zipfile.ZIP_BZIP2, zipfile.ZIP_LZMA}
MAX_NAME_BYTES = 255  # one path component on common filesystems


def show(name, n=80):
    """An entry name fit to echo in a reason: control/format characters dropped, at most n characters."""
    s = "".join(ch for ch in str(name) if unicodedata.category(ch)[0] != "C")
    return s if len(s) <= n else s[:n - 1] + "…"


class ZipRejected(Exception):
    def __init__(self, reason):
        super().__init__(reason)
        self.reason = reason


def _skipped(name):
    parts = name.split("/")
    return parts[0] == "__MACOSX" or parts[-1].startswith("._")


def check(infos, max_entries, max_total, max_ratio):
    """Archive-level limits from the headers (the stream is counted again when read)."""
    if len(infos) > max_entries:
        raise ZipRejected(f"zip 항목이 너무 많습니다: {len(infos)}개 (최대 {max_entries}개)")
    total = 0
    for zi in infos:
        base = show(os.path.basename(zi.filename.replace("\\", "/")))
        if zi.flag_bits & 0x1:
            raise ZipRejected(f"암호화된 항목은 허용되지 않습니다: {base}")
        if zi.compress_type not in SUPPORTED:
            raise ZipRejected(f"지원하지 않는 압축 방식입니다: {base}")
        total += zi.file_size
        if zi.file_size > max(zi.compress_size, 1) * max_ratio:
            raise ZipRejected(f"압축률이 비정상적으로 높습니다: {base}")
    if total > max_total:
        raise ZipRejected(f"압축 해제 크기가 너무 큽니다 (최대 {max_total // 2 ** 20}MB)")


def _entry_checks(zi):
    name = zi.filename.replace("\\", "/")
    if name.startswith("/") or (len(name) > 1 and name[1] == ":"):
        raise ZipRejected(f"절대 경로 항목은 허용되지 않습니다: {show(name)}")
    if ".." in name.split("/"):
        raise ZipRejected(f"상위 경로(..) 항목은 허용되지 않습니다: {show(name)}")
    if stat.S_ISLNK(zi.external_attr >> 16):
        raise ZipRejected(f"심볼릭 링크는 허용되지 않습니다: {show(name)}")
    if len(os.path.basename(name).encode("utf-8", "surrogatepass")) > MAX_NAME_BYTES:
        raise ZipRejected(f"파일 이름이 너무 깁니다: {show(os.path.basename(name))}")
    return name


def extract(zip_path, dest, allowed_ext, max_entries=200, max_total=4 * 2 ** 30, max_ratio=200, flatten_dups=False, origins=None):
    """flatten_dups=True: a repeated basename is written as '0001_<name>', '0002_<name>', ... instead of rejected.
    origins: optional dict, filled with {written basename: entry path inside the zip, '/'-separated}."""
    allowed = {e.lower() for e in allowed_ext}
    with zipfile.ZipFile(zip_path) as z:
        infos = z.infolist()
        if len(infos) > max_entries:  # every entry, skipped ones included
            raise ZipRejected(f"zip 항목이 너무 많습니다: {len(infos)}개 (최대 {max_entries}개)")
        files = []
        for zi in infos:
            name = _entry_checks(zi)
            if zi.is_dir() or _skipped(name):
                continue
            files.append((zi, os.path.basename(name)))
        check([zi for zi, _ in files], max_entries, max_total, max_ratio)
        seen, renamed = set(), []
        for zi, base in files:
            if os.path.splitext(base)[1].lower() not in allowed:
                raise ZipRejected(f"허용되지 않은 파일: {show(base)}")
            orig = base
            if base.lower() in seen:
                if not flatten_dups:
                    raise ZipRejected(f"같은 이름의 파일이 중복됩니다: {show(base)}")
                n = 1
                while f"{n:04d}_{orig}".lower() in seen:
                    n += 1
                base = f"{n:04d}_{orig}"
                if len(base.encode("utf-8", "surrogatepass")) > MAX_NAME_BYTES:
                    raise ZipRejected(f"파일 이름이 너무 깁니다: {show(orig)}")
            seen.add(base.lower())
            renamed.append((zi, base))
            if origins is not None:
                origins[base] = zi.filename.replace("\\", "/")
        files = renamed
        os.makedirs(dest, exist_ok=True)
        written, total = [], 0
        for zi, base in files:
            out = os.path.join(dest, base)
            limit = max(zi.compress_size, 1) * max_ratio
            n = 0
            try:
                with z.open(zi) as src, open(out, "xb") as dst:
                    while True:
                        buf = src.read(CHUNK)
                        if not buf:
                            break
                        n += len(buf)
                        total += len(buf)
                        if total > max_total:
                            raise ZipRejected(f"압축 해제 크기가 너무 큽니다 (최대 {max_total // 2 ** 20}MB)")
                        if n > limit:
                            raise ZipRejected(f"압축률이 비정상적으로 높습니다: {show(base)}")
                        dst.write(buf)
            except (zlib.error, lzma.LZMAError):  # a corrupt deflate / LZMA stream behind a readable directory
                if os.path.exists(out):
                    os.remove(out)
                raise ZipRejected(f"압축을 풀 수 없는 항목입니다: {show(base)}")
            except OSError as e:
                if os.path.exists(out):
                    os.remove(out)
                if e.errno in (errno.ENAMETOOLONG, errno.EILSEQ, errno.EINVAL):
                    raise ZipRejected(f"저장할 수 없는 파일 이름입니다: {show(base)}")
                if e.errno is None:  # the bz2 decompressor ('Invalid data stream'); file system errors carry an errno
                    raise ZipRejected(f"압축을 풀 수 없는 항목입니다: {show(base)}")
                raise
            except BaseException:
                if os.path.exists(out):
                    os.remove(out)
                raise
            written.append(base)
    return written
