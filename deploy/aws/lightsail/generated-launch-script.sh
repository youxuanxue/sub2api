#!/bin/bash
# tokenkey Edge Lightsail bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-lightsail-bootstrap.log) 2>&1
echo "LIGHTSAIL_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${EDGE_ID:?EDGE_ID required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${LIGHTSAIL_REGION:?LIGHTSAIL_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"
# GHCR auth is OPTIONAL: when GHCR_PAT_SSM_NAME is empty the bootstrap relies
# on anonymous pull (works for public ghcr.io/* images, which TokenKey
# currently is). Set GHCR_PAT_SSM_NAME to an SSM SecureString name when the
# image becomes private.
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"

export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@${API_DOMAIN}}"
export TZ_VALUE="${TZ_VALUE:-UTC}"

yum -y update || dnf -y update || true
(yum -y install docker awscli openssl gzip tar || dnf -y install docker aws-cli openssl gzip tar) || true
systemctl enable --now docker || true
if ! command -v docker >/dev/null; then
  (amazon-linux-extras install docker -y || dnf -y install docker) || true
  systemctl enable --now docker || true
fi
if ! docker compose version >/dev/null 2>&1; then
  mkdir -p /usr/local/lib/docker/cli-plugins
  curl -fsSL "https://github.com/docker/compose/releases/download/v2.29.7/docker-compose-linux-$(uname -m)" \
    -o /usr/local/lib/docker/cli-plugins/docker-compose
  chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi

if ! rpm -q amazon-ssm-agent >/dev/null 2>&1; then
  if ! yum -y install amazon-ssm-agent && ! dnf -y install amazon-ssm-agent; then
    echo "BOOTSTRAP_FAIL: cannot install amazon-ssm-agent" >&2
    exit 1
  fi
fi
systemctl enable amazon-ssm-agent
# Register against SSM Hybrid Activation. Fail fast on misconfigured activation —
# silent || true here would mean provision waits 10 minutes before reporting,
# while Lightsail clock + Static IP are already billing.
if ! /usr/bin/amazon-ssm-agent -register -y \
      -id "${SSM_ACTIVATION_ID}" \
      -code "${SSM_ACTIVATION_CODE}" \
      -region "${LIGHTSAIL_REGION}"; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent -register failed (activation id/code/region mismatch?)" >&2
  exit 1
fi
systemctl restart amazon-ssm-agent
for i in 1 2 3 4 5 6; do
  if systemctl is-active --quiet amazon-ssm-agent; then break; fi
  echo "amazon-ssm-agent not active yet (try ${i}/6) — sleep 5s"
  sleep 5
  systemctl restart amazon-ssm-agent || true
done
if ! systemctl is-active --quiet amazon-ssm-agent; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent failed to stay active after register" >&2
  exit 1
fi

mkdir -p /var/lib/tokenkey/caddy/data /var/lib/tokenkey/caddy/config /var/lib/tokenkey/app
COMPOSE_GZB64='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'
CADDY_GZB64='H4sIAAAAAAACA+1VzW4TMRA+7z7FKO0JdTebplRVJQQhDRCpf2qRCifLWU+ypl57sb3bplGeiztPxjgLaRqhigMSFy5ezzfjb37t3QFvblHf4hy+f4PBzTWMxAzh2nNas4ANuRDzqVQY78BQmVokYy091A4doG5cPXEepsbC7mJwOWYnF2eD8flyL4jDsxEbkXhKIteCCHYXQcveDz6Obgaf2eD09OJmdMKG45OrZQpXqPgcKu4LB9wiaOOhqidK5sGdJXfekt5I7V0ax4s4wpJL9dRVHO0Az0tkOYfC+8odd7tBThzlJPUsabL9lFcyVegd6tzOK58aO+sKaTH3xs7jZRw/SQaCJ50bgfDgvIDZg6ziOHpDjJ5iI20UgoYud45Iuy/WgDIzk1Z6tgamvJG50SktcUSxFsgFZfaL6vWQ5wUmQ0OZGgWdNvs9KPl9Qh151e+97B9mWbYHsixrzycKOxSJV66NwhpvckMSIb10v/30gycK14bysra8jzE3vY14ZUleNhNopECzLa8MVpRcKXOHgq2o/wJpZLE0Hpmsnh2V1ntBM6UQfhOExQatQ0b1uJ+vJ/z4KDvKVvpoqmpXMJojtA1XkPQCSL1QvmC1ldBt9xvo2rafuQ3YyxJN7aG3RqmdrK7gU3JFFsn4EhY/cyqM88tto3fG3nErUITdn5pehjbDwtGolPiM3QeigcWazFuuXWWsX92LthDRLWLFlWwQevttDo8Qk1RfRuOqHRwerHS5KSuLzkmjwUynAQvcWx3ZnjQ6URktoIPhcbHraw7SkeS8lblH0YGDrP+E6H8v/10vLX6tqTVsYkR7qegNYk4+EHmWnb1tjeh9W+mobBVVjh5H2pFM/4OSe/jijCZJUQsVjM/fXYRTy/gHk52xtHUGAAA='
QA_B64='H4sIAAAAAAACA61VbXPaRhD+zq94TNwB4hwCT5xOSdWOM1ZdTxJwAI8n4ziaQ7eCG8RJlg4cAvS3dyX8WmjrpP1yI3R3y/O2q2c7zkAbZyCzUekZujQkQ6m0pKCNjZFZOaSGyLQZRiQo2K/P5STCYN6CoiSK5468zpz1KWcw1ZESQWjqN8UsGatjA50hIwtpMYhji5mWcMgGjo3HZMY0d66k4Br8D+ntnTqZGaofZC9/fVfpSM6zWr2UFxM0jZHohEKpo1LX63vt/kmn7R8dfuy55d1Fv/PWa7/1PvofDv1e//Cd5z8+0xLN+sGqXNIhdnBxAb7y+MCqDPcPfK42lhdN8dPlRYOX57Xqp3rxtFf7dReXl69hR2RKQBQPh5RCWNySEnekgoikmSYoazOTkVb4N2zuU+AzdoC+aItmKdQ5D3k9hphBudu4VN54xydtLNZXdqoKrotGDavKt3HIxjqB0pkcRBySbQa5jTtkjRtkO8imKoaKgzH/QZJBiDBOJxyIymJRb8sJZatVBUsMU0ogrr7cI0jizPLb7DtQbtSA4fSlU2M4zn/BaPICEYSCmKDx48EBnJlMnUgP7lMqk4ST6g+ieJD9/baKrrD/i6No5pgpl1wuYdMplUqnx6eHvTyb1UKMgmrl82mn1z/uej0/3zzvdI/cypbaRTssEUw5+MqFCPdFrcjuBcTXPLvr4uzz5XcIZWJswHisT9vr3wG/cZElSyiwW1R+aG4qzZCwO36xO0PLRb1N9jpOxz2ylk3Ibn+z/YvF7jhfySjOArMrPYkAK55SEKcq8xVFnEOfD6Rs820kfZVncrMjNnPJyWDs6YQXs4aVK8vUWVZBWEtcWPRQ8FvareYrIaNEG8In1i7JrjhOo23ynN29zPN2/zwDg/O63U6XW75z6jaLQiJA+ch7x+jxW7fzHveEcf671/UQpJQPbJ8F/xntznm1BoFqZYNxpdUy0wmlOsDzfLpTytMIlSZYoErtdfnbgrOpez6IST2eSvwZOIz00CDkPSSsMLuOa21HCFMZ5P4whtwhVENt8u6zekL5F+N6FPOVYis20ZzH/mn3rO3570/aZ32v6KOnDbwkZbIhyj+o8gso5r7/kpdXDeyhUT9AkbUiBgWA/9L2YsIVKGFyTVZvnhDCtYUT3sBenpqHFPJYrbXbOjD+J0xrQJvQ1BoaTRI7/0ccT+vDmwdfxYbKpT8B+vKaMlcIAAA='
PRUNE_B64='H4sIAAAAAAACA7VV627iRhT+76c48WYDSdfYRKutlgSkKCUpSpasAq3abio02Mcwwh67M2MSmiD1IfqEfZKe8YXAhk2bVisZCc+cy/d95+JXO+6YC3fM1NR6BcNkhuICFzDQbIIe/PXHn5DKTCBEic8iOP/+9BpYmgKP6R7ISAELNUpA5k/BT+I0UQhpFkVQ50LQhfIlT/V+g6IPUM4xgFAmMQwGH6DRaLiTqS+dPEVj/O4t1AkIvnu7f0QJWUDuTIObKenm+XOo2mCc4aLwcvIABMkxYBo5ixC1P0VCJgLAO/QV6ClXEPIIG3CBmJoDhDhRGiT6KDRcdLsfR/2C0HhR0juVyDQB1jzGN5BGmQKco1xQCjKEMJF5nMKYwAguJsAFySA044a8XYG1ib9RIMmkjy0IMI2Shctulatypf+JVV4JiaFE+j/nbEuEccajwPFDQeZG7atU80SwqAXDq4tu/6L788iUb5RTHZ6cDwxUd85IWz5e5XcbKOZQDzBkWaSh6VHlLIUaHMwSSHlKFzyyrEKwtr17/4XoLafpLW2r2/9xdNa77Lbt7alsy+IhfIIdcEKgaJX90oZfj4y8wgJAf5o8arlNohbEXCmj/0aIzt6hcb/jGjwr5BbJoqYYRdQe/gwCrtg4wvbgtOm99wqWzGpswsiPv2ElTOd3WKfc+3By3m05Lwa7GQAwTvViC9rr7ser9pN8r1sHBGsNjjEzEODhgc4eD9pPsb4YqZ9kUQAi0ZAySaMtMU2KCd4MvA39D/1+r38+6n3XtnO8QeLPaCi4UCn6GqrE0HEDnLvCLI3Dzl5zhW89wG79M29ql9r9faNnhm+5rK2i7dt5cpVIGl3jF8806UvHM5r8cONES5ZCTcZl7xU+JBH9z42Xdg26P/WGlnU7peVB5FkAjjSDeERsCGJZAjow+u/t5bPPRYZ059Mqq+5o1A5qxyIR2Kkd7K+s4OgIUDHfWJst82We5S4yTMuQh2uyUeHzYtaa77/1HK9Jz9DzWvnzS42YAnAePKPiM4G1zDCPkEouNNm/Vje6/AnjlCO3K297lzLZVkBU4RiOVwlNndRaczoO7c+Y1rvJf01dpbhO5GK5bNH7kE0MoG1I4IagPMDl6ejk8rJ9CqZo4GiKXK8A3miiDM6s+aYpobNWWMtq5e9lcauy9s4G7SfuVa1HPtEyFTcKblb9nk6L4f+s8sVsipztqocru0+lRMV4bt6vWr8azkLTzjpooA9ZKe8GtRdxGf17MjNaAUK3vRUvtTYfG5BLy936rQ9OlMOrzEzxwAmgBrX9ggEUq6rwMf0wMcNyX3xXKhhjQj4rEu/AhHYPOGd3vz222ir+Wqf8fw2/5pj/RyblFMmYPzOl67S+wuRZVrnc/wZ6n3lINAoAAA=='
printf '%s' "$COMPOSE_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/docker-compose.yml
printf '%s' "$CADDY_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/caddy/Caddyfile.template
envsubst '${API_DOMAIN} ${ACME_EMAIL} ${MAIN_GATEWAY_ALLOWED_CIDR}' \
  < /var/lib/tokenkey/caddy/Caddyfile.template > /var/lib/tokenkey/caddy/Caddyfile

printf '%s' "$QA_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-qa-stale-cleanup.sh
chmod +x /usr/local/bin/tokenkey-qa-stale-cleanup.sh
printf '%s' "$PRUNE_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
chmod +x /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh

SECRET_FILE=/var/lib/tokenkey/.env.secret
if [ ! -f "$SECRET_FILE" ]; then
  umask 077
  gen_secret() { openssl rand -hex 32; }
  gen_pwd() { openssl rand -hex 24; }
  cat > "$SECRET_FILE" <<SECEOF
POSTGRES_PASSWORD=$(gen_pwd)
JWT_SECRET=$(gen_secret)
TOTP_ENCRYPTION_KEY=$(gen_secret)
SECEOF
  chmod 0600 "$SECRET_FILE"
fi
set -a; . "$SECRET_FILE"; set +a

cat > /var/lib/tokenkey/.env <<ENVEOF
API_DOMAIN=${API_DOMAIN}
ACME_EMAIL=${ACME_EMAIL}
TZ=${TZ_VALUE}
SERVER_MODE=release
RUN_MODE=standard
TOKENKEY_IMAGE=${TOKENKEY_IMAGE}
POSTGRES_USER=tokenkey
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
POSTGRES_DB=tokenkey
DATABASE_MAX_OPEN_CONNS=10
DATABASE_MAX_IDLE_CONNS=2
REDIS_PASSWORD=
REDIS_DB=0
REDIS_POOL_SIZE=64
REDIS_MIN_IDLE_CONNS=2
ADMIN_EMAIL=${ADMIN_EMAIL}
ADMIN_PASSWORD=
JWT_SECRET=${JWT_SECRET}
JWT_EXPIRE_HOUR=1
TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}
ENVEOF
chmod 0600 /var/lib/tokenkey/.env

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  # Private-image path: pull the PAT from SSM SecureString and docker login.
  GHCR_PAT="$(aws --region "${LIGHTSAIL_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text)"
  echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin
  unset GHCR_PAT
else
  # Public-image path (default for TokenKey GHCR): no docker login. Anonymous
  # pull works because ghcr.io/* manifests are accessible via anonymous bearer.
  # If the image ever turns private, set GHCR_PAT_SSM_NAME on the workflow side
  # and the docker login branch above engages with no other changes.
  echo "GHCR_PAT_SSM_NAME unset; relying on anonymous pull for public image ${TOKENKEY_IMAGE}"
fi

cat > /etc/systemd/system/tokenkey.service <<'UNITEOF'
[Unit]
Description=tokenkey edge lightsail stack (docker compose)
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/var/lib/tokenkey
EnvironmentFile=/var/lib/tokenkey/.env
ExecStartPre=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env pull
ExecStart=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env up -d --remove-orphans
ExecStop=/usr/bin/docker compose --env-file /var/lib/tokenkey/.env down
TimeoutStartSec=10min

[Install]
WantedBy=multi-user.target
UNITEOF

systemctl daemon-reload
systemctl enable --now tokenkey.service
sleep 30
docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true
echo "LIGHTSAIL_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
