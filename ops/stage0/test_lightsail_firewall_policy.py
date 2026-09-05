#!/usr/bin/env python3
from __future__ import annotations

import unittest

from ops.stage0.lightsail_firewall_policy import classify_ssh_posture


def _ssh_state(cidrs: list[str], *, ipv6: list[str] | None = None) -> dict[str, object]:
    return {
        "fromPort": 22,
        "toPort": 22,
        "protocol": "tcp",
        "state": "open",
        "cidrs": cidrs,
        "ipv6Cidrs": ipv6 or [],
        "cidrListAliases": [],
    }


class LightsailFirewallPolicyTest(unittest.TestCase):
    def test_accepts_closed_or_single_public_ipv4_host_rule(self) -> None:
        self.assertEqual(classify_ssh_posture({"portStates": []}), "closed")
        self.assertEqual(
            classify_ssh_posture({"portStates": [_ssh_state(["123.118.109.225/32"])]}),
            "restricted",
        )

    def test_rejects_world_private_ipv6_or_multiple_ssh_sources(self) -> None:
        unsafe = (
            [_ssh_state(["0.0.0.0/0"])],
            [_ssh_state(["10.0.0.1/32"])],
            [_ssh_state(["123.118.109.225/32", "8.8.8.8/32"])],
            [_ssh_state(["123.118.109.225/32"], ipv6=["::/0"])],
            [
                _ssh_state(["123.118.109.225/32"]),
                _ssh_state(["8.8.8.8/32"]),
            ],
        )
        for states in unsafe:
            with self.subTest(states=states):
                self.assertEqual(
                    classify_ssh_posture({"portStates": states}),
                    "unsafe",
                )


if __name__ == "__main__":
    unittest.main()
