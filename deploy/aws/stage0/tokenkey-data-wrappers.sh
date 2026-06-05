#!/bin/bash
# tokenkey-data-wrappers.sh — install/update the unified data-layer CLI seams
# (tokenkey-psql / tokenkey-pg_dump / tokenkey-redis-cli) on a Stage0 host
# (prod or edge). Single source of truth for both delivery paths:
#   - prod EC2 bootstrap: stage0-ec2-bootstrap.sh fetches this file from SSM
#     (param <prefix>/data-wrappers.b64, embedded by build-cfn.sh) and runs it
#   - existing fleet: ops/stage0/install_data_wrappers_via_ssm.sh ships + runs it
#
# The wrappers read /var/lib/tokenkey/.env at call time, so the same binary
# works in local-container mode (DATABASE_HOST=postgres over the compose
# network) and external-RDS mode (DATABASE_HOST=<rds-endpoint>, sslmode=require)
# on prod and edges alike. They are the ONLY supported psql/redis-cli seam for
# ops scripts — never `docker exec tokenkey-postgres` directly (the container
# does not exist in external mode). See docs/deploy/aws-data-layer-migration.md.
set -euo pipefail

install -m 0755 /dev/stdin /usr/local/bin/tokenkey-psql <<'PSQLEOF'
#!/bin/bash
# tokenkey-psql — unified psql seam (local-container & external-RDS modes).
# Managed by tokenkey-data-wrappers.sh — edit there, not here.
set -euo pipefail
set -a; . /var/lib/tokenkey/.env; set +a
# compose prefixes the project name (actual prod name: tokenkey_tokenkey-network)
# — resolve dynamically, never hardcode. bridge fallback covers external-RDS
# mode on a host whose stack is down (egress still works via bridge).
NET="$(docker network ls --format '{{.Name}}' | grep -m1 'tokenkey-network$' || echo bridge)"
exec docker run --rm -i --network "${NET}" \
  -e PGPASSWORD="${POSTGRES_PASSWORD}" \
  -e PGSSLMODE="${DATABASE_SSLMODE:-disable}" \
  "${TOKENKEY_PG_CLIENT_IMAGE:-postgres:18-alpine}" \
  psql -h "${DATABASE_HOST:-postgres}" -p "${DATABASE_PORT:-5432}" \
       -U "${POSTGRES_USER:-tokenkey}" -d "${POSTGRES_DB:-tokenkey}" "$@"
PSQLEOF

install -m 0755 /dev/stdin /usr/local/bin/tokenkey-pg_dump <<'PGDEOF'
#!/bin/bash
# tokenkey-pg_dump — unified pg_dump seam, dump to stdout.
# Managed by tokenkey-data-wrappers.sh — edit there, not here.
# TOKENKEY_PG_CLIENT_IMAGE (.env / SSM overlay) MUST match the server major
# version once the ledger lives on RDS (client >= server for pg_dump).
set -euo pipefail
set -a; . /var/lib/tokenkey/.env; set +a
NET="$(docker network ls --format '{{.Name}}' | grep -m1 'tokenkey-network$' || echo bridge)"
exec docker run --rm -i --network "${NET}" \
  -e PGPASSWORD="${POSTGRES_PASSWORD}" \
  -e PGSSLMODE="${DATABASE_SSLMODE:-disable}" \
  "${TOKENKEY_PG_CLIENT_IMAGE:-postgres:18-alpine}" \
  pg_dump -h "${DATABASE_HOST:-postgres}" -p "${DATABASE_PORT:-5432}" \
          -U "${POSTGRES_USER:-tokenkey}" -d "${POSTGRES_DB:-tokenkey}" "$@"
PGDEOF

install -m 0755 /dev/stdin /usr/local/bin/tokenkey-redis-cli <<'RCEOF'
#!/bin/bash
# tokenkey-redis-cli — unified redis-cli seam (local-container & external modes).
# Managed by tokenkey-data-wrappers.sh — edit there, not here.
set -euo pipefail
set -a; . /var/lib/tokenkey/.env; set +a
NET="$(docker network ls --format '{{.Name}}' | grep -m1 'tokenkey-network$' || echo bridge)"
ARGS=(-h "${REDIS_HOST:-redis}" -p "${REDIS_PORT:-6379}")
if [ "${REDIS_ENABLE_TLS:-false}" = "true" ]; then ARGS+=(--tls); fi
exec docker run --rm -i --network "${NET}" \
  -e REDISCLI_AUTH="${REDIS_PASSWORD:-}" \
  "${TOKENKEY_REDIS_CLIENT_IMAGE:-redis:8-alpine}" \
  redis-cli "${ARGS[@]}" "$@"
RCEOF

echo "tokenkey data wrappers installed: tokenkey-psql tokenkey-pg_dump tokenkey-redis-cli"
