import io
import json
import unittest
from unittest.mock import patch

from ops.sellers.seller_snapshot_remote import ensure_platform_keys


def existing(name, status="active", routing_mode="universal"):
    return {"id": 1, "name": name, "status": status, "routing_mode": routing_mode, "group_id": None}


class SellerKeyTests(unittest.TestCase):
    @patch("ops.sellers.seller_snapshot_remote.query")
    def test_existing_keys_are_reused_without_writes(self, query):
        ensure_platform_keys(32, [existing(n) for n in ("nanogpt", "poe", "huggingface", "eurouter")])
        query.assert_not_called()

    @patch("ops.sellers.seller_snapshot_remote.query")
    def test_disabled_or_duplicate_name_is_not_replaced(self, query):
        for keys in ([existing("poe", status="disabled")], [existing("poe"), existing("poe")]):
            with self.assertRaises(RuntimeError):
                ensure_platform_keys(32, keys)
        query.assert_not_called()

    @patch("ops.sellers.seller_snapshot_remote.urllib.request.urlopen")
    @patch("ops.sellers.seller_snapshot_remote.query", return_value="test-admin-credential")
    def test_only_missing_universal_key_is_created(self, query, urlopen):
        urlopen.return_value = io.StringIO(json.dumps({"data": {"id": 100, "key": "not-returned"}}))
        result = ensure_platform_keys(32, [existing(n) for n in ("nanogpt", "poe", "huggingface")])
        self.assertIsNone(result)
        request = urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "https://tokenkey.dev/api/v1/admin/users/32/api-keys")
        self.assertEqual(request.method, "POST")
        self.assertEqual(json.loads(request.data), {"name": "eurouter", "routing_mode": "universal"})
        self.assertEqual(urlopen.call_count, 1)


if __name__ == "__main__":
    unittest.main()
