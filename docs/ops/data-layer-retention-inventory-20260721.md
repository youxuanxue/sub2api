---
title: Data-layer retention inventory and runway
status: evidence
captured_at: 2026-07-20T16:51:37Z
updated_at: 2026-07-21T00:32:15Z
target: prod
source: ops/observability/probe-data-layer-retention-inventory.sh
---

# Data-layer retention inventory

This is a read-only production inventory. It did not execute `DELETE`, `DROP PARTITION`,
`TRUNCATE`, `VACUUM`, export, resize, deploy, or restart. The
probe uses read-only PostgreSQL sessions, a 100 ms lock timeout, and a 20 s
statement timeout. Whole-partition bytes are physical evidence; planner row
counts and relation-size ratios are estimates only.

## Observed state

| Signal | Value |
| --- | ---: |
| Data volume (pre-grow snapshot) | 52,655,943,680 B total / 36,164,419,584 B used |
| `usage_logs` | 5,938,176,000 B / 6,850,087 rows observed by the capacity probe |
| `usage_logs` older than 90 days | **0 rows** (bounded indexed count) |
| `ops_system_logs` | 14,036,582,400 B / 7,366,754 estimated live rows (`pg_stat`) |
| `ops_error_logs` | 3,150,708,736 B / 711,789 estimated live rows (`pg_stat`) |
| `qa_records` | 813,129,728 B / 188,012 estimated live rows (`pg_stat`) |
| QA partitions physically droppable now | 320,299,008 B (`2026-04`, `2026-05`) |
| QA local blobs older than 2 days | 856,802,529 B |

The ops legacy partitions currently cross the 30-day cutoff, so their
physically droppable bytes are **0 B today**. Their exact relation sizes and
first eligible drop time are:

| Partition set | Bytes | Earliest eligibility |
| --- | ---: | --- |
| `ops_system_logs_legacy` | 8,309,915,648 B | 2026-07-31T00:00:00Z |
| `ops_error_logs_legacy` | 2,278,719,488 B | 2026-07-31T00:00:00Z |
| Combined | 10,588,635,136 B (~9.86 GiB) | 2026-07-31T00:00:00Z |

The July ops partitions already contain 6,598,656,000 B. Together with the
observed `usage_logs` rate of 3.487 GiB per 30 days, this implies roughly
12-13 GiB/month of residual data growth while the current traffic profile
continues. The ops rate is a short-window extrapolation, not a permanent
capacity promise; keep measuring it.

## Runway scenarios

The offline projection uses a 90-day usage hot layer, 30-day raw ops hot
layer, 2-day QA hot layer, an 85% operational limit, and 13 GiB/month
residual growth. It does not treat PostgreSQL `DELETE` as filesystem reclaim.

| Volume / reclaim evidence | To 85% | To full |
| --- | ---: | ---: |
| Pre-grow 50 GiB; QA partitions only (0.298 GiB) | ~0.3 mo | ~0.9 mo |
| Pre-grow 50 GiB; ops legacy + QA partitions (10.159 GiB) | ~1.1 mo | ~1.7 mo |
| Grow-only 100 GiB; QA partitions only | ~3.6 mo | ~4.7 mo |
| Grow-only 100 GiB; ops legacy + QA partitions (10.159 GiB) | ~4.3 mo | ~5.5 mo |

The QA blob figure is additional host filesystem space and is not included in
the PostgreSQL partition numbers. Including it improves the scenarios by less
than one month and does not change the decision.

## Post-inventory volume update

The separately approved grow-only change completed on 2026-07-21 without an
instance or volume replacement. CloudFormation now records
`DataVolumeSizeGiB=100`; EBS volume `vol-020ce8eda4cf1e5ea` reached 100 GiB and
the mounted ext4 filesystem was grown online from 52,655,943,680 B to
105,492,467,712 B. The post-grow check reported 36,718,223,360 B used,
64,118,976,512 B available, and a refreshed `DataVolumeUsedPercent` of 37%.
PostgreSQL, Redis, the application containers, EC2 status checks, and all
control-plane probes remained healthy. No service was restarted.

The projection table above remains a point-in-time forecast derived from the
pre-grow inventory. The completed expansion selects its 100 GiB scenarios; it
does not change the retention evidence or authorize archive deletion.

## Decision

- Exporting or sealing data without a later approved physical reclaim buys
  **0 months** of `df` runway.
- QA cleanup plus the two ops legacy drops would have bought roughly **one
  month** on the pre-grow 50 GiB volume under the measured growth profile.
- The completed 100 GiB grow-only expansion is a short-term safety buffer, not
  an RDS replacement. At the same growth rate it buys roughly four months
  after the archive reclaim.
- Do not approve a production drop from this inventory alone. The
  non-production `dry-run -> seal -> verify -> restore` rehearsal is complete;
  next run an export-only production canary with an independent
  restore/checksum. A production partition drop, QA blob purge, any further
  volume resize, and RDS migration remain separate approvals.

Next engineering action: retain this probe and its safety test in PR #1390,
then prepare an export-only production canary using the already completed
non-production `dry-run -> seal -> verify -> restore` path. The production
canary requires a separate approval and must not include deletion, scheduling,
or deployment wiring.
