# Setup Planned Reference Report

## Scope

Implemented the approved-plan exception for literal future script references.
Only an unresolved reference on a line containing `script-ref: planned` in a
tracked `docs/approved/` file is ignored. Existing resolution behavior,
unmarked stale references, and every other tracked directory remain unchanged.

## RED Evidence

1. Extended `scripts/checks/script-ref-existence_test.sh` with two behavior
   cases:
   - P10: a marked future `scripts/...` reference in `docs/approved/` is skipped.
   - P11: the same marker outside `docs/approved/` still fails.
2. Before the checker change, ran:

   ```bash
   bash scripts/checks/script-ref-existence_test.sh
   ```

   Result: `P10 planned ref in approved doc (skip)` failed because the checker
   reported its unresolved reference; suite result was `1/11 cases failed`.
3. Before any exception markers, ran:

   ```bash
   python3 scripts/checks/script-ref-existence.py
   ```

   Result: 21 stale references in
   `docs/approved/design-all-edge-ec2-t4g-small-unlimited-migration.md`, all
   corresponding to later planned implementation files.

## GREEN Evidence

1. Added `PLANNED_REFERENCE_MARKER` and
   `is_planned_approved_reference()` to
   `scripts/checks/script-ref-existence.py`. The check only suppresses an
   unresolved reference after normal resolution has failed, the scanned path
   is under `docs/approved/`, and the same source line has the exact marker.
2. Marked exactly 21 currently unresolved future-path lines in the approved
   migration plan. Prose/list lines use HTML comments; command lines use shell
   comments. The one multi-line command was normalized to an equivalent
   single-line runnable command so its shell comment stays on the reference
   line.
3. Ran:

   ```bash
   bash scripts/checks/script-ref-existence_test.sh
   python3 scripts/checks/script-ref-existence.py
   python3 dev-rules/scripts/check_approved_docs.py
   git diff --check
   ./scripts/preflight.sh
   ```

   Results:
   - script-reference self-test: `11/11 cases passed` (the prior nine plus P10/P11)
   - script-reference scan: all literal `scripts/|ops/|tools/` references resolve
   - approved-doc validation: passed
   - whitespace validation: passed
   - full project preflight: passed

## Files Changed

- `scripts/checks/script-ref-existence.py`
- `scripts/checks/script-ref-existence_test.sh`
- `docs/approved/design-all-edge-ec2-t4g-small-unlimited-migration.md`
- `.superpowers/sdd/design-all-edge-ec2-t4g-small-unlimited-migration/setup-planned-ref-report.md`

## Self-Review

- P1 keeps the existing unmarked stale-reference failure contract covered.
- P11 proves the marker does not weaken checking outside `docs/approved/`.
- The guard requires both exact repository location and exact marker, so a
  resolved path never needs the exception and unmarked approved-plan paths
  still fail.
- Confirmed the approved migration plan contains exactly 21 markers, matching
  the original checker failure count.
- No placeholder scripts, web assets, API contracts, or unrelated checker
  behavior were added or changed.

## Concerns

- Full preflight emits its expected advisory warning that `docs/approved/` was
  changed on a `feature/` branch. It does not block this targeted approved-plan
  contract repair.
