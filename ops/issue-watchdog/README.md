# Issue watchdog ledger

`upstream.json` and `anthropic.json` own curated judgments and historical fix evidence.
Edit the appropriate ledger inside the fix or triage PR. Source repositories and
commit trailer names are configured in `scripts/upstream/issue_ledger.py`.

Each entry identifies an `upstream` issue (grouped refs are allowed for shared fixes):

- `judgment`: optional explicit `impact`, `tokenkey_status`, and `rationale` for one issue.
  Keyword classification cannot promote an issue to high risk; record the code or
  reproduction evidence here when making that decision.
- `summary`, `tokenkey_pr`, `fixed_by`, `severity`: recorded fix evidence, when available.
- `fixed_if_all_present`: repository-relative `path:literal-substring` anchors.
  Nonempty, resolving anchors let the scan apply the recorded fix to runtime triage.
  Missing or empty anchors require review. String presence does not prove behavior
  and only applies previously recorded fix evidence to tracking state; run the
  behavioral regression tests in the fix PR before recording that evidence.
- `status` and `history`: preserved historical fix metadata. `history` contains only
  differing fields from older records; it never overrides current judgments or anchors.
  Historical records without anchors do not independently mark a scanned issue fixed.

Validate with `python3 scripts/upstream/issue_ledger.py`. Add
`--commits-range origin/main..HEAD` to check the evidence for issues declared in
`Upstream-Fixes:` / `Anthropic-Fixes:` commit trailers. Preflight runs this gate.
Validation writes no files and does not require a prior scan or local cache.

The shared workflow stores issue snapshots/cursors in Actions cache and publishes
triage, fixes and reports as artifacts. These derived files do not belong in Git.
The migration preserved old judgments and unique fix metadata, including duplicate
reports of the same issue; original snapshots remain accessible in Git history.
`.cache/fingerprint/` belongs to the independent fingerprint workflow.

## Candidate review and relay implementation watch

The shared `scripts/upstream/watchdog_queue.py` publishes gateway scheduling,
billing, upstream account and protocol-conversion leads directly to one
tracking Issue per platform: `[kiro]`, `[openai]`, `[google]`, plus the existing
`[upstream]` aggregate for Wei-Shaw/sub2api and Claude Code. Neither manual approval nor high-risk classification
is required. Leads remain unverified until checked against TokenKey. Existing
individual tracking Issues are retained, but new source entries do not create
individual Issues.

Daily scans append only new or changed entries as task-list comments. Identical
replays do not write; oversized batches split comments, never Issues. Markers
and per-entry signatures in comments are the durable publication state. Human
notes and labels survive updates; only the managed body and task status are
updated. Repositories with the same platform share one Issue. A repository's
`platforms` list owns its destination scope; a single-platform repository stays
in that platform even when discussing another protocol. For multi-platform
repositories, title/body/labels/discussion hints select destinations. Generic
shared fixes without platform hints go to all of that repository's platforms.
Checkbox decisions and lifecycle remain independent in each platform Issue.
Both workflows use a serialized writer.

A checked task records a local resolution or dismissal (add a reason/fix PR in
plain text). Merged ledger judgments and valid recorded fix anchors also remove
entries from the pending set and check their historical tasks. If those anchors
are revoked, a newly scanned affected entry returns to pending. Upstream closure,
PR merge, disappearance from an incremental scan, and cache expiry do not prove a
TokenKey fix. All tasks resolved closes the aggregate; new pending tasks reopen
that same Issue. Fix PRs must reference it without a GitHub closing keyword.
The initial existing watchdog scan backfills its relevant open-issue backlog.

The source catalog is `relays.json`: it owns repository identity, platform tags
and monitoring purpose. `Relay Implementation Watch` scans daily;
manual dispatch defaults to a read-only run. It uses the existing watchdog PAT,
without model calls, credential probes or runtime changes. No upstream text is
executed or interpreted as an instruction. Publication uses title/link/status
metadata; complete external comment bodies are not copied into the fork.

`scripts/upstream/relay-watch.py` reads all pages of recently updated issues and
PRs, plus issue/PR conversation comments (including edited comments). On first
run, cache loss or platform assignment changes it backfills all open Issues/PRs
and the last 30 days of updates (including closed Issues and merged PRs); `--lookback-days` allows a larger
read-only audit. Platform assignments are saved with the cursor, and a source
cursor advances only after every destination has been published. It tracks scheduling, billing, account/credential and protocol-conversion terms in
both the issue body and discussion. GitHub review-thread inline comments and
separate GitHub Discussions are not collected. Merge state comes from REST
`pull_request.merged_at` (PR detail fallback if absent), never from `closed`.
It does not infer applicability from project popularity or automatically copy a
third-party patch. A plain commit without an issue/PR is outside this scanner.

```bash
# Read-only research; does not advance publication cursors.
python3 scripts/upstream/relay-watch.py --repo TsinHzl/kiro2cc-proxy
# Publish only after reviewing the target/scope. Scheduled runs use this mode.
python3 scripts/upstream/relay-watch.py --sync-repo youxuanxue/sub2api
python3 -m unittest scripts.test_relay_watch scripts.test_issue_watchdog_flow -v
```

Every source has its own cursor with overlap. API/publication failures fail the
job, leave its persisted cursor unchanged and retain an error artifact. Retry
replays safely. Both workflows share an external-projection concurrency group.
The platform Issues and comments are durable; expiring artifacts are diagnostic evidence only.

Triage each lead against the current TokenKey owner and record one of: affected
(with reproduction), already covered (with a behavioral test), not applicable
(with reason), or awaiting evidence. For affected behavior, link a focused fix
PR and test; importing code also requires checking the source license. Model
catalog/routing changes go through modelops and candidate eligibility owners;
fingerprint changes use the platform fingerprint skill. Monitoring does not
authorize model activation, production deployment, account changes or merging.

Initial research and observed outage cause:
[relay watch investigation](../../docs/ops/upstream-relay-watch-20261021.md).
