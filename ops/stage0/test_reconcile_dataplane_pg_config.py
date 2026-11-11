import unittest

from reconcile_dataplane_pg_config import command_settings, reconcile_overrides


class ReconcileTest(unittest.TestCase):
    def test_only_command_owned_assignments_follow_the_runtime_owner(self):
        command = ['-D', '/pg', '-c', 'max_connections=200', '-c', 'shared_buffers=2GB']
        config = '# max_connections=100\nmax_connections = 100\nshared_buffers=128MB\narchive_mode=on\n'
        result, removed = reconcile_overrides(config, command_settings(command))
        self.assertEqual(removed, ['max_connections', 'shared_buffers'])
        self.assertEqual(result, "# max_connections=100\nmax_connections = '200' # generated from container command\n"
                         "shared_buffers = '2GB' # generated from container command\narchive_mode=on\n")
        self.assertEqual(reconcile_overrides(result, command_settings(command)), (result, []))
