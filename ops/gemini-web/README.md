# Gemini Web HTTP adapter

Contract and live evidence: [`docs/approved/gemini-web-channel.md`](../../docs/approved/gemini-web-channel.md), §12–13.

Private edge companion, with one Google Web cookie jar per account. TokenKey accounts use
`platform=gemini`, `type=apikey`, `base_url=http://tokenkey-gemini-web:8091` and separate
worker keys. No new backend account type; no cookies in TokenKey credentials.

## Supported subset

`POST /v1beta/models/{model}:generateContent` and `:streamGenerateContent?alt=sse`.
Header: `x-goog-api-key`. Models: `gemini-web-flash`, `gemini-web-pro`,
`gemini-web-pro-image`. These select live Web categories, **not** API model versions.

One user turn, text parts, optional `generationConfig.responseModalities`.
Unknown controls, system instructions, tools, multimodal inputs and multi-turn history
return 400 before generation; they are not silently ignored. Streaming currently emits
one complete SSE response after generation/download, so it does not improve first-token
latency. Only original-image RPC output is downloaded. No preview fallback, upscaling,
automatic generation retries, claimed modelVersion or fabricated usageMetadata.

## State

### Database control plane

The production mode has no account/session JSON files. Give both the edge
backend and this companion the same 32+ character `GEMINI_WEB_CONTROL_TOKEN`,
then start the companion with:

```text
GEMINI_WEB_CONTROL_URL=http://<edge-backend>/api/v1
GEMINI_WEB_CONTROL_TOKEN=<private service token>
```

The URL must resolve only on the edge's private Docker network. In this mode
the Worker starts with no accounts, loads one account's `credentials.gemini_web.runtime`
only after the gateway selects it, and writes refreshed state back with its
version. Do not publish `/api/v1/internal/gemini-web` through Caddy or assign
the service token to an admin or client API key.

A Worker request carries `X-TokenKey-Gemini-Web-Account-ID`, injected by the
gateway from its selected account. It is not an operator input or a public API
header. Cookie values, session exports and the control token must never enter
logs, image configuration, shell arguments or source control.

### Legacy canary volume

A protected volume (directories 0700, files 0600, owner UID 1000) contains:

```text
accounts.json                 # [{"id":"133","api_key":"<random 32+ characters>"}, ...]
133/bundle.json               # {"ua":"<source UA>","cookies":[<domain-aware cookies>]}
133/state.json                # worker-owned atomic cookie/refresh/pause state
483/bundle.json
483/state.json
owner.lock                    # one process/volume owner, including across restarts
```

Export only from the explicitly selected AdsPower profile over local CDP; all browser
Google requests must already use the edge proxy. Never bake these files into an image,
pass them as CLI arguments, or print them. Do not overwrite another account's directory.
The worker owns one volume lease; launching two owners against one volume fails.

### Per-account hot reload

The running Worker checks `accounts.json` and each configured `bundle.json` every five
seconds. Replacing one account's bundle reloads only that account after its in-flight
request completes. Other accounts keep serving; the Worker process does not restart.

Until the TokenKey admin import UI is implemented, its future installer must use this
order for one account: write the new bundle to a temporary file, remove that account's
`state.json`, then atomically rename the new file to `bundle.json`. The old state must be
removed *before* the final bundle rename, otherwise its refreshed cookies can supersede
the newly imported browser session. A malformed replacement is rejected and the existing
in-memory session remains active. `accounts.json` can add/remove independent accounts in
the same way; an added account still needs its own 32+ character Worker key and protected
directory.

## Export a session for review

This is the current operator-only export step. It reads the selected, already logged-in
AdsPower profile through its local CDP endpoint. It does not log in, upload anything, or
change the TokenKey edge. Keep the browser on Gemini and keep its Google traffic bound to
the intended edge proxy before running it.

Run it in uv's temporary isolated environment. This is the supported command on this
Mac and does not modify the uv-managed system Python:

```sh
uv run --with-requirements ops/gemini-web/export-requirements.txt \
  python3 ops/gemini-web/export_adspower_session.py \
  k1e54ley \
  --output /tmp/gemini-web-session-133.json
```

Replace the profile's AdsPower `user_id` and output path. The visible serial number
(for example `133`) is not necessarily the `user_id`; obtain the latter from AdsPower's
local profile list/API. If `websocket-client` is already installed in a dedicated venv,
running that venv's Python directly is also valid. Do not use `pip --user` against
the uv-managed Python.

```sh
uv run --with-requirements ops/gemini-web/export-requirements.txt \
  python3 ops/gemini-web/export_adspower_session.py \
  <AdsPower user_id> \
  --output /tmp/gemini-web-session-<serial>.json
```

The command prints only a safe summary: profile ID, page URL, User-Agent, cookie count,
domains and cookie names. It never prints cookie values. The output file is created with
mode `0600`, and its parent directory is restricted to `0700` when created. Check it
locally without exposing values:

```sh
stat -f '%Sp %N' /tmp/gemini-web-session-133.json  # macOS
python3 -c 'import json,sys; x=json.load(open(sys.argv[1])); print(x["format"], x["source"], len(x["cookies"]), x["user_agent"])' \
  /tmp/gemini-web-session-133.json
```

The exported package contains the browser's actual User-Agent and every CDP cookie field
for the allowlisted Google/Gemini domains, including domain, path, expiry, `httpOnly`,
`secure`, `sameSite`, partition and source attributes. It preserves unknown future CDP
fields as well. This is why a plain `Cookie:` header copied from DevTools, or a small
two-cookie export, is not equivalent. Treat the JSON as a password: do not paste it into
chat, tickets, shell history, Git, or a public upload. This step only prepares a package;
the TokenKey backend import and per-account hot reload are separate implementation work.

On import, and every ten minutes including idle periods, the single session owner
renews via RotateCookies and bootstraps again under the account lock. Every response persists all cookie changes. Auth loss pauses that
account durably; quota rejection applies a five-minute cooldown. Operators re-import
valid cookies after verification. There is no unattended login or browser migration.
Long-term refresh longevity needs observation; a short successful refresh is not proof
of indefinite operation.

## Validation and deployment

```sh
python3 -m venv /tmp/tk-gemini-web-test
/tmp/tk-gemini-web-test/bin/pip install -r ops/gemini-web/requirements.txt
/tmp/tk-gemini-web-test/bin/python -m unittest discover -s ops/gemini-web -v
python3 -m unittest discover -s ops/stage0 -p 'test_probe_account_model*.py'
./scripts/preflight.sh
```

Build/run on the authorized edge, from this directory:

```sh
sudo docker build -t tokenkey-gemini-web:canary .
sudo docker run -d --name tokenkey-gemini-web --restart unless-stopped \
  --network tokenkey_tokenkey-network --user 1000:1000 --read-only \
  --memory 384m --memory-swap 384m --cpus 1 --pids-limit 64 \
  --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --mount type=bind,src=/var/lib/tokenkey/gemini-web,dst=/state \
  tokenkey-gemini-web:canary
```

No host port and no Caddy/public route. Confirm gateway URL security permits that
private HTTP upstream; do not weaken global security to make an import pass. Import
accounts with the canonical `ops/accounts/import-accounts.sh` validation/dry-run/apply
workflow. Test each account exclusively via `ops/observability/run-probe.sh` and
`ops/stage0/probe_account_model.sh`, `ENDPOINT=gemini` / `gemini_image`. Correlation uses
the response X-Request-ID. API-only checks are integration probes, not UI e2e tests.

Commercial price/catalog activation is separate from the private Web account canary.
Do not alias an official paid API model to Web merely to bypass model admission.
Rollback: disable only these two gateway accounts, then stop this companion; the
existing gateway, OAuth accounts and cookie backups are preserved.
