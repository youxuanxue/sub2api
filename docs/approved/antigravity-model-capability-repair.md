---
title: Antigravity account model capability repair
status: approved
approved_by: "feng (2026-10-16 conversation: keep and precisely repair Argon; remove confirmed unsupported Antigravity models and update public catalog; approve discovery and cooldown fixes)"
created: 2026-10-16
---

# Scope

Keep Argon and its high/medium/low mappings. Discovery and quota model lists must
use the account's inference endpoint, including the existing explicit endpoint
override. Do not substitute a successful catalog from another Google endpoint.

On native Antigravity generation, the exact Google HTTP 404 / NOT_FOUND envelope
with message `Requested entity was not found.` is an account/path capability
failure. Reuse the existing account × resolved-wire-model 30-minute cooldown and
bounded candidate failover. Do not retry that account immediately, disable the
whole account, broaden authorization, or alter protocol Plan / billing owners.
Generic route/project 404 responses are not this verdict. Admin tests remain
read-only with respect to account health.

# Evidence and catalog boundary

2026-10-16 production endpoint probes found paid accounts whose daily catalog
exposes `gemini-4-argon-disabled`, while other paid accounts expose Argon wires
and have successful usage. A minimal Argon request on one disabled account
returned native NOT_FOUND on daily, but 429 on prod. This does not justify
changing inference endpoints or removing Argon.

The default `gemini-3.6-flash-tiered` wire was absent from every active serving
catalog sampled, and a minimal request on a healthy paid account returned native
NOT_FOUND. Withdraw this wire and its default `gemini-3.6-flash` and
`gemini-3.5-flash-lite` aliases from Antigravity mapping floors, presets and its
public/menu projection. Keep pricing history, native Gemini models, and explicit
non-tiered wires intact. The structural-dead mapping owner prevents convergence
from restoring the withdrawn aliases.

Paid daily also returned NOT_FOUND for 3.7-high and 3.6-high. Free prod returned
429, so those families are not proven globally unsupported; retain them and
apply account-specific cooldown on confirmed generation failures. Directory
omission, 429, authentication failures and timeouts alone never retire a model.

# Validation and rollout

Regression coverage must prove exact/negative error classification, resolved-wire
cooldown isolation, uncommitted retryable failure across native and compatibility
adapters, no same-account retry, read-only admin tests, endpoint-bound discovery,
and Antigravity-only catalog withdrawal. Run generated-bundle validation and the
catalog refresh selftest. After merge, deploy prod and verify with bounded real
requests; no load testing. Apply only reviewed removal diffs to existing account
and runtime mappings, retaining a rollback snapshot. Merge approval is separate.
