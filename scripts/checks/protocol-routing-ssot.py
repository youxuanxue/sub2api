#!/usr/bin/env python3
"""Guard the protocol-routing SSOT against upstream-merge regressions."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


OWNER_FILES = (
    "backend/internal/engine/protocolrouter/protocol.go",
    "backend/internal/engine/protocolrouter/account.go",
    "backend/internal/engine/protocolrouter/router.go",
    "backend/internal/engine/protocolrouter/registry.go",
    "backend/internal/engine/protocolrouter/router_test.go",
    "backend/internal/engine/protocolrouter/policy_contract_test.go",
    "backend/internal/service/account_supported_protocols.go",
    "backend/internal/service/account_supported_protocols_test.go",
    "backend/internal/service/protocol_endpoint_identity.go",
    "backend/internal/service/protocol_endpoint_identity_test.go",
    "backend/internal/service/protocol_capability_probe.go",
    "backend/internal/service/protocol_capability_probe_gemini.go",
    "backend/internal/service/protocol_capability_probe_test.go",
    "backend/internal/service/protocol_routing_migration.go",
    "backend/internal/service/protocol_routing_migration_test.go",
    "backend/internal/service/openai_apikey_chat_completions_probe_test.go",
    "backend/internal/service/protocol_execution.go",
    "backend/internal/service/protocol_execution_test.go",
    "backend/internal/service/protocol_execution_target_test.go",
    "backend/internal/service/protocol_routing_context.go",
    "backend/internal/service/protocol_routing_context_test.go",
    "backend/internal/service/gateway_scheduling.go",
    "backend/internal/service/openai_account_scheduler.go",
    "backend/internal/service/openai_gateway_scheduling_tk_eligibility_reason.go",
    "backend/internal/service/wire.go",
    "backend/internal/repository/protocol_endpoint_capability_repo.go",
    "backend/internal/repository/protocol_endpoint_capability_repo_integration_test.go",
    "backend/internal/repository/account_repo_protocol_capability_test.go",
    "backend/internal/repository/scheduler_cache.go",
    "backend/internal/repository/scheduler_cache_unit_test.go",
    "backend/ent/schema/account.go",
    "backend/ent/schema/protocol_endpoint_capability.go",
    "backend/migrations/tk_089_protocol_endpoint_capabilities.sql",
    "backend/migrations/tk_089_protocol_endpoint_capabilities_test.go",
    "backend/internal/handler/protocol_request.go",
    "backend/internal/handler/protocol_request_test.go",
    "backend/internal/handler/openai_gemini_protocol_execution.go",
    "backend/internal/handler/openai_gemini_protocol_execution_test.go",
    "backend/internal/handler/wire.go",
    "backend/internal/handler/wire_protocol_ssot_tk.go",
    "backend/internal/handler/openai_gateway_count_tokens.go",
    "backend/internal/handler/gemini_v1beta_handler.go",
    "backend/internal/handler/admin/account_handler.go",
    "backend/internal/server/router.go",
    "backend/internal/server/routes/common.go",
    "backend/internal/server/routes/common_health_test.go",
)

GOVERNED_HANDLERS = (
    "backend/internal/handler/gateway_handler_tk_protocol_execute.go",
    "backend/internal/handler/gateway_handler_chat_completions.go",
    "backend/internal/handler/gateway_handler_responses.go",
    "backend/internal/handler/openai_gateway_handler.go",
    "backend/internal/handler/openai_chat_completions.go",
    "backend/internal/handler/gemini_v1beta_handler.go",
)

HANDLER_GEMINI_EXECUTORS = {
    "backend/internal/handler/gateway_handler_tk_protocol_execute.go": ("MessagesToGemini",),
    "backend/internal/handler/gateway_handler_chat_completions.go": ("ChatToGemini",),
    "backend/internal/handler/gateway_handler_responses.go": ("ResponsesToGemini",),
    "backend/internal/handler/openai_gateway_handler.go": (
        "MessagesToGemini",
        "ResponsesToGemini",
    ),
    "backend/internal/handler/openai_chat_completions.go": ("ChatToGemini",),
    "backend/internal/handler/gemini_v1beta_handler.go": ("GeminiIdentity",),
}

CONCRETE_ADAPTERS = (
    "messagesIdentityAdapter",
    "messagesToResponsesAdapter",
    "messagesToChatAdapter",
    "chatIdentityAdapter",
    "chatToResponsesAdapter",
    "chatToMessagesAdapter",
    "responsesIdentityAdapter",
    "responsesToChatAdapter",
    "responsesToMessagesAdapter",
    "messagesToGeminiAdapter",
    "chatToGeminiAdapter",
    "responsesToGeminiAdapter",
    "geminiIdentityAdapter",
)

LEGACY_ROUTE_SYMBOLS = (
    "GetAPIProtocol",
    "IsAdaptiveAPIProtocol",
    "ShouldUseResponsesAPI",
    "ExtraKeyResponsesSupported",
    "ExtraKeyNativeMessagesSupported",
)

TARGET_BOUNDARIES = {
    "backend/internal/service/openai_gateway_chat_completions.go": (
        "protocolExecutionTarget",
        "protocolExecutionBound",
    ),
    "backend/internal/service/openai_gateway_messages.go": (
        "protocolExecutionTarget",
        "protocolExecutionBound",
    ),
    "backend/internal/service/openai_gateway_forward_tk_protocol_dispatch.go": ("protocolExecutionTarget",),
    "backend/internal/service/openai_gateway_bridge_dispatch.go": ("protocolExecutionBound",),
}

FORWARD_BOUNDARIES = {
    "backend/internal/handler/gateway_handler_tk_protocol_execute.go": (
        r"\bh\.gatewayService\.Forward\s*\(",
    ),
    "backend/internal/handler/gateway_handler_chat_completions.go": (
        r"\.ForwardAsChatCompletions(?:Dispatched)?\s*\(",
    ),
    "backend/internal/handler/gateway_handler_responses.go": (
        r"\.ForwardAsResponses(?:Dispatched)?\s*\(",
    ),
    "backend/internal/handler/openai_chat_completions.go": (
        r"\.ForwardAsChatCompletions(?:Dispatched)?\s*\(",
    ),
    "backend/internal/handler/openai_gateway_handler.go": (
        r"\bh\.gatewayService\.(?:Forward|ForwardAsResponses(?:Dispatched)?|ForwardAsAnthropic(?:Dispatched)?)\s*\(",
    ),
}

AUTHORITATIVE_ACCOUNT_HELPERS = {
    "backend/internal/handler/openai_gateway_handler.go": (
        (
            "prepareResponsesBody",
            r"\bprepareResponsesBody\s*:=\s*func\s*\(\s*executionAccount\s+\*service\.Account\s*,",
            r"\bprepareResponsesBody\s*\(\s*account\s*,",
        ),
    ),
    "backend/internal/handler/openai_chat_completions.go": (
        (
            "prepareBody",
            r"\bprepareBody\s*:=\s*func\s*\(\s*executionAccount\s+\*service\.Account\s*,",
            r"\bprepareBody\s*\(\s*account\s*,",
        ),
    ),
    "backend/internal/handler/gemini_v1beta_handler.go": (
        (
            "forwardNonGoverned",
            r"\bforwardNonGoverned\s*:=\s*func\s*\(\s*executionCtx\s+context\.Context\s*,\s*executionAccount\s+\*service\.Account\s*,",
            r"\bforwardNonGoverned\s*\(\s*executionCtx\s*,\s*account\s*,",
        ),
    ),
}


def strip_go_comments_and_literals(source: str) -> str:
    """Preserve identifiers/operators while blanking comments and literals."""
    out: list[str] = []
    i = 0
    state = "code"
    while i < len(source):
        ch = source[i]
        nxt = source[i + 1] if i + 1 < len(source) else ""
        if state == "code":
            if ch == "/" and nxt == "/":
                out.extend("  ")
                i += 2
                state = "line_comment"
            elif ch == "/" and nxt == "*":
                out.extend("  ")
                i += 2
                state = "block_comment"
            elif ch == '"':
                out.append(" ")
                i += 1
                state = "string"
            elif ch == "'":
                out.append(" ")
                i += 1
                state = "rune"
            elif ch == "`":
                out.append(" ")
                i += 1
                state = "raw"
            else:
                out.append(ch)
                i += 1
        elif state == "line_comment":
            if ch == "\n":
                out.append("\n")
                state = "code"
            else:
                out.append(" ")
            i += 1
        elif state == "block_comment":
            if ch == "*" and nxt == "/":
                out.extend("  ")
                i += 2
                state = "code"
            else:
                out.append("\n" if ch == "\n" else " ")
                i += 1
        elif state in {"string", "rune"}:
            if ch == "\\" and i + 1 < len(source):
                out.extend("  ")
                i += 2
            elif (state == "string" and ch == '"') or (state == "rune" and ch == "'"):
                out.append(" ")
                i += 1
                state = "code"
            else:
                out.append("\n" if ch == "\n" else " ")
                i += 1
        else:  # raw string
            if ch == "`":
                out.append(" ")
                state = "code"
            else:
                out.append("\n" if ch == "\n" else " ")
            i += 1
    return "".join(out)


def contains_identifier(source: str, identifier: str) -> bool:
    return re.search(rf"\b{re.escape(identifier)}\b", source) is not None


def call_spans(source: str, identifier: str) -> list[tuple[int, int]]:
    spans: list[tuple[int, int]] = []
    for match in re.finditer(rf"\b{re.escape(identifier)}\s*\(", source):
        open_paren = source.find("(", match.start(), match.end())
        depth = 0
        for index in range(open_paren, len(source)):
            if source[index] == "(":
                depth += 1
            elif source[index] == ")":
                depth -= 1
                if depth == 0:
                    spans.append((match.start(), index + 1))
                    break
    return spans


def inside_any_span(index: int, spans: list[tuple[int, int]]) -> bool:
    return any(start <= index < end for start, end in spans)


def function_definitions(source: str, identifier: str) -> list[str]:
    definitions: list[str] = []
    pattern = re.compile(
        rf"\bfunc\s+(?:\([^)]*\)\s*)?{re.escape(identifier)}\s*\([^)]*\)[^{{]*{{"
    )
    for match in pattern.finditer(source):
        open_brace = source.find("{", match.start(), match.end())
        depth = 0
        for index in range(open_brace, len(source)):
            if source[index] == "{":
                depth += 1
            elif source[index] == "}":
                depth -= 1
                if depth == 0:
                    definitions.append(source[match.start() : index + 1])
                    break
    return definitions


def function_bodies(source: str, identifier: str) -> list[str]:
    bodies: list[str] = []
    for definition in function_definitions(source, identifier):
        open_brace = definition.find("{")
        bodies.append(definition[open_brace + 1 : -1])
    return bodies


def matching_brace(source: str, open_brace: int) -> int | None:
    depth = 0
    for index in range(open_brace, len(source)):
        if source[index] == "{":
            depth += 1
        elif source[index] == "}":
            depth -= 1
            if depth == 0:
                return index
    return None


def publication_has_preliminary_readiness_guard(body: str) -> bool:
    publish_calls = call_spans(body, "PublishProtocolRoutingProjections")
    if not publish_calls:
        return False
    publish_index = publish_calls[0][0]

    positive_guard = re.compile(
        r"\bif\s+prepared\s*\.\s*CutoverReady\s*\{"
    )
    for match in positive_guard.finditer(body):
        open_brace = body.find("{", match.start(), match.end())
        close_brace = matching_brace(body, open_brace)
        if close_brace is not None and open_brace < publish_index < close_brace:
            return True

    negative_guard = re.compile(
        r"\bif\s*!\s*prepared\s*\.\s*CutoverReady\s*\{"
    )
    for match in negative_guard.finditer(body):
        open_brace = body.find("{", match.start(), match.end())
        close_brace = matching_brace(body, open_brace)
        if close_brace is None or close_brace >= publish_index:
            continue
        if contains_identifier(body[open_brace + 1 : close_brace], "return"):
            return True
    return False


def check(root: Path) -> list[str]:
    errors: list[str] = []
    for relative in OWNER_FILES:
        if not (root / relative).is_file():
            errors.append(f"missing protocol-routing owner/test: {relative}")

    for relative in GOVERNED_HANDLERS:
        path = root / relative
        if not path.is_file():
            errors.append(f"missing governed handler: {relative}")
            continue
        source = strip_go_comments_and_literals(path.read_text(encoding="utf-8"))
        for required in ("WithProtocolRouting", "ExecuteSelectedProtocol"):
            if not contains_identifier(source, required):
                errors.append(f"{relative}: missing {required} execution boundary")
        for required in HANDLER_GEMINI_EXECUTORS.get(relative, ()):
            if not contains_identifier(source, required):
                errors.append(f"{relative}: missing governed Gemini executor {required}")
        for forbidden in LEGACY_ROUTE_SYMBOLS:
            if contains_identifier(source, forbidden):
                errors.append(f"{relative}: handler route decision uses legacy symbol {forbidden}")

        execute_spans = call_spans(source, "ExecuteSelectedProtocol")
        for start, end in execute_spans:
            call = source[start:end]
            if not contains_identifier(call, "ValidateProtocolEndpoint"):
                errors.append(f"{relative}: ExecuteSelectedProtocol is missing endpoint validator")
            if not contains_identifier(call, "LoadProtocolExecutionAccount"):
                errors.append(f"{relative}: ExecuteSelectedProtocol is missing authoritative account loader")
            if not contains_identifier(call, "ProtocolExecutors"):
                errors.append(f"{relative}: ExecuteSelectedProtocol is missing concrete executors")
            if re.search(r"\bProtocolExecutors\s*{\s*}", call):
                errors.append(f"{relative}: ExecuteSelectedProtocol has no route executor")
        if execute_spans and not re.search(r"\brequest\s*\.\s*Body\s*\(", source):
            errors.append(f"{relative}: executor does not consume canonical request.Body")
        if re.search(r"\bTargetProtocol\s*\(", source):
            errors.append(f"{relative}: handler performs a second TargetProtocol route decision")
        for profile in (
            "GeminiEndpointAntigravityCloudCode",
            "GeminiEndpointVertexServiceAccount",
        ):
            if contains_identifier(source, profile):
                errors.append(f"{relative}: handler selects Gemini endpoint profile {profile}")
        for pattern in FORWARD_BOUNDARIES.get(relative, ()):
            for match in re.finditer(pattern, source):
                if not inside_any_span(match.start(), execute_spans):
                    errors.append(
                        f"{relative}: forwarding call outside ExecuteSelectedProtocol boundary"
                    )
        for helper, definition_pattern, call_pattern in AUTHORITATIVE_ACCOUNT_HELPERS.get(relative, ()):
            if not re.search(definition_pattern, source) or not re.search(call_pattern, source):
                errors.append(
                    f"{relative}: {helper} does not consume the authoritative execution account"
                )

    for relative, required_symbols in TARGET_BOUNDARIES.items():
        path = root / relative
        if not path.is_file():
            errors.append(f"missing target-bound transport owner: {relative}")
            continue
        source = strip_go_comments_and_literals(path.read_text(encoding="utf-8"))
        for required in required_symbols:
            if not contains_identifier(source, required):
                errors.append(f"{relative}: missing selected-plan guard {required}")

    protocol_owner = root / "backend/internal/engine/protocolrouter/protocol.go"
    if protocol_owner.is_file():
        source = strip_go_comments_and_literals(protocol_owner.read_text(encoding="utf-8"))
        if not contains_identifier(source, "ProtocolGeminiGenerateContent"):
            errors.append("protocol registry is missing ProtocolGeminiGenerateContent")

    protocol_account_owner = root / "backend/internal/engine/protocolrouter/account.go"
    if protocol_account_owner.is_file():
        source = strip_go_comments_and_literals(protocol_account_owner.read_text(encoding="utf-8"))
        for required in (
            "GeminiEndpointAntigravityCloudCode",
            "GeminiEndpointVertexServiceAccount",
        ):
            if not contains_identifier(source, required):
                errors.append(f"protocol account profile owner is missing {required}")

    canonical_owner = root / "backend/internal/service/account_supported_protocols.go"
    if canonical_owner.is_file():
        source = strip_go_comments_and_literals(canonical_owner.read_text(encoding="utf-8"))
        for forbidden in LEGACY_ROUTE_SYMBOLS:
            if contains_identifier(source, forbidden):
                errors.append(f"canonical capability owner reads legacy symbol {forbidden}")
        for required in (
            "BuildSupportedProtocolsUpdate",
            "ReplaceSupportedProtocols",
            "SeedOfficialSupportedProtocols",
            "GeminiEndpointAntigravityCloudCode",
            "GeminiEndpointVertexServiceAccount",
            "IsNewAPIVertexServiceAccount",
        ):
            if not contains_identifier(source, required):
                errors.append(f"canonical capability owner is missing {required}")
        supported_protocol_bodies = function_bodies(source, "SupportedProtocols")
        if not supported_protocol_bodies:
            errors.append("canonical capability owner is missing Account.SupportedProtocols")
        for body in supported_protocol_bodies:
            if not contains_identifier(body, "ProtocolEndpointCapability"):
                errors.append("Account.SupportedProtocols does not read the linked capability")
            for forbidden in ("Extra", "SupportedProtocolsExtraKey", "LegacySupportedProtocolsProjection"):
                if contains_identifier(body, forbidden):
                    errors.append("Account.SupportedProtocols reads the legacy account projection")
        snapshot_bodies = function_bodies(source, "protocolAccountSnapshot")
        if not snapshot_bodies:
            errors.append("canonical capability owner is missing protocolAccountSnapshot")
        for body in snapshot_bodies:
            if not contains_identifier(body, "protocolCapabilityHasVerifiedRoutingEvidence"):
                errors.append("protocolAccountSnapshot does not fail closed on unverified capability evidence")
            if not contains_identifier(body, "retainResolvedNewAPIExactProtocols"):
                errors.append(
                    "protocolAccountSnapshot still plans NewAPI protocols that have no exact endpoint"
                )
        exact_bodies = function_bodies(source, "protocolExactEndpoints")
        if not exact_bodies:
            errors.append("canonical capability owner is missing protocolExactEndpoints")
        for body in exact_bodies:
            if not contains_identifier(body, "protocolExactEndpoint"):
                errors.append("protocolExactEndpoints does not resolve NewAPI protocol URLs")
            if not contains_identifier(body, "routingSupportedProtocols"):
                errors.append(
                    "protocolExactEndpoints does not limit NewAPI exact URLs to declared protocols"
                )
            if re.search(
                r"protocolExactEndpoint\s*\([\s\S]*?if\s+err\s*!=\s*nil\s*\{\s*return\s+nil,\s*err",
                body,
            ):
                errors.append(
                    "NewAPI exact-endpoint resolution still fail-closes the snapshot on one unsupported protocol"
                )

    identity_owner = root / "backend/internal/service/protocol_endpoint_identity.go"
    if identity_owner.is_file():
        source = strip_go_comments_and_literals(identity_owner.read_text(encoding="utf-8"))
        for required in ("BuildProtocolEndpointIdentity", "CanonicalJSON", "Key", "sha256"):
            if not contains_identifier(source, required):
                errors.append(f"protocol endpoint identity owner is missing {required}")
        normalization_bodies = function_bodies(source, "normalizeEndpointIdentityURL")
        if not normalization_bodies or not all(
            contains_identifier(body, "protocolEndpointCredentialQueryParam")
            for body in normalization_bodies
        ):
            errors.append("protocol endpoint identity normalization does not exclude query credentials")
        credential_query_bodies = function_bodies(
            source,
            "protocolEndpointCredentialQueryParam",
        )
        if not credential_query_bodies or not all(
            contains_identifier(body, "isSensitiveKey") for body in credential_query_bodies
        ):
            errors.append("protocol endpoint identity does not reuse the sensitive query-key classifier")

    identity_test = root / "backend/internal/service/protocol_endpoint_identity_test.go"
    if identity_test.is_file():
        source = strip_go_comments_and_literals(identity_test.read_text(encoding="utf-8"))
        bodies = function_bodies(
            source,
            "TestBuildProtocolEndpointIdentityExcludesQueryCredentialsButKeepsSemanticRouting",
        )
        if not bodies or not all(
            len(call_spans(body, "BuildProtocolEndpointIdentity")) >= 2
            and contains_identifier(body, "Key")
            for body in bodies
        ):
            errors.append("protocol routing is missing the query credential identity regression test")

    service_root = root / "backend/internal/service"
    if service_root.is_dir():
        for path in service_root.glob("*.go"):
            if path.name.endswith("_test.go") or path == identity_owner:
                continue
            source = strip_go_comments_and_literals(path.read_text(encoding="utf-8"))
            if function_definitions(source, "BuildProtocolEndpointIdentity"):
                errors.append(f"{path.relative_to(root)}: duplicates the endpoint identity builder")
            if path.name == "account_supported_protocols.go" and function_definitions(source, "protocolAccountRevision"):
                errors.append("account snapshot still invents a protocol revision hash")
            if path.name not in {"account_supported_protocols.go", "protocol_routing_migration.go"} and contains_identifier(
                source, "LegacySupportedProtocolsProjection"
            ):
                errors.append(f"{path.relative_to(root)}: consumes the legacy protocol projection outside migration")

    router_owner = root / "backend/internal/engine/protocolrouter/router.go"
    if router_owner.is_file():
        source = strip_go_comments_and_literals(router_owner.read_text(encoding="utf-8"))
        execute_bodies = function_bodies(source, "Execute")
        if not execute_bodies:
            errors.append("protocol router is missing Execute stale-plan validation")
        for body in execute_bodies:
            if not contains_identifier(body, "CapabilityKey"):
                errors.append("protocol router Execute is missing capability key identity validation")
            if contains_identifier(body, "CapabilityRevision") or contains_identifier(body, "accountRevision"):
                errors.append("protocol router Execute still treats revision tokens as the stale referee")

    capability_repo = root / "backend/internal/repository/protocol_endpoint_capability_repo.go"
    if capability_repo.is_file():
        source = strip_go_comments_and_literals(capability_repo.read_text(encoding="utf-8"))
        for required in (
            "EnsureAccountLink",
            "GetByAccountID",
            "ListLinkedAccountIDs",
            "AcquireProbeLease",
            "CommitProbeResult",
            "CommitPreparedProbeResult",
            "PublishProtocolRoutingProjections",
            "writeRollbackProjections",
        ):
            if not contains_identifier(source, required):
                errors.append(f"protocol capability repository is missing {required}")
        commit_bodies = function_bodies(source, "CommitProbeResult")
        for body in commit_bodies:
            if not contains_identifier(body, "commitProbeResult") or not re.search(r"\btrue\b", body):
                errors.append("normal capability probe commit does not select atomic publication")
        prepared_commit_bodies = function_bodies(source, "CommitPreparedProbeResult")
        for body in prepared_commit_bodies:
            if not contains_identifier(body, "commitProbeResult") or not re.search(r"\bfalse\b", body):
                errors.append("prepared capability probe commit does not remain silent")
        shared_commit_bodies = function_bodies(source, "commitProbeResult")
        for body in shared_commit_bodies:
            if not contains_identifier(body, "publishProtocolCapability") or not contains_identifier(body, "publish"):
                errors.append("capability probe commit does not separate prepared and published persistence")
        ensure_bodies = function_bodies(source, "ensureProtocolEndpointCapabilityLink")
        for body in ensure_bodies:
            for forbidden in (
                "writeRollbackProjections",
                "publishProtocolCapability",
                "enqueueSchedulerOutbox",
            ):
                if contains_identifier(body, forbidden):
                    errors.append(
                        f"capability link silent preparation publishes legacy state through {forbidden}"
                    )
            if not re.search(
                r"\b\w+\s*\.\s*LastProbedAt\s*!=\s*nil\s*&&\s*!\s*officialSeed\s*\{[\s\S]*?\bseedProtocols\s*=\s*nil",
                body,
            ):
                errors.append("capability historical projection seed survives an accepted probe")
        publication_bodies = function_bodies(source, "publishProtocolCapability")
        for body in publication_bodies:
            if not contains_identifier(body, "writeRollbackProjections"):
                errors.append("capability publication omits the rollback projection")
            if not re.search(
                r"changedAccountIDs\s*,\s*\w+\s*:=\s*writeRollbackProjections\s*\(",
                body,
            ):
                errors.append("capability publication does not derive scheduler invalidations from changed projections")
            if not contains_identifier(body, "enqueueSchedulerOutbox"):
                errors.append("capability publication does not publish scheduler cache invalidation")
            if not re.search(
                r"for\s+_,\s+\w+\s*:=\s*range\s+changedAccountIDs\s*\{[\s\S]*?enqueueSchedulerOutbox\s*\([^)]*&\w+",
                body,
            ):
                errors.append("capability publication does not invalidate all changed accounts")
        publish_all_bodies = function_bodies(source, "PublishProtocolRoutingProjections")
        if not publish_all_bodies or not all(
            contains_identifier(body, "publishProtocolCapability") for body in publish_all_bodies
        ):
            errors.append("protocol capability repository is missing the atomic publication owner")

    migration_owner = root / "backend/internal/service/protocol_routing_migration.go"
    if migration_owner.is_file():
        source = strip_go_comments_and_literals(migration_owner.read_text(encoding="utf-8"))
        if contains_identifier(source, "TrafficReady"):
            errors.append("protocol routing report still carries process TrafficReady")
        if contains_identifier(source, "ProbeAccountProtocolCapabilities"):
            errors.append("protocol routing startup uses the normal probe writer instead of the preparation probe")
        if not contains_identifier(source, "ProbeAccountProtocolCapabilitiesForPreparation"):
            errors.append("protocol routing startup is missing the preparation probe")
        evaluate_bodies = function_bodies(source, "evaluateProtocolRoutingSSOT")
        if not evaluate_bodies:
            errors.append("protocol routing startup is missing account evaluation")
        for body in evaluate_bodies:
            if re.search(r"TrafficReady\s*=\s*false", body):
                errors.append("account evaluation still treats one account remediation as process TrafficReady")
        prepare_bodies = function_bodies(source, "prepareProtocolRoutingSSOT")
        for body in prepare_bodies:
            match = re.search(
                r"if\s+errors\s*\.\s*Is\s*\(\s*err\s*,\s*errProtocolRoutingFinalReadinessNotReady\s*\)\s*\{",
                body,
            )
            if match is None:
                continue
            open_brace = body.find("{", match.start(), match.end())
            close_brace = matching_brace(body, open_brace)
            if close_brace is None:
                continue
            if re.search(r"TrafficReady\s*=\s*false", body[open_brace + 1 : close_brace]):
                errors.append(
                    "publication-time CutoverReady failure still treats one account as process TrafficReady"
                )
        evidence_bodies = function_bodies(source, "protocolCapabilityHasVerifiedRoutingEvidence")
        if not evidence_bodies or not all(
            contains_identifier(body, "OfficialSeed")
            and contains_identifier(body, "InitialProbeCompleted")
            and contains_identifier(body, "ProtocolProbePositive")
            and contains_identifier(body, "IdentityConflict")
            for body in evidence_bodies
        ):
            errors.append("protocol routing verified-evidence helper is missing official, probe, or conflict gates")
        preparation_bodies = function_bodies(source, "prepareProtocolRoutingSSOT")
        for body in preparation_bodies:
            if not contains_identifier(body, "PublishProtocolRoutingProjections"):
                errors.append("protocol routing startup is missing the atomic publication boundary")
                continue
            if not publication_has_preliminary_readiness_guard(body):
                errors.append("protocol routing startup publication lacks an exact preliminary readiness guard")
            publish_calls = call_spans(body, "PublishProtocolRoutingProjections")
            publish_call = body[publish_calls[0][0] : publish_calls[0][1]] if publish_calls else ""
            if (
                not call_spans(publish_call, "validateProtocolRoutingSSOTReadiness")
                or not contains_identifier(publish_call, "CutoverReady")
            ):
                errors.append("protocol routing final readiness is outside the publication transaction callback")

    execution_owner = root / "backend/internal/service/protocol_execution.go"
    if execution_owner.is_file():
        source = strip_go_comments_and_literals(execution_owner.read_text(encoding="utf-8"))
        for adapter in CONCRETE_ADAPTERS:
            if not contains_identifier(source, adapter):
                errors.append(f"protocol execution owner is missing concrete adapter {adapter}")
        if not contains_identifier(source, "ExecuteGeminiProtocolProfile"):
            errors.append("protocol execution owner is missing Gemini profile dispatch")
        for forbidden in ("requestScopedProtocolAdapter", "WithProtocolExecution"):
            if contains_identifier(source, forbidden):
                errors.append(f"protocol execution owner contains generic protocol adapter {forbidden}")
        execute_selected_bodies = function_bodies(source, "ExecuteSelectedProtocol")
        for body in execute_selected_bodies:
            if not contains_identifier(body, "loadAccount"):
                errors.append("selected protocol execution skips authoritative account reload")
            if not contains_identifier(body, "withProtocolExecutionAccount"):
                errors.append("selected protocol execution does not bind the authoritative account to executors")
            if not re.search(r"CredentialPresent\s*:\s*ProtocolAuthorizationPresent\s*\(\s*freshAccount\s*\)", body):
                errors.append("selected protocol execution derives credential readiness from a stale account")
            if not contains_identifier(body, "protocolPlansRoutingEquivalent"):
                errors.append("selected protocol execution skips equivalent-route replan before send")
            if len(call_spans(body, "protocolExecutionPreSendFailure")) < 3:
                errors.append("selected protocol execution leaves a pre-send stale failure outside account failover")
            if not contains_identifier(body, "ErrMissingCredential"):
                errors.append("selected protocol execution leaves a missing credential outside account failover")
        equivalent_bodies = function_bodies(source, "protocolPlansRoutingEquivalent")
        for body in equivalent_bodies:
            if contains_identifier(body, "CapabilityRevision") or contains_identifier(body, "AccountRevision"):
                errors.append("equivalent-route check still compares revision tokens")
        credential_bodies = function_bodies(source, "ProtocolAuthorizationPresent")
        if not credential_bodies or not all(
            contains_identifier(body, "ProtocolAuthorizationSnapshotCredentialKey")
            and contains_identifier(body, "IsNewAPIVertexServiceAccount")
            and contains_identifier(body, "parseVertexServiceAccountKey")
            and contains_identifier(body, "protocolAuthorizationToken")
            for body in credential_bodies
        ):
            errors.append("protocol runtime authorization does not validate the exact credential shape")
        authorization_bodies = function_bodies(source, "protocolRuntimeAuthorizationReady")
        if not authorization_bodies or not all(
            contains_identifier(body, "ProtocolRoutingRequest")
            and contains_identifier(body, "ProtocolAuthorizationPresent")
            for body in authorization_bodies
        ):
            errors.append("protocol runtime authorization hard gate is missing its credential owner")
        pre_send_failure_bodies = function_bodies(source, "protocolExecutionPreSendFailure")
        if not pre_send_failure_bodies or not all(
            contains_identifier(body, "UpstreamFailoverError")
            and contains_identifier(body, "applyGatewayFailoverSemantic")
            and contains_identifier(body, "gatewayFailureSemanticAccountFault")
            for body in pre_send_failure_bodies
        ):
            errors.append("protocol pre-send failure owner does not provide retry-next-account semantics")

    execution_test = root / "backend/internal/service/protocol_execution_test.go"
    if execution_test.is_file():
        source = strip_go_comments_and_literals(execution_test.read_text(encoding="utf-8"))
        bodies = function_bodies(
            source,
            "TestExecuteSelectedProtocolFailsOverMissingAuthorizationBeforeExecutor",
        )
        if not bodies or not all(
            contains_identifier(body, "ExecuteSelectedProtocol") for body in bodies
        ):
            errors.append("protocol routing is missing the missing-authorization execution regression test")

    probe_owner = root / "backend/internal/service/protocol_capability_probe.go"
    if probe_owner.is_file():
        source = strip_go_comments_and_literals(probe_owner.read_text(encoding="utf-8"))
        run_bodies = function_bodies(source, "runEndpointProtocolProbe")
        for body in run_bodies:
            if not contains_identifier(body, "probeProtocolWitnesses"):
                errors.append("endpoint probe bypasses conclusive witness stopping owner")
            if not contains_identifier(body, "IdentityConflict"):
                errors.append("endpoint probe does not preserve conflict when no witness is usable")
            if not contains_identifier(body, "ProbeEvidence"):
                errors.append("endpoint probe does not compare persisted verdict evidence")
            if not contains_identifier(body, "selectProtocolProbeWitnesses"):
                errors.append("endpoint probe bypasses witness eligibility selection")
        resolver_bodies = function_bodies(source, "resolveProtocolProbeGeneration")
        for body in resolver_bodies:
            if not re.search(
                r"\bpriorVerdict\s*:=\s*conclusiveProtocolProbeVerdict\s*\(", body
            ):
                errors.append("probe generation resolver ignores prior conclusive verdict evidence")
            persisted_calls = len(call_spans(body, "persistedProtocolProbeVerdict"))
            persisted_assignments = len(re.findall(
                r"resolution\s*\.\s*Evidence\s*\[[^]]+\]\s*=\s*persistedProtocolProbeVerdict\s*\(",
                body,
            ))
            if persisted_calls == 0 or persisted_assignments != persisted_calls:
                errors.append("probe generation resolver overwrites prior conclusive evidence with inconclusive results")
                errors.append("probe generation resolver discards the persisted verdict result")
        witness_bodies = function_bodies(source, "selectProtocolProbeWitnesses")
        for body in witness_bodies:
            if not contains_identifier(body, "protocolProbeAuthorizationUsable"):
                errors.append("protocol probe witness selection does not filter unusable authorization")
        authorization_bodies = function_bodies(source, "protocolProbeAuthorizationUsable")
        if not authorization_bodies or not all(
            contains_identifier(body, "ProtocolAuthorizationPresent")
            for body in authorization_bodies
        ):
            errors.append("protocol probe authorization does not reuse the shared credential owner")
        token_bodies = function_bodies(source, "protocolAuthorizationToken")
        if not token_bodies or not all(
            contains_identifier(body, "IsOpenAIOAuthLike")
            and contains_identifier(body, "GetOpenAIAccessToken")
            for body in token_bodies
        ):
            errors.append("protocol authorization owner is missing the OpenAI OAuth access token")

    probe_test = root / "backend/internal/service/protocol_capability_probe_test.go"
    if probe_test.is_file():
        source = strip_go_comments_and_literals(probe_test.read_text(encoding="utf-8"))
        bodies = function_bodies(
            source,
            "TestSelectProtocolProbeWitnessesFiltersUnusableAuthorizationBeforeBound",
        )
        if not bodies or not all(
            contains_identifier(body, "selectProtocolProbeWitnesses") for body in bodies
        ):
            errors.append("protocol probe is missing the Vertex probe witness regression test")

    account_schema = root / "backend/ent/schema/account.go"
    if account_schema.is_file():
        raw_source = account_schema.read_text(encoding="utf-8")
        source = strip_go_comments_and_literals(raw_source)
        if 'field.Int64("protocol_endpoint_capability_id")' not in raw_source:
            errors.append("account Ent schema is missing protocol capability FK field")
        if 'edge.From("protocol_endpoint_capability", ProtocolEndpointCapability.Type)' not in raw_source:
            errors.append("account Ent schema is missing protocol capability inverse edge")
        if not contains_identifier(source, "ProtocolEndpointCapability"):
            errors.append("account Ent schema is missing protocol capability type binding")
    capability_schema = root / "backend/ent/schema/protocol_endpoint_capability.go"
    if capability_schema.is_file():
        source = strip_go_comments_and_literals(capability_schema.read_text(encoding="utf-8"))
        if not (contains_identifier(source, "OnDelete") and contains_identifier(source, "Restrict")):
            errors.append("protocol capability Ent edge is missing ON DELETE RESTRICT")

    account_repo = root / "backend/internal/repository/account_repo.go"
    if account_repo.is_file():
        source = strip_go_comments_and_literals(account_repo.read_text(encoding="utf-8"))
        update_credentials_bodies = function_bodies(source, "UpdateCredentials")
        for body in update_credentials_bodies:
            if not contains_identifier(body, "loadAccountForProtocolCapabilityLifecycle"):
                errors.append("credential replacement skips capability lifecycle reload")
            if not contains_identifier(body, "ensureAccountProtocolEndpointCapability"):
                errors.append("credential replacement skips capability identity relink")

    scheduler_owner = root / "backend/internal/repository/scheduler_cache.go"
    if scheduler_owner.is_file():
        source = strip_go_comments_and_literals(scheduler_owner.read_text(encoding="utf-8"))
        metadata_bodies = function_bodies(source, "buildSchedulerMetadataAccount")
        if not metadata_bodies or not all(
            contains_identifier(body, "filterSchedulerCredentialsForProtocolRouting")
            for body in metadata_bodies
        ):
            errors.append("scheduler metadata drops protocol endpoint identity credentials")
        credential_bodies = function_bodies(
            source,
            "filterSchedulerCredentialsForProtocolRouting",
        )
        if not credential_bodies or not all(
            contains_identifier(body, "IsNewAPIVertexServiceAccount")
            and contains_identifier(body, "VertexProjectID")
            and contains_identifier(body, "ProtocolAuthorizationPresent")
            and contains_identifier(body, "ProtocolAuthorizationSnapshotCredentialKey")
            for body in credential_bodies
        ):
            errors.append("scheduler metadata does not preserve protocol identity and authorization readiness")

    scheduler_test = root / "backend/internal/repository/scheduler_cache_unit_test.go"
    if scheduler_test.is_file():
        source = strip_go_comments_and_literals(scheduler_test.read_text(encoding="utf-8"))
        bodies = function_bodies(
            source,
            "TestBuildSchedulerMetadataAccountPreservesProtocolEndpointIdentity",
        )
        if not bodies or not all(
            len(call_spans(body, "BuildProtocolEndpointIdentity")) >= 2
            and contains_identifier(body, "buildSchedulerMetadataAccount")
            and contains_identifier(body, "ProtocolAuthorizationSnapshotCredentialKey")
            and contains_identifier(body, "Key")
            for body in bodies
        ):
            errors.append("protocol routing is missing the scheduler identity and authorization regression test")

    routing_context = root / "backend/internal/service/protocol_routing_context.go"
    if routing_context.is_file():
        source = strip_go_comments_and_literals(routing_context.read_text(encoding="utf-8"))
        for required in ("protocolPlanCache", "getOrPlan", "get"):
            if not contains_identifier(source, required):
                errors.append(f"protocol routing context is missing scheduler plan cache {required}")
        planning_bodies = function_bodies(source, "protocolPlanForAccount")
        if not planning_bodies:
            errors.append("protocol routing context is missing protocolPlanForAccount")
        for body in planning_bodies:
            if not contains_identifier(body, "getOrPlan"):
                errors.append("protocol routing context bypasses the per-account plan cache")
        bodies = function_bodies(source, "attachProtocolPlan")
        if not bodies:
            errors.append("protocol routing context is missing attachProtocolPlan")
        for body in bodies:
            if contains_identifier(body, "protocolPlanForAccount") or re.search(r"\.\s*Plan\s*\(", body):
                errors.append("protocol selection performs secondary protocol planning")
            if not re.search(r"\.\s*get\s*\(", body):
                errors.append("protocol selection does not reuse scheduler-created plan")

    gateway_scheduling = root / "backend/internal/service/gateway_scheduling.go"
    if gateway_scheduling.is_file():
        source = strip_go_comments_and_literals(gateway_scheduling.read_text(encoding="utf-8"))
        bodies = function_bodies(source, "isAccountSchedulableForModelSelection")
        if not bodies or not all(
            re.search(
                r"\breturn\b[\s\S]*?protocolRuntimeAuthorizationReady\s*\([^;]*?\)\s*&&\s*ProtocolRouteLegal\s*\(",
                body,
            )
            for body in bodies
        ):
            errors.append("gateway scheduler authorization hard gate is not composed with protocol legality")

    openai_scheduler = root / "backend/internal/service/openai_account_scheduler.go"
    if openai_scheduler.is_file():
        source = strip_go_comments_and_literals(openai_scheduler.read_text(encoding="utf-8"))
        bodies = function_bodies(source, "isAccountRequestCompatibleReason")
        if not bodies or not all(
            re.search(
                r"if\s*!\s*protocolRuntimeAuthorizationReady\s*\([^;]*?\)\s*\{[\s\S]*?return\s+false\b",
                body,
            )
            and contains_identifier(body, "protocolRequestEligibilityReason")
            for body in bodies
        ):
            errors.append("OpenAI scheduler authorization hard gate is not composed with protocol legality")

    openai_eligibility = root / "backend/internal/service/openai_gateway_scheduling_tk_eligibility_reason.go"
    if openai_eligibility.is_file():
        source = strip_go_comments_and_literals(openai_eligibility.read_text(encoding="utf-8"))
        bodies = function_bodies(source, "openAICompatEligibilityReason")
        if not bodies or not all(
            contains_identifier(body, "protocolRuntimeAuthorizationReady")
            and contains_identifier(body, "protocolRequestEligibilityReason")
            for body in bodies
        ):
            errors.append("OpenAI eligibility authorization diagnostic is missing the runtime hard gate")

    routing_context_test = root / "backend/internal/service/protocol_routing_context_test.go"
    if routing_context_test.is_file():
        source = strip_go_comments_and_literals(routing_context_test.read_text(encoding="utf-8"))
        bodies = function_bodies(
            source,
            "TestOpenAIEligibilityUsesProtocolHardGateWithoutChangingOtherChecks",
        )
        if not bodies or not all(
            contains_identifier(body, "isOpenAICompatibleAccountEligibleForRequest")
            and contains_identifier(body, "isAccountSchedulableForModelSelection")
            for body in bodies
        ):
            errors.append("protocol routing is missing the scheduler authorization regression test")

    # Probe scheduling lives in the TK companion so account_handler.go stays
    # upstream-shaped; accept either path as long as one owner has the bodies.
    account_handler_probe_owners = (
        root / "backend/internal/handler/admin/account_handler_tk_protocol_probe.go",
        root / "backend/internal/handler/admin/account_handler.go",
    )
    account_handler = next((path for path in account_handler_probe_owners if path.is_file()), None)
    if account_handler is not None:
        source = strip_go_comments_and_literals(account_handler.read_text(encoding="utf-8"))
        bodies = function_bodies(source, "scheduleProtocolCapabilityProbes")
        if not bodies:
            errors.append("admin account handler is missing protocol capability probe scheduling")
        for body in bodies:
            if not contains_identifier(body, "ProtocolProbeCandidates"):
                errors.append("admin account handler does not gate the canonical protocol candidate set")
            if not contains_identifier(body, "scheduleProtocolCapabilityProbeBatch"):
                errors.append("admin account handler does not delegate to the bounded account probe batch")
            for forbidden in (
                "ProbeOpenAIAPIKeyChatCompletionsSupport",
                "ProbeOpenAIAPIKeyResponsesSupport",
                "ProbeOpenAIAPIKeyNativeMessagesSupport",
            ):
                if contains_identifier(body, forbidden):
                    errors.append(f"admin account handler fans out per-protocol probe {forbidden}")
        batch_bodies = function_bodies(source, "scheduleProtocolCapabilityProbeBatch")
        if not batch_bodies:
            errors.append("admin account handler is missing the bounded account probe batch")
        for body in batch_bodies:
            if not contains_identifier(body, "ProbeAccountProtocolCapabilitiesBatch"):
                errors.append("admin account handler bypasses the bounded account probe batch")
            for forbidden in (
                "ProbeOpenAIAPIKeyChatCompletionsSupport",
                "ProbeOpenAIAPIKeyResponsesSupport",
                "ProbeOpenAIAPIKeyNativeMessagesSupport",
            ):
                if contains_identifier(body, forbidden):
                    errors.append(f"admin account handler fans out per-protocol probe {forbidden}")

    probe_owner = root / "backend/internal/service/protocol_capability_probe.go"
    if probe_owner.is_file():
        source = strip_go_comments_and_literals(probe_owner.read_text(encoding="utf-8"))
        dispatch_bodies = function_bodies(source, "probeProtocolCapability")
        if not dispatch_bodies or not all(
            contains_identifier(body, "ProtocolGeminiGenerateContent")
            and contains_identifier(body, "probeGeminiGenerateContentSupport")
            for body in dispatch_bodies
        ):
            errors.append("protocol capability owner is missing Gemini probe dispatch")
        probe_dispatches = (
            ("ProtocolMessages", "probeOpenAIAPIKeyNativeMessagesSupport"),
            ("ProtocolChatCompletions", "probeOpenAIAPIKeyChatCompletionsSupport"),
            ("ProtocolResponses", "probeOpenAIAPIKeyResponsesSupport"),
            ("ProtocolGeminiGenerateContent", "probeGeminiGenerateContentSupport"),
        )
        for body in dispatch_bodies:
            for protocol, helper in probe_dispatches:
                if not re.search(
                    rf"case\s+(?:protocolrouter\s*\.\s*)?{protocol}\s*:\s*return\s+(?:s\s*\.\s*)?{helper}\s*\(",
                    body,
                ):
                    errors.append(
                        f"protocol capability probe dispatch ignores the classifier result for {protocol}"
                    )
        legacy_bodies = function_bodies(source, "ProbeAccountProtocolCapabilities")
        result_bodies = function_bodies(source, "ProbeAccountProtocolCapabilitiesNow")
        prepared_bodies = function_bodies(source, "ProbeAccountProtocolCapabilitiesForPreparation")
        aggregate_bodies = function_bodies(source, "probeAccountProtocolCapabilitiesNow")
        if not legacy_bodies:
            errors.append("protocol capability owner is missing aggregate account probe job")
        for body in legacy_bodies:
            if len(call_spans(body, "ProbeAccountProtocolCapabilitiesNow")) != 1:
                errors.append("legacy account probe wrapper must delegate to the result-returning aggregate job exactly once")
        if result_bodies and aggregate_bodies:
            for body in result_bodies:
                if len(call_spans(body, "probeAccountProtocolCapabilitiesNow")) != 1 or not re.search(r"\btrue\b", body):
                    errors.append("normal account probe wrapper does not select published persistence")
        if not prepared_bodies or not all(
            len(call_spans(body, "probeAccountProtocolCapabilitiesNow")) == 1
            and re.search(r"\bfalse\b", body)
            for body in prepared_bodies
        ):
            errors.append("startup preparation probe wrapper does not select silent persistence")
        bodies = aggregate_bodies or result_bodies or legacy_bodies
        for body in bodies:
            for required in (
                "EnsureAccountLink",
                "ProtocolProbeCandidates",
                "protocolProbeCoordinator",
                "runEndpointProtocolProbe",
            ):
                if not contains_identifier(body, required):
                    errors.append(f"aggregate account probe job is missing {required}")
            if not re.search(r"protocolProbeCoordinator\s*\.\s*Do\s*\(\s*capability\s*\.\s*CapabilityKey\b", body):
                errors.append("aggregate account probe job is not coordinated by capability key")
        endpoint_probe_bodies = function_bodies(source, "runEndpointProtocolProbe")
        if not endpoint_probe_bodies:
            errors.append("protocol capability owner is missing endpoint-scoped probe execution")
        for body in endpoint_probe_bodies:
            for required in (
                "AcquireProbeLease",
                "ListLinkedAccountIDs",
                "probeProtocolCapability",
                "commitProtocolProbeResult",
            ):
                if not contains_identifier(body, required):
                    errors.append(f"endpoint-scoped probe execution is missing {required}")
        commit_dispatch_bodies = function_bodies(source, "commitProtocolProbeResult")
        if not commit_dispatch_bodies or not all(
            contains_identifier(body, "CommitProbeResult")
            and contains_identifier(body, "CommitPreparedProbeResult")
            and contains_identifier(body, "publish")
            for body in commit_dispatch_bodies
        ):
            errors.append("protocol capability probe persistence does not separate preparation from publication")
        for forbidden in (
            "PersistProtocolProbeVerdicts",
            "BuildProtocolProbeUpdate",
            "UpdateExtraIfUpdatedAt",
        ):
            if contains_identifier(source, forbidden):
                errors.append(f"protocol capability owner contains account-owned protocol probe writer {forbidden}")

    for relative in (
        "backend/internal/repository/account_repo.go",
        "backend/internal/service/account.go",
    ):
        path = root / relative
        if path.is_file():
            source = strip_go_comments_and_literals(path.read_text(encoding="utf-8"))
            if contains_identifier(source, "UpdateExtraIfUpdatedAt"):
                errors.append(f"{relative}: retains account-owned protocol probe CAS writer")

    gemini_probe_owner = root / "backend/internal/service/protocol_capability_probe_gemini.go"
    if gemini_probe_owner.is_file():
        raw_source = gemini_probe_owner.read_text(encoding="utf-8")
        source = strip_go_comments_and_literals(raw_source)
        for required in ("classifyGeminiProtocolProbe", "probeGeminiGenerateContentSupport"):
            if not contains_identifier(source, required):
                errors.append(f"Gemini protocol probe owner is missing {required}")
        for reason in ("METHOD_NOT_SUPPORTED", "UNSUPPORTED_METHOD"):
            if reason not in raw_source:
                errors.append(f"Gemini protocol probe owner is missing explicit method reason {reason}")

    service_wire = root / "backend/internal/service/wire.go"
    if service_wire.is_file():
        source = strip_go_comments_and_literals(service_wire.read_text(encoding="utf-8"))
        definitions = function_definitions(source, "ProvideProtocolRoutingSSOTReady")
        if not definitions or not all(
            contains_identifier(definition, "prepareProtocolRoutingSSOT")
            for definition in definitions
        ):
            errors.append("protocol routing startup preparation skips probe and revalidation")
        ready_builders = function_bodies(source, "newProtocolRoutingSSOTReady")
        if not ready_builders or not all(contains_identifier(body, "router") for body in ready_builders):
            errors.append("protocol routing readiness builder does not retain the router")
        for body in ready_builders:
            if re.search(r"\brouter\s*=\s*nil\b", body) or re.search(r"\brouter\s*:\s*nil\b", body):
                errors.append("protocol routing readiness must not disable the router")
        if function_bodies(source, "Ready") or contains_identifier(source, "TrafficReady"):
            errors.append("protocol routing still exposes a Ready/TrafficReady process gate")

    handler_wire = root / "backend/internal/handler/wire.go"
    if handler_wire.is_file():
        source = strip_go_comments_and_literals(handler_wire.read_text(encoding="utf-8"))
        for provider in ("ProvideGatewayHandler", "ProvideOpenAIGatewayHandler"):
            definitions = function_definitions(source, provider)
            if not definitions or not all(
                contains_identifier(definition, "ProtocolRoutingSSOTReady")
                and contains_identifier(definition, "EnabledRouter")
                for definition in definitions
            ):
                errors.append(f"{provider} bypasses protocol routing cutover readiness")
        if not contains_identifier(source, "ProvideProtocolRoutedOpenAIGatewayHandler"):
            errors.append("OpenAI protocol companion is not registered in the handler provider set")

    protocol_handler_wire = root / "backend/internal/handler/wire_protocol_ssot_tk.go"
    if protocol_handler_wire.is_file():
        source = strip_go_comments_and_literals(protocol_handler_wire.read_text(encoding="utf-8"))
        definitions = function_definitions(source, "ProvideProtocolRoutedOpenAIGatewayHandler")
        if not definitions or not all(
            contains_identifier(definition, "ProtocolRoutingSSOTReady")
            and contains_identifier(definition, "ProvideOpenAIGatewayHandler")
            and contains_identifier(definition, "SetGeminiProtocolServices")
            for definition in definitions
        ):
            errors.append("OpenAI companion bypasses routing readiness or Gemini protocol services")
    else:
        errors.append("OpenAI Gemini protocol services lack a companion wiring owner")

    server_router = root / "backend/internal/server/router.go"
    if server_router.is_file():
        source = strip_go_comments_and_literals(server_router.read_text(encoding="utf-8"))
        route_bodies = function_bodies(source, "registerRoutes")
        if not route_bodies or not all(
            contains_identifier(body, "RegisterCommonRoutes") for body in route_bodies
        ):
            errors.append("server router does not register common health routes")
        for body in route_bodies:
            if contains_identifier(body, "Ready"):
                errors.append("server router still gates /health on protocol readiness")

    common_routes = root / "backend/internal/server/routes/common.go"
    if common_routes.is_file():
        source = strip_go_comments_and_literals(common_routes.read_text(encoding="utf-8"))
        route_bodies = function_bodies(source, "RegisterCommonRoutes")
        if not route_bodies:
            errors.append("common health routes are missing RegisterCommonRoutes")
        for body in route_bodies:
            if re.search(r"protocol(?:Routing)?Ready", body):
                errors.append("common /health still consults protocol readiness")
            if not contains_identifier(body, "IsDraining"):
                errors.append("common /health is missing the drain gate")

    count_tokens = root / "backend/internal/handler/openai_gateway_count_tokens.go"
    if count_tokens.is_file():
        source = strip_go_comments_and_literals(count_tokens.read_text(encoding="utf-8"))
        for required in (
            "ResponsesPathInputTokens",
            "SelectProtocolAccountForTokenCount",
            "ExecuteSelectedProtocol",
            "ValidateProtocolEndpoint",
            "ProtocolExecutors",
        ):
            if not contains_identifier(source, required):
                errors.append(f"responses input_tokens path is missing {required}")
        if not re.search(r"\brequest\s*\.\s*Body\s*\(", source):
            errors.append("responses input_tokens executor does not consume canonical request.Body")
        if contains_identifier(source, "ProtocolSelectionForAccount"):
            errors.append("responses input_tokens performs secondary protocol planning")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    errors = check(args.root.resolve())
    if errors:
        for error in errors:
            print(f"FAIL: {error}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("ok: protocol-routing SSOT boundaries are intact")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
