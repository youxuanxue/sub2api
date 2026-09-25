#!/usr/bin/env bash
# Runs ON an edge host, delivered by ops/stage0/deploy-gemini-web-worker-via-ssm.sh.
#
# Replaces the Gemini Web Worker container with a CI-built image. Kept as its own
# file (rather than inline SSM command strings) so the container contract stays
# reviewable and testable as shell.
#
# The Worker owns Google browser sessions, crash leases and uncertain-generation
# protection, so the old container is drained with SIGTERM and the full stop
# timeout before being replaced. The previous container is renamed, not deleted:
# a failed rollout leaves evidence on the box.
#
# Usage: deploy-worker-on-host.sh <image> <digest> [stop-timeout-seconds]
set -euo pipefail

IMAGE="${1:?image required}"
DIGEST="${2:?digest required}"
STOP_TIMEOUT="${3:-600}"
CONTAINER=tokenkey-gemini-web
ENV_FILE=/etc/tokenkey/gemini-web.env
READY_TRIES="${READY_TRIES:-60}"
READY_SLEEP="${READY_SLEEP:-5}"

# Reads /readyz and prints the JSON body, tolerating the 503 a not-ready Worker
# returns. Printed as one line so the SSM log stays greppable.
# A not-ready Worker answers /readyz with 503 and still carries its identity, so
# the HTTPError body is the answer here, not an error to swallow.
READ_READYZ='import json,urllib.error,urllib.request
try:
    body = urllib.request.urlopen("http://127.0.0.1:8091/readyz", timeout=3).read()
except urllib.error.HTTPError as exc:
    body = exc.read()
except Exception:
    body = b"{}"
try:
    print(json.dumps(json.loads(body)))
except ValueError:
    print("{}")
'

echo "=== gemini web worker deploy image=${IMAGE} ==="
test -f "$ENV_FILE" || { echo "missing ${ENV_FILE}" >&2; exit 1; }

sudo docker pull "$IMAGE"

# A tag is an operator claim; make the image prove it before it serves traffic.
echo "--- verify image reports the digest its tag claims ---"
REPORTED="$(sudo docker run --rm --entrypoint python "$IMAGE" build_digest.py)"
if [ "$REPORTED" != "$DIGEST" ]; then
  echo "image reports ${REPORTED} but is tagged ${DIGEST}" >&2
  exit 1
fi
echo "image_digest_verified=${REPORTED}"

echo "--- previous container ---"
sudo docker inspect "$CONTAINER" \
  --format 'image={{.Config.Image}} running={{.State.Running}}' 2>/dev/null || echo none

if sudo docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "--- drain old container (up to ${STOP_TIMEOUT}s) ---"
  sudo docker stop -t "$STOP_TIMEOUT" "$CONTAINER" >/dev/null
  sudo docker rename "$CONTAINER" "${CONTAINER}-prev-$(date -u +%Y%m%dT%H%M%SZ)"
fi

echo "--- start new container ---"
# Security contract mirrors ops/gemini-web/README.md; read-only, dropped
# capabilities and the tmpfs are load-bearing for Google credential handling.
sudo docker run -d --name "$CONTAINER" \
  --restart unless-stopped \
  --stop-timeout "$STOP_TIMEOUT" \
  --network tokenkey_tokenkey-network \
  --user 1000:1000 \
  --read-only \
  --memory 384m --memory-swap 384m --cpus 1 --pids-limit 64 \
  --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --env-file "$ENV_FILE" \
  "$IMAGE" >/dev/null

echo "--- wait for /readyz ---"
BODY='{}'
READY=0
for _ in $(seq 1 "$READY_TRIES"); do
  BODY="$(sudo docker exec "$CONTAINER" python -c "$READ_READYZ" 2>/dev/null || echo '{}')"
  case "$BODY" in
    *'"status": "ready"'*) READY=1; break ;;
  esac
  sleep "$READY_SLEEP"
done
if [ "$READY" != 1 ]; then
  echo "worker did not become ready: ${BODY}" >&2
  sudo docker logs --tail 20 "$CONTAINER" 2>&1 | tail -20 >&2 || true
  exit 1
fi
echo "readyz=${BODY}"

# Ready is not enough: the running process must be the build we deployed.
case "$BODY" in
  *"\"build_digest\": \"${DIGEST}\""*) ;;
  *) echo "running worker does not report digest ${DIGEST}: ${BODY}" >&2; exit 1 ;;
esac

echo "--- final state ---"
sudo docker inspect "$CONTAINER" --format \
  'image={{.Config.Image}} running={{.State.Running}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}'
echo "tk_gemini_web_worker_deploy: OK digest=${DIGEST}"
