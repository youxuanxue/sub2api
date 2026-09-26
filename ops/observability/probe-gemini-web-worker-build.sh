#!/usr/bin/env bash
# probe-gemini-web-worker-build.sh — read-only Gemini Web Worker build identity.
#
# Runs ON an edge host (delivered by run-probe.sh). Reads /readyz from the local
# Worker container and reports the build digest + control protocol version it
# serves. The digest is derived from the shipped sources by worker.py, so it is
# evidence about running code rather than a claim made by an image tag.
#
# Read-only: inspects container state and calls the container's own /readyz.
#
# Output: one JSON object with verdict + observed identity.
#   verdict=ok         Worker is ready and reported an identity
#   verdict=review     Worker reachable but not ready, or identity missing
#                      (an older build predates /readyz identity reporting)
#   verdict=setup_error  no Worker container on this host
set -euo pipefail

CONTAINER="${GEMINI_WEB_CONTAINER:-tokenkey-gemini-web}"

emit() {
  printf '{"schema_version":1,"verdict":"%s","container":"%s","image":"%s","running":%s,"health":"%s","status":"%s","build_digest":"%s","control_protocol_version":%s}\n' \
    "$1" "$CONTAINER" "${2:-}" "${3:-false}" "${4:-unknown}" "${5:-}" "${6:-}" "${7:-null}"
}

if ! sudo docker inspect "$CONTAINER" >/dev/null 2>&1; then
  emit setup_error "" false absent "" "" null
  exit 2
fi

IMAGE="$(sudo docker inspect "$CONTAINER" --format '{{.Config.Image}}' 2>/dev/null || echo '')"
RUNNING="$(sudo docker inspect "$CONTAINER" --format '{{.State.Running}}' 2>/dev/null || echo false)"
HEALTH="$(sudo docker inspect "$CONTAINER" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || echo unknown)"

if [ "$RUNNING" != true ]; then
  emit review "$IMAGE" "$RUNNING" "$HEALTH" "" "" null
  exit 1
fi

# Ask the process itself; the image tag is not trusted as identity.
BODY="$(sudo docker exec "$CONTAINER" python -c \
  'import urllib.request;print(urllib.request.urlopen("http://127.0.0.1:8091/readyz",timeout=5).read().decode())' \
  2>/dev/null || true)"
# A not-ready Worker answers 503; urlopen raises, so retry tolerating the error body.
if [ -z "$BODY" ]; then
  BODY="$(sudo docker exec "$CONTAINER" python -c \
    'import urllib.error,urllib.request
try:
    print(urllib.request.urlopen("http://127.0.0.1:8091/readyz",timeout=5).read().decode())
except urllib.error.HTTPError as exc:
    print(exc.read().decode())' \
    2>/dev/null || true)"
fi

if [ -z "$BODY" ]; then
  emit review "$IMAGE" "$RUNNING" "$HEALTH" "" "" null
  exit 1
fi

STATUS="$(printf '%s' "$BODY" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status",""))' 2>/dev/null || echo '')"
DIGEST="$(printf '%s' "$BODY" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("build_digest",""))' 2>/dev/null || echo '')"
PROTOCOL="$(printf '%s' "$BODY" | python3 -c 'import json,sys;v=json.load(sys.stdin).get("control_protocol_version");print("null" if v is None else int(v))' 2>/dev/null || echo null)"

if [ -z "$DIGEST" ] || [ "$STATUS" != ready ]; then
  emit review "$IMAGE" "$RUNNING" "$HEALTH" "$STATUS" "$DIGEST" "$PROTOCOL"
  exit 1
fi

emit ok "$IMAGE" "$RUNNING" "$HEALTH" "$STATUS" "$DIGEST" "$PROTOCOL"
