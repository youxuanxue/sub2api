#!/usr/bin/env bash
# First install only. Never format a disk containing a filesystem or partitions.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run as root' >&2; exit 1; }
device=${1:?Usage: bootstrap-host.sh /dev/verified-empty-data-disk}
expected_bytes=${2:?Expected data disk size in bytes is required}
[[ -b "$device" ]] || { echo 'Not a block device' >&2; exit 1; }
[[ $(lsblk -dnro TYPE "$device") == disk ]] || exit 1
[[ $(blockdev --getsize64 "$device") == "$expected_bytes" ]] || {
  echo 'Data disk size mismatch' >&2; exit 1;
}
if mountpoint -q /var/lib/tokenkey; then
  [[ $(readlink -f "$(findmnt -nro SOURCE /var/lib/tokenkey)") == "$(readlink -f "$device")" ]] || exit 1
else
  [[ $(lsblk -nrpo NAME "$device" | wc -l) == 1 ]] || {
    echo 'Refusing disk with partitions' >&2; exit 1;
  }
  [[ -z $(lsblk -nrpo MOUNTPOINTS "$device" | tr -d '[:space:]') ]] || {
    echo 'Refusing mounted disk' >&2; exit 1;
  }
  [[ -z $(wipefs --noheadings --output TYPE "$device") ]] || {
    echo 'Refusing disk with existing signatures; mount it explicitly' >&2; exit 1;
  }
  install -d -m 0755 /var/lib/tokenkey
  [[ -z $(find /var/lib/tokenkey -mindepth 1 -maxdepth 1 -print -quit) ]] || exit 1
  mkfs.ext4 -L tokenkey-data "$device"
  uuid=$(blkid -s UUID -o value "$device")
  printf 'UUID=%s /var/lib/tokenkey ext4 defaults 0 2\n' "$uuid" >> /etc/fstab
  mount /var/lib/tokenkey
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y docker.io docker-compose-v2 ca-certificates
systemctl enable --now docker
for unit in apt-daily.timer apt-daily-upgrade.timer; do
  systemctl disable --now "$unit"
done
install -d -m 0700 /opt/tokenkey
install -d -m 0755 -o 1000 -g 1000 /var/lib/tokenkey/app
install -d -m 0755 /var/lib/tokenkey/{postgres,redis,caddy/data,caddy/config,pgdump}
echo 'Host bootstrap complete; application not started'
