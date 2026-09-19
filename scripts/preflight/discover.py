#!/usr/bin/env python3
"""Directory suites, excluding tests already owned by a focused preflight gate."""
from pathlib import Path
import sys
import unittest

# The billing probe gate is always selected together with this directory suite.
FOCUSED = {'ops/observability': {'test_probe_user_billing_watch'}}


def without_focused(suite: unittest.TestSuite, modules: set[str]) -> unittest.TestSuite:
    result = unittest.TestSuite()
    for item in suite:
        if isinstance(item, unittest.TestSuite):
            result.addTests(without_focused(item, modules))
        elif item.__class__.__module__ not in modules:
            result.addTest(item)
    return result


def main() -> int:
    directory = sys.argv[1]
    root = Path(__file__).resolve().parents[2]
    # Match python -m unittest: project packages and sibling script imports work.
    sys.path.insert(0, str(root))
    suite = unittest.defaultTestLoader.discover(str(root / directory), pattern='test_*.py', top_level_dir=str(root / directory))
    suite = without_focused(suite, FOCUSED.get(directory, set()))
    return 0 if unittest.TextTestRunner().run(suite).wasSuccessful() else 1


if __name__ == '__main__':
    sys.exit(main())
