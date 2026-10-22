#!/usr/bin/env bash
# Read-only: find cutover compose / caddy paths on Tencent USW without printing secrets.
set -euo pipefail

emit() {
  python3 -c 'import json,sys; k,v=sys.argv[1],sys.argv[2]; print(json.dumps({"k":k,"v":v}, ensure_ascii=False))' "$1" "$2"
}

emit "search_roots" "/var/lib/tokenkey /srv/tokenkey-rehearsal /opt /home"
while IFS= read -r f; do
  emit "compose_candidate" "$f"
done < <(sudo find /var/lib/tokenkey /srv/tokenkey-rehearsal /opt -maxdepth 5 \
  \( -name 'docker-compose*.yml' -o -name 'compose*.yml' -o -name '*cutover*.yml' \) \
  2>/dev/null | head -40)

emit "caddyfile_candidates" "$(sudo find /var/lib/tokenkey /srv/tokenkey-rehearsal -maxdepth 5 -name 'Caddyfile' 2>/dev/null | paste -sd, -)"
emit "formal_dir_listing" "$(sudo ls -la /var/lib/tokenkey/formal 2>/dev/null | awk '{print $9}' | paste -sd, -)"
emit "tokenkey_top" "$(sudo ls -la /var/lib/tokenkey 2>/dev/null | awk '{print $9}' | paste -sd, -)"
emit "rehearsal_top" "$(sudo ls -la /srv/tokenkey-rehearsal 2>/dev/null | awk '{print $9}' | paste -sd, -)"

# Network inspect aliases (names only)
emit "network_tk_cutover" "$(sudo docker network inspect tk-cutover-prod --format '{{range .Containers}}{{.Name}}={{.IPv4Address}};{{end}}' 2>/dev/null || true)"

# App mounts (paths only)
emit "app_mounts" "$(sudo docker inspect tk-cutover-prod-app --format '{{range .Mounts}}{{.Source}}->{{.Destination}};{{end}}' 2>/dev/null || true)"
emit "caddy_mounts" "$(sudo docker inspect tk-cutover-prod-caddy --format '{{range .Mounts}}{{.Source}}->{{.Destination}};{{end}}' 2>/dev/null || true)"
emit "redis_mounts" "$(sudo docker inspect tk-cutover-prod-redis --format '{{range .Mounts}}{{.Source}}->{{.Destination}};{{end}}' 2>/dev/null || true)"
emit "pg_mounts" "$(sudo docker inspect tk-archive-standby --format '{{range .Mounts}}{{.Source}}->{{.Destination}};{{end}}' 2>/dev/null || true)"
emit "app_restart" "$(sudo docker inspect tk-cutover-prod-app --format '{{.HostConfig.RestartPolicy.Name}}' 2>/dev/null || true)"
emit "summary" "compose_find_done"
