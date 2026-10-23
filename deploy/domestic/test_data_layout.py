"""Root-disk data directory classification. Does not touch a live host."""
import subprocess
import tempfile
import unittest
from pathlib import Path

from deploy.domestic import data_layout as dl


class DataLayoutTest(unittest.TestCase):
    def test_mountpoint_accepts_volume_or_same_root_device_only(self):
        self.assertEqual(dl.layout_from_mountpoint(0, 1, 2), 'volume')
        self.assertEqual(dl.layout_from_mountpoint(32, 5, 5), 'root')
        for case in ((32, 5, 6), (1, 5, 5), (32, None, 5), (32, 5, None)):
            with self.subTest(case=case), self.assertRaisesRegex(ValueError, 'root filesystem'):
                dl.layout_from_mountpoint(*case)

    def test_findmnt_accepts_the_data_path_or_root_and_rejects_other_targets(self):
        self.assertEqual(dl.layout_from_findmnt('/var/lib/tokenkey\n', '/var/lib/tokenkey'), 'volume')
        self.assertEqual(dl.layout_from_findmnt('/\n', '/var/lib/tokenkey'), 'root')
        with self.assertRaisesRegex(ValueError, 'not mounted'):
            dl.layout_from_findmnt('/mnt/other\n', '/var/lib/tokenkey')
        with self.assertRaisesRegex(ValueError, 'not mounted'):
            dl.layout_from_findmnt('', '/var/lib/tokenkey')

    def test_symlink_is_refused_before_mountpoint(self):
        with tempfile.TemporaryDirectory() as temp:
            real = Path(temp) / 'real'
            real.mkdir()
            link = Path(temp) / 'link'
            link.symlink_to(real)

            def run(*_args, **_kwargs):
                raise AssertionError('mountpoint must not be used to bless a symlink')

            with self.assertRaisesRegex(ValueError, 'missing'):
                dl.require_directory(link, run=run)

    def test_root_disk_directory_is_accepted_without_a_fake_mount(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)
            calls = []

            def run(args, **_kwargs):
                calls.append(list(args))
                return subprocess.CompletedProcess(args, 32, '', '')

            self.assertEqual(
                dl.require_directory(path, run=run, device=lambda _item: 9),
                'root',
            )
            self.assertEqual(calls, [['mountpoint', '-q', str(path)]])

    def test_alert_plan_omits_data_volume_on_root_and_keeps_it_for_a_mount(self):
        script = Path(__file__).with_name('install-host-alerts.sh')
        for layout, present in (('root', False), ('volume', True)):
            result = subprocess.run(
                ['bash', str(script), layout],
                env={'TOKENKEY_ALERT_INSTALL_PLAN': '1', 'PATH': '/usr/bin:/bin'},
                text=True, capture_output=True, check=False,
            )
            with self.subTest(layout=layout):
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh', result.stdout)
                self.assertEqual(
                    'TOKENKEY_DATA_VOLUME_PATH=/var/lib/tokenkey' in result.stdout,
                    present,
                )
        refused = subprocess.run(
            ['bash', str(script), 'tmpfs'],
            env={'TOKENKEY_ALERT_INSTALL_PLAN': '1', 'PATH': '/usr/bin:/bin'},
            text=True, capture_output=True, check=False,
        )
        self.assertNotEqual(refused.returncode, 0)
        self.assertIn('refusing data layout', refused.stderr)


if __name__ == '__main__':
    unittest.main()
