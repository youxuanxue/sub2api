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
                                  ('worker', 'pids', -1), ('queue', 'cpu_millis', True)]:
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


if __name__ == '__main__':
    unittest.main()
