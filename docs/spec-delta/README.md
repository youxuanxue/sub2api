# Spec delta index

Spec delta documents are small intent records for changes that do not need a
high-risk approval baseline. High-risk designs belong in `docs/approved/`.

Completed one-off PR intent notes (no-web-impact stubs, per-patch CC version
deltas) are deleted after merge; living decisions stay under stable topic names.
Which of the two a given file is, is decided by
`scripts/checks/spec-delta-liveness.py`, not by reading it: run it to see the
current live/stub split. Do not infer that a file is dead because no grep hit
mentions it — Owners tables and sentinel rationales are what make a delta
load-bearing, and the checker is what knows the difference.

## Where new records go

`dev-rules/rules/product-dev.mdc` is the process SSOT and names one location:
`docs/spec-delta-<slug>.md` at the repo root. New records go there.

This directory exists because some records outlived the PR that introduced them
and were given stable topic names; the files in it are cited from Go comments,
sentinels, skills and workflows, so they stay where they are. That is history
with anchors holding it in place, not a second sanctioned location — do not add
new files here, and do not move the existing ones without updating every citation
in the same change.

One consequence worth knowing, since it is easy to hit and hard to guess:
`.preflight/web-surface-alignment.conf` `[alignment_paths]` matches
`^docs/(?:…|spec-delta-|approved/)`, the root prefix only. A root spec-delta
counts as Web/config/contract alignment evidence for that preflight check; a file
under this directory does not.

## Current records

| File | Topic |
| --- | --- |
| [`cc-2.1.160.md`](cc-2.1.160.md) | Claude Code Haiku beta A/B (stable decision) |
| [`cc-fable-5.md`](cc-fable-5.md) | Fable 5 request surface |
| [`cc-oauth-mimicry-fingerprint-scope.md`](cc-oauth-mimicry-fingerprint-scope.md) | OAuth mimicry fingerprint scope beyond UA |
| [`cc-system-prompt.md`](cc-system-prompt.md) | Claude Code system-prompt anchors |
| [`edge-lightsail.md`](edge-lightsail.md) | Lightsail-only edge path |
| [`group-unsupported-model-negative-cache.md`](group-unsupported-model-negative-cache.md) | Group unsupported-model negative cache |
| [`kiro-cache-billing.md`](kiro-cache-billing.md) | Kiro estimated prefix-cache billing and rollback switch |
