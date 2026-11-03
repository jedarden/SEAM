#!/usr/bin/env python3
"""Focused contract tests for the bead CLI output parser."""

import importlib.util
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).parent
SCRIPT_PATHS = (
    ROOT / "bead_visibility_diagnostic.py",
    ROOT / "bead-visibility-diagnostic" / "bead_visibility_diagnostic.py",
)


def load_parser(path: Path):
    spec = importlib.util.spec_from_file_location("visibility_diagnostic", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.parse_cli_bead_ids


class CLIOutputParserTests(unittest.TestCase):
    def test_documented_json_array_output(self):
        payload = json.dumps([{"id": "seam-one"}, {"id": "seam-two"}])
        for path in SCRIPT_PATHS:
            with self.subTest(path=path):
                self.assertEqual(load_parser(path)(payload), ["seam-one", "seam-two"])

    def test_single_object_and_jsonl_compatibility(self):
        cases = (
            ('{"id":"seam-one"}', ["seam-one"]),
            ('{"id":"seam-one"}\n{"id":"seam-two"}\n', ["seam-one", "seam-two"]),
        )
        for path in SCRIPT_PATHS:
            for output, expected in cases:
                with self.subTest(path=path, output=output):
                    self.assertEqual(load_parser(path)(output), expected)

    def test_ignores_records_without_string_ids(self):
        payload = json.dumps([{"id": "seam-one"}, {"id": 7}, {"title": "missing"}])
        for path in SCRIPT_PATHS:
            with self.subTest(path=path):
                self.assertEqual(load_parser(path)(payload), ["seam-one"])


if __name__ == "__main__":
    unittest.main()
