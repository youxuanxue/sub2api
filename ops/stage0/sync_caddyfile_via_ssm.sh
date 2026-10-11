#!/usr/bin/env bash
#
# Stage0 Caddyfile hot-sync primitive.
#
# Why this exists:
#   deploy_via_ssm.sh INTENTIONALLY does NOT refresh /var/lib/tokenkey/caddy/
#   Caddyfile (see its header). The Caddyfile is rendered once at instance
#   launch by CFN UserData (SSM Parameter → base64 -d | gunzip → envsubst). So a
#   Caddyfile directive change in this repo (e.g. lb_try_duration 30s → 120s)
#   lands on a RUNNING host only via instance replacement OR this script.
#
# What this script does (mirrors the CFN UserData render EXACTLY):
#   1. base64 the canonical repo Caddyfile (deploy/aws/stage0/Caddyfile for
#      prod, Caddyfile.edge for edge) — the same file build-cfn.sh embeds into
#      the CFN SSM Parameter, kept bit-identical by `build-cfn.sh --check`
#      (preflight). Shipping the repo file (not re-reading the SSM Parameter)
#      means the sync does not depend on a CFN stack update having already
#      propagated the new blob to Parameter Store.
#   2. On the host: write it to caddy/Caddyfile.template, derive the render vars
#      (same set as boot UserData), then `envsubst` — matching the UserData
#      "envsubst < caddy/Caddyfile.template > caddy/Caddyfile" line:
#        - API_DOMAIN / ACME_EMAIL: sourced from /var/lib/tokenkey/.env (the
#          boot UserData persists both there).
#        - MAIN_GATEWAY_ALLOWED_CIDR (edge only): NOT in .env — boot UserData
#          holds it only transiently. Recovered from the live Caddyfile's
#          `remote_ip` line so the relay allowlist is preserved verbatim, unless
#          MAIN_GATEWAY_ALLOWED_CIDR is set on this invocation (console-adopted
#          hosts may currently have 0.0.0.0/0). For prod the template has no
#          such token, so the var stays empty/no-op.
#      For prod hosts already migrated to blue/green, rewrite the rendered
#      canonical upstream from tokenkey:8080 to tokenkey-${active}:8080 so
#      (ANY host carrying /var/lib/tokenkey/active-color, prod or edge: edges
#      run blue/green too, and gating this on kind=prod pointed a blue/green
#      edge at a tokenkey:8080 container that does not exist there)
#      directive hot-sync never disables the active color.
#   3. Validate the rendered config in a throwaway caddy:2-alpine container.
#   4. Apply IN PLACE (`cat new > Caddyfile`, NOT mv): the compose mount binds
#      the single FILE, so replacing the inode via mv would leave the running
#      tokenkey-caddy container reading the OLD inode. cat-truncate keeps the
#      inode the container already has mapped.
#   5. `docker exec tokenkey-caddy caddy reload` — hot reload, zero connection
#      drop. Verify the new directive is present and Caddy is still serving.
#   6. Rollback (restore backup + reload) on any ERR.
#
# Reboot durability is OUT OF SCOPE for this script: the SSM Parameter that
# UserData reads at the NEXT launch is CFN-owned. build-cfn.sh has already
# refreshed the embedded blob in the template; a normal CFN stack update (or
# re-provision) propagates it to Parameter Store. This script handles the LIVE
# host NOW; the stack update handles the next reboot. Running only this script
# leaves the host live-correct but the SSM Parameter stale until that update —
# acceptable because instance replacement is rare and re-runs UserData against
# whatever the Parameter holds at that time.
#
# Usage:
#   ops/stage0/sync_caddyfile_via_ssm.sh <prod|edge> <instance_id> [comment]
#   EDGE_ID=<edge> ops/stage0/sync_caddyfile_via_ssm.sh edge <mi-id> [comment]
#
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (optional)
#   GLOBAL_SITE_PHASE                 prod-only: candidate, live, or disabled;
#                                     omit to preserve the host's current value
#   GLOBAL_SITE_DOMAIN                required with candidate/live; omit with
#                                     disabled, which clears the persisted value
#   API_ALIAS_DOMAIN                  prod-only optional; set to persist/clear the
#                                     second machine host (e.g. api.callmodel.io)
#   EDGE_ID                           Lightsail Hybrid edge id; when set and
#                                     instance_id is mi-*, targets by tag like
#                                     deploy_via_ssm.sh.
#   MAIN_GATEWAY_ALLOWED_CIDR         edge-only optional override for Caddy
#                                     remote_ip (e.g. 34.194.234.88/32). When
#                                     unset, the live Caddyfile value is reused.
#   ACME_EMAIL                        optional override when host .env has an
#                                     empty ACME_EMAIL= (console-adopted edges).
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default 240)
#   STAGE0_SSM_OUTPUT_DIR             where to drop ssm-params/stdout/stderr

set -euo pipefail

KIND="${1:-}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-sync-caddyfile}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-240}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"

# Shared SSM "resolve managed-instance after tag-targeted send" helper.
# shellcheck source=ssm_resolve_invocation_mi.inc.sh
source "${HERE}/ssm_resolve_invocation_mi.inc.sh"

case "${KIND}" in
  prod) CADDY_SRC="${REPO_ROOT}/deploy/aws/stage0/Caddyfile" ;;
  edge) CADDY_SRC="${REPO_ROOT}/deploy/aws/stage0/Caddyfile.edge" ;;
  *)
    echo "sync_caddyfile_via_ssm: first arg must be 'prod' or 'edge' (got '${KIND}')" >&2
    exit 1
    ;;
esac

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "sync_caddyfile_via_ssm: instance id is required" >&2
  exit 1
fi
if [[ ! -f "${CADDY_SRC}" ]]; then
  echo "sync_caddyfile_via_ssm: missing ${CADDY_SRC}" >&2
  exit 1
fi

APPLY_GLOBAL_PROFILE=false
TARGET_GLOBAL_SITE_PHASE=""
TARGET_GLOBAL_SITE_DOMAIN=""
APPLY_API_ALIAS=false
TARGET_API_ALIAS_DOMAIN=""
global_phase_is_set="${GLOBAL_SITE_PHASE+x}"
global_domain_is_set="${GLOBAL_SITE_DOMAIN+x}"
api_alias_is_set="${API_ALIAS_DOMAIN+x}"

if [[ "${KIND}" == edge && ( -n "${global_phase_is_set}" || -n "${global_domain_is_set}" || -n "${api_alias_is_set}" ) ]]; then
  echo "sync_caddyfile_via_ssm: GLOBAL_SITE_*/API_ALIAS are prod-only" >&2
  exit 1
fi
TARGET_MAIN_GATEWAY_ALLOWED_CIDR=""
if [[ -n "${MAIN_GATEWAY_ALLOWED_CIDR+x}" ]]; then
  if [[ "${KIND}" != edge ]]; then
    echo "sync_caddyfile_via_ssm: MAIN_GATEWAY_ALLOWED_CIDR is edge-only" >&2
    exit 1
  fi
  TARGET_MAIN_GATEWAY_ALLOWED_CIDR="${MAIN_GATEWAY_ALLOWED_CIDR}"
  if [[ -z "${TARGET_MAIN_GATEWAY_ALLOWED_CIDR}" || ! "${TARGET_MAIN_GATEWAY_ALLOWED_CIDR}" =~ ^[0-9A-Fa-f.:/[:space:]]+$ ]]; then
    echo "sync_caddyfile_via_ssm: MAIN_GATEWAY_ALLOWED_CIDR must be a non-empty CIDR list" >&2
    exit 1
  fi
fi
TARGET_ACME_EMAIL=""
if [[ -n "${ACME_EMAIL+x}" ]]; then
  TARGET_ACME_EMAIL="${ACME_EMAIL}"
  if [[ -z "${TARGET_ACME_EMAIL}" || ! "${TARGET_ACME_EMAIL}" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]]; then
    echo "sync_caddyfile_via_ssm: ACME_EMAIL must be a non-empty email address" >&2
    exit 1
  fi
fi
if [[ "${KIND}" == prod ]]; then
  if [[ -z "${global_phase_is_set}" && -n "${global_domain_is_set}" ]]; then
    echo "sync_caddyfile_via_ssm: GLOBAL_SITE_DOMAIN requires GLOBAL_SITE_PHASE" >&2
    exit 1
  fi
  if [[ -n "${global_phase_is_set}" ]]; then
    APPLY_GLOBAL_PROFILE=true
    TARGET_GLOBAL_SITE_PHASE="${GLOBAL_SITE_PHASE}"
    case "${TARGET_GLOBAL_SITE_PHASE}" in
      disabled)
        if [[ -n "${GLOBAL_SITE_DOMAIN:-}" ]]; then
          echo "sync_caddyfile_via_ssm: disabled phase must not include GLOBAL_SITE_DOMAIN" >&2
          exit 1
        fi
        ;;
      candidate|live)
        TARGET_GLOBAL_SITE_DOMAIN="${GLOBAL_SITE_DOMAIN:-}"
        if [[ -z "${global_domain_is_set}" || -z "${TARGET_GLOBAL_SITE_DOMAIN}" ]]; then
          echo "sync_caddyfile_via_ssm: GLOBAL_SITE_DOMAIN is required for ${TARGET_GLOBAL_SITE_PHASE}" >&2
          exit 1
        fi
        if [[ ! "${TARGET_GLOBAL_SITE_DOMAIN}" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$ ]]; then
          echo "sync_caddyfile_via_ssm: GLOBAL_SITE_DOMAIN must be a lowercase DNS hostname" >&2
          exit 1
        fi
        ;;
      *)
        echo "sync_caddyfile_via_ssm: GLOBAL_SITE_PHASE must be disabled, candidate, or live" >&2
        exit 1
        ;;
    esac
  fi
  if [[ -n "${api_alias_is_set}" ]]; then
    APPLY_API_ALIAS=true
    TARGET_API_ALIAS_DOMAIN="${API_ALIAS_DOMAIN}"
    if [[ -n "${TARGET_API_ALIAS_DOMAIN}" && ! "${TARGET_API_ALIAS_DOMAIN}" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$ ]]; then
      echo "sync_caddyfile_via_ssm: API_ALIAS_DOMAIN must be empty or a lowercase DNS hostname" >&2
      exit 1
    fi
  fi
fi

# base64 the canonical repo Caddyfile; tr -d '\n' keeps it a single token so it
# embeds cleanly in the SSM command array.
CADDY_B64="$(base64 < "${CADDY_SRC}" | tr -d '\n')"
RENDER_SCRIPT_B64=""
# Public /privacy /terms HTML is shipped as a gzipped tar via SSM chunks (same
# pattern as deploy_via_ssm_bluegreen.sh) into the existing Caddy data volume.
LEGAL_CHUNKS_JSON='[]'
if [[ "${KIND}" == prod ]]; then
  RENDER_SCRIPT_B64="$(base64 < "${REPO_ROOT}/deploy/aws/stage0/render-prod-caddyfile.sh" | tr -d '\n')"
  LEGAL_DIR="${REPO_ROOT}/deploy/aws/stage0/legal-page"
  if [[ -f "${LEGAL_DIR}/privacy.html" && -f "${LEGAL_DIR}/terms.html" ]]; then
    LEGAL_TAR_B64="$(
      COPYFILE_DISABLE=1 tar -C "${LEGAL_DIR}" -czf - privacy.html terms.html shared.css lang.js \
        | base64 | tr -d '\n'
    )"
    LEGAL_CHUNKS_JSON="$(
      printf '%s' "${LEGAL_TAR_B64}" \
        | fold -w 1000 \
        | jq -R -s 'split("\n") | map(select(length > 0))'
    )"
  fi
fi

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n \
  --arg b64 "${CADDY_B64}" \
  --arg render_b64 "${RENDER_SCRIPT_B64}" \
  --argjson legal_chunks "${LEGAL_CHUNKS_JSON}" \
  --arg kind "${KIND}" \
  --arg apply_global_profile "${APPLY_GLOBAL_PROFILE}" \
  --arg global_site_phase "${TARGET_GLOBAL_SITE_PHASE}" \
  --arg global_site_domain "${TARGET_GLOBAL_SITE_DOMAIN}" \
  --arg apply_api_alias "${APPLY_API_ALIAS}" \
  --arg api_alias_domain "${TARGET_API_ALIAS_DOMAIN}" \
  --arg main_gateway_cidr "${TARGET_MAIN_GATEWAY_ALLOWED_CIDR}" \
  --arg acme_email "${TARGET_ACME_EMAIL}" '{
  # Hetzner/Ubuntu Hybrid SSM runs AWS-RunShellScript under /bin/sh (dash),
  # which rejects `set -o pipefail`. Wrap the whole body in bash -c so edge
  # and Lightsail hosts share one script (bash is present on both).
  commands: [
    "bash -c " + (
      (
        [
          "set -euo pipefail",
          ("KIND=" + ($kind | @sh)),
          ("APPLY_GLOBAL_PROFILE=" + ($apply_global_profile | @sh)),
          ("TARGET_GLOBAL_SITE_PHASE=" + ($global_site_phase | @sh)),
          ("TARGET_GLOBAL_SITE_DOMAIN=" + ($global_site_domain | @sh)),
          ("APPLY_API_ALIAS=" + ($apply_api_alias | @sh)),
          ("TARGET_API_ALIAS_DOMAIN=" + ($api_alias_domain | @sh)),
          ("TARGET_MAIN_GATEWAY_ALLOWED_CIDR=" + ($main_gateway_cidr | @sh)),
          ("TARGET_ACME_EMAIL=" + ($acme_email | @sh)),
          "CADDY_DIR=/var/lib/tokenkey/caddy",
          "LIVE=$CADDY_DIR/Caddyfile",
          "ENV_FILE=/var/lib/tokenkey/.env",
          "TS=$(date +%Y%m%d-%H%M%S)",
          "BACKUP=$CADDY_DIR/Caddyfile.before-$TS",
          "ENV_BACKUP=$ENV_FILE.before-caddy-sync-$TS",
          "echo \"=== sync Caddyfile (kind=$KIND backup=$BACKUP) ===\"",
          "[ -f \"$LIVE\" ] || { echo \"::error::no live Caddyfile at $LIVE — is this a Stage0 host?\"; exit 1; }",
          "[ -f \"$ENV_FILE\" ] || { echo \"::error::no environment file at $ENV_FILE — is this a Stage0 host?\"; exit 1; }",
          "sudo cp -a \"$LIVE\" \"$BACKUP\"",
          "sudo cp -a \"$ENV_FILE\" \"$ENV_BACKUP\"",
          "rollback() { rc=$?; echo \"::warning::sync failed; restoring previous Caddyfile and environment\"; if [ -f \"$ENV_BACKUP\" ]; then sudo cp -a \"$ENV_BACKUP\" \"$ENV_FILE\"; fi; if [ -f \"$BACKUP\" ]; then sudo sh -c \"cat '\''$BACKUP'\'' > '\''$LIVE'\''\"; sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1 | tail -5 || true; fi; exit $rc; }",
          "trap rollback ERR",
          "echo \"=== derive render vars (same set as boot UserData) ===\"",
          "if [ \"$APPLY_GLOBAL_PROFILE\" = true ] || [ \"$APPLY_API_ALIAS\" = true ]; then",
          "  ENV_NEW=$ENV_FILE.new-$TS",
          "  sudo awk -v apply_global=\"$APPLY_GLOBAL_PROFILE\" -v phase=\"$TARGET_GLOBAL_SITE_PHASE\" -v domain=\"$TARGET_GLOBAL_SITE_DOMAIN\" -v apply_alias=\"$APPLY_API_ALIAS\" -v alias=\"$TARGET_API_ALIAS_DOMAIN\" '\''BEGIN { phase_seen=0; domain_seen=0; alias_seen=0 } /^GLOBAL_SITE_PHASE=/ { if (apply_global == \"true\") { print \"GLOBAL_SITE_PHASE=\" phase; phase_seen=1; next } } /^GLOBAL_SITE_DOMAIN=/ { if (apply_global == \"true\") { print \"GLOBAL_SITE_DOMAIN=\" domain; domain_seen=1; next } } /^API_ALIAS_DOMAIN=/ { if (apply_alias == \"true\") { print \"API_ALIAS_DOMAIN=\" alias; alias_seen=1; next } } { print } END { if (apply_global == \"true\") { if (!phase_seen) print \"GLOBAL_SITE_PHASE=\" phase; if (!domain_seen) print \"GLOBAL_SITE_DOMAIN=\" domain } if (apply_alias == \"true\" && !alias_seen) print \"API_ALIAS_DOMAIN=\" alias }'\'' \"$ENV_FILE\" | sudo tee \"$ENV_NEW\" >/dev/null",
          "  sudo chown --reference=\"$ENV_FILE\" \"$ENV_NEW\"",
          "  sudo chmod --reference=\"$ENV_FILE\" \"$ENV_NEW\"",
          "  sudo mv \"$ENV_NEW\" \"$ENV_FILE\"",
          "  if [ \"$APPLY_GLOBAL_PROFILE\" = true ]; then echo \"global homepage phase persisted: $TARGET_GLOBAL_SITE_PHASE\"; fi",
          "  if [ \"$APPLY_API_ALIAS\" = true ]; then echo \"api alias persisted: ${TARGET_API_ALIAS_DOMAIN:-<empty>}\"; fi",
          "fi",
          "# API_DOMAIN / ACME_EMAIL are persisted in the host .env at boot.",
          "set -a; . /var/lib/tokenkey/.env; set +a",
          "[ -n \"${API_DOMAIN:-}\" ] || { echo \"::error::API_DOMAIN empty in /var/lib/tokenkey/.env\"; exit 1; }",
          "if [ -n \"$TARGET_ACME_EMAIL\" ]; then ACME_EMAIL=\"$TARGET_ACME_EMAIL\"; fi",
          "if [ -z \"${ACME_EMAIL:-}\" ]; then",
          "  ACME_EMAIL=\"$(sed -n '\''s/^[[:space:]]*email[[:space:]][[:space:]]*\\(.*\\)$/\\1/p'\'' \"$LIVE\" | head -1)\"",
          "fi",
          "[ -n \"${ACME_EMAIL:-}\" ] || { echo \"::error::ACME_EMAIL empty in /var/lib/tokenkey/.env and live Caddyfile\"; exit 1; }",
          "# MAIN_GATEWAY_ALLOWED_CIDR (edge only) is NOT in .env — it lives only in",
          "# boot UserData. Recover the live value from the remote_ip line in the",
          "# current rendered Caddyfile so the allowlist survives the re-render verbatim,",
          "# unless this invocation supplied TARGET_MAIN_GATEWAY_ALLOWED_CIDR.",
          "if [ -n \"$TARGET_MAIN_GATEWAY_ALLOWED_CIDR\" ]; then",
          "  MAIN_GATEWAY_ALLOWED_CIDR=\"$TARGET_MAIN_GATEWAY_ALLOWED_CIDR\"",
          "else",
          "  MAIN_GATEWAY_ALLOWED_CIDR=\"$(sed -n '\''s/^[[:space:]]*remote_ip[[:space:]][[:space:]]*\\(.*\\)$/\\1/p'\'' \"$LIVE\" | head -1)\"",
          "  MAIN_GATEWAY_ALLOWED_CIDR=\"$(printf '\''%s'\'' \"$MAIN_GATEWAY_ALLOWED_CIDR\" | sed -e '\''s/^[[:space:]]*//'\'' -e '\''s/[[:space:]]*$//'\'')\"",
          "  # The live file can itself be the product of an earlier bad write, so",
          "  # validate what came back rather than propagating it. On 2026-10-11 two",
          "  # edges were rendered with a blank remote_ip this way, which opens the",
          "  # relay allowlist. Fail closed and make the caller pass the CIDR instead.",
          "  if [ \"$KIND\" = edge ]; then",
          "    case \"$MAIN_GATEWAY_ALLOWED_CIDR\" in",
          "      \"\") echo \"::error::live edge Caddyfile $LIVE has no readable remote_ip allowlist; pass MAIN_GATEWAY_ALLOWED_CIDR explicitly\"; exit 1 ;;",
          "      *[!0-9A-Fa-f.:/\\ ]*) echo \"::error::recovered remote_ip allowlist is not a CIDR list: $MAIN_GATEWAY_ALLOWED_CIDR\"; exit 1 ;;",
          "    esac",
          "    case \"$MAIN_GATEWAY_ALLOWED_CIDR\" in",
          "      */*) : ;;",
          "      *) echo \"::error::recovered remote_ip allowlist has no prefix length: $MAIN_GATEWAY_ALLOWED_CIDR\"; exit 1 ;;",
          "    esac",
          "  fi",
          "fi",
          "if [ \"$KIND\" = edge ] && [ -z \"$MAIN_GATEWAY_ALLOWED_CIDR\" ]; then echo \"::error::could not read remote_ip allowlist from live edge Caddyfile $LIVE\"; exit 1; fi",
          "echo \"render context loaded for kind=$KIND\"",
          ("printf '\''%s'\'' \"" + $b64 + "\" | base64 -d | sudo tee \"$CADDY_DIR/Caddyfile.template\" >/dev/null"),
          "if [ \"$KIND\" = prod ]; then",
          ("  printf '\''%s'\'' \"" + $render_b64 + "\" | base64 -d > /tmp/render-prod-caddyfile.sh"),
          "  chmod +x /tmp/render-prod-caddyfile.sh",
          "  site_domain=\"${SITE_DOMAIN:-}\"",
          "  if [ -z \"$site_domain\" ] && case \"$API_DOMAIN\" in api.*) true;; *) false;; esac; then site_domain=\"${API_DOMAIN#api.}\"; fi",
          "  if [ \"$site_domain\" = \"$API_DOMAIN\" ]; then site_domain=; fi",
          "  export SITE_DOMAIN=\"$site_domain\"",
          "  bash /tmp/render-prod-caddyfile.sh \"$CADDY_DIR/Caddyfile.template\" \"$CADDY_DIR/Caddyfile.new\"",
          "else",
          "  envsubst '\''$API_DOMAIN $ACME_EMAIL $MAIN_GATEWAY_ALLOWED_CIDR'\'' < \"$CADDY_DIR/Caddyfile.template\" > \"$CADDY_DIR/Caddyfile.new\"",
          "fi",
          "if [ -r /var/lib/tokenkey/active-color ]; then ACTIVE_COLOR=\"$(sed -n '\''1p'\'' /var/lib/tokenkey/active-color | tr -d '\''[:space:]'\'')\"; case \"$ACTIVE_COLOR\" in blue|green) UPSTREAM=\"tokenkey-$ACTIVE_COLOR:8080\"; sudo awk -v upstream=\"$UPSTREAM\" '\''/^[[:space:]]*reverse_proxy[[:space:]]+/ && $0 ~ /\\{[[:space:]]*$/ { count += 1; if (count == 1) { match($0, /[^[:space:]]/); indent = RSTART > 1 ? substr($0, 1, RSTART - 1) : \"\"; print indent \"reverse_proxy \" upstream \" {\" } else { print }; next } { print } END { if (count != 1) exit 7 }'\'' \"$CADDY_DIR/Caddyfile.new\" | sudo tee \"$CADDY_DIR/Caddyfile.rewritten\" >/dev/null; sudo mv \"$CADDY_DIR/Caddyfile.rewritten\" \"$CADDY_DIR/Caddyfile.new\"; echo \"blue/green active upstream preserved ($KIND): $UPSTREAM\" ;; *) echo \"::error::invalid active-color for blue/green Caddy sync: ${ACTIVE_COLOR:-<empty>}\"; exit 1 ;; esac; fi",
          "echo === validate rendered config in throwaway caddy container ===",
          "sudo docker run --rm -v \"$CADDY_DIR/Caddyfile.new\":/tmp/Caddyfile:ro caddy:2-alpine caddy validate --config /tmp/Caddyfile --adapter caddyfile"
        ]
        + (
          if ($legal_chunks | length) > 0 then
            [
              "echo === write legal pages into existing Caddy data volume ===",
              "sudo mkdir -p \"$CADDY_DIR/data/legal\"",
              "rm -f /tmp/tokenkey-legal.tgz.b64"
            ]
            + ($legal_chunks | map("printf %s " + (. | @sh) + " >> /tmp/tokenkey-legal.tgz.b64"))
            + [
              "base64 -d /tmp/tokenkey-legal.tgz.b64 | sudo tar -xzf - -C \"$CADDY_DIR/data/legal\"",
              "rm -f /tmp/tokenkey-legal.tgz.b64",
              "echo \"legal files:\" && ls -1 \"$CADDY_DIR/data/legal\""
            ]
          else []
          end
        )
        + [
          "echo \"=== apply IN PLACE (cat-truncate keeps inode the bind-mount maps) ===\"",
          "sudo sh -c \"cat '\''$CADDY_DIR/Caddyfile.new'\'' > '\''$LIVE'\''\"",
          "sudo rm -f \"$CADDY_DIR/Caddyfile.new\"",
          "echo === hot reload caddy ===",
          "sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile",
          "echo === verify ===",
          "sudo docker inspect tokenkey-caddy --format '\''caddy state={{.State.Status}} running={{.State.Running}}'\''",
          "grep -nE '\''lb_try_duration|handle /privacy|root \\* /data/legal'\'' \"$LIVE\" || true",
          "trap - ERR",
          "echo === sync done ==="
        ]
      ) | join("\n") | @sh
    )
  ]
}' > "${params_file}"

eff_instance_id="${INSTANCE_ID}"
if [[ "${INSTANCE_ID}" == mi-* && -n "${EDGE_ID:-}" ]]; then
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} kind=${KIND}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
  eff_instance_id="$(ssm_resolve_invocation_mi "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" "${cmd_id}")"
  if [[ "${eff_instance_id}" != "${INSTANCE_ID}" ]]; then
    echo "::warning::SSM send resolved instance ${eff_instance_id}; caller passed ${INSTANCE_ID}"
  fi
else
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --instance-ids "${INSTANCE_ID}" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} kind=${KIND}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
fi

echo "ssm command-id=${cmd_id}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [[ "${status}" != "Success" ]]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
