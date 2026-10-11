# QA on shared uk1: queue and resource foundation

Baseline: [hcloud QA migration](../../../docs/approved/hcloud-bluegreen-qa-migration.md).
This directory does not deploy QA or migrate objects/messages. Default gateway
configuration continues using SQS until explicitly changed.

## Queue database

Provision a **separate PostgreSQL instance**, database `tokenkey_qa_queue`, then
apply `queue.sql` using its bootstrap administrator. The SQL refuses every other
database name and runs transactionally. It is initial installation, not an
idempotent upgrade or a gateway database migration.

Create separate non-superuser LOGIN identities with membership in
`qa_queue_producer` (gateway/canary) and `qa_queue_consumer` (worker). Do not grant
membership in `qa_queue_owner`, table privileges, CREATEDB, CREATEROLE, replication,
or BYPASSRLS. Runtime grants only permit their queue functions. Keep bootstrap
credentials out of every gateway/worker container. TLS must verify the queue
server hostname and CA; private networking does not replace authentication.

Runtime config:

```text
QA_BUNDLE_QUEUE_DRIVER=postgres
QA_BUNDLE_QUEUE_URL=
QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE=/run/secrets/qa-queue.dsn
```

The private credential file (0400/0600, readable only by the service UID) contains
one PostgreSQL URL for `/tokenkey_qa_queue` with `sslmode=verify-full` and, when
needed, `sslrootcert=/run/secrets/queue-ca.pem`. Do not put the URL in command-line
arguments, Compose environment, logs, or the repository.

`qa_queue.control.paused` is changed only by the separate operations owner:
pause before worker replacement/Edge bluegreen; wait for all leases to finish;
replace the worker; verify it; resume. A pause permits ACK of existing work.
Do not force-kill an in-flight worker just to meet a short deploy timeout.
Expired leases are retried up to three times; exhausted jobs remain `dead`.
Cleanup/retention and backup restore must be operationally verified before
cutover. There is intentionally no automatic deletion of pending/dead jobs.

## Resource budget

`resource-policy.json` is the owner for component and aggregate limits.

```bash
python3 ops/qa/colocation_resources.py --format slice
python3 ops/qa/colocation_resources.py --format compose
```

The generated Compose document is a **resource override**, not a runnable stack.
The parent slice must be installed and verified with cgroup v2/systemd before
starting containers. Both worker and queue must use it; any future helper must
fit inside the same aggregate budget. Never scale replicas to bypass the cap.
Scratch must have an actual 4 GiB filesystem/project quota; an ordinary directory
on the Edge root disk is not acceptable. Log rotation is bounded separately.

Before production, complete non-root UIDs, independent mounts and credentials,
TLS/private producer access, object-store egress allowlist, cross-network denial,
scratch quota, recovery and simultaneous Edge release/load tests. Containers
share a kernel and host failure domain; they do not offer absolute isolation.

## Verification

```bash
python3 -m unittest ops.qa.test_colocation_resources
cd backend && go test -tags=unit ./internal/config ./internal/observability/qa/bundle ./internal/observability/qa ./cmd/server
go test -tags=integration ./internal/observability/qa/bundle -run TestUS062 -count=1
```

The bounded live experiment uses `ops/qa/verify_colocation_resources.sh`, delivered
through `run-probe.sh` with explicit `QA_COLOCATION_SYNTHETIC_OPT_IN=1` and companions
`verify_colocation_resources.py`, `colocation_resources.py`, and `resource-policy.json`.
It verifies actual
cgroup placement, CPU throttling and a contained queue-memory OOM using two
networkless, unprivileged synthetic containers. It removes the containers and
temporary slice. It never mounts Edge data or injects production credentials.

The S3 compatibility probe uses only synthetic data in a dedicated verification
bucket. Read `HCLOUD_S3_Access_Key` and `HCLOUD_S3_Secret_Key` from the operator's
protected environment; never put their values in command arguments or logs.
Without `--apply` it prints the plan. With `--apply`, it creates a private bucket
unless `--bucket` selects an existing empty `tokenkey-qa-verify-fsn1-*` bucket.
It deletes its test objects/uploads and retains the empty bucket.

```bash
uv run --no-project --with boto3 python ops/qa/probe_hcloud_object_storage.py
uv run --no-project --with boto3 python ops/qa/probe_hcloud_object_storage.py --apply --bucket tokenkey-qa-verify-fsn1-dd2ca9019b22
```

This verifies the provider API, including conditional writes and ordinary signed GET/CORS; it does not
exercise the production Go store, runtime identities, migration, or browser UI.
See the baseline for live results and the required ETag compatibility change.
