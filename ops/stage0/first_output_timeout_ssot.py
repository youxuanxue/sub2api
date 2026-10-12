#!/usr/bin/env python3
"""Single owner for intended first-output timeout numbers.

Universal stream SSOT defaults to 30s / high-effort 60s (stream cap 60).
OpenAI standard is 60s; OpenAI high-effort is 300s for large gpt-6.*
compaction bodies that stall past shorter budgets without semantic output.
OpenAI keeps explicit keys so `openai_first_output_*=0` remains an emergency
disable without collapsing the universal stream budget.

Every writable copy (viper SetDefault, compose inject fallbacks, remediate
WANT_*, Go code defaults, config.example.yaml) must match this table. Live checks:
prod assert-live-host-state.sh; edges assert-first-output-timeouts.sh (daily +
upgrade/rollback). scripts/checks/first-output-timeout-ssot.py fails preflight
on writable-owner drift.
"""

from __future__ import annotations

# Container / compose / .env keys → intended string values.
EXPECTED_ENV: dict[str, str] = {
    "GATEWAY_STREAM_FIRST_OUTPUT_TIMEOUT_SECONDS": "30",
    "GATEWAY_STREAM_FIRST_OUTPUT_HIGH_EFFORT_TIMEOUT_SECONDS": "60",
    "GATEWAY_OPENAI_FIRST_OUTPUT_TIMEOUT_SECONDS": "60",
    "GATEWAY_OPENAI_HIGH_EFFORT_FIRST_OUTPUT_TIMEOUT_SECONDS": "300",
}

# viper / yaml mapstructure keys → intended int defaults.
EXPECTED_VIPER: dict[str, int] = {
    "gateway.stream_first_output_timeout_seconds": 30,
    "gateway.stream_first_output_high_effort_timeout_seconds": 60,
    "gateway.openai_first_output_timeout_seconds": 60,
    "gateway.openai_high_effort_first_output_timeout_seconds": 300,
}

# Go duration defaults in stream_first_output_budget_tk.go (stream only).
EXPECTED_GO_STREAM_SECONDS: dict[str, int] = {
    "defaultStreamFirstOutputTimeout": 30,
    "defaultStreamFirstOutputHighEffortTimeout": 60,
}
