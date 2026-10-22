#!/usr/bin/env bash
# Read-only: capture non-secret recreate recipe + formal LAYOUT.json for staging.
set -euo pipefail

emit() {
  python3 -c 'import json,sys; print(json.dumps({"k":sys.argv[1],"v":sys.argv[2]}, ensure_ascii=False))' "$1" "$2"
}

for name in tk-cutover-prod-app tk-cutover-prod-caddy tk-cutover-prod-redis tk-archive-standby; do
  emit "$name.image" "$(sudo docker inspect -f '{{.Config.Image}}' "$name" 2>/dev/null || true)"
  emit "$name.image_id" "$(sudo docker inspect -f '{{.Image}}' "$name" 2>/dev/null || true)"
  emit "$name.cmd" "$(sudo docker inspect -f '{{json .Config.Cmd}}' "$name" 2>/dev/null || true)"
  emit "$name.entrypoint" "$(sudo docker inspect -f '{{json .Config.Entrypoint}}' "$name" 2>/dev/null || true)"
  emit "$name.ports" "$(sudo docker inspect -f '{{json .HostConfig.PortBindings}}' "$name" 2>/dev/null || true)"
  emit "$name.network_mode" "$(sudo docker inspect -f '{{.HostConfig.NetworkMode}}' "$name" 2>/dev/null || true)"
  # Env KEY names only
  emit "$name.env_keys" "$(sudo docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$name" 2>/dev/null | sed -n 's/=.*//p' | paste -sd, -)"
done

if [ -f /var/lib/tokenkey/formal/LAYOUT.json ]; then
  # whole file should be non-secret layout metadata
  emit "layout_json" "$(sudo python3 -c 'import json; print(json.dumps(json.load(open("/var/lib/tokenkey/formal/LAYOUT.json"))))' 2>/dev/null || true)"
fi

emit "docker_dir_tokenkey_bytes" "$(sudo du -sb /var/lib/tokenkey/docker 2>/dev/null | awk '{print $1}' || echo 0)"
emit "docker_dir_rehearsal_bytes" "$(sudo du -sb /srv/tokenkey-rehearsal/docker 2>/dev/null | awk '{print $1}' || echo 0)"
emit "same_device" "$(sudo stat -c '%d' /var/lib/tokenkey /srv/tokenkey-rehearsal | paste -sd, -)"
emit "summary" "recipe_capture_done"
