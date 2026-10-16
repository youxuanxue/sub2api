"""Pruning reads literal dotenv data and never executes unrelated values."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[2] / 'deploy/aws/stage0/tokenkey-prune-ghcr-app-tags.sh'


class PruneDotenvTest(unittest.TestCase):
    def test_spaced_cidrs_and_shell_syntax_are_not_executed(self):
        for image in ['ghcr.io/test/app:1.0', '"ghcr.io/test/app:1.0"']:
            with self.subTest(image=image), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                marker = root / 'executed'
                envfile = root / '.env'
                envfile.write_text(f'TOKENKEY_IMAGE={image}\nMAIN_GATEWAY_ALLOWED_CIDR=192.0.2.1/32 192.0.2.2/32\nUNRELATED=$(touch {marker})\n')
                docker = root / 'docker'
                docker.write_text('#!/bin/bash\nprintf "%s\\n" "$*" >> "$TEST_CALLS"\nif [ "$1" = inspect ]; then exit 1; fi\n')
                docker.chmod(0o755)
                calls = root / 'calls'
                result = subprocess.run(['bash', str(SCRIPT)], env={**os.environ, 'PATH': str(root)+':'+os.environ['PATH'], 'TOKENKEY_PRUNE_ENV_FILE': str(envfile), 'TEST_CALLS': str(calls)}, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse(marker.exists(), 'dotenv command substitution was executed')
                self.assertIn('images ghcr.io/test/app --format', calls.read_text())
                self.assertNotIn('rmi ', calls.read_text())


if __name__ == '__main__':
    unittest.main()
