import json
import unittest

from ops.migration.standard_freeze_delta import (
    MAX_FREEZE_SECONDS,
    append_only_guard_sql,
    plan,
)


class StandardFreezeDeltaTest(unittest.TestCase):
    def test_plan_is_small_and_schema_first(self):
        result = plan("schema-a", "schema-a", {"dedup_max_id": 42}, freeze_seconds=600)
        self.assertEqual(result["core_oltp_tables"], ["accounts", "users", "api_keys", "groups", "settings"])
        self.assertEqual(result["dedup_strategy"], "full_refresh")
        self.assertIn("--table=public.usage_billing_dedup_archive", result["commands"]["dedup_dump"])
        self.assertNotIn("COMMIT", result["commands"]["dedup_full_reload"])
        self.assertFalse(result["runtime_deadline_implemented"])
        self.assertIsNone(result["commands"]["dedup_delta"])
        self.assertTrue(result["requires_manual_cutover"])

    def test_append_only_requires_explicit_evidence(self):
        with self.assertRaisesRegex(ValueError, "explicit verification"):
            plan("a", "a", {"dedup_max_id": 42}, dedup_strategy="append_only")
        with self.assertRaisesRegex(ValueError, "candidate_dedup_count"):
            plan("a", "a", {"dedup_max_id": 42}, dedup_strategy="append_only", append_only_verified=True)
        with self.assertRaisesRegex(ValueError, "candidate_archive_count"):
            plan("a", "a", {"dedup_max_id": 42, "candidate_dedup_count": 7}, dedup_strategy="append_only", append_only_verified=True)
        result = plan("a", "a", {"dedup_max_id": 42, "candidate_dedup_count": 7, "candidate_archive_count": 0}, dedup_strategy="append_only", append_only_verified=True)
        self.assertIn("id > 42", result["commands"]["dedup_delta"])
        self.assertIn("prefix_count_matches", result["commands"]["append_only_guard"])

    def test_oltp_dump_excludes_logs_and_dedup(self):
        args = plan("a", "a", {"dedup_max_id": 1})["commands"]["oltp_dump"]
        self.assertIn("--exclude-table-data=usage_logs*", args)
        self.assertIn("--exclude-table-data=public.usage_billing_dedup", args)

    def test_schema_gate_excludes_intentionally_unsynced_qa_raw_partitions(self):
        self.assertIn("table_name NOT LIKE 'qa_records%'", plan("a", "a", {"dedup_max_id": 1})["commands"]["schema_check"])

    def test_schema_mismatch_rejects_before_any_data_plan(self):
        with self.assertRaisesRegex(ValueError, "schema mismatch"):
            plan("schema-a", "schema-b", {"dedup_max_id": 42})

    def test_freeze_window_is_bounded(self):
        with self.assertRaises(ValueError):
            plan("a", "a", {"dedup_max_id": 1}, freeze_seconds=MAX_FREEZE_SECONDS + 1)
        with self.assertRaises(ValueError):
            plan("a", "a", {"dedup_max_id": 1}, freeze_seconds=0)

    def test_append_only_guard_checks_old_prefix(self):
        sql = append_only_guard_sql(123)
        self.assertIn("id <= 123", sql)
        self.assertIn("id > 123", sql)
        with self.assertRaises(ValueError):
            append_only_guard_sql(-1)


if __name__ == "__main__":
    unittest.main()
