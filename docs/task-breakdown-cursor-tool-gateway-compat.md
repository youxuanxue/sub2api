---
title: Cursor tool gateway compatibility implementation
status: approved-for-implementation
approved_by: "feng (conversation approval, 2026-10-01)"
---

## Goal

Keep Cursor as an internal model supply while exposing a provider-neutral model
gateway. The gateway never executes workspace tools.

## In scope

- Return protocol-native `ExecClientThrow + StreamClose` for unsupported local
  exec frames and continue consuming the model stream.
- Normalize declared MCP tool identities across `name`, `tool_name`, and known
  TokenKey/Composer prefixes before external handoff.
- Keep MCP/function handoff as standard `tool_use` / `tool_result` continuation.
- Sanitize public error text and codes while retaining bounded diagnostics in
  operator logs.
- Add unit and service regression coverage for the above behavior.

## Out of scope

- Shell, read, grep, write, git, filesystem, or workspace execution in the
  gateway.
- Switching the relay to Agent mode.
- Advertising Cursor native tool allowlists or exposing the supplier catalog.
- A client-side local-tool companion.

## Dependencies

1. Define the public error mapping and tool identity normalization in the
   Cursor integration package.
2. Update Messages streaming and buffered paths to use the public mapping.
3. Extend service and integration fixtures, then run targeted and package
   tests.
4. Run preflight and review gates before delivery.
