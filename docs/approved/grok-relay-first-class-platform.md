---
title: Grok Relay as First-Class Grok Platform
approved_by: user
approved_at: 2026-06-23
risk: high
status: approved
---

# Grok Relay as First-Class Grok Platform

## Background

Prod Grok edge stubs currently use a NewAPI bridge transport shape even though
the operator intent is Grok capacity. That makes the account list show
`Extension Engine / Key` for `grok-<edge>` stubs and forces operators to reason
about an implementation detail.

TokenKey already has `platform=grok` as a first-class scheduling platform. The
long-term shape should make prod relay stubs Grok accounts too.

## Decision

Use two Grok account shapes:

- Edge real capacity: `platform=grok`, `type=oauth`, xAI OAuth refresh/access
  token, base URL defaulting to `https://api.x.ai/v1`.
- Prod relay stub: `platform=grok`, `type=apikey`, static edge API key plus
  `credentials.base_url=https://api-<edge>.tokenkey.dev`.

The runtime keeps OpenAI-compatible routing. The implementation must distinguish
OpenAI Codex OAuth behavior from Grok OAuth/API-key behavior by platform, not by
`type=oauth` alone.

## Delta

ADDED:

- `platform=grok,type=apikey` is a valid OpenAI-compatible relay account.
- Grok API-key accounts read `credentials.api_key` and `credentials.base_url`.
- Edge panels query Grok relay stubs as Grok pool stubs.
- Migration tooling can dry-run, apply, and roll back prod Grok relay stubs from
  legacy `newapi` transport identity to first-class `grok` identity.

MODIFIED:

- OpenAI Codex OAuth transforms, ChatGPT headers, Codex usage snapshots, and
  browser-UA override apply only to `platform=openai,type=oauth`.
- Grok OAuth continues to use xAI OAuth access tokens and xAI base URL.

NOT CHANGED:

- No schema change.
- No scheduler rewrite.
- No new UI concept.
- NewAPI bridge support remains for genuine Extension Engine accounts and for
  one-version legacy compatibility.

## Migration

Forward migration targets only edge-host Grok relay stubs:

- `accounts.platform='newapi'`
- `accounts.type='apikey'`
- `accounts.credentials.base_url` matches `https://api-*.tokenkey.dev`
- account name or bound group evidence identifies it as Grok

Changes:

- `accounts.platform='grok'`
- `accounts.channel_type=0`
- `accounts.credentials.mirror_platform='grok'`
- bound Grok relay groups move to `groups.platform='grok'`

Rollback restores the captured previous platform/channel values for the same
account and group IDs.

## Validation

- Unit tests cover Grok API-key token/base-url behavior.
- Unit tests cover OpenAI OAuth-only Codex transform guards.
- Unit tests cover edge panel Grok relay platform resolution.
- Contract check: `python scripts/export_agent_contract.py --check`.
- Preflight: `./scripts/preflight.sh`.
