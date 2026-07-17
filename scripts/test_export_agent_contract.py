import tempfile
import unittest
from pathlib import Path

from scripts import export_agent_contract


class RetiredRouteContractTest(unittest.TestCase):
    def test_prune_retired_route_removes_only_tombstoned_bullet(self) -> None:
        doc = """\
- `GET /payment/channels` from `stale/generated/source.go`
- `GET /payment/checkout-info` from `backend/internal/server/routes/payment.go`
"""

        self.assertEqual(
            export_agent_contract.prune_retired_route_bullets(doc),
            "- `GET /payment/checkout-info` from `backend/internal/server/routes/payment.go`\n",
        )

    def test_source_registration_of_retired_route_is_rejected(self) -> None:
        route = {
            "method": "GET",
            "path": "/payment/channels",
            "source": "routes/payment.go",
            "source_literal": "/channels",
            "replacement": "/payment/checkout-info",
        }
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / route["source"]
            source.parent.mkdir(parents=True)
            source.write_text('authenticated.GET("/channels", handler)\n', encoding="utf-8")

            self.assertEqual(
                export_agent_contract.find_retired_source_registrations(root, (route,)),
                [route],
            )


if __name__ == "__main__":
    unittest.main()
