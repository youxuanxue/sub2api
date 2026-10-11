import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

from ops.qa.colocation_resources import load_policy, resource_override, slice_unit


class QAColocationResourcesTest(unittest.TestCase):
    def test_transported_helper_accepts_explicit_policy(self):
        # run-probe transports companions outside the repository layout.
        with tempfile.TemporaryDirectory() as directory:
            source = Path(__file__).with_name('colocation_resources.py')
            target = Path(directory, source.name)
            target.write_text(source.read_text())
            policy_file = Path(directory, 'resource-policy.json')
            policy_file.write_text(json.dumps(load_policy()))
            spec = importlib.util.spec_from_file_location('transported_resources', target)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            self.assertEqual(resource_override(load_policy()),
                             module.resource_override(module.load_policy(policy_file)))

    def test_rejects_overcommit_and_invalid_resource_values(self):
        base = load_policy()
        for scope, key, value in [('worker', 'memory_bytes', base['aggregate']['memory_bytes']),
                                  ('queue', 'cpu_millis', 1000), ('aggregate', 'cpu_millis', 2000),
                                  ('aggregate', 'pids', 257), ('worker', 'pids', -1), ('queue', 'cpu_millis', True)]:
            with self.subTest(scope=scope, key=key, value=value), tempfile.TemporaryDirectory() as directory:
                changed = copy.deepcopy(base)
                changed[scope][key] = value
                path = Path(directory, 'policy.json')
                path.write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    load_policy(path)

    def test_resource_override_shares_budget_and_disables_swap(self):
        policy = load_policy()
        services = resource_override(policy)['services']
        self.assertEqual(sum(s['cpus'] for s in services.values()), policy['aggregate']['cpu_millis'] / 1000)
        self.assertEqual(sum(s['mem_limit'] for s in services.values()), policy['aggregate']['memory_bytes'])
        self.assertEqual({s['cgroup_parent'] for s in services.values()}, {'tokenkey-qa.slice'})
        for service in services.values():
            self.assertEqual(service['mem_limit'], service['memswap_limit'])
        self.assertEqual(services['qa-worker']['stop_grace_period'], '56m')
        self.assertIn('MemorySwapMax=0', slice_unit(policy))


class QAColocationSandboxTest(unittest.TestCase):
    def run_sandbox(self, scenario):
        import contextlib
        import io
        import subprocess
        from types import SimpleNamespace
        from unittest.mock import patch

        script = Path(__file__).with_name('verify_colocation_sandbox.sh').read_text()
        code = script.split("python3 - <<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
        state = {'exists': False, 'removed': False, 'docker_calls': 0}
        output = io.StringIO()

        def run(args, **kwargs):
            stdout = ''
            if args == ['hostname']:
                stdout = 'another-host' if scenario == 'wrong-host' else 'tokenkey-edge-uk1-hz-cax21'
            else:
                state['docker_calls'] += 1
                action = args[1]
                if action == 'create':
                    state['exists'] = True
                    state['name'] = args[args.index('--name') + 1]
                    if scenario == 'create-timeout':
                        raise subprocess.TimeoutExpired(args, 45)
                elif action == 'start':
                    stdout = 'container_boundary_checks_passed'
                elif action == 'ps':
                    stdout = 'synthetic-id' if state['exists'] else ''
                elif action == 'rm':
                    # A successful receipt must never precede confirmed cleanup.
                    self.assertEqual(output.getvalue(), '')
                    if scenario == 'cleanup-failed':
                        raise subprocess.CalledProcessError(1, args)
                    state.update(exists=False, removed=True)
                elif action == 'inspect':
                    if args[-1] == 'tokenkey':
                        stdout = 'sha256:synthetic'
                    elif '--format' in args:
                        stdout = 'different-owner' if scenario == 'unowned' else state['name']
                    else:
                        stdout = json.dumps([{'State': {'ExitCode': 0}, 'Mounts': [], 'HostConfig': {'Binds': None}}])
                else:
                    self.fail(f'unexpected Docker operation: {action}')
            return SimpleNamespace(stdout=stdout, stderr='', returncode=0)

        error = None
        with patch('subprocess.run', side_effect=run), contextlib.redirect_stdout(output):
            try:
                exec(compile(code, str(Path(__file__).with_name('verify_colocation_sandbox.sh')), 'exec'), {})
            except (SystemExit, RuntimeError, subprocess.SubprocessError) as exc:
                error = exc
        return state, output.getvalue(), error

    def test_target_guard_precedes_docker(self):
        state, output, error = self.run_sandbox('wrong-host')
        self.assertIsInstance(error, SystemExit)
        self.assertEqual(state['docker_calls'], 0)
        self.assertEqual(output, '')

    def test_create_timeout_still_removes_owned_container(self):
        import subprocess
        state, output, error = self.run_sandbox('create-timeout')
        self.assertIsInstance(error, subprocess.TimeoutExpired)
        self.assertTrue(state['removed'])
        self.assertFalse(state['exists'])
        self.assertEqual(output, '')

    def test_cleanup_failures_never_report_success_or_remove_foreign_container(self):
        for scenario in ('cleanup-failed', 'unowned'):
            with self.subTest(scenario=scenario):
                state, output, error = self.run_sandbox(scenario)
                self.assertIsNotNone(error)
                self.assertFalse(state['removed'])
                self.assertEqual(output, '')

    def test_success_receipt_follows_cleanup(self):
        state, output, error = self.run_sandbox('success')
        self.assertIsNone(error)
        self.assertTrue(state['removed'])
        self.assertEqual(json.loads(output)['cleaned_up'], True)


if __name__ == '__main__':
    unittest.main()
