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

From this directory, build the container and pass configuration through an operator-managed
environment file containing the two variables above:

```sh
sudo docker build -t tokenkey-gemini-web:canary .
sudo docker run -d --name tokenkey-gemini-web --restart unless-stopped \
  --stop-timeout 600 \
  --network tokenkey_tokenkey-network --user 1000:1000 --read-only \
  --memory 384m --memory-swap 384m --cpus 1 --pids-limit 64 \
  --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --env-file /etc/tokenkey/gemini-web.env tokenkey-gemini-web:canary
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

The container health check uses `/readyz` (recent compatible control API response);
`/healthz` only proves process liveness. Health status alone does not remove an account
from gateway scheduling or restart an unhealthy Docker container. Allow the configured
stop timeout for graceful draining. A forced kill retains crash lease and uncertain-generation
protection; never clear these automatically to make a retry succeed.

The worker bounds image operations and decoded pixels to reduce memory pressure. The
synthetic maximum-pixel test is not a sustained-load guarantee under the container limit.
Web image requests may set `generationConfig.imageConfig.aspectRatio` to `1:1`, `9:16`,
`3:4`, `4:3`, or `16:9`; these values are mapped to the captured Gemini Images RPC
shape. Other image configuration fields are rejected before contacting Google.
The control token retains existing edge admin permissions; redirect rejection prevents
accidental forwarding, not misuse after process compromise.

Authorized integration probes use the existing run-probe / probe_account_model workflow
with `ENDPOINT=gemini` or `gemini_image` and exact account attribution. They are not UI e2e.
Commercial catalog activation is separate. Rollback disables affected Gemini Web accounts
and stops the Worker; it does not alter other gateway accounts.
Drain the Worker before rolling back the backend control API. Preserve current database
cookies and paused state; restoring an old file Worker or stale cookie snapshot is not a
compatible rollback. A backend-only rollback makes this Worker unready.
