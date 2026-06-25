import argparse
import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("reconcile-served-models.py")
SPEC = importlib.util.spec_from_file_location("reconcile_served_models", SCRIPT)
RSM = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = RSM
SPEC.loader.exec_module(RSM)


class ReconcileServedModelsTest(unittest.TestCase):
    def test_probe_variant_and_verdict_aggregation(self):
        model, variant = RSM.normalize_probe_model("qwen3-8b (thinking)")
        self.assertEqual((model, variant), ("qwen3-8b", "thinking"))

        agg = RSM.ProbeAggregate("newapi", "qwen3-8b")
        agg.add("429", "not_allowlisted")
        self.assertEqual(agg.status, "mapping_gap")
        agg.add("200", "servable", "thinking")
        self.assertEqual(agg.status, "servable")

    def test_plan_classifies_candidate_price_probe_and_mirror_drift(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            upstream = root / "upstream.json"
            upstream.write_text(json.dumps({
                "models": [
                    {"id": "qwen-new", "pricing_status": "priced"},
                    {"id": "qwen-missing-price", "pricing_status": "missing"},
                    {"id": "qwen-unprobed", "pricing_status": "priced"},
                ]
            }), encoding="utf-8")
            probe = root / "probe.tsv"
            probe.write_text(
                "newapi\tqwen-new (thinking)\t200\tservable\n"
                "newapi\tqwen-missing-price\t429\tnot_allowlisted\n",
                encoding="utf-8",
            )
            live = root / "live.json"
            live.write_text(json.dumps({
                "60": {
                    "name": "Qwen",
                    "platform": "newapi",
                    "channel_type": 17,
                    "model_mapping": {
                        "qwen-turbo": "qwen-turbo",
                        "qwen-extra": "qwen-extra",
                    },
                },
                "72": {
                    "name": "Qwen-2",
                    "platform": "newapi",
                    "channel_type": 17,
                    "model_mapping": {"qwen-turbo": "qwen-turbo"},
                },
            }), encoding="utf-8")

            plan = RSM.build_plan(argparse.Namespace(
                upstream=[f"60:{upstream}"],
                account_id=None,
                candidate=[],
                probe_results=[str(probe)],
                live_mapping=str(live),
                mirror=["60:72"],
                strict_manifest=False,
                format="json",
            ))

        self.assertEqual([x["model_id"] for x in plan["ready_for_onboard"]], ["qwen-new"])
        self.assertEqual([x["model_id"] for x in plan["price_missing"]], ["qwen-missing-price"])
        self.assertEqual([x["model_id"] for x in plan["probe_needed"]], ["qwen-unprobed"])
        self.assertEqual(plan["probe_commands"][0]["env"], "DASHSCOPE_CHAT_MODELS")
        self.assertEqual(plan["mirror_drift"][0]["missing_in_target"], ["qwen-extra"])


if __name__ == "__main__":
    unittest.main()
