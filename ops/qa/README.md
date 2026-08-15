# QA Operations

The approved target is defined by `docs/approved/design-prod-qa-24h-s3-lifecycle.md`.
Machine-readable policy lives in `ops/qa/policy.yaml`; repository readiness and observed live state are separate in `ops/qa/deploy_rollout.yaml`.

## Target owner

`tokenkey-qa-maintenance.timer` is the only target lifecycle owner. One run provisions future hourly children, archives and restore-verifies sealed hours, drops only exact archive-gated source partitions, then resumes exact-hour Blob/DLQ cleanup. `source_dropped_at` makes post-DROP cleanup bounded and idempotent.

`tokenkey-qa-boundary.timer` is transition-only. Before the append-only `single_owner_activate` receipt it may run `--qa-cutover-provision-only`. Receipt existence permanently forces it disabled/inactive, including sync rollback. It has no archive, DROP, export-orphan, or finalize role.

The stale-cleanup timer, prod stale operator, export-orphan helper, `qa_exports_tmp` mount, and export activation marker are retired and are not packaged or synchronized.

## State and checks

The repository may be `single_owner_ready` while observed live state remains `single_owner_not_activated`. Editing rollout metadata never proves deployment.

```bash
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 scripts/checks/qa-lifecycle-ssot.py
python3 -m unittest ops.qa.test_qa_phase_ops
cd backend && go test -tags=unit ./internal/observability/qa/lifecycle ./cmd/server
```

Phase 2 archive/recovery closeout evidence remains in `docs/approved/design-qa-phase2-archive-closeout.md` and is explicitly historical/superseded for lifecycle ownership. Edge closeout evidence is retained only as history; it is not a deployable prod owner.
