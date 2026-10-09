#!/usr/bin/env python3
"""Regression tests for probe_account_model.sh."""
from __future__ import annotations

import json
import importlib.util
import os
import pathlib
import shutil
import subprocess
import tempfile
import time
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe_account_model.sh"


class ProbeAccountModelTest(unittest.TestCase):
    def run_probe_fixture(self, endpoint: str, **overrides: str) -> tuple[dict, str | None, list[str], str]:
        """Run the actual shell entrypoint; replace only remote infrastructure."""
        with tempfile.TemporaryDirectory(prefix="probe-exact-body-") as td:
            root = pathlib.Path(td)
            for name in ("probe_account_model.sh", "probe_account_model_verdict.py", "smoke_anthropic_realistic.py"):
                shutil.copy2(_SCRIPT.parent / name, root / name)
            (root / "resolve-app-container.sh").write_text(
                'tk_resolve_app_container() { echo tokenkey-test; }\n'
            )
            (root / "probe_reserved_resources.sh").write_text("""
 tk_probe_scope_from_platform() { echo gemini; }
 tk_probe_group_name() { echo __tk_probe_gemini_group; }
 tk_probe_key_name() { echo __tk_probe_gemini_key; }
 tk_probe_prepare_platform_reuse_probe() {
   echo prepare >>"$PROBE_FIXTURE/lifecycle"
   TK_PROBE_SCOPE=gemini; TK_PROBE_GROUP_ID=750
   TK_PROBE_KEY_ID=638; TK_PROBE_KEY=test-only-key
 }
 tk_probe_cleanup_named_group() { echo cleanup >>"$PROBE_FIXTURE/lifecycle"; }
""")
            sudo = root / "sudo"
            sudo.write_text("""#!/usr/bin/env python3
import json, os, pathlib, sys
root = pathlib.Path(os.environ['PROBE_FIXTURE'])
args = sys.argv[1:]
with (root / 'calls').open('a') as output:
    output.write(json.dumps(args) + '\\n')
if 'psql' in args:
    query = args[-1]
    if 'FROM accounts a' in query:
        print(json.dumps({'id': 200, 'name': 'fixture', 'platform': 'gemini'}))
    elif 'FROM usage_logs' in query:
        print(json.dumps({'account_id': 200, 'request_id': 'fixture-request'}))
    elif 'SELECT to_char' in query:
        print('2026-01-01T00:00:00Z')
elif '-lc' in args:
    (root / 'sent-body').write_bytes(sys.stdin.buffer.read())
    print('200', end='')
elif 'cat' in args:
    if args[-1].endswith('headers.txt'):
        print('x-request-id: fixture-request')
    else:
        print('{}')
""")
            sudo.chmod(0o755)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                   "PROBE_FIXTURE": str(root), "ACCOUNT_ID": "200", "MODEL": "nano-2",
                   "ENDPOINT": endpoint, "REQUEST_BODY_JSON": "", "REQUEST_EXTRA_JSON": "",
                   "USAGE_POLL_ATTEMPTS": "1", "USAGE_POLL_INTERVAL_SECONDS": "0",
                   "TK_SMOKE_ANTHROPIC_REALISTIC": "1", "MAX_TOKENS": "32",
                   "PROMPT_TEXT": "hello", "PROBE_REUSE_MODE": "1", "KEEP_PROBE_ARTIFACTS": "0",
                   **overrides}
            proc = subprocess.run(["bash", str(root / _SCRIPT.name)], env=env,
                                  capture_output=True, text=True, check=False, cwd=root)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertFalse((root / "SHOULD_NOT_EXIST").exists())
            report = json.loads(proc.stdout)
            sent = (root / "sent-body").read_text() if (root / "sent-body").exists() else None
            calls = (root / "calls").read_text().splitlines() if (root / "calls").exists() else []
            lifecycle = (root / "lifecycle").read_text() if (root / "lifecycle").exists() else ""
            return report, sent, calls, lifecycle

    @unittest.skipUnless(importlib.util.find_spec("PIL"), "image decoder installed by Gemini Web CI job")
    def test_exact_body_preserves_minimal_requests_and_shell_literals(self) -> None:
        prompt = 'Draw "$HOME" `touch SHOULD_NOT_EXIST` $(touch SHOULD_NOT_EXIST) 苹果'
        for endpoint in ("chat", "responses", "messages"):
            body = {"model": "nano-2", "generationConfig": {
                "responseModalities": ["TEXT", "IMAGE"], "imageConfig": {"aspectRatio": "4:3"}}}
            if endpoint == "responses":
                body["input"] = prompt
            else:
                body["messages"] = [{"role": "user", "content": prompt}]
            if endpoint == "messages":
                # Required protocol control remains explicit for adapter negotiation.
                body["max_tokens"] = 128
            with self.subTest(endpoint=endpoint):
                exact = json.dumps(body, ensure_ascii=False, indent=2)
                report, sent, calls, lifecycle = self.run_probe_fixture(endpoint, REQUEST_BODY_JSON=exact)
                self.assertEqual(sent, exact)
                self.assertTrue(report["probe"]["request_body_override"])
                self.assertTrue(report["probe"]["expect_image_output"])
                self.assertEqual(report["verdict"], "uncorrelated_success")
                self.assertFalse(report["response"]["compat_image"]["valid"])
                permission_writes = [json.loads(call)[-1] for call in calls if "SET allow_image_generation" in call]
                self.assertEqual(len(permission_writes), 1)
                self.assertIn("WHERE id = 750;", permission_writes[0])
                self.assertEqual(lifecycle, "prepare\ncleanup\n")
                self.assertNotIn("instructions", json.loads(sent))
                self.assertNotIn("system", json.loads(sent))

    def test_concurrent_platform_probe_files_and_cleanup_are_isolated(self) -> None:
        # Execute the real container shell from each entrypoint invocation. Only curl
        # is replaced: hold request B open while A reads and cleans up its artifacts.
        with tempfile.TemporaryDirectory(prefix="probe-concurrent-") as td:
            root = pathlib.Path(td)
            audio = root / "audio.wav"
            audio.write_bytes(b"fixture-audio")
            curl = root / "curl"
            curl.write_text("""#!/usr/bin/env python3
import os, pathlib, sys, time
args = sys.argv[1:]
root = pathlib.Path(os.environ['RACE_ROOT'])
label = os.environ['RACE_LABEL']
headers = pathlib.Path(args[args.index('-D') + 1])
body = pathlib.Path(args[args.index('-o') + 1])
headers.write_text('x-request-id: ' + label + '\\n')
(root / (label + '-headers')).touch()
wait_for = root / ('B-headers' if label == 'A' else 'release-B')
deadline = time.monotonic() + 10
while not wait_for.exists():
    if time.monotonic() > deadline:
        sys.exit('fixture synchronization timed out')
    time.sleep(0.01)
if '--data-binary' in args:
    request = args[args.index('--data-binary') + 1][1:]
else:
    request = next(a for a in args if a.startswith('file=@'))[6:].split(';')[0]
body.write_bytes(pathlib.Path(request).read_bytes())
print('200', end='')
""")
            curl.chmod(0o755)
            for endpoint in ("images", "transcriptions"):
                with self.subTest(endpoint=endpoint):
                    for marker in ("A-headers", "B-headers", "release-B"):
                        (root / marker).unlink(missing_ok=True)
                    runs = []
                    for label, operation in (("A", endpoint), ("B", "messages")):
                        _, _, calls, _ = self.run_probe_fixture(operation, AUDIO_FILE=str(audio))
                        commands = [json.loads(call) for call in calls]
                        send = next(c for c in commands if '-lc' in c)
                        env = {**os.environ, 'PATH': str(root) + os.pathsep + os.environ['PATH'],
                               'RACE_ROOT': str(root), 'RACE_LABEL': label}
                        for i, arg in enumerate(send):
                            if arg == '-e':
                                key, value = send[i + 1].split('=', 1)
                                env[key] = value.replace('/tmp/', str(root) + '/')
                        shell = send[-1].replace('/tmp/', str(root) + '/')
                        proc = subprocess.Popen(['sh', '-c', shell], env=env, stdin=subprocess.PIPE,
                                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                        proc.stdin.write(label.encode())
                        proc.stdin.close()
                        proc.stdin = None
                        runs.append((proc, commands))
                        if label == 'A':
                            # Ensure B overwrites A's headers on the buggy implementation.
                            deadline = time.monotonic() + 10
                            while not (root / 'A-headers').exists():
                                if time.monotonic() > deadline:
                                    self.fail('first request did not write headers')
                                time.sleep(0.01)
                    try:
                        for index, (proc, commands) in enumerate(runs):
                            out, err = proc.communicate(timeout=15)
                            self.assertEqual((proc.returncode, out), (0, b'200'), err)
                            reads = [c[-1] for c in commands if 'cat' in c and '-lc' not in c]
                            contents = {pathlib.Path(path).name: pathlib.Path(path.replace('/tmp/', str(root) + '/')).read_text()
                                        for path in reads}
                            label = 'AB'[index]
                            self.assertEqual(next(v for k, v in contents.items() if k.endswith('headers.txt')),
                                             'x-request-id: ' + label + '\n')
                            self.assertEqual(next(v for k, v in contents.items() if k.endswith('response.json')), label)
                            cleanup = next(c for c in commands if 'rm' in c)
                            args = [a.replace('/tmp/', str(root) + '/') for a in cleanup[cleanup.index('rm'):]]
                            subprocess.run(args, check=True)
                            (root / 'release-B').touch()
                    finally:
                        (root / 'release-B').touch()
                        for proc, _ in runs:
                            if proc.poll() is None:
                                proc.kill()
                            proc.communicate()

    def test_exact_body_errors_precede_remote_calls_and_probe_resources(self) -> None:
        cases = [
            ("chat", "[]", {}), ("responses", "{broken", {}),
            ("chat", '{"model":"other","messages":[]}', {}),
            ("chat", '{"model":"nano-2","temperature":NaN}', {}),
            ("messages", '{"model":"nano-2","messages":[]}', {}),
            ("messages", '{"model":"nano-2","max_tokens":true}', {}),
            ("messages", '{"model":"nano-2","max_tokens":0}', {}),
            ("chat", '{"model":"nano-2"}', {"REQUEST_EXTRA_JSON": "{}"}),
            ("transcriptions", '{"model":"nano-2"}', {}),
        ]
        for endpoint, body, extra in cases:
            with self.subTest(endpoint=endpoint, body=body, extra=extra):
                report, sent, calls, lifecycle = self.run_probe_fixture(endpoint, REQUEST_BODY_JSON=body, **extra)
                self.assertEqual(report["verdict"], "setup_error")
                self.assertIsNone(sent)
                self.assertEqual(calls, [])
                self.assertEqual(lifecycle, "")

    def test_default_probe_payloads_keep_existing_controls(self) -> None:
        for endpoint in ("chat", "responses", "messages"):
            with self.subTest(endpoint=endpoint):
                report, sent, calls, lifecycle = self.run_probe_fixture(endpoint)
                body = json.loads(sent)
                self.assertFalse(report["probe"]["request_body_override"])
                self.assertFalse(report["probe"]["expect_image_output"])
                self.assertFalse(any("SET allow_image_generation" in call for call in calls))
                self.assertEqual(body["model"], "nano-2")
                self.assertEqual(lifecycle, "prepare\ncleanup\n")
                if endpoint == "responses":
                    self.assertEqual(body["instructions"], "You are a terse probe responder.")
                    self.assertEqual(body["max_output_tokens"], 32)
                else:
                    self.assertEqual(body["max_tokens"], 32)
                if endpoint == "messages":
                    self.assertTrue(body["system"])

    def test_missing_reserved_resources_reports_structured_setup_error(self) -> None:
        with tempfile.TemporaryDirectory(prefix="probe-account-model-test-") as td:
            isolated_script = pathlib.Path(td) / "probe_account_model.sh"
            shutil.copy2(_SCRIPT, isolated_script)

            proc = subprocess.run(
                ["bash", str(isolated_script)],
                capture_output=True,
                text=True,
                check=False,
            )

        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertEqual(payload["verdict"], "setup_error")
        self.assertIn("missing probe_reserved_resources.sh companion", payload["error"])
        self.assertNotIn("command not found", proc.stderr)

    def test_reuse_mode_delegates_resource_lifecycle_to_shared_owner(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then', script)
        self.assertIn('tk_probe_prepare_platform_reuse_probe "$PLATFORM" "$ACCOUNT_ID"', script)
        self.assertIn('PROBE_SCOPE="${TK_PROBE_SCOPE}"', script)
        self.assertIn('GROUP_ID="${TK_PROBE_GROUP_ID}"', script)
        self.assertIn('API_KEY_ID="${TK_PROBE_KEY_ID}"', script)
        self.assertIn('API_KEY="${TK_PROBE_KEY}"', script)
        self.assertIn('GROUP_NAME="$(tk_probe_group_name "$PROBE_SCOPE")"', script)
        self.assertIn('KEY_NAME="$(tk_probe_key_name "$PROBE_SCOPE")"', script)
        self.assertNotIn('SELECT id::text\n  FROM groups', script)
        self.assertNotIn('NEW_API_KEY="$(new_probe_api_key)"', script)

    def test_psql_id_capture_is_quiet_and_reports_sql_errors(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn("-X -q -A -t -v ON_ERROR_STOP=1", script)
        self.assertIn("psql_capture_numeric() {", script)
        self.assertIn("2>\"$errfile\"", script)
        self.assertIn("fail_json \"${message}: ${err:-psql failed}\"", script)
        self.assertIn("no numeric id returned", script)

    def test_app_container_auto_resolves_blue_green(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn('APP_CONTAINER="${APP_CONTAINER:-auto}"', script)
        # The resolver itself is the canonical shared owner, not a local copy:
        # a re-introduced hand-rolled loop here is what let a STOPPED container be
        # reported as live. Assert the sourced call, and that no copy came back.
        self.assertIn("resolve-app-container.sh", script)
        self.assertIn('tk_resolve_app_container "$APP_CONTAINER"', script)
        self.assertIn("app container unresolved", script)
        self.assertNotIn("resolve_app_container() {", script)
        self.assertNotIn("for candidate in tokenkey tokenkey-blue tokenkey-green", script)

    def test_reuse_mode_uses_shared_probe_helper(self) -> None:
        script = _SCRIPT.read_text()
        self.assertIn("probe_reserved_resources.sh", script)
        self.assertIn('if [[ "$PROBE_REUSE_MODE" == "1" ]]; then', script)
        self.assertIn("${SCRIPT_DIR}/probe_reserved_resources.sh", script)
        self.assertIn('tk_probe_prepare_platform_reuse_probe "$PLATFORM" "$ACCOUNT_ID"', script)
        self.assertIn('tk_probe_cleanup_named_group "$GROUP_ID" "$GROUP_NAME" "$KEY_NAME" reusable', script)
        self.assertNotIn("tk_probe_unbind_account_from_stale_probe_groups", script)

    def test_oneoff_supported_model_scopes_json_keeps_shell_escaped_quotes(self) -> None:
        script = _SCRIPT.read_text()

        expected = r"""false, '{}'::jsonb, true, '[\"claude\", \"gemini_text\", \"gemini_image\"]'::jsonb,"""
        broken = """false, '{}'::jsonb, true, '["claude", "gemini_text", "gemini_image"]'::jsonb,"""
        self.assertIn(expected, script)
        self.assertNotIn(broken, script)

    def test_rebinding_group_notifies_scheduler_snapshot(self) -> None:
        script = _SCRIPT.read_text()
        self.assertIn('INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)', script)
        self.assertIn("VALUES ('group_changed', NULL, ${GROUP_ID}, NULL);", script)

        cleanup_at = script.index("cleanup() {")
        trap_at = script.index("trap cleanup EXIT", cleanup_at)
        cleanup_sql = script[cleanup_at:trap_at]
        self.assertIn("tk_probe_cleanup_named_group", cleanup_sql)

    def test_probe_script_wires_verdict_module_and_embeddings_endpoint(self) -> None:
        script = _SCRIPT.read_text()

        self.assertIn(
            "messages|count_tokens|chat|responses|embeddings|images|speech",
            script,
        )
        self.assertIn('elif endpoint == "embeddings":', script)
        self.assertIn('"input": prompt', script)
        self.assertIn('embeddings) PATH_SUFFIX="/v1/embeddings"', script)
        self.assertIn('elif endpoint == "speech":', script)
        self.assertIn('else "longanlingxin"', script)
        self.assertIn('speech) PATH_SUFFIX="/v1/audio/speech"', script)
        self.assertIn("PROBE_SCRIPT_DIR", script)
        self.assertIn("from probe_account_model_verdict import classify_probe_verdict", script)
        self.assertIn("probe_account_model_verdict.py", script)


if __name__ == "__main__":
    unittest.main()
