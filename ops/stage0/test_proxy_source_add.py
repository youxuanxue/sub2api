import unittest
from ops.stage0.proxy_source_add import patched_unit


class ProxySourceTest(unittest.TestCase):
    def test_source_addition_preserves_old_sources_and_is_idempotent(self):
        text = "ExecStart=/bin/sh -c '/usr/sbin/iptables -A TK_CN_PROXY -s 43.162.83.233/32 -j ACCEPT; /usr/sbin/iptables -A TK_CN_PROXY -s 154.8.214.179/32 -j ACCEPT; /usr/sbin/iptables -A TK_CN_PROXY -j DROP;'\n"
        result = patched_unit(text, '43.162.126.44', '43.162.83.233')
        self.assertIn('-s 43.162.83.233/32 -j ACCEPT;', result)
        self.assertIn('-s 154.8.214.179/32 -j ACCEPT;', result)
        self.assertIn('-s 43.162.126.44/32 -j ACCEPT;', result)
        self.assertEqual(result.count('-j DROP;'), 1)
        self.assertEqual(patched_unit(result, '43.162.126.44', '43.162.83.233'), result)
        for bad in ('', text.replace('43.162.83.233', '1.1.1.1'), text + text):
            with self.assertRaises(ValueError):
                patched_unit(bad, '43.162.126.44', '43.162.83.233')
        with self.assertRaises(ValueError):
            patched_unit(text, '1.2.3.4; false', '43.162.83.233')
