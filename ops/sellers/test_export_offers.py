import copy
import unittest

from ops.sellers.export_offers import build_offers, effective_rate, public_prices


class SellerQuoteTests(unittest.TestCase):
    def test_override_replaces_default_and_zero_is_valid(self):
        snapshot = {"groups": [{"status": "active", "rate_multiplier": 2, "user_rate_multiplier": 0.5}]}
        self.assertEqual(str(effective_rate(snapshot)), "0.5")
        snapshot["groups"][0]["user_rate_multiplier"] = 0
        self.assertEqual(str(effective_rate(snapshot)), "0")
        snapshot["groups"][0]["user_rate_multiplier"] = None
        self.assertEqual(str(effective_rate(snapshot)), "2")

    def test_mixed_group_prices_cannot_be_guessed(self):
        with self.assertRaisesRegex(ValueError, "routing attribution"):
            effective_rate({"groups": [
                {"status": "active", "rate_multiplier": 1, "user_rate_multiplier": 0.5},
                {"status": "active", "rate_multiplier": 1, "user_rate_multiplier": 0.8},
            ]})

    def test_units_cache_tiers_and_peak_conditions(self):
        pricing = {"currency": "USD", "input_per_1k_tokens": "0.002", "cache_write_per_1k": "0.003",
                   "tiers": [{"min_tokens": 32000, "max_tokens": 128000, "input_per_1k_tokens": "0.004"}],
                   "peak_valley": {"timezone": "Asia/Shanghai", "windows": ["09:00-12:00"],
                                   "peak_multiplier": 2, "input_per_1k_tokens": "0.004"}}
        before = copy.deepcopy(pricing)
        rate = effective_rate({"groups": [{"status": "active", "rate_multiplier": 1, "user_rate_multiplier": 0.5}]})
        rows = public_prices(pricing, rate)
        self.assertEqual(rows[0]["cost_usd"], "0.000001")
        self.assertEqual(rows[0]["usd_per_million_tokens"], "1")
        self.assertEqual(rows[1]["type"], "cache_write")
        self.assertEqual(rows[1]["usd_per_million_tokens"], "1.5")
        self.assertEqual(rows[2]["context_tier"], {"min_tokens": 32000, "max_tokens": 128000})
        self.assertEqual(rows[3]["peak_window"]["timezone"], "Asia/Shanghai")
        self.assertEqual(pricing, before)

    def test_or_fallback_is_not_silently_published_as_public_price(self):
        offers = build_offers({
            "captured_at": "example", "user": {"id": 32}, "model_id_prefix": "tokenkey/",
            "groups": [{"status": "active", "rate_multiplier": 1, "user_rate_multiplier": 0.5}],
            "public_pricing": {"data": []},
            "catalog": {"data": [{"id": "tokenkey/unlisted"}]},
        })
        self.assertEqual(offers["models"], [])
        self.assertEqual(offers["excluded_from_public_quote"][0]["model_id"], "tokenkey/unlisted")

    def test_supply_alias_is_held_without_changing_canonical_price(self):
        snapshot = {
            "captured_at": "example", "user": {"id": 32}, "model_id_prefix": "tokenkey/",
            "groups": [{"status": "active", "rate_multiplier": 1, "user_rate_multiplier": 0.5}],
            "public_pricing": {"data": [
                {"model_id": "old", "pricing": {"currency": "USD", "input_per_1k_tokens": "0.001"}},
                {"model_id": "current", "pricing": {"currency": "USD", "input_per_1k_tokens": "0.002"}},
            ]},
            "catalog": {"data": [{"id": "tokenkey/old"}, {"id": "tokenkey/current"}]},
        }
        bundle = {"account_model_mapping": {"account_overrides": [
            {"model_mapping": {"old": "current", "current": "current"}},
        ]}}
        offers = build_offers(snapshot, bundle)
        self.assertEqual([r["source_model_id"] for r in offers["models"]], ["current"])
        self.assertEqual(offers["models"][0]["seller_prices"][0]["usd_per_million_tokens"], "1")
        self.assertEqual(offers["excluded_from_public_quote"][0]["possible_served_models"], ["current"])
        self.assertTrue(offers["mapping_bundle_reviewed"])


if __name__ == "__main__":
    unittest.main()
