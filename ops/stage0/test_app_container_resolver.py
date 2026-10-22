#!/usr/bin/env python3
"""SSM unique-running snippets must ignore stopped containers."""

from __future__ import annotations

import importlib.util
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]


def _load(name: str, path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class UniqueRunningSnippetTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.resolver = _load("resolve_app_container", ROOT / "ops/lib/resolve_app_container.py")
        cls.check = _load("app_container_resolver_check", ROOT / "scripts/checks/app-container-resolver.py")

    def run_snippet(self, states: dict[str, str], *, candidates: tuple[str, ...], variable: str) -> str:
        lines = self.resolver.remote_unique_running_snippet(
            docker="docker",
            variable=variable,
            candidates=candidates,
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fakebin = root / "bin"
            fakebin.mkdir()
            docker = fakebin / "docker"
            docker.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env bash
                    set -u
                    name="${@: -1}"
                    state_var="STATE_$(printf '%s' "$name" | tr '-' '_' | tr '[:lower:]' '[:upper:]')"
                    state="${!state_var:-missing}"
                    case "$state" in
                      running) printf '%s\\n' true ;;
                      stopped) printf '%s\\n' false ;;
                      *) exit 1 ;;
                    esac
                    """
                ),
                encoding="utf-8",
            )
            docker.chmod(0o755)
            script = root / "snippet.sh"
            script.write_text("#!/usr/bin/env bash\nset -u\n" + "\n".join(lines) + f'\nprintf "%s\\n" "${variable}"\n', encoding="utf-8")
            script.chmod(0o755)
            env = {**os.environ, "PATH": f"{fakebin}:{os.environ.get('PATH', '')}"}
            for name, state in states.items():
                env["STATE_" + name.replace("-", "_").upper()] = state
            proc = subprocess.run(["bash", str(script)], env=env, capture_output=True, text=True, check=False)
            self.assertEqual(proc.returncode, 0, msg=proc.stderr)
            return proc.stdout.strip()

    def test_stopped_preferred_name_loses_to_the_running_candidate(self) -> None:
        chosen = self.run_snippet(
            {"tokenkey-postgres": "stopped", "tk-archive-standby": "running"},
            candidates=self.resolver.POSTGRES_CANDIDATES,
            variable="PG_CONTAINER",
        )
        self.assertEqual(chosen, "tk-archive-standby")

    def test_zero_or_two_running_candidates_leave_the_variable_empty(self) -> None:
        empty_both = self.run_snippet(
            {"tokenkey-postgres": "running", "tk-archive-standby": "running"},
            candidates=self.resolver.POSTGRES_CANDIDATES,
            variable="PG_CONTAINER",
        )
        empty_none = self.run_snippet(
            {"tokenkey-postgres": "stopped", "tk-archive-standby": "missing"},
            candidates=self.resolver.POSTGRES_CANDIDATES,
            variable="PG_CONTAINER",
        )
        self.assertEqual(empty_both, "")
        self.assertEqual(empty_none, "")

    def test_postgres_candidates_match_the_shell_owner(self) -> None:
        shell = (ROOT / "ops/lib/resolve-app-container.sh").read_text(encoding="utf-8")
        self.assertIn(
            'TK_POSTGRES_CONTAINER_CANDIDATES="tokenkey-postgres tk-archive-standby"',
            shell,
        )
        self.assertEqual(self.resolver.POSTGRES_CANDIDATES, ("tokenkey-postgres", "tk-archive-standby"))
        self.assertEqual(self.resolver.REDIS_CANDIDATES, ("tokenkey-redis", "tk-cutover-prod-redis"))

    def test_daily_diagnostics_renders_the_snippet_instead_of_an_existence_loop(self) -> None:
        workflow = (ROOT / ".github/workflows/ops-daily-diagnostics.yml").read_text(encoding="utf-8")
        self.assertEqual(workflow.count("remote_unique_running_snippet"), 3)
        self.assertIsNone(self.check.data_container_selection_copy(workflow))
        self.assertIsNone(self.check._assert_data_container_detector())

    def test_resolver_owner_check_accepts_the_repo(self) -> None:
        self.assertEqual(self.check.main(), 0)


if __name__ == "__main__":
    unittest.main()
