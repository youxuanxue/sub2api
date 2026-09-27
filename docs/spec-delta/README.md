# Spec delta index

Spec delta documents are small intent records for changes that do not need a
high-risk approval baseline. High-risk designs belong in `docs/approved/`.

Completed one-off PR intent notes (no-web-impact stubs, per-patch CC version
deltas) are deleted after merge; living decisions stay under stable topic names.

## Two valid locations — pick by lifetime

Both forms are correct and both are in active use. Do not "fix" one into the
other: the existing files in each are load-bearing and referenced from Go
comments, sentinels, skills and workflows.

| Form | Use for | Example |
| --- | --- | --- |
| `docs/spec-delta-<slug>.md` (root) | Single-PR intent record. Written to explain one change's Background / Delta / Scenarios / Validation, per `product-dev.mdc`. | `docs/spec-delta-edge-model-rejection.md` |
| `docs/spec-delta/<topic>.md` (this directory) | Living decision under a stable topic name, updated across many PRs. | [`cc-system-prompt.md`](cc-system-prompt.md) |

**One functional difference, and it is the reason to prefer root for new PR
records.** `.preflight/web-surface-alignment.conf` `[alignment_paths]` matches
`^docs/(?:…|spec-delta-|approved/)` — the root prefix only. A root spec-delta
therefore counts as Web/config/contract alignment evidence and satisfies that
preflight check; a file added under this directory does not, so a backend change
paired only with a directory spec-delta still needs alignment evidence elsewhere
or a `no-web-impact` token in the PR description.

A root record that turns into an ongoing topic may graduate into this directory,
but moving it means updating every code comment, sentinel rationale and skill
reference in the same change. Leave it where it is unless there is a reason
beyond tidiness.

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
