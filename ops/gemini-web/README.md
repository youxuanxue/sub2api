# Gemini Web HTTP adapter

Current contract and owner list: [Gemini Web channel](../../docs/approved/gemini-web-channel.md).
The Worker runs only from database-backed sessions. No state volume or local account manifest.

## Configuration

TokenKey accounts use `platform=gemini`, `type=apikey`, a Worker base URL and an account API key.
The selected edge account owns `credentials.gemini_web.runtime`; the gateway supplies its ID.
The Worker validates both the ID and the account key.

Configure the Worker process with:

```text
GEMINI_WEB_CONTROL_URL=https://<edge>/api/v1
GEMINI_WEB_CONTROL_TOKEN=<existing edge API key owned by an active admin>
```

The backend uses the existing edge authentication chain; it needs no Gemini-specific token
environment variable or network restriction. Missing Worker configuration fails startup.
Treat runtime cookies and both API keys as secrets. Do not include their values in logs,
shell arguments, image layers or Git.

## Export for operator review

The exporter reads one already logged-in AdsPower profile via local CDP; it does not upload
or install credentials. Keep the browser's Google traffic on its authorized edge proxy.

```sh
uv run --with-requirements ops/gemini-web/export-requirements.txt \
  python3 ops/gemini-web/export_adspower_session.py \
  <AdsPower-user_id> --output /tmp/gemini-web-session.json
```

The export is a protected operator artifact, not Worker runtime storage. It contains
`user_agent` and complete CDP cookie records, with a safe summary printed separately.
In the target edge admin, copy the Worker account when a separate session is needed, then open
the copy under Accounts → edit. The copy keeps an empty `gemini_web` declaration without any
cookies, so the same control is labelled **Initialize Gemini Web Worker session**; an existing
runtime is labelled **Replace Gemini Web browser session**. Choose this JSON and review the cookie
count and committed runtime version. Initialization requires scheduling to be disabled and leaves
it disabled; enable it manually after review. Replacement preserves the existing scheduling state.
The file must be at most 2 MiB. A session conflict means a Worker operation or another import
won; retry later after checking the account. Plain API-key accounts and production relays cannot
receive browser sessions here.

One edge Worker serves multiple accounts with separate runtimes; copying an account does not
require another Worker. Copies made before this support was deployed lost their Worker declaration:
copy the real Worker account again after upgrading instead of guessing identity from its name.

Import success confirms database persistence, not Google sign-in or browser identity.
Never put this export into an API Key field; do not send it to the production relay account.

## Validation

```sh
uv run --with-requirements ops/gemini-web/requirements.txt \
  python3 -m unittest discover -s ops/gemini-web -v
uv run --with-requirements ops/gemini-web/requirements.txt \
  python3 -m unittest discover -s ops/stage0 -p 'test_probe_account_model*.py'
./scripts/preflight.sh
```

Image probes require Pillow in the Python environment running the probe. The pinned
dependency is in requirements.txt; missing decoder support cannot produce a servable verdict.
Tests use synthetic cookies and protocol fixtures, without Google calls. Backend regression
tests cover redaction, duplicate reset, both gateway entry points, edge auth and database CAS.

## Deployment after authorization

Images come from CI only. `gemini-web-worker.yml` builds one `linux/amd64` image
per content change on push to main and tags it with the build digest, verifying
that the image reports the digest it was tagged with. Do not `docker build` on a
host: a hand-built image is what produced `tokenkey-gemini-web:1.8.253-147512b9d`,
a tag naming a commit that never landed on main.

Deploy an edge with `deploy-gemini-web-worker.yml` (SSM pull + recreate under the
container limits below), one edge per dispatch:

```text
edge_id           us3 | us4 | us5 | us6   (deployable edges; prod runs no Worker)
build_digest      the 12-hex tag the publish job printed as gemini_web_build_digest
confirm_instance  instance_name from deploy/aws/lightsail/edge-targets-lightsail.json
```

The run fails unless the container it started answers `/readyz` reporting that
exact digest, checked twice: once on the host, once afterwards through
`run-probe.sh` so the evidence comes from the same checker the fleet view reads.
The previous container is renamed `tokenkey-gemini-web-prev-<UTC>` rather than
removed, so a bad deploy leaves its logs behind. After the last edge, confirm
convergence with `bash ops/observability/check-gemini-web-worker-fleet.sh`.

Retention is the other half of that rename: pets accumulate by design, so
`prune-worker-pets.sh` removes the superseded ones on a schedule of your choosing.

```bash
# dry-run (default): prints the plan, mutates nothing
bash ops/observability/run-probe.sh --target edge:us4 \
    --script ops/gemini-web/prune-worker-pets.sh
# apply, retaining the newest superseded container as a rollback target
bash ops/observability/run-probe.sh --target edge:us4 \
    --script ops/gemini-web/prune-worker-pets.sh --env TK_WORKER_PRUNE_APPLY=1
```

It never touches the running Worker or the image that Worker runs, identifies the
running container by state rather than by name, matches images by ID (several tags
share one build), and refuses outright when no Worker is running — with nothing
running there is no way to tell a superseded container from the one an operator is
about to restart. `TK_WORKER_PRUNE_KEEP` sets how many superseded containers stay
as evidence (default 1). Fixtures: `bash ops/gemini-web/test_prune_worker_pets.sh`.

The runtime contract the deploy workflow applies, for reference when debugging a
host — configuration comes from an operator-managed environment file holding the
two variables above:

```text
--restart unless-stopped --stop-timeout 600
--network tokenkey_tokenkey-network --user 1000:1000 --read-only
--memory 384m --memory-swap 384m --cpus 1 --pids-limit 64
--cap-drop ALL --security-opt no-new-privileges
--tmpfs /tmp:rw,noexec,nosuid,size=16m
--env-file /etc/tokenkey/gemini-web.env
```

Use normal edge networking and upstream URL-security settings. Do not weaken global
security to admit an upstream URL. Database-mode rollout must install valid runtimes
before enabling those accounts; the old volume is not a fallback.

Upgrade the backend first, keep Web accounts out of user-facing groups during validation,
and establish the first local Worker declaration through the existing account update API.
Further accounts use the copy → edit → import flow above. Do not use the check below as a
prerequisite for importing into a paused copy: it requires scheduling to be enabled.
Use the new image with the same environment/network settings to run `python worker.py --check
<account-id> [<account-id>...]` before starting service. The check requires active, schedulable
accounts with concurrency=1, valid local cookie records and no paused/pending/cooldown state.
It is read-only and does not establish Google session validity. Only enable user-facing
routing after an authorized account-attributed canary succeeds.

## Build identity and fleet convergence

The Worker deploys on its own cadence: prod does not run it, and the four
deployable edges do. Nothing couples it to a gateway release, so the only way to
prove every edge runs one build is to ask each Worker what it runs.

`build_digest.py` owns the contract: sha256 over every shipped file that changes
behaviour (`DIGEST_FILES`, including `requirements.txt` — identical code on
different pinned dependencies is a different build), first 12 hex. CI runs it
standalone to tag the image and the Worker imports it at runtime, so the tag and
the process cannot disagree. Run it yourself with
`python3 ops/gemini-web/build_digest.py`; it needs no dependencies.

`/readyz` reports `build_digest` and `control_protocol_version` on both the 200
and 503 answers, and `--check` prints the same pair. `Server:` carries
`GeminiWebAdapter/<digest>`. The digest is evidence about running code, not a
claim made by an image tag — a hand-built image reusing another tag is still
detected.

Read the whole fleet in one command (read-only, per-edge SSM):

```sh
bash ops/observability/check-gemini-web-worker-fleet.sh
```

Exit `0` converged, `1` review (digests disagree, a Worker is not ready, or a
build predates identity reporting), `2` setup/transport failure. The denominator
is deployable edges; prod having no Worker container is correct state, never
drift. Differing image tags over one shared digest still count as converged.

`CONTROL_PROTOCOL_VERSION` is the contract with the backend control API. Bump it
only together with that API: `check_control` rejects any other value, so a
mismatch makes every Worker unready rather than silently misbehaving.

Cadence is decoupled, compatibility is not. The backend side is
`handler.GeminiWebControlProtocolVersion`, and
`scripts/checks/gemini-web-control-protocol.py` fails preflight when the two sides
disagree, so a one-sided bump cannot reach main. When both move, that release is a
paired one: ship the backend, dispatch `deploy-gemini-web-worker.yml` for every
deployable edge, and confirm with the fleet check before considering it done — a
half-converged fleet means the un-deployed edges are out of service.

The container health check uses `/readyz` (recent compatible control API response);
`/healthz` only proves process liveness. Health status alone does not remove an account
from gateway scheduling or restart an unhealthy Docker container. Allow the configured
stop timeout for graceful draining. A forced kill retains crash lease and uncertain-generation
protection; never clear these automatically to make a retry succeed.

The worker bounds image operations and decoded pixels to reduce memory pressure. The
synthetic maximum-pixel test is not a sustained-load guarantee under the container limit.
Web image requests may set `generationConfig.imageConfig.aspectRatio` to `1:1`, `9:16`,
`3:4`, `4:3`, or `16:9`; these values are mapped to the captured Gemini Images RPC
shape. For image requests, an omitted or null `imageConfig` uses default image
options. Other image configuration fields are rejected before contacting Google.
The control token retains existing edge admin permissions; redirect rejection prevents
accidental forwarding, not misuse after process compromise.

Authorized integration probes use the existing run-probe / probe_account_model workflow
with `ENDPOINT=gemini` or `gemini_image` and exact account attribution. They are not UI e2e.
After the gateway compatibility change is deployed, use `REQUEST_BODY_JSON` to send
an exact single-turn payload. This bypasses the probe's default system/instructions
and token limits. It must be a JSON object with the selected model, and cannot be
combined with `REQUEST_EXTRA_JSON`. Messages still requires a positive `max_tokens`. The approved Web best-effort contract
does not guarantee this budget: Plan records the adjustment and omits the unsupported
limit only in its effective upstream request. General Gemini retains the original limit.

For an authorized image canary, first confirm the target account and current deployment,
then run one request at a time (replace the example account/model with the verified values):

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/stage0/probe_account_model.sh \
  --timeout-seconds 600 \
  --env ACCOUNT_ID=200 --env MODEL=gemini-3.1-flash-image --env ENDPOINT=chat \
  --env REQUEST_TIMEOUT_SECONDS=480 \
  --env 'REQUEST_BODY_JSON={"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":"Draw a small red apple on a white table"}],"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"4:3"}}}'
```

For Responses, use `ENDPOINT=responses` and replace `messages` with
`"input":"Draw a small red apple on a white table"`. For Messages, use
`ENDPOINT=messages`, retain `messages`, and add `"max_tokens":1024`; confirm the
Web Plan records `gemini_web_best_effort_token_limit`. Omit `imageConfig` to test defaults,
or set it to null to test explicit null. The explicit IMAGE modality activates image
permission only on the reserved probe group and requires decodable image output for a
servable verdict. Preserve account attribution and the server response request ID.
Set `"stream":true` in the exact body to validate buffered SSE; the probe requires the
protocol terminal event and rejects error or truncated streams even when image data arrived.
This direct reserved-key probe does not establish Universal-key parity; validate that
separately with a normally authorized test key after deployment.

Run the existing cleanup dry-run after the canaries:

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/cleanup-probe-resources.sh
```

Commercial catalog activation is separate. Rollback disables affected Gemini Web accounts
and stops the Worker; it does not alter other gateway accounts.
Drain the Worker before rolling back the backend control API. Preserve current database
cookies and paused state; restoring an old file Worker or stale cookie snapshot is not a
compatible rollback. A backend-only rollback makes this Worker unready.
