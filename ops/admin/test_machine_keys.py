import contextlib
import importlib.util
import io
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import stat
import tempfile
import threading
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("machine_keys", Path(__file__).with_name("machine-keys.py"))
machine_keys = importlib.util.module_from_spec(spec)
spec.loader.exec_module(machine_keys)


class MachineKeyCLI(unittest.TestCase):
    def test_create_writes_private_key_without_stdout_disclosure(self):
        with tempfile.TemporaryDirectory() as directory:
            dest = Path(directory) / "key"
            out = io.StringIO()
            issued = {"credential": {"id": "a" * 32, "name": "test"}, "key": "synthetic-machine-secret"}
            with patch.dict(os.environ, {"TOKENKEY_ADMIN_JWT": "synthetic-session"}), patch.object(machine_keys, "request", return_value=issued), contextlib.redirect_stdout(out):
                code = machine_keys.main(["--base-url", "https://admin.example.test", "create", "--name", "test", "--scope", "accounts:read", "--key-file", str(dest)])
            self.assertEqual(code, 0)
            self.assertEqual(dest.read_text(), issued["key"] + "\n")
            self.assertEqual(stat.S_IMODE(dest.stat().st_mode), 0o600)
            self.assertEqual(json.loads(out.getvalue()), issued["credential"])
            self.assertNotIn(issued["key"], out.getvalue())

    def test_existing_file_and_symlink_never_issue_or_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            dest = Path(directory) / "key"
            dest.write_text("keep")
            link = Path(directory) / "link"
            link.symlink_to(dest)
            with patch.dict(os.environ, {"TOKENKEY_ADMIN_JWT": "session"}), patch.object(machine_keys, "request") as request, contextlib.redirect_stderr(io.StringIO()):
                for path in (dest, link):
                    code = machine_keys.main(["--base-url", "https://admin.example.test", "create", "--name", "test", "--scope", "accounts:read", "--key-file", str(path)])
                    self.assertEqual(code, 1)
                request.assert_not_called()
            self.assertEqual(dest.read_text(), "keep")

    def test_rejects_remote_http(self):
        with patch.dict(os.environ, {"TOKENKEY_ADMIN_JWT": "session"}), patch.object(machine_keys, "request") as request, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):
                machine_keys.main(["--base-url", "http://admin.example.test", "list"])
            request.assert_not_called()

    def test_redirect_does_not_receive_authorization(self):
        seen = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                seen.append(self.path)
                self.send_response(302)
                self.send_header("Location", "/capture")
                self.end_headers()

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with self.assertRaisesRegex(RuntimeError, "HTTP 302"):
                machine_keys.request(f"http://127.0.0.1:{server.server_port}", "synthetic-secret-session", "GET")
            self.assertEqual(seen, ["/api/v1/admin/settings/machine-admin-keys"])
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
