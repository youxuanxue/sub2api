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
The backend import UI and identity validation remain unimplemented; past file-canary
installation commands are removed. Never put this export into an API Key field.

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
  --network tokenkey_tokenkey-network --user 1000:1000 --read-only \
  --memory 384m --memory-swap 384m --cpus 1 --pids-limit 64 \
  --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --env-file /etc/tokenkey/gemini-web.env tokenkey-gemini-web:canary
```

Use normal edge networking and upstream URL-security settings. Do not weaken global
security to admit an upstream URL. Database-mode rollout must install valid runtimes
before enabling those accounts; the old volume is not a fallback.

Authorized integration probes use the existing run-probe / probe_account_model workflow
with `ENDPOINT=gemini` or `gemini_image` and exact account attribution. They are not UI e2e.
Commercial catalog activation is separate. Rollback disables affected Gemini Web accounts
and stops the Worker; it does not alter other gateway accounts.
