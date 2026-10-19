#!/usr/bin/env python3
"""Single owner for intended first-output timeout numbers.

Edit THIS file, then regenerate writable copies:

  python3 scripts/checks/first-output-timeout-ssot.py --apply

Universal stream SSOT defaults to 120s / high-effort 600s (stream cap 600).
OpenAI standard is 120s; OpenAI high-effort is 600s. Compact path,
compaction_trigger input, or request bodies at/above
OPENAI_FORCE_HIGH_BODY_BYTES force the high-effort budget even when
reasoning.effort is medium/low — prod gpt-6.* compact/large-body stalls
were dying on the standard floor.
OpenAI keeps explicit keys so `openai_first_output_*=0` remains an emergency
disable without collapsing the universal stream budget.

NewAPI Chat non-streaming first-output (whole-body useful JSON wait) is also
owned here: default 600s, config/remediate max 900s. Streaming NewAPI override
`newapi_chat_first_output_timeout` stays 0=inherit stream SSOT (not listed).

Generated owners (do not hand-edit numbers): viper SetDefault, compose inject
fallbacks, remediate env arrays / WANT_TIMEOUT, Go stream/large-body defaults,
config.example.yaml, Stage0 docker-compose env fallbacks, config max bound.
Preflight runs `--check`. Live hosts: assert-live-host-state.sh /
assert-first-output-timeouts.sh.
"""

from __future__ import annotations

# Stream / OpenAI / Kiro env keys → intended string values.
# Used by remediate-first-output-timeouts.sh (does NOT include NewAPI nonstream;
# that path has its own remediatescript).
EXPECTED_ENV_STREAM: dict[str, str] = {
    "GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS": "120",
    "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS": "600",
    "GATEWAY_KIRO_FIRST_OUTPUT_TIMEOUT_SECONDS": "120",
    "GATEWAY_KIRO_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS": "600",
    "GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS": "120",
    "GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS": "600",
}

# NewAPI Chat non-stream whole-body wait (separate remediatescript + compose inject).
EXPECTED_ENV_NEWAPI_NONSTREAM: dict[str, str] = {
    "GATEWAY_NEWAPI_CHAT_NONSTREAM_FIRST_OUTPUT_TIMEOUT": "600",
}

# Union used for compose inject markers + env-fallback rewrites across deploy files.
EXPECTED_ENV: dict[str, str] = {
    **EXPECTED_ENV_STREAM,
    **EXPECTED_ENV_NEWAPI_NONSTREAM,
}

# viper / yaml mapstructure keys → intended int defaults.
EXPECTED_VIPER: dict[str, int] = {
    "gateway.stream_first_output_timeout_seconds": 120,
    "gateway.stream_first_output_high_effort_timeout_seconds": 600,
    "gateway.kiro_first_output_timeout_seconds": 120,
    "gateway.kiro_high_effort_first_output_timeout_seconds": 600,
    "gateway.openai_first_output_timeout_seconds": 120,
    "gateway.openai_high_effort_first_output_timeout_seconds": 600,
    "gateway.newapi_chat_nonstream_first_output_timeout": 600,
}

# Go duration defaults in stream_first_output_budget_tk.go (Kiro also covers non-stream completion).
EXPECTED_GO_STREAM_SECONDS: dict[str, int] = {
    "defaultStreamFirstOutputTimeout": 120,
    "defaultStreamFirstOutputHighEffortTimeout": 600,
    "defaultKiroFirstOutputTimeout": 120,
    "defaultKiroHighEffortFirstOutputTimeout": 600,
    "defaultNewAPIChatNonstreamFirstOutput": 600,
}

# Config validation / remediate WANT_TIMEOUT upper bound (seconds).
NEWAPI_CHAT_NONSTREAM_FIRST_OUTPUT_MAX = 900

# OpenAI force-high policy: body length threshold mirrored by
# openAIFirstOutputLargeBodyBytes in openai_first_output_timeout.go.
OPENAI_FORCE_HIGH_BODY_BYTES = 1_048_576  # 1 MiB
