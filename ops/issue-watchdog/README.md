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
  and cannot close a tracking Issue; run the behavioral regression tests in the fix PR.
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
