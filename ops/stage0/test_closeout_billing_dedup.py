import unittest
from unittest.mock import patch
from pathlib import Path

import closeout_billing_dedup as closeout


class DedupCloseoutTest(unittest.TestCase):
    def test_multiline_pg_json_preserves_every_index(self):
        with patch.object(closeout, 'psql', return_value='SET\n[{"name":"a"}, \n {"name":"b"}]'):
            self.assertEqual(closeout.query('select json_agg(t)'), [{'name': 'a'}, {'name': 'b'}])

    def test_owner_change_without_archive_conflict_protection_is_rejected(self):
        root = Path(__file__).resolve().parents[2]
        repository = (root / 'backend/internal/repository/dashboard_aggregation_repo.go').read_text()
        policy = (root / 'backend/internal/service/data_lifecycle_policy_tk.go').read_text()
        with self.assertRaises(ValueError):
            closeout.owner_plan(repository.replace('ON CONFLICT (request_id, api_key_id) DO NOTHING', ''), policy)

    def test_hot_purge_cannot_target_the_archive_table(self):
        root = Path(__file__).resolve().parents[2]
        source = (root / 'backend/internal/repository/dashboard_aggregation_repo_tk_dedup_archive.go').read_text()
        bad = source.replace('DELETE FROM usage_billing_dedup\n', 'DELETE FROM usage_billing_dedup_archive\n')
        with self.assertRaises(ValueError):
            closeout.purge_plan(bad)
