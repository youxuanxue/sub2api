"""Contract tests for app-node warm fanout; no network or live hosts."""
from __future__ import annotations

import json
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import remote_exec
import warm_fanout

TAG = "1.8.380"
REV = "a" * 40
APP_B = remote_exec.Node("app-b", "app", "lhins-0kxx7cco", "10.2.4.13", None)
APP_A = remote_exec.Node(
    "app-a",
    "app",
    "lhins-6qmb3fd6",
    "10.2.4.11",
    "mi-06568963b4b3c3239",
    ssm_region="eu-west-2",
)


def receipt(node: str, channel: str) -> dict:
    return {
        "schema_version": 1,
        "tag": TAG,
        "revision": REV,
        "architecture": "amd64",
        "image_id": "sha256:" + node[-1] * 64,
        "local_tag": warm_fanout.identity(TAG, REV),
        "channel": channel,
    }


class FakeRunner:
    def __init__(self, results):
        self.results = results
        self.calls = []

    def __call__(self, node, script, *, timeout, via):
        self.calls.append((node.name, via, timeout, script))
        value = self.results[node.name]
        if isinstance(value, Exception):
            raise value
        return value


class WarmFanoutTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.out = pathlib.Path(self.temp.name) / "warm.json"

    def test_cn_prod_mixes_tat_and_ssm_in_manifest_order(self):
        runner = FakeRunner({
            "app-b": remote_exec.Result(
                "tat", "Success", 0, json.dumps(receipt("app-b", "tat"), indent=2), ""
            ),
            "app-a": remote_exec.Result(
                "ssm", "Success", 0, json.dumps(receipt("app-a", "ssm"), indent=2), ""
            ),
        })
        payload = warm_fanout.warm_all(
            [APP_B, APP_A],
            tag=TAG,
            revision=REV,
            transport="s3",
            script_for=lambda node: f"warm {node.name}",
            channel="auto",
            receipt_out=self.out,
            timeout=1200,
            runner=runner,
        )
        self.assertEqual(payload["outcome"], "success")
        self.assertEqual([(n["name"], n["channel"]) for n in payload["nodes"]],
                         [("app-b", "tat"), ("app-a", "ssm")])
        self.assertEqual([(c[0], c[1]) for c in runner.calls],
                         [("app-b", "tat"), ("app-a", "ssm")])
        self.assertEqual(json.loads(self.out.read_text())["outcome"], "success")

    def test_failure_is_fail_stop_and_receipt_records_partial_work(self):
        runner = FakeRunner({
            "app-b": remote_exec.Result("tat", "Failed", 2, "", "pull failed"),
            "app-a": remote_exec.Result("ssm", "Success", 0, "{}", ""),
        })
        with self.assertRaisesRegex(warm_fanout.WarmFanoutError, "pull failed"):
            warm_fanout.warm_all(
                [APP_B, APP_A],
                tag=TAG,
                revision=REV,
                transport="s3",
                script_for=lambda node: f"warm {node.name}",
                channel="auto",
                receipt_out=self.out,
                timeout=1200,
                runner=runner,
            )
        self.assertEqual(len(runner.calls), 1)
        payload = json.loads(self.out.read_text())
        self.assertEqual(payload["outcome"], "failed")
        self.assertEqual(payload["nodes"][0]["status"], "Failed")

    def test_bad_receipt_identity_fails(self):
        bad = receipt("app-b", "tat")
        bad["revision"] = "b" * 40
        runner = FakeRunner({
            "app-b": remote_exec.Result("tat", "Success", 0, json.dumps(bad), ""),
            "app-a": remote_exec.Result("ssm", "Success", 0, "{}", ""),
        })
        with self.assertRaisesRegex(warm_fanout.WarmFanoutError, "revision"):
            warm_fanout.warm_all(
                [APP_B, APP_A],
                tag=TAG,
                revision=REV,
                transport="s3",
                script_for=lambda node: f"warm {node.name}",
                channel="auto",
                receipt_out=self.out,
                timeout=1200,
                runner=runner,
            )
        self.assertEqual(len(runner.calls), 1)

    def test_ssm_request_without_id_is_refused_before_dispatch(self):
        runner = FakeRunner({
            "app-b": remote_exec.Result("tat", "Success", 0, "{}", ""),
            "app-a": remote_exec.Result("ssm", "Success", 0, "{}", ""),
        })
        with self.assertRaisesRegex(warm_fanout.WarmFanoutError, "requires ssm_instance_id"):
            warm_fanout.warm_all(
                [APP_B],
                tag=TAG,
                revision=REV,
                transport="s3",
                script_for=lambda node: f"warm {node.name}",
                channel="ssm",
                receipt_out=self.out,
                timeout=1200,
                runner=runner,
            )
        self.assertEqual(runner.calls, [])

    def test_ghcr_script_writes_rolling_receipt_without_replacing_conflicts(self):
        script = warm_fanout.build_ghcr_script("ghcr.io/example/sub2api", TAG, REV)
        self.assertIn("docker pull \"$SOURCE\"", script)
        self.assertIn(warm_fanout.identity(TAG, REV), script)
        self.assertIn(f"/var/lib/tokenkey/image-distribution/{TAG}-{REV}.json", script)
        self.assertIn("different filesystem layers", script)
        self.assertIn("org.opencontainers.image.revision", script)

    def test_last_json_object_handles_pretty_receipt_with_logs(self):
        text = "pull log\n" + json.dumps(receipt("app-b", "tat"), indent=2) + "\n"
        self.assertEqual(warm_fanout.last_json_object(text)["tag"], TAG)

    def test_render_then_dispatch_validates_identity_and_script_sha(self):
        render = pathlib.Path(self.temp.name) / "render.json"
        rendered = warm_fanout.render_all(
            [APP_B, APP_A],
            tag=TAG,
            revision=REV,
            transport="s3",
            script_for=lambda node: f"warm {node.name} https://private.invalid/signed",
            channel="auto",
            render_out=render,
        )
        entries = warm_fanout.load_render(
            render,
            [APP_B, APP_A],
            tag=TAG,
            revision=REV,
            transport="s3",
            channel="auto",
        )
        self.assertEqual(
            [(node.name, via) for node, via, _ in entries],
            [("app-b", "tat"), ("app-a", "ssm")],
        )
        self.assertEqual(rendered["nodes"][0]["script_sha256"],
                         warm_fanout.hashlib.sha256(
                             rendered["nodes"][0]["script"].encode()
                         ).hexdigest())

        tampered = json.loads(render.read_text())
        tampered["nodes"][1]["script"] += "\n# changed"
        render.write_text(json.dumps(tampered))
        with self.assertRaisesRegex(warm_fanout.WarmFanoutError, "sha256"):
            warm_fanout.load_render(
                render,
                [APP_B, APP_A],
                tag=TAG,
                revision=REV,
                transport="s3",
                channel="auto",
            )

    def test_load_render_rejects_changed_node_order_or_channel(self):
        render = pathlib.Path(self.temp.name) / "render.json"
        warm_fanout.render_all(
            [APP_B, APP_A],
            tag=TAG,
            revision=REV,
            transport="s3",
            script_for=lambda node: f"warm {node.name}",
            channel="auto",
            render_out=render,
        )
        original = render.read_text()
        for mutate, pattern in (
            (lambda data: data["nodes"].reverse(), "identity"),
            (lambda data: data["nodes"][0].update(channel="ssm"), "channel"),
        ):
            data = json.loads(original)
            mutate(data)
            render.write_text(json.dumps(data))
            with self.assertRaisesRegex(warm_fanout.WarmFanoutError, pattern):
                warm_fanout.load_render(
                    render,
                    [APP_B, APP_A],
                    tag=TAG,
                    revision=REV,
                    transport="s3",
                    channel="auto",
                )

    def test_dispatch_rendered_uses_artifact_without_s3_client(self):
        render = pathlib.Path(self.temp.name) / "render.json"
        warm_fanout.render_all(
            [APP_B, APP_A],
            tag=TAG,
            revision=REV,
            transport="s3",
            script_for=lambda node: f"warm {node.name}",
            channel="auto",
            render_out=render,
        )
        results = [
            remote_exec.Result("tat", "Success", 0,
                               json.dumps(receipt("app-b", "tat")), ""),
            remote_exec.Result("ssm", "Success", 0,
                               json.dumps(receipt("app-a", "ssm")), ""),
        ]
        calls = []

        def runner(node, script, *, timeout, via):
            calls.append((node, via, timeout, script))
            return results.pop(0)

        with patch.object(warm_fanout.remote_exec, "app_nodes",
                          return_value=[APP_B, APP_A]):
            code = warm_fanout.main([
                "dispatch-rendered",
                "--manifest", "ignored.json",
                "--tag", TAG,
                "--revision", REV,
                "--transport", "s3",
                "--channel", "auto",
                "--render-file", str(render),
                "--receipt-out", str(self.out),
            ], runner=runner)
        self.assertEqual(code, 0)
        self.assertEqual([call[0].name for call in calls],
                         ["app-b", "app-a"])
        self.assertEqual(json.loads(self.out.read_text())["outcome"], "success")


if __name__ == "__main__":
    unittest.main()
