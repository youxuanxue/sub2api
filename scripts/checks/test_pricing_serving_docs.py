from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("pricing-serving-docs.py")
SPEC = importlib.util.spec_from_file_location("pricing_serving_docs", SCRIPT)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PricingServingDocsContractTest(unittest.TestCase):
    def fixture(self) -> Path:
        root = Path(tempfile.mkdtemp())
        approved = root / "docs/approved"
        approved.mkdir(parents=True)
        (approved / "pricing-serving-single-source-of-truth.md").write_text(
            "---\n"
            "status: approved\n"
            "related_prs: [1821]\n"
            "related_design: docs/approved/pricing-availability-source-of-truth.md, docs/approved/protocol-routing-ssot.md\n"
            "supersedes: pricing-availability-source-of-truth.md serving-owner claims\n"
            "---\n"
            "新执行由 RequestPlan 规划；异步任务由 TaskContinuation 按提交时绑定续接。\n",
            encoding="utf-8",
        )
        (approved / "pricing-availability-source-of-truth.md").write_text(
            "---\n"
            "status: approved\n"
            "superseded_by: docs/approved/pricing-serving-single-source-of-truth.md\n"
            "---\n",
            encoding="utf-8",
        )
        (approved / "protocol-routing-ssot.md").write_text(
            "---\n"
            "status: approved\n"
            "---\n"
            "只治理 generation endpoint capability 与 protocol route。\n",
            encoding="utf-8",
        )
        (approved / "universal-key-capability-discovery.md").write_text(
            "video discovery 只投影 submit 能力；fetch/status 由已有 task id 与 ownership 决定。\n",
            encoding="utf-8",
        )
        return root

    def test_accepts_one_way_delivery_to_protocol_link(self) -> None:
        self.assertEqual(MODULE.check(self.fixture()), [])

    def test_rejects_missing_availability_supersession(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/pricing-availability-source-of-truth.md"
        path.write_text(path.read_text(encoding="utf-8").replace(
            "superseded_by: docs/approved/pricing-serving-single-source-of-truth.md\n", ""
        ), encoding="utf-8")
        self.assertTrue(any("superseded_by" in error for error in MODULE.check(root)))

    def test_rejects_reverse_protocol_link_to_delivery_design(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/protocol-routing-ssot.md"
        path.write_text(
            path.read_text(encoding="utf-8").replace(
                "status: approved\n",
                "status: approved\n"
                "related_design: docs/approved/pricing-serving-single-source-of-truth.md\n",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("one-way" in error for error in MODULE.check(root)))

    def test_rejects_reverse_protocol_body_link_to_delivery_design(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/protocol-routing-ssot.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "See docs/approved/pricing-serving-single-source-of-truth.md for the owner.\n",
            encoding="utf-8",
        )
        self.assertTrue(any("one-way" in error for error in MODULE.check(root)))

    def test_rejects_protocol_copy_of_delivery_formula(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/protocol-routing-ssot.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "CatalogPolicy + RequestPlan + RuntimeReadiness defines delivery.\n",
            encoding="utf-8",
        )
        errors = MODULE.check(root)
        self.assertTrue(
            any("delivery formula" in error or "delivery term" in error for error in errors)
        )

    def test_rejects_protocol_use_of_delivery_terms(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/protocol-routing-ssot.md"
        path.write_text(
            path.read_text(encoding="utf-8") + "Then apply RuntimeReadiness gates.\n",
            encoding="utf-8",
        )
        self.assertTrue(any("delivery term RuntimeReadiness" in error for error in MODULE.check(root)))

    def test_rejects_claude_copy_of_delivery_formula(self) -> None:
        root = self.fixture()
        path = root / "CLAUDE.md"
        path.write_text(
            "Model delivery SSOT（CatalogPolicy + RequestPlan + RuntimeReadiness）\n",
            encoding="utf-8",
        )
        self.assertTrue(any("CLAUDE.md" in error and "delivery formula" in error for error in MODULE.check(root)))

    def test_rejects_old_two_fact_price_vocabulary(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/priced-or-it-doesnt-ship.md"
        path.write_text("家族 floor 是对 PRICE 事实的估计，只**读**两个事实。\n", encoding="utf-8")
        self.assertTrue(any("secondary delivery-truth claim" in error for error in MODULE.check(root)))

    def test_rejects_priced_displayable_ssot_slogan(self) -> None:
        root = self.fixture()
        path = root / "backend/internal/service/pricing_catalog_supported_models_tk.go"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("// intersected with public priced+displayable SSOT\n", encoding="utf-8")
        self.assertTrue(any("priced+displayable" in error for error in MODULE.check(root)))

    def test_rejects_served_plus_priced_slogan(self) -> None:
        root = self.fixture()
        path = root / ".cursor/skills/tokenkey-onboard-model/SKILL.md"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("# 上架一个模型（served + priced）\n", encoding="utf-8")
        self.assertTrue(any("served + priced" in error for error in MODULE.check(root)))

    def test_rejects_availability_copy_of_delivery_formula(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/pricing-availability-source-of-truth.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "CatalogPolicy + RequestPlan + RuntimeReadiness = delivery.\n",
            encoding="utf-8",
        )
        self.assertTrue(any("delivery formula" in error for error in MODULE.check(root)))

    def test_rejects_main_doc_without_1821_relation(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/pricing-serving-single-source-of-truth.md"
        path.write_text(path.read_text(encoding="utf-8").replace("[1821]", "[]"), encoding="utf-8")
        self.assertTrue(any("#1821" in error for error in MODULE.check(root)))

    def test_rejects_secondary_client_facing_serving_truth(self) -> None:
        root = self.fixture()
        path = root / "backend/internal/service/pricing_catalog_candidates_tk.go"
        path.parent.mkdir(parents=True)
        path.write_text(
            "// ServableClientFacingIDs is the SINGLE client-facing servable truth.\n",
            encoding="utf-8",
        )
        self.assertTrue(any("secondary delivery-truth claim" in error for error in MODULE.check(root)))

    def test_rejects_universal_capability_as_second_ssot(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/universal-key-capability-discovery.md"
        path.write_text("3. **一个能力 SSOT。** 网站只投影这个真值。\n", encoding="utf-8")
        self.assertTrue(any("secondary delivery-truth claim" in error for error in MODULE.check(root)))

    def test_rejects_priced_servable_slogan_in_claude(self) -> None:
        root = self.fixture()
        path = root / "CLAUDE.md"
        path.write_text(
            "official upstream aliases display when priced+servable\n",
            encoding="utf-8",
        )
        self.assertTrue(any("secondary delivery-truth claim" in error for error in MODULE.check(root)))

    def test_rejects_priced_plus_servable_chinese_slogan(self) -> None:
        root = self.fixture()
        path = root / ".cursor/skills/tokenkey-modelops-planner/SKILL.md"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("已核实 有价 + 可服务，则必须进入公开 catalog\n", encoding="utf-8")
        self.assertTrue(any("secondary delivery-truth claim" in error for error in MODULE.check(root)))

    def test_rejects_onion_layers_as_delivery_truth(self) -> None:
        root = self.fixture()
        path = root / "docs/all-platform-model-inventory.md"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("TokenKey 的完整目录是一个四层洋葱\n", encoding="utf-8")
        self.assertTrue(any("secondary delivery-truth claim" in error for error in MODULE.check(root)))

    def test_rejects_intersection_slogan_outside_the_old_file_list(self) -> None:
        root = self.fixture()
        path = root / "backend/internal/handler/gateway_handler.go"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("// TK: filter to priced ∩ ¬unreachable (Goal 2, R-003).\n", encoding="utf-8")
        errors = MODULE.check(root)
        self.assertTrue(any("priced ∩" in error or "¬unreachable" in error for error in errors))

    def test_rejects_unified_servable_ssot_slogan(self) -> None:
        root = self.fixture()
        path = root / "backend/internal/handler/admin/account_handler_available_models_test.go"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("grok defaults must mirror the unified servable SSOT\n", encoding="utf-8")
        self.assertTrue(any("unified servable SSOT" in error for error in MODULE.check(root)))

    def test_rejects_unpriced_never_blocks_as_current_policy(self) -> None:
        root = self.fixture()
        path = root / "backend/internal/service/gateway_service_tk_served_zero_cost.go"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("// 背景：unpriced never blocks\n", encoding="utf-8")
        self.assertTrue(any("unpriced never blocks" in error for error in MODULE.check(root)))

    def test_rejects_catalog_equivalence_as_delivery_truth(self) -> None:
        root = self.fixture()
        path = root / "scripts/checks/ssot-delta-gate.py"
        path.parent.mkdir(parents=True)
        path.write_text("Structural SSOT (servable ↔ priced ↔ display intent) stays here.\n", encoding="utf-8")
        self.assertTrue(any("secondary delivery-truth claim" in error for error in MODULE.check(root)))

    def test_allows_servable_as_probe_evidence(self) -> None:
        root = self.fixture()
        path = root / "ops/pricing/README.md"
        path.parent.mkdir(parents=True)
        path.write_text(
            "A probe verdict of servable is evidence, not RequestPlan or RuntimeReadiness.\n",
            encoding="utf-8",
        )
        self.assertEqual(MODULE.check(root), [])

    def test_rejects_point_in_time_main_alignment_sha(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/pricing-serving-single-source-of-truth.md"
        path.write_text(
            path.read_text(encoding="utf-8").replace(
                "status: approved\n", f"status: approved\naligned_origin_main: {'a' * 40}\n"
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("point-in-time" in error for error in MODULE.check(root)))

    def test_rejects_point_in_time_protocol_alignment_sha(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/protocol-routing-ssot.md"
        path.write_text(
            path.read_text(encoding="utf-8").replace(
                "status: approved\n", f"status: approved\naligned_origin_main: {'a' * 40}\n"
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("point-in-time" in error for error in MODULE.check(root)))

    def test_rejects_protocol_doc_that_replans_video_fetch(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/protocol-routing-ssot.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "video submit 与 fetch/status 由同一个 Plan contract 选择下一次 route。\n",
            encoding="utf-8",
        )
        self.assertTrue(any("video continuation" in error for error in MODULE.check(root)))

    def test_rejects_protocol_doc_that_denies_submit_time_route_ownership(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/protocol-routing-ssot.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "异步任务记录保存执行结果，不拥有下一次 route 选择。\n",
            encoding="utf-8",
        )
        self.assertTrue(any("video continuation" in error for error in MODULE.check(root)))

    def test_rejects_capability_discovery_that_dry_plans_video_fetch(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/universal-key-capability-discovery.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "video fetch/status 也必须通过 RequestPlan dry planning 才能执行。\n",
            encoding="utf-8",
        )
        self.assertTrue(any("video continuation" in error for error in MODULE.check(root)))

    def test_rejects_capability_discovery_that_dry_plans_image_task_reads(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/universal-key-capability-discovery.md"
        path.write_text(
            path.read_text(encoding="utf-8")
            + "image task status/result 也必须通过 RequestPlan dry planning。\n",
            encoding="utf-8",
        )
        self.assertTrue(any("task continuation" in error for error in MODULE.check(root)))

    def test_rejects_missing_task_continuation_contract(self) -> None:
        root = self.fixture()
        path = root / "docs/approved/pricing-serving-single-source-of-truth.md"
        path.write_text(
            path.read_text(encoding="utf-8").replace("TaskContinuation", "task resume"),
            encoding="utf-8",
        )
        self.assertTrue(any("TaskContinuation" in error for error in MODULE.check(root)))

    def test_protocol_doc_does_not_own_task_continuation(self) -> None:
        root = self.fixture()
        protocol = root / "docs/approved/protocol-routing-ssot.md"
        self.assertNotIn("TaskContinuation", protocol.read_text(encoding="utf-8"))
        self.assertEqual(MODULE.check(root), [])

    def test_prefilter_keeps_known_forbidden_samples(self) -> None:
        samples = (
            "SINGLE client-facing servable truth",
            "official upstream aliases display when priced+servable",
            "intersected with public priced+displayable SSOT",
            "上架一个模型（served + priced）",
            "per pricing-availability-source-of-truth.md §2.4 / R-002 it is a feed",
            "家族 floor 是对 PRICE 事实的估计，只**读**两个事实。",
            "grok defaults must mirror the unified servable SSOT",
            "Structural SSOT (servable ↔ priced ↔ display intent) stays here.",
            "truthful callable menus",
            "unpriced never blocks serving",
            "¬unreachable accounts stay hidden",
            "四层洋葱",
            "有价 + 可服务",
            "one entry, four facts",
            "列出即可调用",
            "account.go:639",
            "pricing-availability-source-of-truth.md §2.5",
            "Goal 2 of pricing-availability-source-of-truth.md",
            "served-model-reconcile-planner.md",
            "tokenkey-modelops-planner 分支 C",
        )
        for sample in samples:
            with self.subTest(sample=sample):
                self.assertTrue(MODULE.secondary_truth_candidate(sample), sample)

    def test_prefilter_skips_unrelated_go(self) -> None:
        self.assertFalse(
            MODULE.secondary_truth_candidate("package service\n\nfunc add(a int, b int) int { return a + b }\n")
        )

    def test_rejects_obsolete_availability_section_references_in_both_orders(self) -> None:
        samples = (
            "See R-001 of\n// docs/approved/pricing-availability-source-of-truth.md.",
            "See R-003 /\n// docs/approved/pricing-availability-source-of-truth.md#availability-structural-pruning.",
            "Why this helper exists (R-004 of\n// docs/approved/pricing-availability-source-of-truth.md):",
            "pricing-availability single-source-of-truth (R-001 of\n"
            "// docs/approved/pricing-availability-source-of-truth.md).",
            "docs/approved/pricing-availability-source-of-truth.md\n"
            "// §2.4 (Goal 1, R-002).",
            "see §8 of docs/approved/pricing-availability-source-of-truth.md",
        )
        for index, sample in enumerate(samples):
            with self.subTest(sample=sample):
                root = self.fixture()
                path = root / f"backend/internal/service/stale_reference_{index}.go"
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("package service\n// " + sample + "\n", encoding="utf-8")
                self.assertTrue(
                    any("secondary delivery-truth claim" in error for error in MODULE.check(root))
                )


if __name__ == "__main__":
    unittest.main()
