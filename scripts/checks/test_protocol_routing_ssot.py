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
            authoritative_helper = {
                "backend/internal/handler/openai_gateway_handler.go": (
                    "prepareResponsesBody := func(executionAccount *service.Account, request CanonicalRequest) []byte { return request.Body() }; "
                    "prepareResponsesBody(account, request); "
                ),
                "backend/internal/handler/openai_chat_completions.go": (
                    "prepareBody := func(executionAccount *service.Account, request CanonicalRequest) []byte { return request.Body() }; "
                    "prepareBody(account, request); "
                ),
                "backend/internal/handler/gemini_v1beta_handler.go": (
                    "forwardNonGoverned := func(executionCtx context.Context, executionAccount *service.Account, request CanonicalRequest) (any, error) { return request.Body(), nil }; "
                    "forwardNonGoverned(executionCtx, account, request); "
                ),
            }.get(relative, "")
            path.write_text(
                "package fixture\n"
                f"func route(){{ WithProtocolRouting(); {authoritative_helper} ExecuteSelectedProtocol(ctx, router, selection, account, h.gatewayService.ValidateProtocolEndpoint, h.gatewayService.LoadProtocolExecutionAccount, ProtocolExecutors{{ChatIdentity: func(ctx Context, account *service.Account, plan Plan, request CanonicalRequest) (any, error) {{ return request.Body(), nil }}, {gemini_executors}: route}}) }}\n",
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
            "func protocolExecutionPreSendFailure(){ UpstreamFailoverError(); NextAccountRetry() }\n"
            "func ExecuteSelectedProtocol(){ freshAccount := loadAccount(); if freshAccount == nil { protocolExecutionPreSendFailure() }; if !protocolPlansRoutingEquivalent(plan, router.Plan(request, fresh)) { protocolExecutionPreSendFailure() }; withProtocolExecutionAccount(freshAccount); state := ExecutionAccountState{CredentialPresent: ProtocolAuthorizationPresent(freshAccount)}; if router.Execute(state) == ErrStalePlan || router.Execute(state) == ErrMissingCredential { protocolExecutionPreSendFailure() } }\n"
            "func protocolPlansRoutingEquivalent(scheduled, fresh Plan) bool { return scheduled.CapabilityKey() == fresh.CapabilityKey() && scheduled.Endpoint() == fresh.Endpoint() }\n"
            "func ProtocolAuthorizationPresent(account Account){ ProtocolAuthorizationSnapshotCredentialKey(); if account.IsNewAPIVertexServiceAccount(){ parseVertexServiceAccountKey(account) }; protocolAuthorizationToken(account) }\n"
            "func protocolRuntimeAuthorizationReady(ctx Context, account Account){ ProtocolRoutingRequest(ctx); ProtocolAuthorizationPresent(account) }\n",
            encoding="utf-8",
        )
        execution_test = root / "backend/internal/service/protocol_execution_test.go"
        execution_test.write_text(
            "package fixture\n"
            "func TestExecuteSelectedProtocolFailsOverMissingAuthorizationBeforeExecutor(){ ExecuteSelectedProtocol() }\n",
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
        router_owner = root / "backend/internal/engine/protocolrouter/router.go"
        router_owner.write_text(
            "package fixture\n"
            "func (r *Router) Execute(plan Plan, state ExecutionAccountState){ "
            "if state.CapabilityKey != plan.capabilityKey { ErrStalePlan() } }\n",
            encoding="utf-8",
        )
        canonical_owner = root / "backend/internal/service/account_supported_protocols.go"
        canonical_owner.write_text(
            "package fixture\n"
            "func (account *Account) SupportedProtocols(){ account.ProtocolEndpointCapability.SupportedProtocols() }\n"
            "func protocolGeminiEndpointProfile(account Account){ GeminiEndpointAntigravityCloudCode(); GeminiEndpointVertexServiceAccount(); account.IsNewAPIVertexServiceAccount() }\n"
            "func BuildSupportedProtocolsUpdate(){}\n"
            "func ReplaceSupportedProtocols(){}\n"
            "func SeedOfficialSupportedProtocols(){}\n"
            "func protocolAccountSnapshot(capability Capability){ if !protocolCapabilityHasVerifiedRoutingEvidence(capability) { fail() } }\n"
            "func protocolExactEndpoints(){ endpoint, err := protocolExactEndpoint(); if err != nil { continue }; endpoints[protocol] = endpoint }\n",
            encoding="utf-8",
        )
        identity_owner = root / "backend/internal/service/protocol_endpoint_identity.go"
        identity_owner.parent.mkdir(parents=True, exist_ok=True)
        identity_owner.write_text(
            "package fixture\n"
            "func BuildProtocolEndpointIdentity(){}\n"
            "func (identity ProtocolEndpointIdentity) CanonicalJSON(){}\n"
            "func (identity ProtocolEndpointIdentity) Key(){ sha256.Sum256() }\n"
            "func normalizeEndpointIdentityURL(){ protocolEndpointCredentialQueryParam(); Query().Encode() }\n"
            "func protocolEndpointCredentialQueryParam(name string){ isSensitiveKey(name) }\n",
            encoding="utf-8",
        )
        identity_test = root / "backend/internal/service/protocol_endpoint_identity_test.go"
        identity_test.write_text(
            "package fixture\n"
            "func TestBuildProtocolEndpointIdentityExcludesQueryCredentialsButKeepsSemanticRouting(){ "
            "first := BuildProtocolEndpointIdentity(); second := BuildProtocolEndpointIdentity(); first.Key(); second.Key() }\n",
            encoding="utf-8",
        )
        capability_repo = root / "backend/internal/repository/protocol_endpoint_capability_repo.go"
        capability_repo.parent.mkdir(parents=True, exist_ok=True)
        capability_repo.write_text(
            "package fixture\n"
            "func EnsureAccountLink(){}\n"
            "func ensureProtocolEndpointCapabilityLink(){ if capability.LastProbedAt != nil && !officialSeed { seedProtocols = nil }; countProtocolCapabilityLinkedAccounts() }\n"
            "func GetByAccountID(){}\n"
            "func ListLinkedAccountIDs(){}\n"
            "func AcquireProbeLease(){}\n"
            "func CommitProbeResult(){ commitProbeResult(true) }\n"
            "func CommitPreparedProbeResult(){ commitProbeResult(false) }\n"
            "func commitProbeResult(publish bool){ if publish { publishProtocolCapability() } }\n"
            "func PublishProtocolRoutingProjections(){ publishProtocolCapability() }\n"
            "func publishProtocolCapability(){ changedAccountIDs, _ := writeRollbackProjections(); for _, changedAccountID := range changedAccountIDs { accountID := changedAccountID; enqueueSchedulerOutbox(&accountID) } }\n"
            "func writeRollbackProjections(){}\n",
            encoding="utf-8",
        )
        migration_owner = root / "backend/internal/service/protocol_routing_migration.go"
        migration_owner.write_text(
            "package fixture\n"
            "func MigrateProtocolRoutingSSOT(){ LegacySupportedProtocolsProjection() }\n"
            "func validateProtocolRoutingSSOTReadiness(){}\n"
            "func evaluateProtocolRoutingSSOT(){ report.CutoverReady = false }\n"
            "func prepareProtocolRoutingSSOT(){ ProbeAccountProtocolCapabilitiesForPreparation(); prepared := MigrateProtocolRoutingSSOT(); if prepared.CutoverReady { PublishProtocolRoutingProjections(func(){ final := validateProtocolRoutingSSOTReadiness(); if !final.CutoverReady { return errProtocolRoutingFinalReadinessNotReady } }) } }\n"
            "func protocolCapabilityHasVerifiedRoutingEvidence(capability Capability){ capability.ProbeEvidence.OfficialSeed; capability.ProbeEvidence.InitialProbeCompleted; ProtocolProbePositive; capability.IdentityConflict }\n",
            encoding="utf-8",
        )
        count_tokens = root / "backend/internal/handler/openai_gateway_count_tokens.go"
        count_tokens.parent.mkdir(parents=True, exist_ok=True)
        count_tokens.write_text(
            "package fixture\n"
            "func route(){ ResponsesPathInputTokens(); SelectProtocolAccountForTokenCount(); ExecuteSelectedProtocol(ctx, router, selection, account, h.gatewayService.ValidateProtocolEndpoint, h.gatewayService.LoadProtocolExecutionAccount, ProtocolExecutors{ResponsesIdentity: func(ctx Context, account Account, plan Plan, request CanonicalRequest) (any, error) { return request.Body(), nil }}) }\n",
            encoding="utf-8",
        )
        routing_context = root / "backend/internal/service/protocol_routing_context.go"
        routing_context.parent.mkdir(parents=True, exist_ok=True)
        routing_context.write_text(
            "package fixture\n"
            "type protocolPlanCache struct{}\n"
            "func (c *protocolPlanCache) getOrPlan(key protocolPlanCacheKey, compute func() (Plan, error)) (Plan, error){ return compute() }\n"
            "func (c *protocolPlanCache) get(id int64) (Plan, bool){ return Plan{}, true }\n"
            "func protocolPlanForAccount(){ plans.getOrPlan() }\n"
            "func attachProtocolPlan(){ plans.get() }\n",
            encoding="utf-8",
        )
        routing_context_test = root / "backend/internal/service/protocol_routing_context_test.go"
        routing_context_test.write_text(
            "package fixture\n"
            "func TestOpenAIEligibilityUsesProtocolHardGateWithoutChangingOtherChecks(){ "
            "isOpenAICompatibleAccountEligibleForRequest(); isAccountSchedulableForModelSelection() }\n",
            encoding="utf-8",
        )
        gateway_scheduling = root / "backend/internal/service/gateway_scheduling.go"
        gateway_scheduling.write_text(
            "package fixture\n"
            "func isAccountSchedulableForModelSelection() bool { return protocolRuntimeAuthorizationReady() && ProtocolRouteLegal() }\n",
            encoding="utf-8",
        )
        openai_eligibility = root / "backend/internal/service/openai_gateway_scheduling_tk_eligibility_reason.go"
        openai_eligibility.write_text(
            "package fixture\n"
            "func openAICompatEligibilityReason(){ protocolRuntimeAuthorizationReady(); protocolRequestEligibilityReason() }\n",
            encoding="utf-8",
        )
        openai_scheduler = root / "backend/internal/service/openai_account_scheduler.go"
        openai_scheduler.write_text(
            "package fixture\n"
            "func isAccountRequestCompatibleReason() (bool, string) { if !protocolRuntimeAuthorizationReady() { return false, \"authorization\" }; if eligible, reason := protocolRequestEligibilityReason(); !eligible { return false, reason }; return true, \"\" }\n",
            encoding="utf-8",
        )
        account_handler = root / "backend/internal/handler/admin/account_handler.go"
        account_handler.write_text(
            "package fixture\n"
            "func scheduleProtocolCapabilityProbes(){ service.ProtocolProbeCandidates(); scheduleProtocolCapabilityProbeBatch() }\n"
            "func scheduleProtocolCapabilityProbeBatch(){ service.ProbeAccountProtocolCapabilitiesBatch() }\n",
            encoding="utf-8",
        )
        account_repo = root / "backend/internal/repository/account_repo.go"
        account_repo.parent.mkdir(parents=True, exist_ok=True)
        account_repo.write_text(
            "package fixture\n"
            "func UpdateCredentials(){ loadAccountForProtocolCapabilityLifecycle(); ensureAccountProtocolEndpointCapability() }\n",
            encoding="utf-8",
        )
        probe_owner = root / "backend/internal/service/protocol_capability_probe.go"
        probe_owner.write_text(
            "package fixture\n"
            "type protocolProbeCoordinator struct{}\n"
            "func probeProtocolCapability(protocol Protocol) (Observation, bool) { switch protocol { case ProtocolMessages: return probeOpenAIAPIKeyNativeMessagesSupport(); case ProtocolChatCompletions: return probeOpenAIAPIKeyChatCompletionsSupport(); case ProtocolResponses: return probeOpenAIAPIKeyResponsesSupport(); case ProtocolGeminiGenerateContent: return probeGeminiGenerateContentSupport() }; return Observation{}, false }\n"
            "func probeOpenAIAPIKeyNativeMessagesSupport(){}\n"
            "func probeOpenAIAPIKeyChatCompletionsSupport(){}\n"
            "func probeOpenAIAPIKeyResponsesSupport(){}\n"
            "func probeGeminiGenerateContentSupport(){}\n"
            "func ProbeAccountProtocolCapabilities(){ ProbeAccountProtocolCapabilitiesNow() }\n"
            "func ProbeAccountProtocolCapabilitiesForPreparation(){ probeAccountProtocolCapabilitiesNow(false) }\n"
            "func ProbeAccountProtocolCapabilitiesNow(){ probeAccountProtocolCapabilitiesNow(true) }\n"
            "func probeAccountProtocolCapabilitiesNow(publish bool){ EnsureAccountLink(); ProtocolProbeCandidates(); protocolProbeCoordinator.Do(capability.CapabilityKey, func(){ runEndpointProtocolProbe(publish) }) }\n"
            "func runEndpointProtocolProbe(publish bool){ AcquireProbeLease(); ListLinkedAccountIDs(); selectProtocolProbeWitnesses(); probeProtocolWitnesses(); probeProtocolCapability(); IdentityConflict(); ProbeEvidence(); commitProtocolProbeResult(publish) }\n"
            "func commitProtocolProbeResult(publish bool){ if publish { CommitProbeResult() } else { CommitPreparedProbeResult() } }\n"
            "func selectProtocolProbeWitnesses(){ protocolProbeAuthorizationUsable() }\n"
            "func protocolProbeAuthorizationUsable(account Account){ ProtocolAuthorizationPresent(account) }\n"
            "func protocolAuthorizationToken(account Account){ if account.IsOpenAIOAuthLike(){ account.GetOpenAIAccessToken() } }\n"
            "func resolveProtocolProbeGeneration(){ priorVerdict := conclusiveProtocolProbeVerdict(priorEvidence); resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, modelSpecific); resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, inconclusive) }\n",
            encoding="utf-8",
        )
        probe_test = root / "backend/internal/service/protocol_capability_probe_test.go"
        probe_test.write_text(
            "package fixture\n"
            "func TestSelectProtocolProbeWitnessesFiltersUnusableAuthorizationBeforeBound(){ selectProtocolProbeWitnesses() }\n",
            encoding="utf-8",
        )
        scheduler_owner = root / "backend/internal/repository/scheduler_cache.go"
        scheduler_owner.parent.mkdir(parents=True, exist_ok=True)
        scheduler_owner.write_text(
            "package fixture\n"
            "func buildSchedulerMetadataAccount(account Account){ filterSchedulerCredentialsForProtocolRouting(account) }\n"
            "func filterSchedulerCredentialsForProtocolRouting(account Account){ ProtocolAuthorizationPresent(account); ProtocolAuthorizationSnapshotCredentialKey(); if account.IsNewAPIVertexServiceAccount(){ account.VertexProjectID() } }\n",
            encoding="utf-8",
        )
        scheduler_test = root / "backend/internal/repository/scheduler_cache_unit_test.go"
        scheduler_test.write_text(
            "package fixture\n"
            "func TestBuildSchedulerMetadataAccountPreservesProtocolEndpointIdentity(){ "
            "full := BuildProtocolEndpointIdentity(); metadata := buildSchedulerMetadataAccount(); "
            "BuildProtocolEndpointIdentity(metadata); ProtocolAuthorizationSnapshotCredentialKey(); full.Key() }\n",
            encoding="utf-8",
        )
        account_schema = root / "backend/ent/schema/account.go"
        account_schema.write_text(
            "package fixture\n"
            "func Fields(){ field.Int64(\"protocol_endpoint_capability_id\") }\n"
            "func Edges(){ edge.From(\"protocol_endpoint_capability\", ProtocolEndpointCapability.Type) }\n",
            encoding="utf-8",
        )
        capability_schema = root / "backend/ent/schema/protocol_endpoint_capability.go"
        capability_schema.write_text(
            "package fixture\n"
            "func Edges(){ edge.To(\"accounts\", Account.Type).Annotations(entsql.OnDelete(entsql.Restrict)) }\n",
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
            "func (r ProtocolRoutingSSOTReady) Ready() bool { return r.Report.TrafficReady }\n"
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
            "func probeProtocolCapability(protocol Protocol) (Observation, bool) { switch protocol { case ProtocolMessages: return probeOpenAIAPIKeyNativeMessagesSupport(); case ProtocolChatCompletions: return probeOpenAIAPIKeyChatCompletionsSupport(); case ProtocolResponses: return probeOpenAIAPIKeyResponsesSupport(); case ProtocolGeminiGenerateContent: return probeGeminiGenerateContentSupport() }; return Observation{}, false }\n"
            "func probeOpenAIAPIKeyNativeMessagesSupport(){}\n"
            "func probeOpenAIAPIKeyChatCompletionsSupport(){}\n"
            "func probeOpenAIAPIKeyResponsesSupport(){}\n"
            "func probeGeminiGenerateContentSupport(){}\n"
            "func ProbeAccountProtocolCapabilities(){ ProbeAccountProtocolCapabilitiesNow() }\n"
            "func ProbeAccountProtocolCapabilitiesForPreparation(){ probeAccountProtocolCapabilitiesNow(false) }\n"
            "func ProbeAccountProtocolCapabilitiesNow(){ probeAccountProtocolCapabilitiesNow(true) }\n"
            "func probeAccountProtocolCapabilitiesNow(publish bool){ EnsureAccountLink(); ProtocolProbeCandidates(); protocolProbeCoordinator.Do(capability.CapabilityKey, func(){ runEndpointProtocolProbe(publish) }) }\n"
            "func runEndpointProtocolProbe(publish bool){ AcquireProbeLease(); ListLinkedAccountIDs(); selectProtocolProbeWitnesses(); probeProtocolWitnesses(); probeProtocolCapability(); IdentityConflict(); ProbeEvidence(); commitProtocolProbeResult(publish) }\n"
            "func commitProtocolProbeResult(publish bool){ if publish { CommitProbeResult() } else { CommitPreparedProbeResult() } }\n"
            "func selectProtocolProbeWitnesses(){ protocolProbeAuthorizationUsable() }\n"
            "func protocolProbeAuthorizationUsable(account Account){ ProtocolAuthorizationPresent(account) }\n"
            "func protocolAuthorizationToken(account Account){ if account.IsOpenAIOAuthLike(){ account.GetOpenAIAccessToken() } }\n"
            "func resolveProtocolProbeGeneration(){ priorVerdict := conclusiveProtocolProbeVerdict(priorEvidence); resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, modelSpecific); resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, inconclusive) }\n",
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
                "case ProtocolGeminiGenerateContent: return probeGeminiGenerateContentSupport()",
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

    def test_rejects_readiness_accessor_that_ignores_traffic_report(self) -> None:
        root = self.fixture()
        wire = root / "backend/internal/service/wire.go"
        wire.write_text(
            wire.read_text(encoding="utf-8").replace(
                "return r.Report.TrafficReady",
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

    def test_rejects_legacy_account_owned_probe_writer(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8") + "func PersistProtocolProbeVerdicts(){}\n",
            encoding="utf-8",
        )
        self.assertTrue(any("account-owned protocol probe writer" in error for error in MODULE.check(root)))

    def test_rejects_account_id_probe_coordination(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "protocolProbeCoordinator.Do(capability.CapabilityKey",
                "protocolProbeCoordinator.Do(accountID",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("capability key" in error for error in MODULE.check(root)))

    def test_rejects_runtime_legacy_supported_protocol_read(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/account_supported_protocols.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "account.ProtocolEndpointCapability.SupportedProtocols()",
                "account.Extra[SupportedProtocolsExtraKey]",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("legacy account projection" in error for error in MODULE.check(root)))

    def test_rejects_legacy_projection_consumer_outside_migration(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_context.go"
        owner.write_text(
            owner.read_text(encoding="utf-8") + "func bad(){ LegacySupportedProtocolsProjection() }\n",
            encoding="utf-8",
        )
        self.assertTrue(any("legacy protocol projection" in error for error in MODULE.check(root)))

    def test_rejects_duplicate_endpoint_identity_builder(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/account_supported_protocols.go"
        owner.write_text(
            owner.read_text(encoding="utf-8") + "func BuildProtocolEndpointIdentity(){}\n",
            encoding="utf-8",
        )
        self.assertTrue(any("endpoint identity builder" in error for error in MODULE.check(root)))

    def test_rejects_endpoint_identity_normalization_without_query_credential_filter(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_endpoint_identity.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "protocolEndpointCredentialQueryParam();",
                "noop();",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("query credentials" in error for error in MODULE.check(root)))

    def test_rejects_endpoint_identity_query_filter_without_sensitive_key_owner(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_endpoint_identity.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("isSensitiveKey(name)", "false"),
            encoding="utf-8",
        )
        self.assertTrue(any("sensitive query-key classifier" in error for error in MODULE.check(root)))

    def test_rejects_missing_query_credential_identity_regression_test(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_endpoint_identity_test.go"
        owner.write_text("package fixture\n", encoding="utf-8")
        self.assertTrue(any("query credential identity regression test" in error for error in MODULE.check(root)))

    def test_rejects_revision_token_stale_guard(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/engine/protocolrouter/router.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "if state.CapabilityKey != plan.capabilityKey { ErrStalePlan() }",
                "if state.CapabilityKey != plan.capabilityKey || state.CapabilityRevision != plan.capabilityRevision { ErrStalePlan() }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("revision tokens" in error for error in MODULE.check(root)))

    def test_rejects_equivalent_route_revision_compare(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_execution.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "scheduled.CapabilityKey() == fresh.CapabilityKey()",
                "scheduled.CapabilityKey() == fresh.CapabilityKey() && scheduled.CapabilityRevision() == fresh.CapabilityRevision()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("revision tokens" in error for error in MODULE.check(root)))

    def test_rejects_account_revision_hash(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/account_supported_protocols.go"
        owner.write_text(
            owner.read_text(encoding="utf-8") + "func protocolAccountRevision(){}\n",
            encoding="utf-8",
        )
        self.assertTrue(any("protocol revision hash" in error for error in MODULE.check(root)))

    def test_rejects_capability_repository_without_generation_commit(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/protocol_endpoint_capability_repo.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "func CommitProbeResult(){ commitProbeResult(true) }\n",
                "",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("CommitProbeResult" in error for error in MODULE.check(root)))

    def test_rejects_capability_commit_without_scheduler_invalidation(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/protocol_endpoint_capability_repo.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("enqueueSchedulerOutbox(&accountID)", "noop()", 1),
            encoding="utf-8",
        )
        self.assertTrue(any("scheduler cache invalidation" in error for error in MODULE.check(root)))

    def test_rejects_capability_link_that_publishes_legacy_state(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/protocol_endpoint_capability_repo.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "countProtocolCapabilityLinkedAccounts() }",
                "countProtocolCapabilityLinkedAccounts(); writeRollbackProjections(); enqueueSchedulerOutbox() }",
                1,
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("silent preparation" in error for error in MODULE.check(root)))

    def test_rejects_capability_link_that_reseeds_history_after_an_accepted_probe(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/protocol_endpoint_capability_repo.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "if capability.LastProbedAt != nil && !officialSeed { seedProtocols = nil }",
                "if capability.ProbeEvidence.InitialProbeCompleted && !officialSeed { seedProtocols = nil }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("historical projection" in error for error in MODULE.check(root)))

    def test_rejects_startup_migration_without_atomic_projection_publication(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_migration.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "PublishProtocolRoutingProjections",
                "MissingProtocolRoutingPublication",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("publication boundary" in error for error in MODULE.check(root)))

    def test_rejects_startup_publication_with_unconditional_cutover_guard(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_migration.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "if prepared.CutoverReady {",
                "if true || prepared.CutoverReady {",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("preliminary readiness guard" in error for error in MODULE.check(root)))

    def test_rejects_startup_publication_guarded_by_unrelated_report(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_migration.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "if prepared.CutoverReady {",
                "if unrelated.CutoverReady {",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("preliminary readiness guard" in error for error in MODULE.check(root)))

    def test_rejects_final_readiness_outside_publication_transaction(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_migration.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "PublishProtocolRoutingProjections(func(){ final := validateProtocolRoutingSSOTReadiness(); if !final.CutoverReady { return errProtocolRoutingFinalReadinessNotReady } })",
                "PublishProtocolRoutingProjections(); final := validateProtocolRoutingSSOTReadiness(); if !final.CutoverReady { return errProtocolRoutingFinalReadinessNotReady }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("publication transaction callback" in error for error in MODULE.check(root)))

    def test_rejects_newapi_exact_endpoints_that_fail_closed_on_one_protocol(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/account_supported_protocols.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "func protocolExactEndpoints(){ endpoint, err := protocolExactEndpoint(); if err != nil { continue }; endpoints[protocol] = endpoint }",
                "func protocolExactEndpoints(){ endpoint, err := protocolExactEndpoint(); if err != nil { return nil, err }; endpoints[protocol] = endpoint }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(
            any("one unsupported protocol" in error for error in MODULE.check(root))
        )

    def test_rejects_account_evaluation_that_flips_process_traffic_ready(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_migration.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "func evaluateProtocolRoutingSSOT(){ report.CutoverReady = false }",
                "func evaluateProtocolRoutingSSOT(){ report.TrafficReady = false }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("process TrafficReady" in error for error in MODULE.check(root)))

    def test_rejects_startup_migration_that_uses_normal_probe_writer(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_routing_migration.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "ProbeAccountProtocolCapabilitiesForPreparation()",
                "ProbeAccountProtocolCapabilities()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("preparation probe" in error for error in MODULE.check(root)))

    def test_rejects_execution_without_authoritative_account_reload(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_execution.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("freshAccount := loadAccount();", "freshAccount := cachedAccount;"),
            encoding="utf-8",
        )
        self.assertTrue(any("authoritative account reload" in error for error in MODULE.check(root)))

    def test_rejects_execution_without_authoritative_account_binding(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_execution.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("withProtocolExecutionAccount(freshAccount);", "noop();"),
            encoding="utf-8",
        )
        self.assertTrue(any("bind the authoritative account" in error for error in MODULE.check(root)))

    def test_rejects_runtime_authorization_that_accepts_any_credentials_map(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_execution.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "ProtocolAuthorizationSnapshotCredentialKey(); if account.IsNewAPIVertexServiceAccount(){ parseVertexServiceAccountKey(account) }; protocolAuthorizationToken(account)",
                "len(account.Credentials) > 0",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("exact credential shape" in error for error in MODULE.check(root)))

    def test_rejects_gateway_scheduler_without_runtime_authorization_gate(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/gateway_scheduling.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("protocolRuntimeAuthorizationReady() && ", ""),
            encoding="utf-8",
        )
        self.assertTrue(any("gateway scheduler authorization hard gate" in error for error in MODULE.check(root)))

    def test_rejects_openai_scheduler_without_runtime_authorization_gate(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/openai_account_scheduler.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace('if !protocolRuntimeAuthorizationReady() { return false, "authorization" }; ', ""),
            encoding="utf-8",
        )
        self.assertTrue(any("OpenAI scheduler authorization hard gate" in error for error in MODULE.check(root)))

    def test_rejects_openai_eligibility_without_authorization_diagnostic(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/openai_gateway_scheduling_tk_eligibility_reason.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("protocolRuntimeAuthorizationReady();", ""),
            encoding="utf-8",
        )
        self.assertTrue(any("OpenAI eligibility authorization diagnostic" in error for error in MODULE.check(root)))

    def test_rejects_missing_credential_without_pre_send_failover(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_execution.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(" || router.Execute(state) == ErrMissingCredential", ""),
            encoding="utf-8",
        )
        self.assertTrue(any("missing credential outside account failover" in error for error in MODULE.check(root)))

    def test_rejects_missing_runtime_authorization_regression_tests(self) -> None:
        root = self.fixture()
        (root / "backend/internal/service/protocol_execution_test.go").write_text("package fixture\n", encoding="utf-8")
        (root / "backend/internal/service/protocol_routing_context_test.go").write_text("package fixture\n", encoding="utf-8")
        errors = MODULE.check(root)
        self.assertTrue(any("missing-authorization execution regression test" in error for error in errors))
        self.assertTrue(any("scheduler authorization regression test" in error for error in errors))

    def test_rejects_pre_send_stale_failure_without_account_failover(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_execution.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "if freshAccount == nil { protocolExecutionPreSendFailure() }",
                "if freshAccount == nil { ErrProtocolRouteUnavailable() }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("pre-send stale failure" in error for error in MODULE.check(root)))

    def test_rejects_handler_helper_that_captures_scheduled_account(self) -> None:
        root = self.fixture()
        handler = root / "backend/internal/handler/openai_gateway_handler.go"
        handler.write_text(
            handler.read_text(encoding="utf-8")
            .replace(
                "prepareResponsesBody := func(executionAccount *service.Account, request CanonicalRequest)",
                "prepareResponsesBody := func(request CanonicalRequest)",
            )
            .replace("prepareResponsesBody(account, request)", "prepareResponsesBody(request)"),
            encoding="utf-8",
        )
        self.assertTrue(any("authoritative execution account" in error for error in MODULE.check(root)))

    def test_rejects_probe_without_conclusive_witness_stopping(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("probeProtocolWitnesses()", "probeEveryWitness()"),
            encoding="utf-8",
        )
        self.assertTrue(any("conclusive witness stopping" in error for error in MODULE.check(root)))

    def test_rejects_probe_resolver_without_prior_verdict_comparison(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("conclusiveProtocolProbeVerdict(priorEvidence)", "currentVerdict()"),
            encoding="utf-8",
        )
        self.assertTrue(any("prior conclusive verdict" in error for error in MODULE.check(root)))

    def test_rejects_probe_resolver_that_overwrites_conclusive_history(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8")
            .replace("persistedProtocolProbeVerdict(priorVerdict, modelSpecific)", "currentVerdict()")
            .replace("persistedProtocolProbeVerdict(priorVerdict, inconclusive)", "currentVerdict()"),
            encoding="utf-8",
        )
        self.assertTrue(any("overwrites prior conclusive evidence" in error for error in MODULE.check(root)))

    def test_rejects_probe_dispatch_that_ignores_classifier_result(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "return probeGeminiGenerateContentSupport()",
                "probeGeminiGenerateContentSupport(); return Observation{}, false",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("classifier result" in error for error in MODULE.check(root)))

    def test_rejects_any_probe_dispatch_branch_that_ignores_classifier_result(self) -> None:
        cases = (
            ("probeOpenAIAPIKeyNativeMessagesSupport", "ProtocolMessages"),
            ("probeOpenAIAPIKeyChatCompletionsSupport", "ProtocolChatCompletions"),
            ("probeOpenAIAPIKeyResponsesSupport", "ProtocolResponses"),
            ("probeGeminiGenerateContentSupport", "ProtocolGeminiGenerateContent"),
        )
        for helper, protocol in cases:
            with self.subTest(protocol=protocol):
                root = self.fixture()
                owner = root / "backend/internal/service/protocol_capability_probe.go"
                owner.write_text(
                    owner.read_text(encoding="utf-8").replace(
                        f"return {helper}()",
                        f"{helper}(); return Observation{{}}, false",
                    ),
                    encoding="utf-8",
                )
                self.assertTrue(any("classifier result" in error for error in MODULE.check(root)))

    def test_rejects_probe_resolver_that_discards_persisted_verdict_result(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8")
            .replace(
                "resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, modelSpecific)",
                "persistedProtocolProbeVerdict(priorVerdict, modelSpecific); resolution.Evidence[protocol] = modelSpecific",
            )
            .replace(
                "resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, inconclusive)",
                "persistedProtocolProbeVerdict(priorVerdict, inconclusive); resolution.Evidence[protocol] = inconclusive",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("persisted verdict result" in error for error in MODULE.check(root)))

    def test_rejects_probe_resolver_when_only_one_persisted_verdict_result_is_used(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, modelSpecific)",
                "persistedProtocolProbeVerdict(priorVerdict, modelSpecific); resolution.Evidence[protocol] = modelSpecific",
                1,
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("persisted verdict result" in error for error in MODULE.check(root)))

    def test_rejects_capability_commit_that_invalidates_only_one_changed_account(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/protocol_endpoint_capability_repo.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "func publishProtocolCapability(){ changedAccountIDs, _ := writeRollbackProjections(); for _, changedAccountID := range changedAccountIDs { accountID := changedAccountID; enqueueSchedulerOutbox(&accountID) } }",
                "func publishProtocolCapability(){ changedAccountIDs, _ := writeRollbackProjections(); accountID := changedAccountIDs[0]; enqueueSchedulerOutbox(&accountID) }",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("all changed accounts" in error for error in MODULE.check(root)))

    def test_rejects_gateway_scheduler_that_ignores_both_hard_gate_results(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/gateway_scheduling.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "return protocolRuntimeAuthorizationReady() && ProtocolRouteLegal()",
                "protocolRuntimeAuthorizationReady(); ProtocolRouteLegal(); return true",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("composed with protocol legality" in error for error in MODULE.check(root)))

    def test_rejects_openai_scheduler_that_ignores_both_hard_gate_results(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/openai_account_scheduler.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "if !protocolRuntimeAuthorizationReady() { return false, \"authorization\" }; if eligible, reason := protocolRequestEligibilityReason(); !eligible { return false, reason }",
                "protocolRuntimeAuthorizationReady(); ProtocolRouteLegal()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("composed with protocol legality" in error for error in MODULE.check(root)))

    def test_rejects_probe_witness_selection_without_authorization_filter(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("protocolProbeAuthorizationUsable()", "ProtocolAuthorizationPresent()"),
            encoding="utf-8",
        )
        self.assertTrue(any("unusable authorization" in error for error in MODULE.check(root)))

    def test_rejects_vertex_probe_authorization_without_exact_service_account_validation(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "ProtocolAuthorizationPresent(account)",
                "protocolAuthorizationToken(account)",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("shared credential owner" in error for error in MODULE.check(root)))

    def test_rejects_protocol_authorization_owner_without_openai_oauth_token(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "if account.IsOpenAIOAuthLike(){ account.GetOpenAIAccessToken() }",
                "noop()",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("OpenAI OAuth access token" in error for error in MODULE.check(root)))

    def test_rejects_scheduler_metadata_that_drops_protocol_identity_credentials(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/scheduler_cache.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "filterSchedulerCredentialsForProtocolRouting(account)",
                "filterSchedulerCredentials(account.Credentials)",
                1,
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("protocol endpoint identity credentials" in error for error in MODULE.check(root)))

    def test_rejects_scheduler_protocol_identity_filter_without_vertex_project_derivation(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/scheduler_cache.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("account.VertexProjectID()", "noop()"),
            encoding="utf-8",
        )
        self.assertTrue(any("authorization readiness" in error for error in MODULE.check(root)))

    def test_rejects_scheduler_protocol_filter_without_authorization_snapshot(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/scheduler_cache.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace(
                "ProtocolAuthorizationPresent(account); ProtocolAuthorizationSnapshotCredentialKey();",
                "noop();",
            ),
            encoding="utf-8",
        )
        self.assertTrue(any("authorization readiness" in error for error in MODULE.check(root)))

    def test_rejects_missing_scheduler_endpoint_identity_regression_test(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/scheduler_cache_unit_test.go"
        owner.write_text("package fixture\n", encoding="utf-8")
        self.assertTrue(any("scheduler identity and authorization regression test" in error for error in MODULE.check(root)))

    def test_rejects_missing_vertex_probe_witness_regression_test(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/service/protocol_capability_probe_test.go"
        owner.write_text("package fixture\n", encoding="utf-8")
        self.assertTrue(any("Vertex probe witness regression test" in error for error in MODULE.check(root)))

    def test_rejects_account_schema_without_explicit_capability_fk(self) -> None:
        root = self.fixture()
        owner = root / "backend/ent/schema/account.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace('field.Int64("protocol_endpoint_capability_id")', 'field.Int64("wrong_fk")'),
            encoding="utf-8",
        )
        self.assertTrue(any("FK field" in error for error in MODULE.check(root)))

    def test_rejects_capability_schema_without_delete_restrict(self) -> None:
        root = self.fixture()
        owner = root / "backend/ent/schema/protocol_endpoint_capability.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("entsql.Restrict", "entsql.SetNull"),
            encoding="utf-8",
        )
        self.assertTrue(any("ON DELETE RESTRICT" in error for error in MODULE.check(root)))

    def test_rejects_credential_replacement_without_identity_relink(self) -> None:
        root = self.fixture()
        owner = root / "backend/internal/repository/account_repo.go"
        owner.write_text(
            owner.read_text(encoding="utf-8").replace("ensureAccountProtocolEndpointCapability()", "noop()"),
            encoding="utf-8",
        )
        self.assertTrue(any("capability identity relink" in error for error in MODULE.check(root)))

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
