#!/bin/sh
# Disposable Docker-in-Docker only: no network, only synthetic Redis fixture.
# Image needs docker daemon/client, rsync, acl and attr; /redis-fixture.tar is
# a locally saved Redis image. /fixture-old and /fixture-new are fresh Docker
# volumes, never host directories. The caller removes the disposable container
# and those two owned volumes after collecting the result.
set -eu
[ -f /.dockerenv ] || { echo 'requires a disposable container' >&2; exit 1; }
mkdir -p /fixture-old /fixture-new /var/lib/tokenkey /srv/tokenkey-rehearsal /run/dind
mount --bind /fixture-old /var/lib/tokenkey
mount --bind /fixture-old /srv/tokenkey-rehearsal
mkdir -p /var/lib/tokenkey/app
printf 'synthetic qa\n' > /var/lib/tokenkey/app/ledger
export DOCKER_HOST=unix:///run/dind/docker.sock
start_daemon() {
  dockerd --host "$DOCKER_HOST" --data-root /srv/tokenkey-rehearsal/docker --exec-root /run/dind/exec --pidfile /run/dind/daemon.pid --storage-driver overlay2 --feature containerd-snapshotter=false --iptables=false --ip6tables=false --ip-forward=false --ip-masq=false --bridge=none > /run/dind/daemon.log 2>&1 &
  daemon_pid=$!
  n=0
  until docker info >/dev/null 2>&1; do
    n=$((n+1)); [ "$n" -lt 150 ] || { cat /run/dind/daemon.log; exit 1; }
    sleep .2
  done
}
start_daemon
docker load -i /redis-fixture.tar >/dev/null
docker volume create named-state >/dev/null
docker run -d --name active --network none --restart=no -v /var/lib/tokenkey/app:/qa -v named-state:/named redis:8-alpine sh -c 'echo named-proof > /named/proof; sleep 3600' >/dev/null
docker run -d --name client --network none --restart=no redis:8-alpine sh -c 'echo anonymous-proof > /data/proof; sleep 3600' >/dev/null
docker create --name standby --network none --restart=no redis:8-alpine >/dev/null
docker inspect active client standby --format '{{.Id}} {{.Name}} {{json .Mounts}}' > /run/dind/before
# Online pre-copy cannot be used as a startup image.
rsync -aHAXSx --numeric-ids /var/lib/tokenkey/ /fixture-new/
docker stop -t 10 active client >/dev/null
kill "$daemon_pid"; wait "$daemon_pid"
# Actual final offline copy with checksum, retaining anonymous-volume paths.
rsync -aHAXSx --numeric-ids --checksum --delete-delay /var/lib/tokenkey/ /fixture-new/
umount /srv/tokenkey-rehearsal
umount /var/lib/tokenkey
mount --bind /fixture-new /var/lib/tokenkey
mount --bind /fixture-new /srv/tokenkey-rehearsal
start_daemon
docker inspect active client standby --format '{{.Id}} {{.Name}} {{json .Mounts}}' > /run/dind/after
cmp /run/dind/before /run/dind/after
docker start active client >/dev/null
[ "$(docker exec active cat /named/proof)" = named-proof ]
[ "$(docker exec client cat /data/proof)" = anonymous-proof ]
[ "$(docker exec active cat /qa/ledger)" = 'synthetic qa' ]
[ "$(docker inspect standby --format '{{.State.Status}}')" = created ]
[ "$(docker info --format '{{.DockerRootDir}}')" = /srv/tokenkey-rehearsal/docker ]
docker stop -t 10 active client >/dev/null
kill "$daemon_pid"; wait "$daemon_pid"
printf '{"overlay2_daemon_restart":"passed","container_identity_and_volumes":"passed","standby_remained_stopped":true}\n'
