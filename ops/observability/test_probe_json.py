#!/usr/bin/env python3
from __future__ import annotations

import json
import unittest

from probe_json import iter_probe_json, loads_probe_json


class ProbeJsonTest(unittest.TestCase):
    def test_single_object(self) -> None:
        self.assertEqual(loads_probe_json('{"a": 1}\n'), {"a": 1})

    def test_trailing_noise_ignored_when_one_doc(self) -> None:
        self.assertEqual(loads_probe_json('{"a": 1}\nok\n'), {"a": 1})

    def test_duplicate_fanout_collapses(self) -> None:
        line = json.dumps({"schema_version": 1, "n": 2}, ensure_ascii=False)
        self.assertEqual(loads_probe_json(f"{line}\n{line}\n"), {"schema_version": 1, "n": 2})

    def test_conflict_raises(self) -> None:
        with self.assertRaises(json.JSONDecodeError):
            loads_probe_json('{"a": 1}\n{"a": 2}\n')

    def test_iter_collects_all(self) -> None:
        docs = iter_probe_json('{"a": 1}\n{"b": 2}\n')
        self.assertEqual(docs, [{"a": 1}, {"b": 2}])


if __name__ == "__main__":
    unittest.main()
