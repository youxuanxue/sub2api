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
and per-entry signatures in comments are the durable publication state. Platform
markers own Issue identity even if a display label is removed. Marked Issues and
comments are accepted only from the publishing account or repository writers;
other public comments cannot change tracking state. Human
notes and labels survive updates; only the managed body and task status are
updated. Repositories with the same platform share one Issue. A repository's
`platforms` list owns its destination scope; a single-platform repository stays
in that platform even when discussing another protocol. For multi-platform
repositories, title/body/labels/discussion hints select destinations. Generic
shared fixes without platform hints go to all of that repository's platforms.
Derived topics/platform hints from discussions are retained in the source
checkpoint, so later neutral replies or merge events do not erase earlier
classification. Checkpoints without this context are replayed. Checkbox decisions
and lifecycle remain independent in each platform Issue.
Both workflows use a serialized writer.

A checked task records a local resolution or dismissal (add a reason/fix PR in
plain text) and then **leaves the pending set**: the tracking Issue counts only
what TokenKey still owes. Its line stays as `- [x]`, because `manual` is rebuilt
from the live comments on every run -- that line is the only durable carrier of
the tick, so deleting it would discard the verdict and re-offer the task on the
next round. A ledger-resolved task has its line removed instead: git already
holds that audit trail in the ledger entry and the fix commit.

Only our own verdict retires a task: a checkbox, or a recorded fix judgment with
resolving anchors. Upstream closure and PR merge never do. An upstream merge is a
parity lead, not a resolution -- it is exactly the case where TokenKey may still
be missing that behavior -- so it stays in the queue and is also listed under
`upstream_fixes` in the scan report for a focused parity check.

Relay findings also pass an off-topic gate: what a relay repository does for
ITSELF is not news about relaying. Calibrated against one hand-labelled day of
output (33 findings), the patterns dropped 5 of the 9 off-topic items and none of
the 24 worth reading -- desktop UI of their own app, ads, their design RFCs, their
own CI failures, and `dashboard`/`payment` scoped changes.

`feat(...)` product work is deliberately NOT dropped. On that day no new feature
mattered to us, so excluding features would have scored zero false positives too
-- but a new feature is how an upstream protocol change first appears ("support
new beta header"), and missing one costs more than reading a few extra lines.
Only titles are matched: bodies quote logs and screenshots, so a body match
rejects real reports that merely mention someone's dashboard.

The topic gate is per source. A relay/gateway repository keeps the full table --
"账号" alone runs ~45% true positives there, so a high hit rate reflects what that
repository is about. A CLI client's tracker needs four terms dropped (`session`,
`schedul`, `usage`, `image`): `session` matched 3255 of claude-code's titles at
roughly 2% true positives, because every issue there mentions a session. The
narrowing is scoped rather than global, since dropping those words everywhere
would blind the relay side. Measured effect: claude-code 5230 -> 1813 gated,
Wei-Shaw/sub2api unchanged at 1217.

Migrating an existing long tracking Issue needs one read-only step first:

    python3 scripts/upstream/watchdog-run.py seed-published --source upstream \
      --sync-repo <owner/repo> --seed-from-issue <number>

That records every ref the old Issue already carries as delivered, writing only
the checkpoint. Without it the first run after the split republishes the whole
backlog as intel -- measured on #2769, 3985 lines in one period instead of the
~714 weekly delta. Seeding is idempotent and never infers a verdict: a human tick
is read from the Issue's comments, so seeding cannot retire anything. Once seeded,
strip the old Issue's `watchdog-tracking` marker before closing it, or discovery
(which scans `state=all`) reopens it as soon as the queue is non-empty.

The tracking Issue holds only work TokenKey still owes: an entry whose
`judgment.tokenkey_status` is in `QUEUE_STATUSES`, or whose recorded fix anchors
have rotted. Keyword hits carry `KEYWORD_STATUSES` -- the classifier states it has
not verified them -- so they are leads, not tasks, and go to a separate intel
Issue. An entry with no known status fails closed into the queue: a visible extra
task is cheaper than a silently lost one.

Intel rotates per ISO week (`<!-- watchdog-intel:v1 platform:X period:YYYY-Www -->`,
its own marker namespace so the queue's discovery scan cannot see it and raise on a
duplicate). A new period opens a fresh Issue and closes the previous one -- closed,
never deleted, so a lead stays searchable long after the report artifact expires,
which is why intel needs an Issue carrier at all. Leads are plain bullets, never
checkboxes: ticking one there would never be seen, because the promotion path is a
judgment in the ledger, which moves the ref into the queue on the next run.
Deduplication is on the content signature alone and never includes the period --
including it would republish every still-open lead each week, which is the
accumulation the rotation exists to prevent.

The first scan of a source takes the lookback window only. There is no
full-backlog bootstrap: mirroring every open upstream issue once produced a
thousands-strong tracking Issue that nobody read, which defeats the point of
having a queue.

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
