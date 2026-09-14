#!/usr/bin/env python3
"""Producer configuration resolution must be independent of worker readiness."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from resolve_qa_bundle_coordinates import coordinates


class CoordinatesTest(unittest.TestCase):
    def stack(self, status="UPDATE_IN_PROGRESS"):
        return {"Stacks": [{"StackStatus": status, "Outputs": [
            {"OutputKey": "QaBundleBucketName", "OutputValue": "example-qa-bundles"},
            {"OutputKey": "QaBundleQueueUrl",
             "OutputValue": "https://sqs.us-east-1.amazonaws.com/123456789012/example-qa"},
        ]}]}

    def test_worker_update_does_not_block_stable_producer_coordinates(self):
        self.assertEqual(coordinates(self.stack(), "us-east-1"), {
            "bucket": "example-qa-bundles",
            "queue_url": "https://sqs.us-east-1.amazonaws.com/123456789012/example-qa",
        })

    def test_missing_wrong_region_or_injected_coordinates_fail_closed(self):
        for value in ("", "None", "bucket\nother=value"):
            stack = self.stack()
            stack["Stacks"][0]["Outputs"][0]["OutputValue"] = value
            with self.subTest(value=value), self.assertRaises(ValueError):
                coordinates(stack, "us-east-1")
        with self.assertRaises(ValueError):
            coordinates(self.stack(), "us-west-2")
        with self.assertRaises(ValueError):
            coordinates({"Stacks": []}, "us-east-1")

    def test_cli_only_describes_stack_and_never_writes_output_on_aws_failure(self):
        for failure in (False, True):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                aws = root / "aws"
                aws.write_text("#!/usr/bin/env python3\nimport json, sys\n"
                               "assert sys.argv[1:3] == ['cloudformation', 'describe-stacks']\n"
                               + ("sys.exit(42)\n" if failure else
                                  "print(" + repr(json.dumps(self.stack())) + ")\n"))
                aws.chmod(0o755)
                output = root / "output"
                result = subprocess.run([
                    sys.executable, str(Path(__file__).with_name("resolve_qa_bundle_coordinates.py")),
                    "--stack", "qa", "--region", "us-east-1", "--github-output", str(output),
                ], env={**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"]},
                    capture_output=True, text=True)
                if failure:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(output.exists())
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("bucket=example-qa-bundles\n", output.read_text())


if __name__ == "__main__":
    unittest.main()
