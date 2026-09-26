#!/usr/bin/env bash
# Fixture tests for prune-worker-pets.sh against a fake docker/sudo.
#
# The retention rules are what stand between "tidy host" and "deleted the running
# Worker", so they are tested against fixtures rather than trusted on the host.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${SCRIPT_DIR}/prune-worker-pets.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0

# Fake sudo/docker. FIXTURE picks the host state; ACTIONS records mutations.
mkdir -p "$WORK/bin"
cat >"$WORK/bin/sudo" <<'EOS'
#!/usr/bin/env bash
exec "$@"
EOS
cat >"$WORK/bin/docker" <<'EOS'
#!/usr/bin/env bash
# args: ps|images|rm|rmi|inspect
mode="$1"; shift
case "$mode" in
ps)
  all=0; running_only=0
  for a in "$@"; do
    [ "$a" = "-a" ] && all=1
    [ "$a" = "status=running" ] && running_only=1
  done
  # format is the last --format value
  fmt=""
  prev=""
  for a in "$@"; do
    [ "$prev" = "--format" ] && fmt="$a"
    prev="$a"
  done
  while IFS='|' read -r name state image_id created; do
    [ -n "$name" ] || continue
    if [ "$running_only" = 1 ] && [ "$state" != running ]; then continue; fi
    if [ "$all" = 0 ] && [ "$state" != running ]; then continue; fi
    out="$fmt"
    # Docker 25 has no .ImageID on `ps`; rendering it is a template error, so the
    # fake must not offer it either or the test would pass against a real failure.
    case "$fmt" in
      *ImageID*) echo "failed to execute template: can't evaluate field ImageID" >&2; exit 1 ;;
    esac
    out="${out//\{\{.Names\}\}/$name}"
    out="${out//\{\{.State\}\}/$state}"
    out="${out//\{\{.CreatedAt\}\}/$created}"
    out="${out//\{\{.Image\}\}/img-$image_id}"
    printf '%b\n' "$out"
  done < "$FIXTURE_CONTAINERS"
  ;;
images)
  fmt=""; prev=""
  for a in "$@"; do
    [ "$prev" = "--format" ] && fmt="$a"
    prev="$a"
  done
  while IFS='|' read -r tag image_id; do
    [ -n "$tag" ] || continue
    out="$fmt"
    out="${out//\{\{.Repository\}\}:\{\{.Tag\}\}/$tag}"
    out="${out//\{\{.ID\}\}/$image_id}"
    printf '%b\n' "$out"
  done < "$FIXTURE_IMAGES"
  ;;
rm)
  echo "rm $*" >> "$ACTIONS"
  ;;
rmi)
  echo "rmi $*" >> "$ACTIONS"
  ;;
inspect)
  # args: <name> --format <tmpl>
  target="$1"; shift
  fmt=""; prev=""
  for a in "$@"; do
    [ "$prev" = "--format" ] && fmt="$a"
    prev="$a"
  done
  case "$fmt" in
    *.Image*)
      # Image id comes from inspect, as the script now does on Docker 25.
      while IFS='|' read -r name state image_id created; do
        [ "$name" = "$target" ] || continue
        echo "sha256:${image_id}"
      done < "$FIXTURE_CONTAINERS"
      ;;
    *)
      # the running worker stays running unless a fixture says otherwise
      echo "${FIXTURE_INSPECT:-true}"
      ;;
  esac
  ;;
*) exit 1 ;;
esac
EOS
chmod +x "$WORK/bin/sudo" "$WORK/bin/docker"
export PATH="$WORK/bin:$PATH"

run_case() {
	# run_case <name> <containers> <images> <expect-exit> [extra args...]
	local name="$1" containers="$2" images="$3" expect="$4"
	shift 4
	printf '%s' "$containers" >"$WORK/containers"
	printf '%s' "$images" >"$WORK/images"
	: >"$WORK/actions"
	FIXTURE_CONTAINERS="$WORK/containers" FIXTURE_IMAGES="$WORK/images" \
		ACTIONS="$WORK/actions" bash "$TARGET" "$@" >"$WORK/out" 2>"$WORK/err"
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

assert_absent() {
	# assert_absent <label> <pattern> <file>
	if grep -qF "$2" "$3"; then
		echo "FAIL ${1}: found forbidden '${2}'"
		failures=$((failures + 1))
	fi
}
assert_present() {
	if ! grep -qF "$2" "$3"; then
		echo "FAIL ${1}: missing '${2}'"
		sed 's/^/    /' "$3"
		failures=$((failures + 1))
	fi
}

# us4's real shape: one running plus four superseded, five distinct images.
US4_CONTAINERS='tokenkey-gemini-web|running|img1|2026-09-24 21:37:19
tokenkey-gemini-web-prev-1.8.252|exited|img2|2026-09-23 13:17:17
tokenkey-gemini-web-previous-aa2764c6a7|exited|img3|2026-09-22 13:24:16
tokenkey-gemini-web-legacy-20260922|exited|img4|2026-09-21 06:40:19
tokenkey-gemini-web-before-image-mode|exited|img5|2026-09-20 14:56:42
'
US4_IMAGES='tokenkey-gemini-web:1.8.253-147512b9d|img1
tokenkey-gemini-web:fix-aa2764c6a7|img2
tokenkey-gemini-web:db-2a7cce1941|img3
tokenkey-gemini-web:image-mode-20260921|img4
tokenkey-gemini-web:canary|img5
'

echo "--- dry-run never mutates ---"
run_case "dry-run exits 0" "$US4_CONTAINERS" "$US4_IMAGES" 0
assert_present "dry-run reports plan" "worker_pet_prune: DRY-RUN containers=3 images=3" "$WORK/out"
if [ -s "$WORK/actions" ]; then
	echo "FAIL dry-run mutated docker state:"
	sed 's/^/    /' "$WORK/actions"
	failures=$((failures + 1))
else
	echo "ok   dry-run performed no docker rm/rmi"
fi

echo "--- apply keeps the running worker and one rollback target ---"
run_case "apply exits 0" "$US4_CONTAINERS" "$US4_IMAGES" 0 --apply
assert_present "removes the three oldest" "worker_pet_prune: OK containers=3 images=3" "$WORK/out"
assert_absent "never removes the running container" "rm tokenkey-gemini-web\n" "$WORK/actions"
for forbidden in "rm tokenkey-gemini-web$" "rmi img1" "rm tokenkey-gemini-web-prev-1.8.252" "rmi img2"; do
	if grep -qE "^${forbidden}" "$WORK/actions"; then
		echo "FAIL apply removed a retained item: ${forbidden}"
		sed 's/^/    /' "$WORK/actions"
		failures=$((failures + 1))
	fi
done
for expected in "rm tokenkey-gemini-web-previous-aa2764c6a7" "rm tokenkey-gemini-web-legacy-20260922" "rm tokenkey-gemini-web-before-image-mode" "rmi img3" "rmi img4" "rmi img5"; do
	assert_present "apply removes ${expected}" "$expected" "$WORK/actions"
done

echo "--- no running worker refuses rather than guessing ---"
run_case "refuses with no running worker" 'tokenkey-gemini-web-old|exited|img9|2026-09-01 00:00:00
' 'tokenkey-gemini-web:old|img9
' 2 --apply
assert_present "says why" "worker_pet_prune: SKIPPED no-running-worker" "$WORK/out"
if [ -s "$WORK/actions" ]; then
	echo "FAIL refusal still mutated state"
	failures=$((failures + 1))
else
	echo "ok   refusal performed no docker rm/rmi"
fi

echo "--- a second running worker is kept for a human ---"
run_case "second running kept" 'tokenkey-gemini-web|running|img1|2026-09-24 21:37:19
tokenkey-gemini-web-canary|running|img2|2026-09-23 13:17:17
tokenkey-gemini-web-old|exited|img3|2026-09-01 00:00:00
' 'tokenkey-gemini-web:a|img1
tokenkey-gemini-web:b|img2
tokenkey-gemini-web:c|img3
' 0 --apply --keep 0
assert_present "flags the anomaly" "running, not the primary" "$WORK/out"
if grep -qE "^rmi img2" "$WORK/actions"; then
	echo "FAIL removed a running worker's image"
	failures=$((failures + 1))
else
	echo "ok   second running worker's image retained"
fi

echo "--- keep 0 removes every superseded container ---"
run_case "keep 0" "$US4_CONTAINERS" "$US4_IMAGES" 0 --apply --keep 0
assert_present "all four removed" "worker_pet_prune: OK containers=4 images=4" "$WORK/out"
if grep -qE "^rmi img1" "$WORK/actions"; then
	echo "FAIL keep 0 removed the running worker's image"
	failures=$((failures + 1))
else
	echo "ok   keep 0 still protects the running image"
fi

echo "--- clean host is a no-op ---"
run_case "clean host" 'tokenkey-gemini-web|running|img1|2026-09-25 14:05:18
' 'tokenkey-gemini-web:1.8.253-147512b9d|img1
' 0 --apply
assert_present "reports nothing to do" "worker_pet_prune: OK nothing-to-remove" "$WORK/out"

echo "--- shared image id across tags is retained once referenced ---"
run_case "shared image id" 'tokenkey-gemini-web|running|img1|2026-09-25 14:05:18
tokenkey-gemini-web-old|exited|img1|2026-09-01 00:00:00
' 'tokenkey-gemini-web:new|img1
tokenkey-gemini-web:alias|img1
' 0 --apply --keep 0
if grep -qE "^rmi img1" "$WORK/actions"; then
	echo "FAIL removed the image the running worker uses (shared by tag alias)"
	failures=$((failures + 1))
else
	echo "ok   image shared with the running worker retained under both tags"
fi

echo "--- bad --keep is rejected ---"
run_case "rejects non-numeric keep" "$US4_CONTAINERS" "$US4_IMAGES" 1 --keep abc

if [ "$failures" -ne 0 ]; then
	echo "prune-worker-pets fixtures: FAIL (${failures})"
	exit 1
fi
echo "prune-worker-pets fixtures: PASS"
