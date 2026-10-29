"""Extract embedded <script type=application/json id=json-*> tables from a COT360 HTML.
Usage: python -I html2json.py <html> <outdir>"""
import json, os, re, sys


def main(path, outdir):
    os.makedirs(outdir, exist_ok=True)
    h = open(path, encoding="utf-8").read()
    pat = r'<script[^>]*id="(json-[^"]+)"[^>]*type="application/json"[^>]*>(.*?)</script>'
    for bid, body in re.findall(pat, h, re.S):
        j = json.loads(body)
        fn = re.sub(r"[^A-Za-z0-9]+", "_", bid).strip("_") + ".json"
        json.dump(j, open(os.path.join(outdir, fn), "w"))
        print(f"{bid}\t{len(j.get('rows', []))} rows\t{j.get('headers')}")


if __name__ == "__main__":
    main(*sys.argv[1:])
