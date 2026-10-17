"""Content delta planning: updates/deletions/late lower IDs must not be lost."""
import copy
import contextlib
import io
import unittest
from unittest.mock import patch

from ops.migration.fresh_host_delta import candidate_guard, changes, ident, main, predicate


class DeltaPlanTests(unittest.TestCase):
    def setUp(self):
        self.base = {"version": 1, "width": 100000, "schema": "same-schema", "tables": {
            "users": [{"bucket": 0, "rows": 1, "digest": ["100"]}],
            "usage_billing_dedup": [
                {"bucket": 0, "rows": 2, "digest": ["200"]},
                {"bucket": 4, "rows": 1, "digest": ["400"]}],
        }}

    def test_same_count_balance_update_refreshes_users(self):
        source = copy.deepcopy(self.base)
        source["tables"]["users"][0]["digest"] = ["101"]
        self.assertEqual(changes(self.base, source), [{"table": "users", "bucket": None}])

    def test_late_lower_id_and_deleted_tail_are_both_replaced(self):
        source = copy.deepcopy(self.base)
        source["tables"]["usage_billing_dedup"] = [{"bucket": 0, "rows": 3, "digest": ["201"]}]
        self.assertEqual(changes(self.base, source), [
            {"table": "usage_billing_dedup", "bucket": 0},
            {"table": "usage_billing_dedup", "bucket": 4}])
        self.assertEqual(predicate({"table": "usage_billing_dedup", "bucket": -1}),
                         "id >= -100000 AND id < 0")

    def test_schema_drift_and_unsafe_identifiers_fail_closed(self):
        source = copy.deepcopy(self.base)
        source["schema"] = "new-column"
        with self.assertRaisesRegex(ValueError, "schema changed"):
            changes(self.base, source)
        with self.assertRaises(ValueError):
            ident('users"; DROP DATABASE tokenkey;--')

    def test_live_source_can_never_be_apply_target(self):
        with patch('ops.migration.fresh_host_delta.socket.gethostname', return_value='tokenkey-prod-hz-cax21'):
            with self.assertRaisesRegex(RuntimeError, "exact fresh AWS candidate"):
                candidate_guard('tokenkey-prod-tokyo-20261017')

    def test_all_table_scope_cannot_export_or_apply(self):
        for operation in ('export', 'apply'):
            stderr = io.StringIO()
            with patch('sys.argv', ['delta', operation, '--scope', 'all']), contextlib.redirect_stderr(stderr):
                with self.assertRaises(SystemExit) as exc:
                    main()
            self.assertEqual(exc.exception.code, 2)
            self.assertIn('read-only manifest audit', stderr.getvalue())


if __name__ == '__main__':
    unittest.main()
