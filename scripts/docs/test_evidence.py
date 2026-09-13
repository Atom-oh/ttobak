import shutil
import tempfile
from pathlib import Path
import unittest

import check_docs


class GeneratedEvidenceTests(unittest.TestCase):
    def test_archived_provider_notes_retain_exact_bytes(self):
        self.assertEqual(check_docs.validate_evidence(check_docs.ROOT), [])

    def test_missing_or_rewritten_evidence_fails_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for relative in check_docs.RAW_EVIDENCE:
                target = root / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(check_docs.ROOT / relative, target)
            first, second, *_ = check_docs.RAW_EVIDENCE
            (root / first).write_text("An English translation is not the original model output.\n")
            (root / second).unlink()
            errors = check_docs.validate_evidence(root)
            self.assertEqual(len(errors), 2)
            self.assertTrue(any(first in error for error in errors))
            self.assertTrue(any(second in error for error in errors))


if __name__ == "__main__":
    unittest.main()
