# QA on shared uk1: queue and resource foundation

Live fleet SSOT: [`../README.md`](../README.md).  
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

## Producer and maintenance deployment coordinates

`producer.json` owns non-secret Hetzner coordinates. The resolver supports
`--provider hetzner --manifest deploy/hetzner/qa/producer.json` without CFN.
The gateway workflow defaults to `QA_RUNTIME_PROVIDER=aws`; changing that
repository variable is a migration cutover action, not a way to provision QA.
Complete the migration baseline before selecting `hetzner`. Gateway deploys
reject a queue-backend transition or changed PostgreSQL storage coordinates;
only the separate frozen migration may update the host coordinates first.

QA storage accepts `QA_BUNDLE_STORAGE_CREDENTIALS_FILE` and
`QA_ARCHIVE_STORAGE_CREDENTIALS_FILE`. Each private JSON file contains only
`access_key_id` and `secret_access_key`. Do not set inline keys at the same time.
The gateway and maintenance mount `/var/lib/tokenkey/qa-secrets/bundle.json`,
`raw.json`, `queue.dsn` and `queue-ca.pem` at the corresponding
`/run/secrets/tokenkey-qa/` paths. Credential files must be owned by UID 1000,
mode 0400 or 0600. The user approved reusing the existing project S3 identity across producer,
worker and recovery; this does not provide S3 role isolation. Queue login roles
and bootstrap credentials remain separate. Never mount queue bootstrap credentials
in the gateway or worker. Replace an identity through a new component preparation;
file mounts and SDK initialization do not support hot key rotation.

On hcloud prod, `/var/lib/tokenkey/aws/credentials` must also be a private
UID 1000 file. Its parent is mounted read-only at `/aws` so the existing Hybrid
refresh timer can atomically rotate the file. This identity remains separate
from QA storage. Blue/green refuses a missing data Volume before touching the
host and installs mount guards for both Docker and the application service.

## Runtime preparation tooling

`ops/qa/hcloud_runtime.py` renders a runnable Compose project, PostgreSQL TLS/HBA
configuration, and firewall tables scoped to `tk-qa0`. Both images must be pinned
by digest. The worker is behind the `worker` Compose profile, so preparing the
queue does not start consuming jobs. The parent slice and two bounded ext4
filesystems must be prepared before using Compose.

`ops/qa/hcloud_credentials.py` reads the existing protected Hetzner environment
credentials and creates a new private output directory. It refuses to overwrite
an existing generation. Keep `operator/` (CA private key and bootstrap SQL) out of
runtime containers. Install queue files as UID 70 and producer/worker files as
UID 1000, with private directories and 0400 files. Queue TLS certificates expire
after 90 days; rotate through a drained component preparation before expiry.

`ops/qa/hcloud_host.py --policy ... <operation>` only runs as root on the approved
uk1 hostname. `prepare` mounts preallocated QA-only filesystems and installs the
resource slice; `bootstrap` initializes only an absent QA data directory, applies
`queue.sql` and starts with claims paused. `queue-start` / `worker-start` enforce
mounts, private credentials and the scoped firewall. `pause`, `status`, `drain`,
and `resume` operate only on the dedicated QA queue. Replacement requires claims
to be paused and leases drained. No operation restarts Docker or a gateway.

These tools are not yet a cutover command: persistent startup, backup/restore,
release coordination, migration reconciliation and actual allow/deny tests must
pass before the first producer switch.

`ops/qa/migrate_hcloud_objects.py --kind raw|bundle --ledger <private.sqlite>` is
read-only unless `--apply` is supplied. Pre-copy uses a bounded single multipart
stream, explicit bandwidth limit, conditional publication and full SHA256
readback. Its ledger permits restart after a completed upload, but refuses
foreign target objects and changes to immutable source objects. Source lifecycle
expiry is required and recorded in target metadata and the ledger. The migration
owner installs and reads back absolute-date lifecycle rules keyed by an object
expiry tag, alongside ordinary raw/Bundle retention. It preserves source tags and
explicitly reapplies them after multipart completion (Hetzner drops initiation
tags). Tag readback is required before recording success. The tool never deletes source objects, switches
producers, or declares migration complete.
