#!/usr/bin/env bash
# Read-only live-host collection for live_host_state_verdict.py (assert-live-host parity).
set -u

ENV_FILTER="${ENV_FILTER:?ENV_FILTER is required}"
app_container="${APP_CONTAINER:-auto}"
if [ "$app_container" = auto ]; then
  if [ -r /var/lib/tokenkey/active-color ]; then
    color="$(sed -n '1p' /var/lib/tokenkey/active-color 2>/dev/null | tr -d '[:space:]')"
    case "$color" in
      blue|green) app_container="tokenkey-$color" ;;
      *) app_container=tokenkey ;;
    esac
  else
    app_container=tokenkey
  fi
fi
printf 'APPCONTAINER {"name":"%s"}\n' "$app_container"
img="$(sudo docker inspect "$app_container" --format '{{.Config.Image}}' 2>/dev/null || true)"
printf 'RUNIMAGE {"image":"%s"}\n' "$img"
sudo docker exec "$app_container" printenv 2>/dev/null \
  | grep -E "$ENV_FILTER" \
  | while IFS='=' read -r k v; do
      [ -n "${k:-}" ] || continue
      printf 'ENV {"key":"%s","value":"%s"}\n' "$k" "$v"
    done