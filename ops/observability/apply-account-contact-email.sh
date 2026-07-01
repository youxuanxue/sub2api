#!/bin/bash
# apply-account-contact-email.sh — Set OAuth contact email on one account (extra + credentials).
#
# Mirrors backend ApplyAccountEmail canonical keys. Use via run-probe.sh:
#   bash ops/observability/run-probe.sh --target edge:us5 \
#     --env ACCOUNT_NAME=kiro-us5 \
#     --env ACCOUNT_EMAIL=user@example.com \
#     --script ops/observability/apply-account-contact-email.sh
set -u

NAME="${ACCOUNT_NAME:-}"
EMAIL="${ACCOUNT_EMAIL:-}"
if [ -z "$NAME" ] || [ -z "$EMAIL" ]; then
  echo "apply-account-contact-email: ACCOUNT_NAME and ACCOUNT_EMAIL required" >&2
  exit 2
fi

# Reject obvious injection in operator-supplied literals.
case "$NAME" in
  *"'"*|*'"'*|*';'*|*'\\'*) echo "apply-account-contact-email: invalid ACCOUNT_NAME" >&2; exit 2 ;;
esac
case "$EMAIL" in
  *"'"*|*'"'*|*';'*|*'\\'*) echo "apply-account-contact-email: invalid ACCOUNT_EMAIL" >&2; exit 2 ;;
esac

PSQL='docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t'

echo "=== apply contact email: name=$NAME ==="
$PSQL -c "
WITH upd AS (
  UPDATE accounts SET
    extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object('email_address', '${EMAIL}', 'email', '${EMAIL}'),
    credentials = COALESCE(credentials, '{}'::jsonb) || jsonb_build_object('email_address', '${EMAIL}', 'email', '${EMAIL}'),
    updated_at = NOW()
  WHERE deleted_at IS NULL AND name = '${NAME}'
  RETURNING id, name, platform, type, status, schedulable, extra, credentials
)
SELECT row_to_json(t) FROM (
  SELECT id, name, platform, type, status, schedulable,
    extra->>'email_address' AS extra_email_address,
    extra->>'email' AS extra_email,
    credentials->>'email' AS cred_email,
    credentials->>'email_address' AS cred_email_address,
    COALESCE(
      NULLIF(trim(extra->>'email_address'), ''),
      NULLIF(trim(extra->>'email'), ''),
      NULLIF(trim(credentials->>'email_address'), ''),
      NULLIF(trim(credentials->>'email'), '')
    ) AS resolved_email
  FROM upd
) t;
" 2>&1
