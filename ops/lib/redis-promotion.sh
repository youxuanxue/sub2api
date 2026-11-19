#!/usr/bin/env bash
# Callbacks execute redis-cli locally or over the private migration connection.
# No writes in the prerequisite check. Keep the file on the persistent /data mount.
tk_redis_persistence_check() {
  local client="$1" config
  config="$("$client" INFO server | tr -d '\r' | awk -F: '$1 == "config_file" {print $2}')" || return 1
  [ "$config" = /data/tokenkey-redis.conf ] || {
    echo 'FAIL Redis must use persistent /data/tokenkey-redis.conf; prepare the replica before the window' >&2
    return 1
  }
}

tk_redis_set_role() {
  local client="$1" host="$2" port="${3:-}" result
  # redis-cli returns exit 0 for Redis error replies unless --exit-on-error is
  # enabled; require the exact reply as well as successful transport.
  if [ "$host" = NO ]; then
    result="$("$client" REPLICAOF NO ONE)" || return 1
  else
    result="$("$client" REPLICAOF "$host" "$port")" || return 1
  fi
  [ "$result" = OK ] || { echo 'FAIL Redis role change refused' >&2; return 1; }
  tk_redis_rewrite "$client"
}

# Data-host prerequisite: CONFIG REWRITE must survive both restart and recreate.
tk_redis_startup_check() {
  local container="$1" startup mount
  startup="$(docker inspect -f '{{json .Config.Entrypoint}} {{json .Config.Cmd}}' "$container")" || return 1
  case "$startup" in *replicaof*|*slaveof*) echo 'FAIL startup flags override persistent Redis role' >&2; return 1 ;; esac
  mount="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}}:{{.RW}}{{end}}{{end}}' "$container")" || return 1
  case "$mount" in bind:true|volume:true) ;; *) echo 'FAIL Redis needs writable persistent /data mount' >&2; return 1 ;; esac
}

tk_redis_rewrite() {
  local result
  result="$("$1" CONFIG REWRITE)" || return 1
  [ "$result" = OK ] || { echo 'FAIL Redis role changed but CONFIG REWRITE failed; do not resume writers' >&2; return 1; }
}
