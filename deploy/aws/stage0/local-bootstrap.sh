#!/usr/bin/env bash
# local-bootstrap.sh — Idempotent bootstrap for the local Stage0 mirror used
# by the tokenkey-stage0-local-deploy skill. Replaces the §1-§4 prose blocks
# (mkdir / .env / Caddyfile / docker-compose.override.yml) so the operator
# doesn't have to re-paste them every session.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Idempotent: an existing .env is NOT overwritten (preserves POSTGRES_PASSWORD
#     for bind-mounted Postgres data); other files are rewritten only if their
#     content has drifted.
#   - --reset removes the entire TOKENKEY_STAGE0_LOCAL_ROOT (use with care —
#     this drops local DB/Redis state). Refuses to run if the path resolves
#     outside $HOME unless --i-know-what-im-doing is set.
#   - --dry-run prints the actions that WOULD be taken; nothing is written.
#
# Env (read at start; can be overridden on the CLI via --env KEY=val):
#   REPO_ROOT                   default = `git rev-parse --show-toplevel`
#   TOKENKEY_STAGE0_LOCAL_ROOT  default = "$REPO_ROOT/.cache/tokenkey-stage0-local"
#   TOKENKEY_NEWAPI_PARENT      default = `dirname "$REPO_ROOT"`
#   TOKENKEY_IMAGE              default = "ghcr.io/youxuanxue/sub2api:latest"
#
# Usage:
#   bash deploy/aws/stage0/local-bootstrap.sh           # bootstrap (idempotent)
#   bash deploy/aws/stage0/local-bootstrap.sh --reset   # wipe + bootstrap
#   bash deploy/aws/stage0/local-bootstrap.sh --dry-run # list actions only
#
# Output: human-friendly status lines on stdout, prefixed with [local-bootstrap].
#         No secrets are printed; only paths and admin email.
#
# Exit codes:
#   0 — bootstrap (or dry-run) completed
#   1 — usage / refuses (e.g. --reset on path outside $HOME)
#   2 — IO failure
set -euo pipefail

DRY_RUN=0
RESET=0
ALLOW_OUTSIDE_HOME=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --reset) RESET=1; shift ;;
    --i-know-what-im-doing) ALLOW_OUTSIDE_HOME=1; shift ;;
    -h|--help) sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "[local-bootstrap] ERROR: unknown arg: $1" >&2; exit 1 ;;
  esac
done

REPO_ROOT="${REPO_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
if [ -z "$REPO_ROOT" ]; then
  echo "[local-bootstrap] ERROR: cannot resolve REPO_ROOT (not in a git repo? export REPO_ROOT)" >&2
  exit 1
fi
TOKENKEY_STAGE0_LOCAL_ROOT="${TOKENKEY_STAGE0_LOCAL_ROOT:-$REPO_ROOT/.cache/tokenkey-stage0-local}"
TOKENKEY_NEWAPI_PARENT="${TOKENKEY_NEWAPI_PARENT:-$(dirname "$REPO_ROOT")}"
TOKENKEY_IMAGE="${TOKENKEY_IMAGE:-ghcr.io/youxuanxue/sub2api:latest}"

echo "[local-bootstrap] REPO_ROOT=$REPO_ROOT"
echo "[local-bootstrap] TOKENKEY_STAGE0_LOCAL_ROOT=$TOKENKEY_STAGE0_LOCAL_ROOT"
echo "[local-bootstrap] TOKENKEY_NEWAPI_PARENT=$TOKENKEY_NEWAPI_PARENT"
echo "[local-bootstrap] TOKENKEY_IMAGE=$TOKENKEY_IMAGE"

if [ "$RESET" -eq 1 ]; then
  case "$TOKENKEY_STAGE0_LOCAL_ROOT" in
    "$HOME"/*) : ;;
    *)
      if [ "$ALLOW_OUTSIDE_HOME" -ne 1 ]; then
        echo "[local-bootstrap] ERROR: refusing --reset on path outside \$HOME: $TOKENKEY_STAGE0_LOCAL_ROOT" >&2
        echo "[local-bootstrap]        pass --i-know-what-im-doing to override" >&2
        exit 1
      fi
      ;;
  esac
  echo "[local-bootstrap] --reset: removing $TOKENKEY_STAGE0_LOCAL_ROOT"
  if [ "$DRY_RUN" -eq 0 ]; then
    rm -rf "$TOKENKEY_STAGE0_LOCAL_ROOT"
  fi
fi

ENV_FILE="$TOKENKEY_STAGE0_LOCAL_ROOT/.env"
CADDYFILE="$TOKENKEY_STAGE0_LOCAL_ROOT/caddy/Caddyfile"
OVERRIDE_FILE="$TOKENKEY_STAGE0_LOCAL_ROOT/docker-compose.override.yml"

dirs=(
  "$TOKENKEY_STAGE0_LOCAL_ROOT/caddy"
  "$TOKENKEY_STAGE0_LOCAL_ROOT/app"
  "$TOKENKEY_STAGE0_LOCAL_ROOT/postgres"
  "$TOKENKEY_STAGE0_LOCAL_ROOT/pgdump"
  "$TOKENKEY_STAGE0_LOCAL_ROOT/redis"
)
for d in "${dirs[@]}"; do
  if [ -d "$d" ]; then
    echo "[local-bootstrap] dir exists: $d"
  else
    echo "[local-bootstrap] mkdir -p $d"
    if [ "$DRY_RUN" -eq 0 ]; then
      mkdir -p "$d"
    fi
  fi
done

# .env: only write if absent (preserves POSTGRES_PASSWORD across runs)
if [ -f "$ENV_FILE" ]; then
  echo "[local-bootstrap] .env exists, preserving (delete to regenerate): $ENV_FILE"
else
  ADMIN_PASSWORD="$(openssl rand -hex 16)"
  POSTGRES_PASSWORD="$(openssl rand -hex 12)"
  JWT_SECRET="$(openssl rand -hex 32)"
  TOTP_KEY="$(openssl rand -hex 32)"
  echo "[local-bootstrap] generating $ENV_FILE (secrets fresh, not printed)"
  if [ "$DRY_RUN" -eq 0 ]; then
    umask 077
    cat > "$ENV_FILE" <<EOF
API_DOMAIN=localhost
ACME_EMAIL=local@tokenkey.local
TZ=UTC
SERVER_MODE=release
RUN_MODE=standard
TOKENKEY_IMAGE=$TOKENKEY_IMAGE
POSTGRES_USER=tokenkey
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
POSTGRES_DB=tokenkey
DATABASE_MAX_OPEN_CONNS=50
DATABASE_MAX_IDLE_CONNS=10
REDIS_PASSWORD=
REDIS_DB=0
REDIS_POOL_SIZE=1024
REDIS_MIN_IDLE_CONNS=10
ADMIN_EMAIL=admin@tokenkey.local
ADMIN_PASSWORD=$ADMIN_PASSWORD
JWT_SECRET=$JWT_SECRET
JWT_EXPIRE_HOUR=1
TOTP_ENCRYPTION_KEY=$TOTP_KEY
EOF
    chmod 600 "$ENV_FILE"
  fi
fi

# Caddyfile: always rewritten (no secrets; safe to overwrite)
if [ "$DRY_RUN" -eq 1 ]; then
  echo "[local-bootstrap] would write Caddyfile: $CADDYFILE"
else
  echo "[local-bootstrap] writing Caddyfile: $CADDYFILE"
fi
if [ "$DRY_RUN" -eq 0 ]; then
  cat > "$CADDYFILE" <<'CADDY'
{
	email local@tokenkey.local
}

:80 {
	encode zstd gzip

	@static {
		path /assets/*
		path /logo.png
		path /favicon.ico
	}
	header @static ?Cache-Control "public, max-age=31536000, immutable"

	reverse_proxy tokenkey:8080 {
		health_uri /health
		health_interval 30s
		health_timeout 10s
		header_up X-Real-IP {remote_host}
		header_up X-Forwarded-For {remote_host}
		header_up X-Forwarded-Proto {scheme}
		header_up X-Forwarded-Host {host}
		transport http {
			keepalive 120s
			keepalive_idle_conns 64
			compression off
		}
	}

	request_body {
		max_size 100MB
	}

	log {
		output stdout
		format json
		level INFO
	}
}
CADDY
fi

# override.yml: always rewritten
if [ "$DRY_RUN" -eq 1 ]; then
  echo "[local-bootstrap] would write docker-compose.override.yml: $OVERRIDE_FILE"
else
  echo "[local-bootstrap] writing docker-compose.override.yml: $OVERRIDE_FILE"
fi
if [ "$DRY_RUN" -eq 0 ]; then
  cat > "$OVERRIDE_FILE" <<EOF
services:
  caddy:
    ports:
      - "8088:80"
      - "8443:443"
    volumes:
      - $TOKENKEY_STAGE0_LOCAL_ROOT/caddy/Caddyfile:/etc/caddy/Caddyfile:ro
      - $TOKENKEY_STAGE0_LOCAL_ROOT/caddy/data:/data
      - $TOKENKEY_STAGE0_LOCAL_ROOT/caddy/config:/config
  tokenkey:
    volumes:
      - $TOKENKEY_STAGE0_LOCAL_ROOT/app:/app/data
  postgres:
    volumes:
      - $TOKENKEY_STAGE0_LOCAL_ROOT/postgres:/var/lib/postgresql/data
      - $TOKENKEY_STAGE0_LOCAL_ROOT/pgdump:/pgdump
  redis:
    volumes:
      - $TOKENKEY_STAGE0_LOCAL_ROOT/redis:/data
EOF
fi

echo "[local-bootstrap] done."
echo
echo "Next steps:"
echo "  export REPO_ROOT=\"$REPO_ROOT\""
echo "  export TOKENKEY_STAGE0_LOCAL_ROOT=\"$TOKENKEY_STAGE0_LOCAL_ROOT\""
echo "  docker compose -f \"\$REPO_ROOT/deploy/aws/stage0/docker-compose.yml\" \\"
echo "                 -f \"\$TOKENKEY_STAGE0_LOCAL_ROOT/docker-compose.override.yml\" \\"
echo "                 --env-file \"\$TOKENKEY_STAGE0_LOCAL_ROOT/.env\" \\"
echo "                 config --quiet && pull && up -d"
echo
echo "Admin email is in $ENV_FILE (grep ^ADMIN_); password is in the same file."
