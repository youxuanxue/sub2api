---
title: Prod replay capability matrix
status: draft
risk: high
---

prod replay is a deployment verification tool. It has no runtime capture path
and must not add latency, memory, disk or sensitive-data handling to the live
gateway. Its denominator is the finite capability manifest in
`ops/stage0/prod-replay-capabilities.json`.

Each entry covers model, protocol, request type (plain, stream, tool, thinking,
multimodal) and key type (direct or universal). Concrete model entries come
from the served-model catalog. Historical successful requests are preferred;
missing history is filled by deterministic requests derived from the declared
TokenKey capability. Every replay runs in the existing isolated environment.

The manifest is updated when models, protocols or request semantics change.
Post-release checks run the delta after each release and fail closed on a
missing or failed capability. A green replay never authorizes traffic cutover.

Replay is an explicit heavy deployment operation. Ordinary releases and small
changes keep the existing release and smoke path; they do not run this matrix
implicitly. Only an operator explicitly selecting `operation=replay` may
create the isolated replay environment and consume upstream quota.
