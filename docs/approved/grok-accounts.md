---
title: Grok account platforms
approved_by: user
approved_at: 2026-06-23
risk: high
status: approved
---

# Grok accounts

Grok accounts use `platform=grok` with either native OAuth refresh/access tokens
or an API key and an explicitly configured upstream base URL. Both use
OpenAI-compatible routing. Native OAuth defaults to `https://api.x.ai/v1`.

OpenAI Codex transforms, ChatGPT headers and Codex usage snapshots apply only to
`platform=openai,type=oauth`; Grok OAuth/API-key accounts retain their own identity.

Account import is owned by `ops/accounts/import-accounts.sh`. Validation covers
Grok token/base-URL selection and the OpenAI OAuth-only transform guards.
