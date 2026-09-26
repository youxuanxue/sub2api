#!/usr/bin/env bash
# Fixture tests for deploy-worker-on-host.sh against a fake docker/sudo.
#
# The failure paths are the point. A deploy that leaves a not-ready container
# under the canonical name leaves it in the serving path (health status alone does
# not remove accounts from gateway scheduling — ops/gemini-web/README.md), so the
# restore behaviour is tested against fixtures rather than discovered on an edge.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/deploy-worker-on-host.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0

mkdir -p "$WORK/bin"
cat >"$WORK/bin/sudo" <<'EOS'
#!/usr/bin/env bash
exec "$@"
EOS

# Fake docker. STATE holds container names (one per line) so rename/create/start
# behave like a host; ACTIONS records every mutation for assertions.
cat >"$WORK/bin/docker" <<'EOS'
#!/usr/bin/env bash
exists() { grep -qxF "$1" "$STATE"; }
mode="$1"; shift
case "$mode" in
pull)
  echo "pull $*" >>"$ACTIONS"
  ;;
run)
  detached=0
  for a in "$@"; do [ "$a" = "-d" ] && detached=1; done
  if [ "$detached" = 1 ]; then
    name=""
    prev=""
    for a in "$@"; do
      [ "$prev" = "--name" ] && name="$a"
      prev="$a"
    done
    echo "run -d $name" >>"$ACTIONS"
    echo "$name" >>"$STATE"
    echo "fakecontainerid"
  else
    # `docker run --rm --entrypoint python <image> build_digest.py`
    echo "run --rm build_digest" >>"$ACTIONS"
    cat "$FIXTURE_IMAGE_DIGEST"
  fi
  ;;
inspect)
  target="$1"; shift
  exists "$target" || { echo "No such object: $target" >&2; exit 1; }
  fmt=""; prev=""
  for a in "$@"; do
    [ "$prev" = "--format" ] && fmt="$a"
    prev="$a"
  done
  if [ -n "$fmt" ]; then echo "image=fake running=true health=none"; fi
  exit 0
  ;;
stop)
  name="${!#}"
  echo "stop $name" >>"$ACTIONS"
  exists "$name" || exit 1
  ;;
start)
  echo "start $1" >>"$ACTIONS"
  [ "${FIXTURE_START_FAILS:-0}" = 1 ] && exit 1
  exists "$1" || exit 1
  ;;
rename)
  echo "rename $1 $2" >>"$ACTIONS"
  exists "$1" || exit 1
  if exists "$2"; then echo "conflict: name $2 already in use" >&2; exit 1; fi
  { grep -vxF "$1" "$STATE" || true; } >"$STATE.tmp"
  mv "$STATE.tmp" "$STATE"
  echo "$2" >>"$STATE"
  ;;
exec)
  # `docker exec <name> python -c <code>` — the /readyz reader.
  exists "$1" || exit 1
  cat "$FIXTURE_READYZ"
  ;;
logs)
  echo "worker log line"
  ;;
*) echo "fake docker: unhandled ${mode}" >&2; exit 1 ;;
esac
EOS
chmod +x "$WORK/bin/sudo" "$WORK/bin/docker"
export PATH="$WORK/bin:$PATH"

# The script requires the host env file to exist before it touches anything.
printf 'GEMINI_WEB_FAKE=1\n' >"$WORK/gemini-web.env"

DIGEST=abc123def456
IMAGE="ghcr.io/example/tokenkey-gemini-web:${DIGEST}"

run_case() {
	# run_case <name> <initial-containers> <image-digest> <readyz-json> <expect-exit>
	local name="$1" containers="$2" image_digest="$3" readyz="$4" expect="$5"
	printf '%s' "$containers" >"$WORK/state"
	printf '%s' "$image_digest" >"$WORK/image_digest"
	printf '%s' "$readyz" >"$WORK/readyz"
	: >"$WORK/actions"
	# READY_TRIES/READY_SLEEP are the same knobs the SSM wrapper derives its budget
	# from, so exercising them here also covers that they reach the loop.
	STATE="$WORK/state" ACTIONS="$WORK/actions" \
		FIXTURE_IMAGE_DIGEST="$WORK/image_digest" FIXTURE_READYZ="$WORK/readyz" \
		FIXTURE_START_FAILS="${FIXTURE_START_FAILS:-0}" \
		READY_TRIES=2 READY_SLEEP=0 ENV_FILE="$WORK/gemini-web.env" \
		bash "$TARGET" "$IMAGE" "$DIGEST" 5 >"$WORK/out" 2>"$WORK/err"
	local code=$?
	if [ "$code" != "$expect" ]; then
		echo "FAIL ${name}: exit ${code}, expected ${expect}"
		sed 's/^/    /' "$WORK/out" "$WORK/err"
		failures=$((failures + 1))
		return 1
	fi
	echo "ok   ${name} (exit ${code})"
	return 0
}

assert_in() {
	# assert_in <label> <needle> <file>
	if ! grep -qF -e "$2" "$3"; then
		echo "FAIL ${1}: missing '${2}'"
		sed 's/^/    /' "$3"
		failures=$((failures + 1))
	fi
}
assert_not_in() {
	if grep -qF -e "$2" "$3"; then
		echo "FAIL ${1}: found forbidden '${2}'"
		sed 's/^/    /' "$3"
		failures=$((failures + 1))
	fi
}

READY_OK="{\"status\": \"ready\", \"build_digest\": \"${DIGEST}\"}"
READY_WRONG_DIGEST='{"status": "ready", "build_digest": "0000deadbeef"}'
NOT_READY="{\"status\": \"starting\", \"build_digest\": \"${DIGEST}\"}"

echo "--- happy path replaces the worker and renames the old one ---"
run_case "deploy succeeds" 'tokenkey-gemini-web
' "$DIGEST" "$READY_OK" 0
assert_in "reports OK with the digest" "tk_gemini_web_worker_deploy: OK digest=${DIGEST}" "$WORK/out"
assert_in "drains before replacing" "stop tokenkey-gemini-web" "$WORK/actions"
assert_in "previous is renamed, not deleted" "rename tokenkey-gemini-web tokenkey-gemini-web-prev-" "$WORK/actions"
assert_in "rename target carries the digest for uniqueness" "-${DIGEST}" "$WORK/actions"
assert_in "new container started" "run -d tokenkey-gemini-web" "$WORK/actions"
assert_not_in "nothing restored on success" "RESTORED" "$WORK/out"

echo "--- fresh host with no previous container ---"
run_case "deploy on empty host" '' "$DIGEST" "$READY_OK" 0
assert_in "still reports OK" "tk_gemini_web_worker_deploy: OK digest=${DIGEST}" "$WORK/out"
assert_not_in "nothing to drain" "stop tokenkey-gemini-web" "$WORK/actions"

echo "--- readiness timeout restores the previous worker ---"
run_case "timeout restores" 'tokenkey-gemini-web
' "$DIGEST" "$NOT_READY" 1
assert_in "says it restored" "tk_gemini_web_worker_deploy: RESTORED" "$WORK/out"
# Two stops: the planned drain, then the failed container on the restore path.
if [ "$(grep -cF -e "stop tokenkey-gemini-web" "$WORK/actions")" -lt 2 ]; then
	echo "FAIL timeout restores: not-ready container was left running"
	sed 's/^/    /' "$WORK/actions"
	failures=$((failures + 1))
else
	echo "ok   not-ready container stopped before restore"
fi
assert_in "failed container kept for diagnosis" "tokenkey-gemini-web-failed-" "$WORK/actions"
assert_in "previous renamed back to the canonical name" "tokenkey-gemini-web-prev-" "$WORK/actions"
assert_in "previous started again" "start tokenkey-gemini-web" "$WORK/actions"
if ! grep -qxF 'tokenkey-gemini-web' "$WORK/state"; then
	echo "FAIL timeout restores: host left without a canonical worker"
	sed 's/^/    /' "$WORK/state"
	failures=$((failures + 1))
else
	echo "ok   canonical container exists again after restore"
fi

echo "--- a ready worker reporting the wrong digest is rolled back ---"
run_case "wrong digest restores" 'tokenkey-gemini-web
' "$DIGEST" "$READY_WRONG_DIGEST" 1
assert_in "names the mismatch" "does not report digest ${DIGEST}" "$WORK/err"
assert_in "restores" "tk_gemini_web_worker_deploy: RESTORED" "$WORK/out"

echo "--- failure on a fresh host reports that no worker runs ---"
run_case "no previous to restore" '' "$DIGEST" "$NOT_READY" 1
assert_in "says NO-PREVIOUS" "tk_gemini_web_worker_deploy: NO-PREVIOUS" "$WORK/out"
assert_not_in "does not claim a restore" "RESTORED" "$WORK/out"

echo "--- an un-startable previous container is reported, not hidden ---"
FIXTURE_START_FAILS=1 run_case "restore failure is loud" 'tokenkey-gemini-web
' "$DIGEST" "$NOT_READY" 1
assert_in "says RESTORE-FAILED" "tk_gemini_web_worker_deploy: RESTORE-FAILED" "$WORK/out"
assert_not_in "never claims success" "tk_gemini_web_worker_deploy: OK" "$WORK/out"

echo "--- an image whose digest contradicts its tag never replaces the worker ---"
run_case "image digest mismatch aborts early" 'tokenkey-gemini-web
' "999999999999" "$READY_OK" 1
assert_in "names both digests" "image reports 999999999999 but is tagged ${DIGEST}" "$WORK/err"
assert_not_in "running worker is left untouched" "stop tokenkey-gemini-web" "$WORK/actions"
assert_not_in "no new container started" "run -d" "$WORK/actions"

if [ "$failures" -ne 0 ]; then
	echo "deploy-worker-on-host fixtures: FAIL (${failures})"
	exit 1
fi
echo "deploy-worker-on-host fixtures: PASS"
