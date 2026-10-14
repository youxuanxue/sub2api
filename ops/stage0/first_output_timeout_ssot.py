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

Generated owners (do not hand-edit numbers): viper SetDefault, compose inject
fallbacks, remediate env arrays, Go stream/large-body defaults, config.example.yaml.
Preflight runs `--check`. Live hosts: assert-live-host-state.sh /
assert-first-output-timeouts.sh.
"""

from __future__ import annotations

# Container / compose / .env keys → intended string values.
EXPECTED_ENV: dict[str, str] = {
    "GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS": "120",
    "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS": "600",
    "GATEWAY_KIRO_FIRST_OUTPUT_TIMEOUT_SECONDS": "120",
    "GATEWAY_KIRO_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS": "600",
    "GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS": "120",
    "GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS": "600",
}

# viper / yaml mapstructure keys → intended int defaults.
EXPECTED_VIPER: dict[str, int] = {
    "gateway.stream_first_output_timeout_seconds": 120,
    "gateway.stream_first_output_high_effort_timeout_seconds": 600,
    "gateway.kiro_first_output_timeout_seconds": 120,
    "gateway.kiro_high_effort_first_output_timeout_seconds": 600,
    "gateway.openai_first_output_timeout_seconds": 120,
    "gateway.openai_high_effort_first_output_timeout_seconds": 600,
}

# Go duration defaults in stream_first_output_budget_tk.go (Kiro also covers non-stream completion).
EXPECTED_GO_STREAM_SECONDS: dict[str, int] = {
    "defaultStreamFirstOutputTimeout": 120,
    "defaultStreamFirstOutputHighEffortTimeout": 600,
    "defaultKiroFirstOutputTimeout": 120,
    "defaultKiroHighEffortFirstOutputTimeout": 600,
}

# OpenAI force-high policy: body length threshold mirrored by
# openAIFirstOutputLargeBodyBytes in openai_first_output_timeout.go.
OPENAI_FORCE_HIGH_BODY_BYTES = 1_048_576  # 1 MiB
