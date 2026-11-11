import tempfile
from pathlib import Path
import unittest

import check_docs


def records(*names):
    directory = tempfile.TemporaryDirectory()
    target = Path(directory.name) / "docs/decisions"
    target.mkdir(parents=True)
    for name in names:
        (target / name).write_text("# record\n")
    return directory


class DecisionRecordTests(unittest.TestCase):
    def test_repository_records_are_within_the_limit(self):
        self.assertEqual(check_docs.validate_decisions(check_docs.ROOT), [])

    def test_index_and_template_are_not_records(self):
        with records("README.md", ".template.md", "ADR-001-a.md", "ADR-030-b.md") as root:
            self.assertEqual(check_docs.validate_decisions(Path(root)), [])

    def test_number_above_thirty_fails(self):
        with records("ADR-030-a.md", "ADR-031-b.md") as root:
            errors = check_docs.validate_decisions(Path(root))
        self.assertEqual(len(errors), 1)
        self.assertIn("ADR-031-b.md", errors[0])

    def test_zero_and_malformed_names_fail(self):
        with records("ADR-000-a.md", "ADR-7-b.md", "ADR-012.md") as root:
            self.assertEqual(len(check_docs.validate_decisions(Path(root))), 3)

    def test_shared_number_fails(self):
        with records("ADR-004-a.md", "ADR-004-b.md") as root:
            errors = check_docs.validate_decisions(Path(root))
        self.assertEqual(len(errors), 1)
        self.assertIn("ADR-004-a.md", errors[0])


if __name__ == "__main__":
    unittest.main()
