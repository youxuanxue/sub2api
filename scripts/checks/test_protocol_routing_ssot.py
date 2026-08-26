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
            gemini_executors = {
                "backend/internal/handler/gateway_handler.go": "MessagesToGemini",
                "backend/internal/handler/gateway_handler_chat_completions.go": "ChatToGemini",
                "backend/internal/handler/gateway_handler_responses.go": "ResponsesToGemini",
                "backend/internal/handler/openai_gateway_handler.go": "MessagesToGemini: route, ResponsesToGemini",
                "backend/internal/handler/openai_chat_completions.go": "ChatToGemini",
                "backend/internal/handler/gemini_v1beta_handler.go": "GeminiIdentity",
            }[relative]
            path.write_text(
                "package fixture\n"
                f"func route(){{ WithProtocolRouting(); ExecuteSelectedProtocol(ctx, router, selection, account, h.gatewayService.ValidateProtocolEndpoint, ProtocolExecutors{{ChatIdentity: func(ctx Context, plan Plan, request CanonicalRequest) (any, error) {{ return request.Body(), nil }}, {gemini_executors}: route}}) }}\n",
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
            "type messagesToGeminiAdapter struct{}\n"
            "type chatToGeminiAdapter struct{}\n"
            "type responsesToGeminiAdapter struct{}\n"
            "type geminiIdentityAdapter struct{}\n"
            "func ExecuteGeminiProtocolProfile(){}\n"
            "func ExecuteSelectedProtocol(){}\n",
            encoding="utf-8",
        )
        protocol_owner = root / "backend/internal/engine/protocolrouter/protocol.go"
        protocol_owner.write_text(
            "package fixture\n"
            "const ProtocolGeminiGenerateContent Protocol = \"gemini_generate_content\"\n",
            encoding="utf-8",
        )
        protocol_account_owner = root / "backend/internal/engine/protocolrouter/account.go"
        protocol_account_owner.write_text(
            "package fixture\n"
            "const GeminiEndpointAntigravityCloudCode GeminiEndpointProfile = \"antigravity_cloudcode\"\n"
            "const GeminiEndpointVertexServiceAccount GeminiEndpointProfile = \"vertex_service_account\"\n",
            encoding="utf-8",
        )
        canonical_owner = root / "backend/internal/service/account_supported_protocols.go"
        canonical_owner.write_text(
            "package fixture\n"
            "func protocolGeminiEndpointProfile(account Account){ GeminiEndpointAntigravityCloudCode(); GeminiEndpointVertexServiceAccount(); account.IsNewAPIVertexServiceAccount() }\n"
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
            "func probeProtocolCapability(protocol Protocol){ switch protocol { case ProtocolGeminiGenerateContent: probeGeminiGenerateContentSupport() } }\n"
            "func probeGeminiGenerateContentSupport(){}\n"
            "func PersistProtocolProbeVerdicts(){}\n"
            "func ProbeAccountProtocolCapabilities(){ ProtocolProbeCandidates(); protocolProbeCoordinator(); probeProtocolCapability(); PersistProtocolProbeVerdicts() }\n",
            encoding="utf-8",
        )
        gemini_probe_owner = root / "backend/internal/service/protocol_capability_probe_gemini.go"
        gemini_probe_owner.write_text(
            "package fixture\n"
            "func classifyGeminiProtocolProbe(){ METHOD_NOT_SUPPORTED(); UNSUPPORTED_METHOD() }\n"
            "func probeGeminiGenerateContentSupport(){}\n",
            encoding="utf-8",
        )
        service_wire = root / "backend/internal/service/wire.go"
        service_wire.write_text(
            "package fixture\n"
            "type ProtocolRoutingSSOTReady struct{ Report Report; router *Router }\n"
            "func (r ProtocolRoutingSSOTReady) EnabledRouter(){}\n"
            "func (r ProtocolRoutingSSOTReady) Ready() bool { return r.Report.CutoverReady }\n"
            "func newProtocolRoutingSSOTReady(report Report, router *Router) ProtocolRoutingSSOTReady { return ProtocolRoutingSSOTReady{Report: report, router: router} }\n"
            "func ProvideProtocolRoutingSSOTReady(){ prepareProtocolRoutingSSOT() }\n",
            encoding="utf-8",
        )
        handler_wire = root / "backend/internal/handler/wire.go"
        handler_wire.write_text(
            "package fixture\n"
            "func ProvideGatewayHandler(ready ProtocolRoutingSSOTReady){ SetProtocolRouter(ready.EnabledRouter()) }\n"
            "func ProvideOpenAIGatewayHandler(ready ProtocolRoutingSSOTReady){ SetProtocolRouter(ready.EnabledRouter()) }\n"
            "var ProviderSet = wire.NewSet(ProvideProtocolRoutedOpenAIGatewayHandler)\n",
            encoding="utf-8",
        )
        protocol_wire = root / "backend/internal/handler/wire_protocol_ssot_tk.go"
        protocol_wire.write_text(
            "package fixture\n"
            "func ProvideProtocolRoutedOpenAIGatewayHandler(ready ProtocolRoutingSSOTReady){ "
            "ProvideOpenAIGatewayHandler(ready); SetGeminiProtocolServices() }\n",
            encoding="utf-8",
        )
        server_router = root / "backend/internal/server/router.go"
        server_router.parent.mkdir(parents=True, exist_ok=True)
        server_router.write_text(
            "package fixture\n"
            "func registerRoutes(ready ProtocolRoutingSSOTReady){ RegisterCommonRoutes(ready.Ready()) }\n",
            encoding="utf-8",
        )
        common_routes = root / "backend/internal/server/routes/common.go"
        common_routes.parent.mkdir(parents=True, exist_ok=True)
        common_routes.write_text(
            "package fixture\n"
            "func RegisterCommonRoutes(protocolReady bool){ if !protocolReady { http.StatusServiceUnavailable; not_ready() } }\n",
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

    def test_accepts_result_returning_probe_owner_with_legacy_wrapper(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            "package fixture\n"
            "type protocolProbeCoordinator struct{}\n"
            "func probeProtocolCapability(protocol Protocol){ switch protocol { case ProtocolGeminiGenerateContent: probeGeminiGenerateContentSupport() } }\n"
            "func probeGeminiGenerateContentSupport(){}\n"
            "func PersistProtocolProbeVerdicts(){}\n"
            "func ProbeAccountProtocolCapabilities(){ ProbeAccountProtocolCapabilitiesNow() }\n"
            "func ProbeAccountProtocolCapabilitiesNow(){ ProtocolProbeCandidates(); protocolProbeCoordinator(); probeProtocolCapability(); PersistProtocolProbeVerdicts() }\n",
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

    def test_rejects_missing_gemini_execution_adapter(self) -> None:
        root = self.fixture()
        execution = root / "backend/internal/service/protocol_execution.go"
        execution.write_text(
            execution.read_text(encoding="utf-8").replace(
                "type responsesToGeminiAdapter struct{}\n",
                "",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("responsesToGeminiAdapter" in error for error in MODULE.check(root)))

    def test_rejects_missing_gemini_profile_dispatch_owner(self) -> None:
        root = self.fixture()
        execution = root / "backend/internal/service/protocol_execution.go"
        execution.write_text(
            execution.read_text(encoding="utf-8").replace(
                "func ExecuteGeminiProtocolProfile(){}\n",
                "",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("Gemini profile dispatch" in error for error in MODULE.check(root)))

    def test_rejects_handler_level_gemini_profile_selection(self) -> None:
        root = self.fixture()
        handler = root / "backend/internal/handler/gemini_v1beta_handler.go"
        handler.write_text(
            handler.read_text(encoding="utf-8")
            + "func bad(){ GeminiEndpointAntigravityCloudCode() }\n",
            encoding="utf-8",
        )
        self.assertTrue(any("handler selects Gemini endpoint profile" in error for error in MODULE.check(root)))

    def test_rejects_governed_handler_missing_gemini_executor(self) -> None:
        root = self.fixture()
        handler = root / "backend/internal/handler/openai_chat_completions.go"
        handler.write_text(
            handler.read_text(encoding="utf-8").replace(
                "ChatToGemini: route",
                "ChatToMessages: route",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("ChatToGemini" in error for error in MODULE.check(root)))

    def test_rejects_missing_gemini_probe_dispatch(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "case ProtocolGeminiGenerateContent: probeGeminiGenerateContentSupport()",
                "case ProtocolResponses: probeProtocolCapability()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("Gemini probe dispatch" in error for error in MODULE.check(root)))

    def test_rejects_gemini_governance_without_exact_vertex_predicate(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/account_supported_protocols.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "account.IsNewAPIVertexServiceAccount()",
                "account.Type == AccountTypeServiceAccount",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("IsNewAPIVertexServiceAccount" in error for error in MODULE.check(root)))

    def test_rejects_router_disabled_when_readiness_is_false(self) -> None:
        root = self.fixture()
        wire = root / "backend/internal/service/wire.go"
        wire.write_text(
            wire.read_text(encoding="utf-8").replace(
                "return ProtocolRoutingSSOTReady{Report: report, router: router}",
                "if !report.CutoverReady { router = nil }; return ProtocolRoutingSSOTReady{Report: report, router: router}",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("must not disable the router" in error for error in MODULE.check(root)))

    def test_rejects_readiness_accessor_that_ignores_cutover_report(self) -> None:
        root = self.fixture()
        wire = root / "backend/internal/service/wire.go"
        wire.write_text(
            wire.read_text(encoding="utf-8").replace(
                "return r.Report.CutoverReady",
                "return true",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("Ready accessor" in error for error in MODULE.check(root)))

    def test_rejects_health_route_without_protocol_readiness_gate(self) -> None:
        root = self.fixture()
        common = root / "backend/internal/server/routes/common.go"
        common.write_text(
            common.read_text(encoding="utf-8").replace(
                "if !protocolReady { http.StatusServiceUnavailable; not_ready() }",
                "ok()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("health readiness gate" in error for error in MODULE.check(root)))

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

    def test_rejects_openai_gemini_wiring_outside_protocol_companion(self) -> None:
        root = self.fixture()
        wire = root / "backend/internal/handler/wire_protocol_ssot_tk.go"
        wire.write_text(
            wire.read_text(encoding="utf-8").replace(
                "; SetGeminiProtocolServices()",
                "",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("Gemini protocol services" in error for error in MODULE.check(root)))

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
