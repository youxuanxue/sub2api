import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]


class EdgeHealthWatchWorkflowTest(unittest.TestCase):
    def test_discovery_distinguishes_empty_fleet_from_resolver_failure(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/edge-health-watch.yml').read_text())
        script = next(step['run'] for step in workflow['jobs']['watch']['steps'] if step.get('id') == 'fleet')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root / 'python3'
            fake.write_text('#!/bin/sh\nprintf "%s" "$TEST_EDGES"\nexit "$TEST_RESOLVE_STATUS"\n')
            fake.chmod(0o755)
            for status, edges, expected in ((0, '', 'active=false'), (0, 'zz1\n', 'active=true'), (2, '', None)):
                with self.subTest(status=status, edges=edges):
                    output = root / f'output-{status}-{len(edges)}'
                    result = subprocess.run(['bash', '-c', script], capture_output=True, text=True,
                        env={**os.environ, 'PATH': directory + ':' + os.environ['PATH'],
                             'GITHUB_OUTPUT': str(output), 'TEST_EDGES': edges, 'TEST_RESOLVE_STATUS': str(status)})
                    actual = output.read_text() if output.exists() else ''
                    if expected:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertEqual(actual.strip(), expected)
                    else:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertEqual(actual, '')


if __name__ == '__main__':
    unittest.main()
