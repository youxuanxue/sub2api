#!/usr/bin/env bash
# Install the canonical bounded Docker log policy on a live Stage0 prod host and
# recreate only Caddy so its existing unbounded json log is released. Postgres,
# Redis, and the active blue/green app container are not restarted.
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-container-log-policy}}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ ! "$INSTANCE_ID" =~ ^i-[a-zA-Z0-9]+$ ]]; then
  echo "sync-container-log-policy: prod EC2 instance id (i-*) is required" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/docker-compose.yml"
[[ -f "$COMPOSE_SRC" ]] || { echo "missing $COMPOSE_SRC" >&2; exit 1; }

COMPOSE_B64="$(base64 < "$COMPOSE_SRC" | tr -d '\n')"
mkdir -p "$OUTPUT_DIR"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n --arg compose "$COMPOSE_B64" '{
  commands: [
    "set -euo pipefail",
    "ROOT=/var/lib/tokenkey",
    "COMPOSE=$ROOT/docker-compose.yml",
    "CANDIDATE=$ROOT/docker-compose.yml.log-policy-new",
    "BACKUP=$ROOT/docker-compose.yml.before-log-policy-$(date -u +%Y%m%dT%H%M%SZ)",
    "rollback() { rc=$?; trap - ERR; echo rollback: restoring previous compose and Caddy >&2; if [ -f \"$BACKUP\" ]; then sudo cp -a \"$BACKUP\" \"$COMPOSE\"; sudo docker compose --project-name tokenkey --env-file \"$ROOT/.env\" -f \"$COMPOSE\" up -d --no-deps --force-recreate caddy || true; fi; exit $rc; }",
    "trap rollback ERR",
    "echo === before ===",
    "sudo docker inspect tokenkey-caddy --format '\''log={{json .HostConfig.LogConfig}} path={{.LogPath}}'\''",
    "OLD_LOG=$(sudo docker inspect tokenkey-caddy --format '\''{{.LogPath}}'\'')",
    "OLD_BYTES=$(sudo stat -c %s \"$OLD_LOG\" 2>/dev/null || echo 0)",
    "df -P /",
    ("echo " + $compose + " | base64 -d | sudo tee \"$CANDIDATE\" >/dev/null"),
    "sudo docker compose --project-name tokenkey --env-file \"$ROOT/.env\" -f \"$CANDIDATE\" config --quiet",
    "sudo cp -a \"$COMPOSE\" \"$BACKUP\"",
    "sudo mv \"$CANDIDATE\" \"$COMPOSE\"",
    "sudo docker compose --project-name tokenkey --env-file \"$ROOT/.env\" -f \"$COMPOSE\" up -d --no-deps --force-recreate caddy",
    "for i in $(seq 1 30); do status=$(sudo docker inspect tokenkey-caddy --format '\''{{.State.Status}}'\'' 2>/dev/null || true); [ \"$status\" = running ] && break; sleep 1; done",
    "[ \"$(sudo docker inspect tokenkey-caddy --format '\''{{.State.Status}}'\'')\" = running ]",
    "[ \"$(sudo docker inspect tokenkey-caddy --format '\''{{.HostConfig.LogConfig.Type}}'\'')\" = json-file ]",
    "MAX_SIZE=$(sudo docker inspect tokenkey-caddy --format '\''{{index .HostConfig.LogConfig.Config \"max-size\"}}'\''); [ \"$MAX_SIZE\" = 100m ]",
    "MAX_FILE=$(sudo docker inspect tokenkey-caddy --format '\''{{index .HostConfig.LogConfig.Config \"max-file\"}}'\''); [ \"$MAX_FILE\" = 5 ]",
    "API_DOMAIN=$(sed -n '\''s/^API_DOMAIN=//p'\'' \"$ROOT/.env\" | head -1); API_DOMAIN=${API_DOMAIN:-api.tokenkey.dev}",
    "healthy=0; for i in $(seq 1 12); do if curl -fsS --max-time 10 \"https://${API_DOMAIN}/health\" >/dev/null; then healthy=1; break; fi; sleep 5; done; [ \"$healthy\" = 1 ]",
    "trap - ERR",
    "echo === after ===",
    "sudo docker inspect tokenkey-caddy --format '\''log={{json .HostConfig.LogConfig}} path={{.LogPath}} status={{.State.Status}}'\''",
    "NEW_LOG=$(sudo docker inspect tokenkey-caddy --format '\''{{.LogPath}}'\'')",
    "NEW_BYTES=$(sudo stat -c %s \"$NEW_LOG\" 2>/dev/null || echo 0)",
    "printf '\''old_log_bytes=%s new_log_bytes=%s backup=%s\\n'\'' \"$OLD_BYTES\" \"$NEW_BYTES\" \"$BACKUP\"",
    "df -P /",
    "echo sync-container-log-policy: OK"
  ],
  executionTimeout: ["300"]
}' > "$params_file"

if [ "${STAGE0_RENDER_ONLY:-0}" = "1" ]; then
  echo "sync-container-log-policy: render-only params=$params_file"
  exit 0
fi

cmd_id="$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "$COMMENT" \
  --timeout-seconds "$TIMEOUT_SECONDS" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"
echo "ssm command-id=${cmd_id}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "command_id=${cmd_id}" >> "$GITHUB_OUTPUT"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" --instance-id "$INSTANCE_ID" --query Status --output text 2>/dev/null || echo InProgress)"
  case "$status" in Success|Failed|TimedOut|Cancelled) break ;; esac
  if [ "$(date +%s)" -ge "$deadline" ]; then status="TimedOut"; break; fi
  sleep 5
done

aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" --instance-id "$INSTANCE_ID" --query StandardOutputContent --output text > "$stdout_file"
aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" --instance-id "$INSTANCE_ID" --query StandardErrorContent --output text > "$stderr_file"
tail -c 8192 "$stdout_file"
tail -c 8192 "$stderr_file" >&2

if [ "$status" != Success ]; then
  echo "sync-container-log-policy: SSM status=$status" >&2
  exit 1
fi
