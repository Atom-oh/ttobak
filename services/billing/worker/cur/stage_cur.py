"""Stage one uploaded CUR zip (untrusted) for one account and normalize it to Parquet.

Usage: python3 -I stage_cur.py <zip> <work_dir> <acct> [prev_dir]
Extracts to <work>/cur_raw, renames data files to generated names in <work>/cur_in (both removed afterwards), writes <work>/cur_norm/part-0.parquet; rows in prev_dir (an
earlier cur_norm) whose billing period the new files do not cover are carried over, covered periods take the new files.
Exit 0 and a one-line JSON summary on stdout; 3 with {"reason": ...} when the upload is invalid; 2 for a bad account id.
"""
import gzip, json, os, re, shutil, sys, zipfile, zlib

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)  # -I drops the script dir
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "scripts"))
import safezip  # noqa: E402
import normalize  # noqa: E402

ACCT = re.compile(r"[A-Za-z0-9_-]{1,64}")
ALLOWED = {".parquet", ".csv", ".gz", ".json"}
DATA = (".parquet", ".csv", ".gz")
CHUNK = 1 << 20
MAX_RATIO = 200


def _gunzip(src, dst, budget_left, label):
    """Stream-decompress one gzip CSV to a plain file; returns the bytes written. Counts while writing."""
    limit = max(os.path.getsize(src), 1) * MAX_RATIO
    n = 0
    try:
        with gzip.open(src, "rb") as zin, open(dst, "xb") as out:
            while True:
                buf = zin.read(CHUNK)
                if not buf:
                    break
                n += len(buf)
                if n > budget_left:
                    raise normalize.CurRejected("CUR 파일이 너무 큽니다")
                if n > limit:
                    raise normalize.CurRejected("압축률이 비정상적으로 높습니다: " + label)
                out.write(buf)
    except (OSError, EOFError, zlib.error):  # gzip.BadGzipFile is an OSError
        raise normalize.CurRejected("CUR 파일을 읽을 수 없습니다: " + label)
    return n


def generate_names(raw, gen_dir, names, origins):
    """Rename extracted files to f0001.parquet / f0001.csv ... (gzip decompressed on the way) so that untrusted
    names never reach DuckDB. Returns (paths, labels {generated: shown original}, groups {generated: folder}, bytes
    of CSV: plain plus gunzipped, refused past normalize.MAX_UNCOMPRESSED)."""
    order = sorted(names, key=lambda n: (os.path.dirname(origins.get(n, n)), origins.get(n, n)))
    paths, labels, groups, used = [], {}, {}, 0
    shutil.rmtree(gen_dir, ignore_errors=True)
    os.makedirs(gen_dir)
    for i, name in enumerate(order, 1):
        ext = os.path.splitext(name)[1].lower()
        gen = "f%04d.%s" % (i, "parquet" if ext == ".parquet" else "csv")
        src, dst = os.path.join(raw, name), os.path.join(gen_dir, gen)
        label = safezip.show(name)
        if ext == ".gz":
            used += _gunzip(src, dst, normalize.MAX_UNCOMPRESSED - used, label)
            os.remove(src)
        else:
            if ext == ".csv":  # plain CSV shares the budget with gunzipped CSV (Parquet counts by its footers)
                used += os.path.getsize(src)
                if used > normalize.MAX_UNCOMPRESSED:
                    raise normalize.CurRejected("CUR 파일이 너무 큽니다")
            os.replace(src, dst)
        paths.append(dst)
        labels[gen] = label
        groups[gen] = os.path.dirname(origins.get(name, name))
    return paths, labels, groups, used


def reject(reason):
    print(json.dumps({"reason": reason}, ensure_ascii=False))
    return 3


def main(zip_path, work, acct, prev_dir=None):
    if not ACCT.fullmatch(acct or ""):
        print("invalid account id", file=sys.stderr)
        return 2
    work = os.path.abspath(work)
    raw = os.path.join(work, "cur_raw")
    gen_dir = os.path.join(work, "cur_in")  # generated names only; DuckDB reads nothing else
    out = os.path.join(work, "cur_norm")
    try:
        if not zipfile.is_zipfile(zip_path):
            return reject("zip 형식이 아닙니다")
        try:
            origins = {}
            names = safezip.extract(zip_path, raw, ALLOWED, max_entries=5000, max_total=20 * 2 ** 30, max_ratio=200,
                                    flatten_dups=True, origins=origins)
            # .json files are optional manifests: never opened
            data = [n for n in names if n.lower().endswith(DATA)]
            if not data:
                return reject("CUR 파일을 찾지 못했습니다")
            files, labels, groups, used = generate_names(raw, gen_dir, data, origins)
            seen = normalize.check_readable(files, work, labels)
            if used + seen["bytes"] > normalize.MAX_UNCOMPRESSED:
                return reject("CUR 파일이 너무 큽니다")
            summary = normalize.run(files, out, prev_dir=prev_dir, labels=labels, groups=groups)
        except safezip.ZipRejected as e:
            return reject(e.reason)
        except normalize.CurRejected as e:
            return reject(e.reason)
        except (zipfile.BadZipFile, zipfile.LargeZipFile, NotImplementedError, EOFError):
            return reject("zip 형식이 아닙니다")
        print(json.dumps(summary, ensure_ascii=False))
        return 0
    finally:
        shutil.rmtree(raw, ignore_errors=True)
        shutil.rmtree(gen_dir, ignore_errors=True)
        shutil.rmtree(os.path.join(work, "duckdb_tmp"), ignore_errors=True)


if __name__ == "__main__":
    if len(sys.argv) not in (4, 5):
        sys.exit("usage: stage_cur.py <zip> <work_dir> <acct> [prev_dir]")
    sys.exit(main(*sys.argv[1:]))
