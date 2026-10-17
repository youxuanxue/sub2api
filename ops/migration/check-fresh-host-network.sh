#!/usr/bin/env bash
# Disposable no-volume Docker regression; no candidate business process starts.
set -euo pipefail
EXPECTED=${1:?exact held candidate hostname required}
case "$EXPECTED" in
 tokenkey-prod-tokyo-20261017|tokenkey-prod-aws-20261015|tokenkey-edge-uk1-aws-20261015|tokenkey-edge-uk2-aws-20261015) ;;
 *) exit 20 ;;
esac
[ "$(hostname)" = "$EXPECTED" ]
[ -f /var/lib/tokenkey/migration/DATA_SYNC_HOLD ]
[ "$(docker ps --format '{{.Names}}')" = tokenkey-postgres ]
before=$(docker inspect tokenkey-postgres --format '{{.Id}} {{json .Mounts}}')
net=tk-migration-regression-$$
container=$net-container
cleanup() {
 docker rm -f "$container" >/dev/null 2>&1 || :
 docker network rm "$net" >/dev/null 2>&1 || :
}
trap cleanup EXIT
id=$(docker network create --internal "$net")
docker create --name "$container" --network "$id" --entrypoint sh caddy:2-alpine -c 'sleep 120' >/dev/null
docker start "$container" >/dev/null
docker stop --time 1 "$container" >/dev/null
docker network disconnect -f "$net" "$container"
docker network rm "$net" >/dev/null
docker network create --internal "$net" >/dev/null
docker network connect "$net" "$container"
if docker start "$container" >/dev/null 2>&1; then echo 'FAIL: expected immutable network ID rejection'; exit 1; fi
docker rm "$container" >/dev/null
docker create --name "$container" --network "$net" --entrypoint sh caddy:2-alpine -c 'sleep 120' >/dev/null
docker start "$container" >/dev/null
docker stop --time 1 "$container" >/dev/null
docker network disconnect -f "$net" "$container"
docker network rm "$net" >/dev/null
docker network create --internal "$net" >/dev/null
docker network connect "$net" "$container"
docker start "$container" >/dev/null
[ "$(docker inspect "$container" --format '{{.State.Running}}')" = true ]
cleanup
trap - EXIT
[ "$before" = "$(docker inspect tokenkey-postgres --format '{{.Id}} {{json .Mounts}}')" ]
[ "$(docker ps --format '{{.Names}}')" = tokenkey-postgres ]
[ "$(docker network inspect tokenkey_tokenkey-network --format '{{.Internal}}')" = true ]
echo 'PASS: reproduced deleted network ID failure; named-network recreation starts; PG identity/mounts unchanged; candidate stays held'
