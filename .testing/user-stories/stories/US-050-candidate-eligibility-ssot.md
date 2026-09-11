# US-050-candidate-eligibility-ssot

- ID: US-050
- Title: Universal and direct requests share authorization-scoped candidate scheduling
- Priority: P0
- As a / I want / So that: As a key user, I want eligible accounts to compete within my authorized scope and payment tier, so group display order and duplicate memberships do not change scheduling or bypass my billing rights.
- Trace: `docs/approved/candidate-eligibility-ssot.md`
- Risk Focus:
  - 逻辑错误: preserve complete authorization paths, deduplicate equivalent execution candidates, and keep model support separate from availability.
  - 行为回归: preserve native/media capability owners and subscription priority while removing group-order scheduling effects.
  - 安全问题: enforce authorized scope, forced platform and matching execution/billing attribution on initial selection and retries.
  - 运行时: account selection owns slot races, session affinity and resource release; group-level scoring cannot replace account competition.

## Acceptance Criteria

1. AC-001 (positive): Given authorized Vertex and Antigravity accounts When one is unavailable Then the resolver selects the eligible peer using the real request Plan.
2. AC-002 (negative): Given configured capability but no currently available account When resolving Then return capacity 429; unknown evidence must not become entitlement 403.
3. AC-003 (regression): Given native and valid converted routes When comparing authorized account candidates Then native support adds no priority advantage.
4. AC-004 (regression): Given authorized Gemini/Antigravity accounts When evaluating Then account membership, actual capability and endpoint permissions control admission; legacy mixed-pool opt-ins and group platform equality do not exclude a legal account.
5. AC-005 (regression): Given sustained empty-pool feedback When choosing an account Then the same scoped temporary penalty applies, all-saturated pools remain last resorts, and subscription priority survives.
6. AC-006 (integration): Given a compressed Responses compact request When authentication evaluates candidates Then the exact profile/path reaches Plan before billing binding and raw body/headers remain unchanged.
7. AC-007 (mechanical): Given removal of a shared filter, saturation reader or consuming call site When running sentinels Then preflight fails.
8. AC-008 (regression): Given equivalent authorized and billing paths When group display order, names, IDs or enumeration order change Then the account selection result is invariant for identical scheduling inputs and random seed.
9. AC-009 (positive): Given eligible accounts in different authorized groups within one payment tier When a peer account wins the common account policy Then production selection uses that account and its legal billing path regardless of group display order.
10. AC-010 (regression): Given the same execution candidates and equivalent authorization/billing policies When memberships are duplicated or groups are split/merged Then candidate multiplicity, scheduling weight and concurrency accounting remain unchanged.
11. AC-011 (negative): Given separate grants for account A/model X and account B/model Y When requesting Y Then A cannot borrow B's model grant; its selected path must independently pass every authorization and policy gate.
12. AC-012 (regression): Given a Direct key and a Universal key with the same single effective authorized group and billing context When evaluating the same effective model, request policy and scheduling state Then candidate eligibility, selection and billing are equivalent; Direct group preprocessing may make identical raw model strings non-equivalent.
13. AC-013 (integration): Given duplicate billing origins, quota exhaustion or a final admission failure When selecting or reselecting Then execution, subscription/balance checks, pricing, profit gates and reservations use the same legal attribution, and discarded reservations/slots are released.
14. AC-014 (negative): Given a revoked authorization, stale sticky binding, non-replayable request or stored media task When retrying or restoring affinity Then the shared scope and existing execution-affinity/replay guards remain enforced.
15. AC-015 (regression): Given a verified authorization scope and a subscription read, window-maintenance or candidate-evaluation error When another authorized balance candidate is available Then the resolver selects it and normal wallet/key quota checks still apply; without a verified alternative the infrastructure error is preserved.
16. AC-016 (positive): Given an account authorized through a group with a different platform label When the account has a legal adaptor and supports the actual model/endpoint/features Then the group label alone cannot exclude it; incompatible features and missing endpoint permissions still reject the path.
17. AC-017 (integration): Given the same account and execution policy with authorized balance origins sharing the applicable tariff but differing in effective multiplier When attributing billing Then select the lowest eligible effective multiplier and use its origin for admission, holds and final charges without ranking different accounts by price; Direct scope cannot borrow another group's discount.
18. AC-018 (negative): Given origins with different tariffs, compaction policies or subscription entitlements When evaluating equivalence Then do not merge them or apply the lowest-multiplier comparison as if they were equivalent.
19. AC-019 (regression): Given an existing session whose authorized account allows sticky-only traffic When evaluating before billing Then admit that session while rejecting a new session from the same sticky-only account; changing billing origin preserves the session binding without merging users or keys.
20. AC-020 (integration): Given preferred and lower-priority eligible accounts When the preferred account is full or loses a slot race Then use an available peer before waiting; among ready peers use effective priority, soft affinity and random equivalent ties, preserving required affinity; configured concurrency only caps admission.
21. AC-021 (regression): Given a soft-sticky namespace cutover When old soft bindings expire Then new bindings may cold-start once, while existing Responses ownership and submitted media task routes remain valid and later group-origin changes do not reset stable session state.
22. AC-022 (regression): Given a shared user-rate query When its first caller cancels Then other callers still receive the loaded rate within the query's finite timeout; a real query failure uses base multiplier 1 without caching that fallback, and a successful absent override still uses the group default.
23. AC-023 (regression): Given native Messages or a legal conversion through account 115/group 1 When checking endpoint permission Then the actual Plan and explicit permission semantics decide admission, without blanket native denial or a billing-platform exemption; Direct and Universal remain equivalent for the same effective request and scope.
24. AC-024 (regression): Given Direct, legacy default-mode and Universal keys sharing a group When resolving exact/family/default group model mappings, selection fallback, Responses body rewriting or Gemini Messages forwarding Then Direct preserves its existing behavior and Universal ignores group mappings without mutating billing or endpoint fields; account mappings still reach actual upstream execution.

25. AC-025 (negative): Given a body read that exceeds the ingress limit or fails after a valid JSON prefix When Universal pre-reads directly or after another preprocessing consumer Then reject before candidate lookup/billing binding, preserve the original read error and return 413 for oversize; complete identity/compressed/multipart payloads remain readable unchanged.
26. AC-026 (integration): Given a Universal WebSocket connection When the first response.create arrives Then validate continuation ownership and the actual account before authorized-path/payment selection, admit valid subscription-only requests without a premature wallet rejection, and never query an ungrouped pool because the handshake has no model; subsequent turns retain execution affinity and billing enforcement.
27. AC-027 (regression): Given equivalent execution and payment paths through differently labeled groups When using Universal Then billing-label changes do not select a different user/platform limit bucket; wallet, Key and actual subscription/source limits remain enforced. Direct platform quotas keep their existing semantics.
28. AC-028 (regression): Given a channel or group-platform query failure When subsequent requests hit the short error cache Then retain the infrastructure error without querying again or reporting an absent channel; after expiry, successful recovery restores the configured tariff and normal cache TTL.
29. AC-029 (regression): Given a normal account and a window-guard reserve account in the same admitted payment tier When memberships are split or duplicated Then global empty-pool recovery keeps the reserve out while the normal candidate remains; when no normal candidate remains it may recover reserves that still pass hard gates.
30. AC-030 (regression): Given supplier-managed projections sharing an endpoint and credential across protocols When a confirmed credential-wide failure occurs or clears Then all projections observe that state; model limits retain the actual upstream model, protocol Plans stay separate, and configured concurrency is not merged merely because credentials match.

31. AC-031 (regression): Given Direct or Universal discovery through OpenAI, Anthropic, Gemini, Antigravity or Codex When projecting capabilities Then use the same complete support paths and billing-policy equivalence as candidate selection, preserve protocol response formats and Direct custom lists, and do not bind payment or let a conflicting path hide a legal peer.

32. AC-032 (regression): Given Direct and Universal requests on any account platform When an account accumulates three attributable failures in a fixed 90-second window Then apply the existing +1000 priority penalty for the resolved upstream model; native paths without a Plan use their forwarding model resolver, large configured capacity and soft affinity cannot rescue it, hard continuation remains fixed, and counter outages preserve configured priority. Caller cancellation and dedicated fault handling do not add this penalty; a shared observation does not also trigger the legacy OpenAI health breaker.
33. AC-033 (integration): Given replayable NewAPI Chat When the upstream hangs before headers or after empty stream output Then cancel the actual connection, release its slot and try another account, returning complete output and one successful usage record; at most three attempts are allowed.
34. AC-034 (negative): Given content or function-tool output before an upstream truncation When finishing the attempt Then never replay or synthesize successful completion, and retain known partial usage; caller cancellation and hard continuations cannot enter pre-output failover.
35. AC-035 (regression): Given thinking and forced tools When evaluating admitted accounts Then prefer an unadjusted Plan within the existing payment tier; unavailable/excluded peers allow thinking-first auto fallback without altering tools, cache or retry input. Direct/Universal streaming and buffered handlers forward the evaluated body; changed endpoint capabilities invalidate stale Plans before transport. Conversion permission constrains Plan before compatibility preference, plan caches remain isolated by that permission, and a cheaper same-account origin cannot displace an authorized exact Plan.
36. AC-036 (regression): Given candidate requests with enabled profit control When admitting, changing billing origins, acquiring or waiting for a slot, or starting a WS turn Then the shared profit owner uses the actual billing origin and pricing instant; an ineligible cost releases capacity and cannot execute.
37. AC-037 (regression): Given an opaque model priced only by the actual billing group's card When checking serving prices Then admission agrees with settlement; unrelated-group and empty prices cannot grant admission.
38. AC-038 (regression): Given Direct and Universal user catalogs When presenting models and authorized groups Then project candidate support across platform memberships and request aliases, preserving cross-origin policy conflicts and rejecting unsupported channel-only rows.
39. AC-039 (negative): Given channel or restricted-account catalog rows When structurally-gone evidence exists Then hide the model; transient evidence preserves it.
40. AC-040 (regression): Given overlapping account/group model sets When building a user menu Then reuse metadata/channel inputs and batch retirement evidence within the request; discovery reuses only its fixed account facts, subsequent requests observe changes, and runtime selection retains fresh account validation. File or registry replacement rotates membership and prices together, including atomic replacement preserving mtime.


41. AC-041 (regression): Given a native-only Direct or Universal path with a thinking/tool adjustment When acquiring or waiting for capacity Then fresh Plan validation preserves conversion permission and remains identical to initial selection; genuinely changed capabilities still invalidate stale Plans.
42. AC-042 (negative): Given Cursor and native Messages, Chat or Responses content When planning Then the actual execution converter and Cursor parser reject unsupported image/thinking content before selection; healthy peers remain eligible and ordinary text/tool schemas are preserved.
43. AC-043 (regression): Given a usable group image/per_request card for a custom model When admitting and recording image usage Then admission and settlement agree on that group's card; unrelated, empty or unusable cards cannot establish a price.
44. AC-044 (regression): Given Codex model aliases and scoped group/channel cards When resolving prices or comparing candidate origins Then literal exact/wildcard cards precede canonical fallback, equivalent tariffs remain eligible, unequal tariffs remain conflicting, and recorded usage uses the resolved group rate.

The Direct/Universal mapping boundary is approved in
`docs/approved/candidate-request-policy-convergence.md`. Mapping isolation, global
account selection, billing rebinding and hard-continuation compatibility are now
implemented in this branch. The coverage section below distinguishes executable
evidence from remaining acceptance work; implementation is not deployment.
Administrative rate changes between hold estimation and settlement are allowed;
the same billing origin is required, but request-level price freezing is not.
Profit-gate platform migration is deferred by the user's explicit decision;
AC-013 preserves existing applicable checks without requiring that expansion.

## Assertions

- Native and converted routes compete by account policy without group rank.
- Cooling and disabled accounts cannot turn supported capacity into a 403.
- Antigravity saturation affects only its resolved model; expiry restores base priority.
- Healthy balance capacity cannot displace a usable subscription due to saturation alone.
- Request parsing precedes billing binding and restores compressed bytes exactly.
- Thinking-dependent model admission, cooldown and saturation use the same request state before account selection and during execution.
- Initial selection, slot acquisition, wait recheck and retry consume CandidateRequest.
- Request-local billing state is snapshotted before asynchronous settlement.
- New soft-affinity identities exclude billing groups; persisted Responses ownership supports legacy reads and rollback-compatible writes.

Legacy group-evaluator tests below remain useful adapter regressions, including
main #2049 Gemini/Responses/count_tokens behavior. They alone do not prove the new
production scheduler. AC-004 follows the approved account-scope policy; its old
mixed-pool test documents only the remaining legacy adapter's behavior.

## Linked Tests

- `backend/internal/service/candidate_thinking_tools_tk_test.go`::`TestCandidateThinkingToolsSlotRecheckPreservesConversionPermission`
- `backend/internal/integration/cursor/messages_test.go`::`TestValidateMessagesContentSharesNativeParser`
- `backend/internal/service/candidate_cursor_ssot_test.go`::`TestCursorPlanPreservesEmulatedWebSearchHistoryCompatibility`
- `backend/internal/service/candidate_cursor_ssot_test.go`::`TestCursorPlanRejectsUnsupportedNativeContent`
- `backend/internal/service/candidate_cursor_ssot_test.go`::`TestCursorNativeContentFailureDoesNotWinCandidateSelection`
- `backend/internal/service/pricing_scope_consistency_tk_test.go`::`TestPricingScopeGroupNormalizedRecordUsage`
- `backend/internal/service/pricing_scope_consistency_tk_test.go`::`TestPricingScopeEquivalentChannelCardsDoNotRejectCandidate`
- `backend/internal/service/pricing_scope_consistency_tk_test.go`::`TestPricingScopeImageGroupPriceGuardSettlementParity`
- `backend/internal/service/pricing_scope_consistency_tk_test.go`::`TestPricingScopeImageEmptyCardsRemainBlocked`
- `backend/internal/service/pricing_scope_consistency_tk_test.go`::`TestPricingScopeLiteralAndWildcardBeforeNormalized`

- `backend/internal/service/me_pricing_performance_tk_test.go`::`TestMePricingMenuBatchesAvailabilityAndReusesInputs`
- `backend/internal/service/me_pricing_performance_tk_test.go`::`TestMenuBatchAvailabilityFailureKeepsModelsWithoutQueryStorm`
- `backend/internal/service/candidate_discovery_snapshot_tk_test.go`::`TestCandidateDiscoveryPlanCacheAvoidsRepeatedSnapshotsAndRefreshesNextRequest`
- `backend/internal/service/pricing_catalog_lookup_perf_tk_test.go`::`TestCatalogMembershipAtomicReplacementWithSameMTime`
- `backend/internal/service/candidate_profit_tk_test.go`::`TestCandidateProfitNativeMessagesRejectsBeforeReservation`
- `backend/internal/service/candidate_profit_tk_test.go`::`TestCandidateProfitReselectionUsesActualBillingOrigin`
- `backend/internal/service/candidate_profit_tk_test.go`::`TestCandidateProfitFreshCostAfterSlotReleasesAndReselects`
- `backend/internal/service/candidate_profit_tk_test.go`::`TestCandidateProfitWaitRechecksFreshCost`
- `backend/internal/service/candidate_profit_tk_test.go`::`TestCandidateProfitWebSocketRevalidationRefreshesOriginGate`
- `backend/internal/service/gateway_priced_serving_group_tk_test.go`::`TestPricedServingGroupPriceMatchesSettlement`
- `backend/internal/service/gateway_priced_serving_group_tk_test.go`::`TestPricedServingUsesCurrentKeyBillingGroup`
- `backend/internal/service/me_pricing_candidate_tk_test.go`::`TestCandidatePricingMenuUsesAuthorizedSupport`
- `backend/internal/service/me_pricing_candidate_tk_test.go`::`TestCandidatePricingMenuRejectsPhantomChannelAndConflictingOrigins`
- `backend/internal/service/me_pricing_candidate_tk_test.go`::`TestCandidatePricingMenuPrunesEveryPriceSource`
- `backend/internal/service/me_pricing_candidate_tk_test.go`::`TestCandidatePricingMenuScopedPriceWithProductionFilter`
- `backend/internal/service/me_pricing_candidate_tk_test.go`::`TestCandidatePricingMenuDoesNotRestoreHiddenManifestRows`
- `frontend/e2e/us050-candidate-pricing-catalog.e2e.ts`

- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityGoogleBackends`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityThinkingModelReadiness`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityCapacityIsNotEntitlement`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityUnknownCapabilityIsNotEntitlement`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityNativeAndConverterEqual`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityMixedPoolUsesSchedulerMembership`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilitySaturationPreservesBillingTier`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityAntigravitySaturationScopeAndParity`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityProductionWiring`
- `backend/internal/service/candidate_eligibility_test.go`::`TestCandidateEligibilityHardGatesPrecedeWindowRecovery`
- `backend/internal/service/candidate_eligibility_tk_tokensea_test.go`::`TestCandidateEligibilityTokenseaUsesPlanAcrossEntrances`
- `backend/internal/repository/antigravity_saturation_counter_cache_test.go`::`TestAntigravitySaturationCounterCache_FixedWindow`
- `backend/internal/server/middleware/universal_routing_tk_test.go`::`TestMaybeResolveUniversal_CandidatePlanPrecedesBillingAndPreservesBody`
- `backend/internal/server/middleware/universal_routing_tk_test.go`::`TestMaybeResolveUniversal_CapacityDoesNotDenyEntitlement`
- `backend/internal/service/universal_subscription_fallback_test.go`::`TestResolve_SubscriptionReadErrorFallback`
- `backend/internal/service/universal_subscription_fallback_test.go`::`TestResolve_SubscriptionMaintenanceErrorFallsBack`
- `backend/internal/service/universal_subscription_fallback_test.go`::`TestResolve_SubscriptionCandidateErrorFallback`
- `backend/internal/server/middleware/universal_routing_tk_test.go`::`TestAuthMiddleware_SubscriptionReadFailureBalanceChecks`
- `backend/internal/service/user_group_rate_resolver_test.go`::`TestUserGroupRateResolverResolve_CanceledLeaderDoesNotCancelSharedRead`
- `backend/internal/service/user_group_rate_resolver_test.go`::`TestUserGroupRateResolverResolve_QueryFailureUsesOneAndRecovers`
- `backend/internal/service/user_group_rate_resolver_test.go`::`TestUserGroupRateResolverResolve_NoOverrideUsesGroupDefault`
- `backend/internal/service/openai_gateway_record_usage_test.go`::`TestOpenAIGatewayServiceRecordUsage_FallsBackToOneOnResolverError`
- `backend/internal/handler/universal_group_model_mapping_test.go`::`TestGroupModelMappingRoutingMode`
- `backend/internal/handler/universal_group_model_mapping_test.go`::`TestUniversalGroupDefaultModelCannotReenterThroughFallback`
- `backend/internal/service/gemini_messages_compat_service_test.go`::`TestGeminiMessagesCompatServiceForward_UniversalKeepsAccountMapping`
- `backend/internal/service/model_rate_limit_test.go`::`TestIsModelRateLimited_UniversalGeminiUsesAccountMapping`
- `backend/internal/server/middleware/universal_body_tk_test.go`::`TestMaybeResolveUniversal_RejectsIncompleteBody`
- `backend/internal/server/middleware/universal_body_tk_test.go`::`TestUniversalBodyPeek_PreservesCompletePayload`
- `backend/internal/server/middleware/universal_body_tk_test.go`::`TestUniversalBodyPeek_PreservesFailureForDirectConsumer`
- `backend/internal/server/middleware/universal_routing_tk_test.go`::`TestPeekImageEditModel_MultipartRestoresBody`
- `backend/internal/service/channel_service_test.go`::`TestBuildCache_DBError`
- `backend/internal/service/channel_service_test.go`::`TestBuildCache_GroupPlatformError`
- `backend/internal/service/channel_service_test.go`::`TestBuildCache_QueryFailureRecovers`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateAccountPriorityIgnoresGroupTopology`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateDeduplicatesMembershipBeforeSelection`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateReadyPeerAndSlotRace`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateFreshRevocationReleasesSlot`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateRebindingFailureNeverReturnsAcquiredAccount`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateWaitRechecksAuthorizationAndPlan`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateWindowRecoveryRunsAfterUnion`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateNativeMessagesDoesNotNeedConversionPermission`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateDirectCompositeAliasPrecedesPlan`
- `backend/internal/server/middleware/candidate_request_tk_test.go`::`TestUS050_HTTPAuthCarriesActualCandidateAndRebindsBillingContext`
- `backend/internal/server/middleware/candidate_request_tk_test.go`::`TestUS050_GoogleAuthUsesCandidateActualAccount`
- `backend/internal/server/middleware/candidate_request_tk_test.go`::`TestUS050_UniversalWebSocketDefersPaymentButEnforcesKeyLimits`
- `backend/internal/handler/candidate_protocol_execute_tk_test.go`::`TestUS050_CandidateNativeRetryUsesAccountTransport`
- `backend/internal/handler/candidate_media_tk_test.go`::`TestUS050_CandidateMediaUsesSelectedModelAndReleasesSlot`
- `backend/internal/handler/candidate_media_tk_test.go`::`TestUS050_CandidateStoredVideoKeepsSubmissionRouteAcrossOrigins`
- `backend/internal/server/middleware/candidate_request_tk_test.go`::`TestUS050_DefaultImageModelUsesCandidateAdmission`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateCapacityIsOnlyAnAdmissionLimit`
- `backend/internal/service/candidate_failure_tk_test.go`::`TestCandidateFailurePriorityDirectUniversalAndModelIsolation`
- `backend/internal/service/candidate_failure_tk_test.go`::`TestCandidateFailureNativeExecutionUsesForwardModel`
- `backend/internal/service/candidate_failure_tk_test.go`::`TestCandidateTransportFailureAttribution`
- `backend/internal/service/candidate_chat_attempt_tk_test.go`::`TestCandidateChatBudgetAndReplayBoundaries`
- `backend/internal/handler/candidate_chat_failover_tk_test.go`::`TestUS050_CandidateChatHangFailoverCompletesAndMetersOnce`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateModelGrantsCannotBeBorrowed`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateWindowRecoveryAndStickyAdmission`
- `backend/internal/service/candidate_selection_tk_test.go`::`TestGlobalCandidateNativeConverterParityAndGoogleFailover`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestCandidateBillingSelectedOriginReachesHoldAndSettlement`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestCandidateBillingOriginUsesSettlementServedModel`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestCandidateBillingOriginUsesUserPriceAndIgnoresGroupOrder`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestCandidateBillingOriginQueryFailureIsNeverTariffEquality`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestCandidateBillingOriginRejectsUnequalApplicablePolicies`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestCandidateBillingOwnReservationCoversAdmissionAndReselection`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestCandidateBillingFailedRebindRestoresPathWithoutRevivingReleasedHold`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestUniversalBillingAdmissionSkipsPlatformQuotaButKeepsBalance`
- `backend/internal/service/candidate_billing_tk_test.go`::`TestUniversalUsageBillingNeverRevivesGroupPlatformQuota`
- `backend/internal/handler/candidate_billing_snapshot_tk_test.go`::`TestCandidateBillingSnapshotSurvivesNextTurnBeforeWorkerRuns`
- `backend/internal/service/candidate_identity_tk_test.go`::`TestCandidateContinuation_NewBindingSurvivesBillingOriginAndKeyChange`
- `backend/internal/service/candidate_identity_tk_test.go`::`TestCandidateContinuation_LegacyReadsOnlyAuthorizedOrigins`
- `backend/internal/service/candidate_identity_tk_test.go`::`TestCandidateContinuation_UnknownUnownedAndFailedLookupsDoNotBecomeNewRequests`
- `backend/internal/service/candidate_identity_tk_test.go`::`TestCandidateContinuation_DoesNotMixOwnerAndAccountNamespaces`
- `backend/internal/repository/gateway_cache_candidate_identity_test.go`::`TestGatewayCache_CandidateAffinityFollowsAccountAcrossBillingOrigins`
- `backend/internal/repository/gateway_cache_candidate_identity_test.go`::`TestGatewayCache_CandidateIdentityPreservesSubmittedMediaBindings`
- `backend/internal/repository/gateway_cache_candidate_identity_test.go`::`TestGatewayCache_CandidateHardStatePersistsAndExpiresWithoutExtendingOwnership`
- `backend/internal/handler/candidate_websocket_tk_test.go`::`TestCandidateWebSocket_FirstFrameAndEachTurnUseAuthorizedPlan`
- `backend/internal/handler/candidate_websocket_tk_test.go`::`TestCandidateWebSocket_RechecksKeyAndEntitlementBeforeNextFrame`
- `backend/internal/handler/candidate_websocket_tk_test.go`::`TestCandidateWebSocket_ZeroWalletUsesActiveSubscription`
- `backend/internal/handler/candidate_websocket_tk_test.go`::`TestCandidateWebSocket_ContinuationPreservesOwnerAndAccountAcrossKeyChange`
- `backend/internal/handler/candidate_websocket_tk_test.go`::`TestCandidateWebSocket_PassthroughReacquiresSlotsForEachTurn`
- `backend/internal/handler/candidate_websocket_tk_test.go`::`TestCandidateWebSocket_ReasoningPolicyFollowsEachTurnBillingOrigin`
- `backend/internal/service/supplier_credential_fault_test.go`::`TestUS050_SupplierCredentialFaultSharesAcrossProtocolProjections`
- `backend/internal/service/supplier_credential_fault_test.go`::`TestUS050_SupplierCredentialRecoveryPreservesPeerModelLimitsAndManualPause`
- `backend/internal/service/supplier_credential_fault_test.go`::`TestUS050_SupplierCredentialFailureFromRotatedCredentialDoesNotBlockPeers`
- `backend/internal/repository/account_repo_supplier_fault_test.go`::`TestSupplierCredentialFaultUpdateGuardsSnapshotAndPreservesIndependentState`
- `backend/internal/service/candidate_discovery_tk_test.go`::`TestUS050_CandidateDiscoveryRejectsConflictingOriginButKeepsPeer`
- `backend/internal/service/candidate_discovery_tk_test.go`::`TestUS050_CandidateDiscoveryKeepsLegalProtocolAfterAnotherPolicyConflict`
- `backend/internal/handler/candidate_discovery_tk_test.go`::`TestUS050_DirectDiscoveryUsesSupportProjectionAndKeepsClientSchemas`
- `backend/internal/handler/candidate_discovery_tk_test.go`::`TestUS050_CodexDiscoveryUsesActualAccountsAndPreservesManifest`
- `backend/internal/server/middleware/candidate_discovery_tk_test.go`::`TestUS050_CandidateDiscoveryAliasesDoNotRequirePayment`

The new tests exercise real service selectors, authentication/handler handoffs,
WebSocket sockets with a controlled upstream, and production cache key formats.
They are backend integration/unit tests, not UI E2E or live supplier probes.

### Coverage Boundaries

| Criteria | Current evidence | Remaining acceptance work |
| --- | --- | --- |
| AC-001/003/004/016/023 | Shared capability regressions, actual-account HTTP/Gemini admission, native account 115 retry, global Vertex/Antigravity failover and equal-priority native/converter competition | Live supplier verification is outside this task. |
| AC-008/009/010 | Real selectors cover conflicting group order, renamed/renumbered split groups, duplicate membership, capacity admission and random ties | Random outcomes are tested for both eligible peers with wide distribution bounds, not a fixed global RNG seed. |
| AC-011/012 | Explicit A/X versus B/Y no-borrowing; Direct/Universal HTTP handoff, model isolation and billing-scope checks | No local acceptance gap. |
| AC-013/017/018/027 | Minimum-origin admission, real hold estimation and final settlement are connected; rollback, delayed snapshots and quota checks also covered | Live price/cost comparison remains a release prerequisite. |
| AC-014/021/026 | Authorization/Plan rechecks after waits, namespace-aware owner tests, stored-media key preservation, actual task polling across key/origin changes with a controlled upstream and real socket turn tests | Production rolling upgrade/rollback has not been exercised. |
| AC-019/020 | Global selectors cover sticky-only old/new session admission, capacity admission, random ties, priority and slot races | No local acceptance gap. |
| AC-029 | Full candidate selection covers split/duplicate membership, normal versus reserve accounts, and hard gates after global recovery | No local acceptance gap. |
| AC-030/031 | Supplier credential sharing/recovery and real repository guards; discovery supports actual accounts and native response schemas | No production configuration writes or live supplier verification were performed. |

| AC-032/033/034 | Direct/Universal handler tests cover real transport cancellation, failover, attempt cap, partial text/tool output and usage preservation; scoped counter and replay boundary tests cover exclusions | Production comparison and original Kimi task completion remain pending. No traffic switch is authorized. |
| AC-036/037 | Profit selection, origin changes, post-slot and WS rechecks; group-only price admission and wrong-group rejection | Live price/cost comparison remains a release prerequisite. |
| AC-038/039 | Candidate catalog tests include production price filters, scoped-only models, hidden manifest rows and retirement evidence; Playwright exercises desktop/mobile pricing filters and readable authorized groups | UI requests use fixtures; they do not prove live supplier capability or production billing. |

Keep the story in InTest until the
remaining production acceptance evidence above is available; these are validation
boundaries, not pending implementation or architectural decisions.
- Run:

```bash
(cd backend && go test -tags unit ./internal/service ./internal/server/middleware ./internal/repository ./internal/handler ./internal/engine/protocolrouter ./internal/server)
python3 scripts/sentinels/check-gateway-tk.py --quiet
python3 scripts/checks/protocol-routing-ssot.py
python3 -m unittest discover -s scripts/checks -p 'test_protocol_routing_ssot.py'
python3 .testing/user-stories/verify_quality.py
```

## Status

- [ ] InTest

The approved 2026-09-08 account scheduling, equivalent-origin billing, discovery
and session-identity policies are implemented in this branch. Local verification
passes with the coverage boundaries listed above. Ordinary soft-sticky cold start is
accepted; hard execution ownership retains compatible reads and writes.
The user prohibits release and deployment in this task.

## Evidence

The implementation passes the complete backend suite (`go test -p 4 -tags unit
./...`), golangci-lint and full preflight against origin/main. Focused billing,
hold, WebSocket and identity cases also pass with the race detector. Protocol
routing guards, gateway sentinels, Story quality and Wire checks pass. These
checks cover the reviewed implementation; production acceptance is still bounded
as documented above. No release, deployment or production write occurred.
