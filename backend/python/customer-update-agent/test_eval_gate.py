import contextlib
import io
import json
import os
import tempfile
import unittest

import eval_gate
import gate


class FakeBackend:
    """Scores by a marker in the context: YES gives needed 0.9, otherwise 0.1."""
    backend = "fake"

    def score_options(self, question, options, context):
        yes = "YES" in context
        return [gate.OptionScore(gate.NEEDED, 0.9 if yes else 0.1, ""),
                gate.OptionScore(gate.NOT_NEEDED, 0.1 if yes else 0.9, "")]


def row(context, label):
    return {"question": "q", "options": [{"id": "needed", "text": "a"}, {"id": "not_needed", "text": "b"}],
            "context": context, "label": label, "lunaScore": 0.8}


def run(rows, backend="test_eval_gate:FakeBackend"):
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "l.jsonl")
        with open(path, "w", encoding="utf-8") as fh:
            for r in rows:
                fh.write(json.dumps(r, ensure_ascii=False) + "\n")
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stderr(err):
            code = eval_gate.main(["--labels", path, "--backend", backend], out=out)
        return code, out.getvalue(), err.getvalue()


class EvalGateTest(unittest.TestCase):
    def rows(self):
        r = []
        r += [row("YES a", "needed")] * 150          # true positives
        r += [row("NO a", "needed")] * 50            # false negatives
        r += [row("YES 한국어", "not_needed")] * 50   # false positives, Korean
        r += [row("NO a", "not_needed")] * 50        # true negatives
        return r

    def test_precision_recall_and_korean_subset(self):
        code, text, _ = run(self.rows())
        self.assertEqual(code, 0)
        self.assertIn("n=300", text)
        self.assertIn(f"precision=0.750 {eval_gate.RECALL}=0.750", text)
        self.assertIn(f"korean n=50 precision=0.000 {eval_gate.RECALL}=n/a", text)
        self.assertIn("errors=0", text)

    def test_too_few_pairs_exit_1_with_message(self):
        code, text, err = run(self.rows()[:299])
        self.assertEqual(code, 1)
        self.assertIn("n=299", text)
        self.assertIn("need at least 300 labelled pairs", err)

    def test_zero_denominator_is_na(self):
        code, text, _ = run([row("NO a", "not_needed")] * 300)
        self.assertEqual(code, 0)
        self.assertIn(f"precision=n/a {eval_gate.RECALL}=n/a", text)

    def test_no_label_text_printed(self):
        _, text, err = run([row("SECRET 한국어", "not_needed")] * 300)
        self.assertNotIn("SECRET", text + err)

    def test_backend_error_counts_as_not_needed(self):
        class Flaky(FakeBackend):
            def score_options(self, question, options, context):
                if "BOOM" in context:
                    raise RuntimeError("x")
                return super().score_options(question, options, context)

        eval_gate.Flaky = Flaky
        rows = [row("BOOM YES", "needed")] * 10 + [row("YES a", "needed")] * 290
        code, text, _ = run(rows, backend="eval_gate:Flaky")
        self.assertEqual(code, 0)
        self.assertIn("errors=10", text)
        self.assertIn(f"{eval_gate.RECALL}=0.967", text)

    def test_luna_uses_production_prompt(self):
        import sys
        import types
        calls = []
        fake = types.ModuleType("llm")
        fake.build_agent = lambda role, prompt, tools=None: calls.append((role, prompt)) or object()
        fake.ask_json = lambda agent, prompt, required: {"scores": []}
        saved = sys.modules.get("llm")
        sys.modules["llm"] = fake
        try:
            backend = eval_gate.load_backend("luna")
        finally:
            sys.modules["llm"] = saved
        self.assertEqual(calls, [("gate", gate.GATE_SYSTEM_PROMPT)])
        self.assertIsInstance(backend, gate.LunaGate)

    def test_rule_is_the_gate_rule(self):
        self.assertFalse(gate.is_needed(0.7, 0.7))
        self.assertTrue(gate.is_needed(0.7, 0.6))
        self.assertFalse(gate.is_needed(0.69, 0.1))


if __name__ == "__main__":
    unittest.main()
