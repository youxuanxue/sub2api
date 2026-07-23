#!/usr/bin/env bash
# Run Claude Code with a file-backed prompt and preserve its pipeline exit code.

set -uo pipefail

: "${PROMPT_FILE:?PROMPT_FILE is required}"
: "${ANTHROPIC_MODEL:?ANTHROPIC_MODEL is required}"
: "${MAX_BUDGET_USD:?MAX_BUDGET_USD is required}"
: "${ALLOWED_TOOLS:?ALLOWED_TOOLS is required}"
: "${OUTPUT_FILE:?OUTPUT_FILE is required}"
: "${RUNNER_TEMP:?RUNNER_TEMP is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"

if [ ! -f "$PROMPT_FILE" ]; then
  echo "headless agent prompt file not found: $PROMPT_FILE" >&2
  exit 2
fi
if [ ! -f "$RUNNER_TEMP/redact-agent-stream.py" ]; then
  echo "headless agent redactor not found in RUNNER_TEMP" >&2
  exit 2
fi

set +e  # preflight-allow: swallow - PIPESTATUS captures Claude's exit below
claude -p \
  --model "$ANTHROPIC_MODEL" \
  --max-budget-usd "$MAX_BUDGET_USD" \
  --allowedTools "$ALLOWED_TOOLS" \
  --input-format text \
  --output-format stream-json --verbose \
  --exclude-dynamic-system-prompt-sections \
  < "$PROMPT_FILE" \
  2>&1 \
  | python3 "$RUNNER_TEMP/redact-agent-stream.py" \
  | tee "$OUTPUT_FILE"
pipeline_status=("${PIPESTATUS[@]}")
set -e

claude_code=${pipeline_status[0]}
redactor_code=${pipeline_status[1]}
tee_code=${pipeline_status[2]}
echo "exit_code=$claude_code" >> "$GITHUB_OUTPUT"
echo "agent exited with code $claude_code"

if [ "$redactor_code" -ne 0 ]; then
  echo "::error::headless agent redactor exited $redactor_code"
  exit "$redactor_code"
fi
if [ "$tee_code" -ne 0 ]; then
  echo "::error::headless agent output writer exited $tee_code"
  exit "$tee_code"
fi
if [ "${FAIL_ON_ERROR:-true}" = "true" ] && [ "$claude_code" -ne 0 ]; then
  echo "::error::headless agent exited $claude_code"
  exit "$claude_code"
fi
