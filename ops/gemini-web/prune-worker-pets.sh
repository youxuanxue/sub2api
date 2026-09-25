#!/usr/bin/env bash
# prune-worker-pets.sh — retention for superseded Gemini Web Worker containers/images.
#
# Runs ON an edge host (delivered by run-probe.sh). Dry-run by default.
#
# deploy-worker-on-host.sh renames the previous container instead of removing it,
# so a bad deploy leaves its logs behind. That is deliberate, and it means pets
# accumulate by design — this script is the other half of that contract, not a
# manual sweep. us4 reached five containers and five 190MB images this way.
#
# What is never touched:
#   - the running Worker container, whatever it is called
#   - the image that container runs (by image ID, not by tag)
#   - the newest KEEP superseded containers and the images they reference
#   - anything outside the tokenkey-gemini-web namespace
#
# Ordering matters: containers first, then images, and an image is only removed
# once no container references it. Newest-first retention is by container
# creation time, so "keep the last one" means the most recent rollback target.
set -uo pipefail

APPLY="${TK_WORKER_PRUNE_APPLY:-0}"
KEEP="${TK_WORKER_PRUNE_KEEP:-1}"
NAME_PREFIX=tokenkey-gemini-web

usage() {
	cat <<'USAGE'
Usage:
  bash ops/gemini-web/prune-worker-pets.sh [--apply] [--keep N]

Default is dry-run: prints exactly what would be removed and exits 0.
--apply performs the removal. --keep N retains the newest N superseded
containers (and their images) as rollback evidence; default 1, minimum 0.
USAGE
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--apply)
		APPLY=1
		shift
		;;
	--keep)
		KEEP="${2:-}"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "prune-worker-pets: unknown arg: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

case "$KEEP" in
'' | *[!0-9]*)
	echo "prune-worker-pets: --keep must be a non-negative integer, got: ${KEEP}" >&2
	exit 1
	;;
esac

echo "=== gemini web worker pet retention (apply=${APPLY} keep=${KEEP}) ==="

# Resolve a container's image as the 12-hex prefix `docker images` prints. Docker
# 25's `docker ps` has no .ImageID field (it renders an empty template error), so
# ask inspect: an empty image id here would make every image look unreferenced.
image_id_of() {
	sudo docker inspect "$1" --format '{{.Image}}' 2>/dev/null |
		sed 's/^sha256://' | cut -c1-12
}

# The running Worker is identified by state, not by name: an operator may have
# renamed it, and a name-based guess is how a live container gets deleted.
RUNNING_NAME=""
RUNNING_IMAGE_ID=""
while IFS= read -r name; do
	[ -n "$name" ] || continue
	RUNNING_NAME="$name"
	RUNNING_IMAGE_ID="$(image_id_of "$name")"
done < <(sudo docker ps --filter "name=${NAME_PREFIX}" --filter status=running \
	--format '{{.Names}}' 2>/dev/null)

if [ -n "$RUNNING_NAME" ] && [ -z "$RUNNING_IMAGE_ID" ]; then
	# Without the running image id, every image below looks unreferenced.
	echo "cannot resolve the image of running ${RUNNING_NAME}; refusing to prune" >&2
	echo "worker_pet_prune: SKIPPED unresolved-running-image"
	exit 2
fi

if [ -z "$RUNNING_NAME" ]; then
	# Refuse rather than guess. With no running Worker we cannot tell a superseded
	# container from the one someone is about to restart.
	echo "no running ${NAME_PREFIX} container on this host; refusing to prune" >&2
	echo "worker_pet_prune: SKIPPED no-running-worker"
	exit 2
fi
echo "running:   ${RUNNING_NAME} image=${RUNNING_IMAGE_ID}"

# Superseded = every other container in the namespace, newest first.
SUPERSEDED=()
while IFS= read -r line; do
	[ -n "$line" ] || continue
	name="$(printf '%s' "$line" | cut -d'|' -f2)"
	SUPERSEDED+=("${line}|$(image_id_of "$name")")
done < <(sudo docker ps -a --filter "name=${NAME_PREFIX}" \
	--format '{{.CreatedAt}}|{{.Names}}|{{.State}}' 2>/dev/null |
	grep -v "|${RUNNING_NAME}|" | sort -r)

echo "superseded containers: ${#SUPERSEDED[@]}"

REMOVE_CONTAINERS=()
KEPT_IMAGE_IDS="${RUNNING_IMAGE_ID}"
index=0
for entry in ${SUPERSEDED+"${SUPERSEDED[@]}"}; do
	# entry is created|name|state|image_id (image id appended by the loop above)
	created="$(printf '%s' "$entry" | cut -d'|' -f1)"
	name="$(printf '%s' "$entry" | cut -d'|' -f2)"
	state="$(printf '%s' "$entry" | cut -d'|' -f3)"
	image_id="$(printf '%s' "$entry" | cut -d'|' -f4)"
	if [ "$state" = running ]; then
		# Two running Workers is a real anomaly worth a human, not a deletion.
		echo "  KEEP   ${name} (running, not the primary — investigate)"
		KEPT_IMAGE_IDS="${KEPT_IMAGE_IDS} ${image_id}"
		continue
	fi
	if [ "$index" -lt "$KEEP" ]; then
		echo "  KEEP   ${name} (rollback evidence, created ${created})"
		KEPT_IMAGE_IDS="${KEPT_IMAGE_IDS} ${image_id}"
		index=$((index + 1))
		continue
	fi
	echo "  REMOVE ${name} (${state}, created ${created})"
	REMOVE_CONTAINERS+=("$name")
	index=$((index + 1))
done

# Images are resolved after container retention so a kept container's image can
# never be selected, and matched by ID because several tags share one build.
REMOVE_IMAGES=()
while IFS="$(printf '\t')" read -r tag image_id; do
	[ -n "$image_id" ] || continue
	case " ${KEPT_IMAGE_IDS} " in
	*" ${image_id} "*)
		echo "  KEEP   image ${tag} (${image_id}, referenced by a retained container)"
		continue
		;;
	esac
	echo "  REMOVE image ${tag} (${image_id})"
	REMOVE_IMAGES+=("$image_id")
done < <(sudo docker images "${NAME_PREFIX}*" --format '{{.Repository}}:{{.Tag}}	{{.ID}}' 2>/dev/null)

if [ "${#REMOVE_CONTAINERS[@]}" -eq 0 ] && [ "${#REMOVE_IMAGES[@]}" -eq 0 ]; then
	echo "worker_pet_prune: OK nothing-to-remove containers=0 images=0"
	exit 0
fi

if [ "$APPLY" != 1 ]; then
	echo "dry-run: would remove ${#REMOVE_CONTAINERS[@]} container(s) and ${#REMOVE_IMAGES[@]} image(s)"
	echo "worker_pet_prune: DRY-RUN containers=${#REMOVE_CONTAINERS[@]} images=${#REMOVE_IMAGES[@]}"
	exit 0
fi

echo "--- applying ---"
failures=0
for name in ${REMOVE_CONTAINERS+"${REMOVE_CONTAINERS[@]}"}; do
	if sudo docker rm "$name" >/dev/null 2>&1; then
		echo "removed container ${name}"
	else
		echo "failed to remove container ${name}" >&2
		failures=$((failures + 1))
	fi
done
for image_id in ${REMOVE_IMAGES+"${REMOVE_IMAGES[@]}"}; do
	# No -f: an image still referenced by something we did not enumerate must
	# survive, and the refusal is the signal.
	if sudo docker rmi "$image_id" >/dev/null 2>&1; then
		echo "removed image ${image_id}"
	else
		echo "kept image ${image_id} (still referenced)" >&2
	fi
done

echo "--- final state ---"
sudo docker ps -a --filter "name=${NAME_PREFIX}" --format '{{.Names}}	{{.State}}	{{.Image}}' || true
sudo docker images "${NAME_PREFIX}*" --format '{{.Repository}}:{{.Tag}}	{{.ID}}' || true

# The running Worker must still be running and unchanged.
STILL="$(sudo docker inspect "$RUNNING_NAME" --format '{{.State.Running}}' 2>/dev/null || echo false)"
if [ "$STILL" != true ]; then
	echo "prune disturbed the running worker ${RUNNING_NAME}" >&2
	echo "worker_pet_prune: FAILED running-worker-disturbed"
	exit 1
fi

if [ "$failures" -ne 0 ]; then
	echo "worker_pet_prune: FAILED container-removals=${failures}"
	exit 1
fi

echo "worker_pet_prune: OK containers=${#REMOVE_CONTAINERS[@]} images=${#REMOVE_IMAGES[@]}"
