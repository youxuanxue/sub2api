from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("protocol-routing-ssot.py")
SPEC = importlib.util.spec_from_file_location("protocol_routing_ssot", SCRIPT)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ProtocolRoutingSSOTTest(unittest.TestCase):
    def fixture(self) -> Path:
        root = Path(tempfile.mkdtemp())
        for relative in MODULE.OWNER_FILES:
            path = root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("package fixture\n", encoding="utf-8")
        for relative in MODULE.GOVERNED_HANDLERS:
            path = root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(
                "package fixture\n"
                "func route(){ WithProtocolRouting(); ExecuteSelectedProtocol(ctx, router, selection, account, h.gatewayService.ValidateProtocolEndpoint, ProtocolExecutors{ChatIdentity: func(ctx Context, plan Plan, request CanonicalRequest) (any, error) { return request.Body(), nil }}) }\n",
                encoding="utf-8",
            )
        for relative, symbols in MODULE.TARGET_BOUNDARIES.items():
            path = root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(
                "package fixture\nfunc route(){ " + ";".join(f"{symbol}()" for symbol in symbols) + " }\n",
                encoding="utf-8",
            )
        execution = root / "backend/internal/service/protocol_execution.go"
        execution.write_text(
            "package fixture\n"
            "type messagesIdentityAdapter struct{}\n"
            "type messagesToResponsesAdapter struct{}\n"
            "type messagesToChatAdapter struct{}\n"
            "type chatIdentityAdapter struct{}\n"
            "type chatToResponsesAdapter struct{}\n"
            "type chatToMessagesAdapter struct{}\n"
            "type responsesIdentityAdapter struct{}\n"
            "type responsesToChatAdapter struct{}\n"
            "type responsesToMessagesAdapter struct{}\n"
            "func ExecuteSelectedProtocol(){}\n",
            encoding="utf-8",
        )
        canonical_owner = root / "backend/internal/service/account_supported_protocols.go"
        canonical_owner.write_text(
            "package fixture\n"
            "func BuildSupportedProtocolsUpdate(){}\n"
            "func ReplaceSupportedProtocols(){}\n"
            "func SeedOfficialSupportedProtocols(){}\n",
            encoding="utf-8",
        )
        count_tokens = root / "backend/internal/handler/openai_gateway_count_tokens.go"
        count_tokens.parent.mkdir(parents=True, exist_ok=True)
        count_tokens.write_text(
            "package fixture\n"
            "func route(){ ResponsesPathInputTokens(); SelectProtocolAccountForTokenCount(); ExecuteSelectedProtocol(ctx, router, selection, account, h.gatewayService.ValidateProtocolEndpoint, ProtocolExecutors{ResponsesIdentity: func(ctx Context, plan Plan, request CanonicalRequest) (any, error) { return request.Body(), nil }}) }\n",
            encoding="utf-8",
        )
        routing_context = root / "backend/internal/service/protocol_routing_context.go"
        routing_context.parent.mkdir(parents=True, exist_ok=True)
        routing_context.write_text(
            "package fixture\n"
            "type protocolPlanCache struct{}\n"
            "func (c *protocolPlanCache) put(plan Plan){}\n"
            "func (c *protocolPlanCache) get(id int64, revision string) (Plan, bool){ return Plan{}, true }\n"
            "func attachProtocolPlan(){ plans.get() }\n",
            encoding="utf-8",
        )
        account_handler = root / "backend/internal/handler/admin/account_handler.go"
        account_handler.write_text(
            "package fixture\n"
            "func scheduleProtocolCapabilityProbes(){ service.ProtocolProbeCandidates(); scheduleProtocolCapabilityProbeBatch() }\n"
            "func scheduleProtocolCapabilityProbeBatch(){ service.ProbeAccountProtocolCapabilitiesBatch() }\n",
            encoding="utf-8",
        )
        probe_owner = root / "backend/internal/service/protocol_capability_probe.go"
        probe_owner.write_text(
            "package fixture\n"
            "type protocolProbeCoordinator struct{}\n"
            "func probeProtocolCapability(){}\n"
            "func PersistProtocolProbeVerdicts(){}\n"
            "func ProbeAccountProtocolCapabilities(){ ProtocolProbeCandidates(); protocolProbeCoordinator(); probeProtocolCapability(); PersistProtocolProbeVerdicts() }\n",
            encoding="utf-8",
        )
        service_wire = root / "backend/internal/service/wire.go"
        service_wire.write_text(
            "package fixture\n"
            "type ProtocolRoutingSSOTReady struct{}\n"
            "func (r ProtocolRoutingSSOTReady) EnabledRouter(){}\n"
            "func newProtocolRoutingSSOTReady(){ CutoverReady() }\n"
            "func ProvideProtocolRoutingSSOTReady(){ prepareProtocolRoutingSSOT() }\n",
            encoding="utf-8",
        )
        handler_wire = root / "backend/internal/handler/wire.go"
        handler_wire.write_text(
            "package fixture\n"
            "func ProvideGatewayHandler(ready ProtocolRoutingSSOTReady){ SetProtocolRouter(ready.EnabledRouter()) }\n"
            "func ProvideOpenAIGatewayHandler(ready ProtocolRoutingSSOTReady){ SetProtocolRouter(ready.EnabledRouter()) }\n",
            encoding="utf-8",
        )
        return root

    def test_accepts_complete_fixture_and_ignores_comments_and_strings(self) -> None:
        root = self.fixture()
        handler = root / MODULE.GOVERNED_HANDLERS[0]
        handler.write_text(
            handler.read_text(encoding="utf-8")
            + '// GetAPIProtocol ShouldUseResponsesAPI\nvar note = "ExtraKeyResponsesSupported"\n',
            encoding="utf-8",
        )
        self.assertEqual(MODULE.check(root), [])

    def test_rejects_legacy_handler_decision(self) -> None:
        root = self.fixture()
        handler = root / MODULE.GOVERNED_HANDLERS[0]
        handler.write_text(handler.read_text(encoding="utf-8") + "func bad(){ GetAPIProtocol() }\n", encoding="utf-8")
        self.assertTrue(any("GetAPIProtocol" in error for error in MODULE.check(root)))

    def test_rejects_forward_bypass_outside_selected_protocol_execution(self) -> None:
        root = self.fixture()
        handler = root / "backend/internal/handler/gateway_handler_responses.go"
        handler.write_text(
            handler.read_text(encoding="utf-8")
            + "func bad(){ h.gatewayService.ForwardAsResponses() }\n",
            encoding="utf-8",
        )
        self.assertTrue(any("outside ExecuteSelectedProtocol" in error for error in MODULE.check(root)))

    def test_rejects_removed_owner(self) -> None:
        root = self.fixture()
        (root / MODULE.OWNER_FILES[0]).unlink()
        self.assertTrue(any("missing protocol-routing owner" in error for error in MODULE.check(root)))

    def test_rejects_generic_request_scoped_adapter(self) -> None:
        root = self.fixture()
        execution = root / "backend/internal/service/protocol_execution.go"
        execution.write_text(execution.read_text(encoding="utf-8") + "type requestScopedProtocolAdapter struct{}\n", encoding="utf-8")
        self.assertTrue(any("generic protocol adapter" in error for error in MODULE.check(root)))

    def test_rejects_handler_target_protocol_switch(self) -> None:
        root = self.fixture()
        handler = root / MODULE.GOVERNED_HANDLERS[0]
        handler.write_text(handler.read_text(encoding="utf-8") + "func bad(plan Plan){ switch plan.TargetProtocol(){} }\n", encoding="utf-8")
        self.assertTrue(any("TargetProtocol" in error for error in MODULE.check(root)))

    def test_rejects_executor_that_does_not_use_canonical_body(self) -> None:
        root = self.fixture()
        handler = root / MODULE.GOVERNED_HANDLERS[0]
        handler.write_text(handler.read_text(encoding="utf-8").replace("request.Body()", "body"), encoding="utf-8")
        self.assertTrue(any("canonical request.Body" in error for error in MODULE.check(root)))

    def test_rejects_input_tokens_secondary_planning(self) -> None:
        root = self.fixture()
        handler = root / "backend/internal/handler/openai_gateway_count_tokens.go"
        handler.write_text(handler.read_text(encoding="utf-8") + "func bad(){ ProtocolSelectionForAccount() }\n", encoding="utf-8")
        self.assertTrue(any("secondary protocol planning" in error for error in MODULE.check(root)))

    def test_rejects_selection_time_secondary_planning(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_context.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "func attachProtocolPlan(){ plans.get() }",
                "func attachProtocolPlan(){ protocolPlanForAccount() }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("selection performs secondary protocol planning" in error for error in MODULE.check(root)))

    def test_rejects_per_protocol_probe_fanout(self) -> None:
        root = self.fixture()
        handler = root / "backend/internal/handler/admin/account_handler.go"
        handler.write_text(
            handler.read_text(encoding="utf-8").replace(
                "service.ProbeAccountProtocolCapabilitiesBatch()",
                "service.ProbeOpenAIAPIKeyResponsesSupport()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("per-protocol probe" in error for error in MODULE.check(root)))

    def test_rejects_handler_that_bypasses_bounded_account_probe_batch(self) -> None:
        root = self.fixture()
        handler = root / "backend/internal/handler/admin/account_handler.go"
        handler.write_text(
            handler.read_text(encoding="utf-8").replace(
                "service.ProbeAccountProtocolCapabilitiesBatch()",
                "service.ProbeAccountProtocolCapabilities()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("bounded account probe batch" in error for error in MODULE.check(root)))

    def test_rejects_multiple_candidate_set_persistence_calls(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "PersistProtocolProbeVerdicts() }",
                "PersistProtocolProbeVerdicts(); PersistProtocolProbeVerdicts() }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("exactly once" in error for error in MODULE.check(root)))

    def test_rejects_handler_wiring_that_bypasses_cutover_readiness(self) -> None:
        root = self.fixture()
        wire = root / "backend/internal/handler/wire.go"
        wire.write_text(
            wire.read_text(encoding="utf-8").replace(
                "ready ProtocolRoutingSSOTReady",
                "router *protocolrouter.Router",
            ).replace(
                "ready.EnabledRouter()",
                "router",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("cutover readiness" in error for error in MODULE.check(root)))

    def test_rejects_startup_wiring_that_skips_prepare_and_revalidation(self) -> None:
        root = self.fixture()
        wire = root / "backend/internal/service/wire.go"
        wire.write_text(
            wire.read_text(encoding="utf-8").replace(
                "prepareProtocolRoutingSSOT()",
                "MigrateProtocolRoutingSSOT()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("startup preparation" in error for error in MODULE.check(root)))


if __name__ == "__main__":
    unittest.main()
