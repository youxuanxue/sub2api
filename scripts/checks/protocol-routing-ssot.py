#!/usr/bin/env python3
"""Guard the protocol-routing SSOT against upstream-merge regressions."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


OWNER_FILES = (
    "backend/internal/engine/protocolrouter/router.go",
    "backend/internal/engine/protocolrouter/registry.go",
    "backend/internal/engine/protocolrouter/router_test.go",
    "backend/internal/engine/protocolrouter/policy_contract_test.go",
    "backend/internal/service/account_supported_protocols.go",
    "backend/internal/service/account_supported_protocols_test.go",
    "backend/internal/service/protocol_capability_probe.go",
    "backend/internal/service/protocol_capability_probe_test.go",
    "backend/internal/service/protocol_routing_migration.go",
    "backend/internal/service/protocol_routing_migration_test.go",
    "backend/internal/service/openai_apikey_chat_completions_probe_test.go",
    "backend/internal/service/protocol_execution.go",
    "backend/internal/service/protocol_execution_test.go",
    "backend/internal/service/protocol_execution_target_test.go",
    "backend/internal/service/protocol_routing_context.go",
    "backend/internal/service/protocol_routing_context_test.go",
    "backend/internal/service/wire.go",
    "backend/internal/handler/protocol_request.go",
    "backend/internal/handler/protocol_request_test.go",
    "backend/internal/handler/wire.go",
    "backend/internal/handler/openai_gateway_count_tokens.go",
    "backend/internal/handler/admin/account_handler.go",
)

GOVERNED_HANDLERS = (
    "backend/internal/handler/gateway_handler.go",
    "backend/internal/handler/gateway_handler_chat_completions.go",
    "backend/internal/handler/gateway_handler_responses.go",
    "backend/internal/handler/openai_gateway_handler.go",
    "backend/internal/handler/openai_chat_completions.go",
)

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
    "backend/internal/service/openai_gateway_forward.go": ("protocolExecutionTarget",),
    "backend/internal/service/openai_gateway_bridge_dispatch.go": ("protocolExecutionBound",),
}

FORWARD_BOUNDARIES = {
    "backend/internal/handler/gateway_handler.go": (
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
        for forbidden in LEGACY_ROUTE_SYMBOLS:
            if contains_identifier(source, forbidden):
                errors.append(f"{relative}: handler route decision uses legacy symbol {forbidden}")

        execute_spans = call_spans(source, "ExecuteSelectedProtocol")
        for start, end in execute_spans:
            call = source[start:end]
            if not contains_identifier(call, "ValidateProtocolEndpoint"):
                errors.append(f"{relative}: ExecuteSelectedProtocol is missing endpoint validator")
            if not contains_identifier(call, "ProtocolExecutors"):
                errors.append(f"{relative}: ExecuteSelectedProtocol is missing concrete executors")
            if re.search(r"\bProtocolExecutors\s*{\s*}", call):
                errors.append(f"{relative}: ExecuteSelectedProtocol has no route executor")
        if execute_spans and not re.search(r"\brequest\s*\.\s*Body\s*\(", source):
            errors.append(f"{relative}: executor does not consume canonical request.Body")
        if re.search(r"\bTargetProtocol\s*\(", source):
            errors.append(f"{relative}: handler performs a second TargetProtocol route decision")
        for pattern in FORWARD_BOUNDARIES.get(relative, ()):
            for match in re.finditer(pattern, source):
                if not inside_any_span(match.start(), execute_spans):
                    errors.append(
                        f"{relative}: forwarding call outside ExecuteSelectedProtocol boundary"
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
        ):
            if not contains_identifier(source, required):
                errors.append(f"canonical capability owner is missing {required}")

    execution_owner = root / "backend/internal/service/protocol_execution.go"
    if execution_owner.is_file():
        source = strip_go_comments_and_literals(execution_owner.read_text(encoding="utf-8"))
        for adapter in CONCRETE_ADAPTERS:
            if not contains_identifier(source, adapter):
                errors.append(f"protocol execution owner is missing concrete adapter {adapter}")
        for forbidden in ("requestScopedProtocolAdapter", "WithProtocolExecution"):
            if contains_identifier(source, forbidden):
                errors.append(f"protocol execution owner contains generic protocol adapter {forbidden}")

    routing_context = root / "backend/internal/service/protocol_routing_context.go"
    if routing_context.is_file():
        source = strip_go_comments_and_literals(routing_context.read_text(encoding="utf-8"))
        for required in ("protocolPlanCache", "put", "get"):
            if not contains_identifier(source, required):
                errors.append(f"protocol routing context is missing scheduler plan cache {required}")
        bodies = function_bodies(source, "attachProtocolPlan")
        if not bodies:
            errors.append("protocol routing context is missing attachProtocolPlan")
        for body in bodies:
            if contains_identifier(body, "protocolPlanForAccount") or re.search(r"\.\s*Plan\s*\(", body):
                errors.append("protocol selection performs secondary protocol planning")
            if not re.search(r"\.\s*get\s*\(", body):
                errors.append("protocol selection does not reuse scheduler-created plan")

    account_handler = root / "backend/internal/handler/admin/account_handler.go"
    if account_handler.is_file():
        source = strip_go_comments_and_literals(account_handler.read_text(encoding="utf-8"))
        bodies = function_bodies(source, "scheduleProtocolCapabilityProbes")
        if not bodies:
            errors.append("admin account handler is missing protocol capability probe scheduling")
        for body in bodies:
            if not contains_identifier(body, "ProtocolProbeCandidates"):
                errors.append("admin account handler does not gate the canonical protocol candidate set")
            if not contains_identifier(body, "ProbeAccountProtocolCapabilities"):
                errors.append("admin account handler does not schedule the aggregate account probe job")
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
        bodies = function_bodies(source, "ProbeAccountProtocolCapabilities")
        if not bodies:
            errors.append("protocol capability owner is missing aggregate account probe job")
        for body in bodies:
            for required in ("ProtocolProbeCandidates", "protocolProbeCoordinator", "probeProtocolCapability"):
                if not contains_identifier(body, required):
                    errors.append(f"aggregate account probe job is missing {required}")
            if len(call_spans(body, "PersistProtocolProbeVerdicts")) != 1:
                errors.append("aggregate account probe job must persist the complete candidate set exactly once")

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
        if not ready_builders or not all(
            contains_identifier(body, "CutoverReady") for body in ready_builders
        ):
            errors.append("protocol routing startup readiness is not gated by CutoverReady")

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
