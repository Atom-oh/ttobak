"""Agents or report over one data dir, as a child process of the worker (plain python3: boto3 is needed for Bedrock).

Usage:
  ai_step.py agents <data_dir> <cot|cur> <feed_path|-> <cache_dir>   -> last stdout line {"ai": "ok"|"none"}
  ai_step.py report <data_dir> <out_md> <public_url>
Runs with the task role's credentials (Bedrock, and the FsiSearch Lambda when FSI_SEARCH_FUNCTION is set); the worker's
account-scoped S3 session is never passed here.
"""
import json, os, sys

APP = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(APP, "scripts"))


def main(argv):
    if len(argv) == 5 and argv[0] == "agents":
        import agents
        d, mode, feed, cache = argv[1:]
        if mode not in ("cot", "cur"):
            return 2
        kw = {} if mode == "cot" else {"areas": agents.CUR_AREAS, "synth": False, "extras": False}
        if mode == "cot":
            import tag_signals
            tag_signals.ensure(d)  # versions published before tag signals existed
        os.makedirs(cache, exist_ok=True)
        fn = os.environ.get("FSI_SEARCH_FUNCTION")  # FsiSearch Lambda (infra app.js); unset: the "not connected" note
        if mode == "cot" and fn:
            kw["search"] = agents.lambda_search(fn)
        res = agents.run(d, cache=cache, feed_path=None if feed == "-" else feed, **kw)
        print(json.dumps({"ai": res["ai"]}))
        return 0
    if len(argv) == 4 and argv[0] == "report":
        import report
        return report.main(argv[1:])
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
