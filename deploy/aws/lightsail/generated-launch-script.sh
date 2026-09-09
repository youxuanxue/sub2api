#!/bin/bash
# tokenkey Edge Lightsail bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-lightsail-bootstrap.log) 2>&1
echo "LIGHTSAIL_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${EDGE_ID:?EDGE_ID required}"
: "${INSTANCE_NAME:?INSTANCE_NAME required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${LIGHTSAIL_REGION:?LIGHTSAIL_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"
: "${ALLOW_SECRET_GENERATE:=false}"

case "${ALLOW_SECRET_GENERATE}" in
  true|false) ;;
  *) echo "BOOTSTRAP_FAIL: ALLOW_SECRET_GENERATE must be true or false" >&2; exit 1 ;;
esac

if command -v hostnamectl >/dev/null 2>&1; then
  hostnamectl set-hostname "${INSTANCE_NAME}" || true
else
  hostname "${INSTANCE_NAME}" 2>/dev/null || true
fi

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

SWAP_SIZE_GIB="${SWAP_SIZE_GIB:-2}"
if [ "${SWAP_SIZE_GIB}" -gt 0 ] && [ ! -f /swapfile ]; then
  fallocate -l "${SWAP_SIZE_GIB}G" /swapfile || dd if=/dev/zero of=/swapfile bs=1M count=$((SWAP_SIZE_GIB * 1024)) status=progress
  chmod 0600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

if ! rpm -q amazon-ssm-agent >/dev/null 2>&1; then
  if ! yum -y install amazon-ssm-agent && ! dnf -y install amazon-ssm-agent; then
    echo "BOOTSTRAP_FAIL: cannot install amazon-ssm-agent" >&2
    exit 1
  fi
fi
systemctl enable amazon-ssm-agent
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

mkdir -p /var/lib/tokenkey/caddy/data /var/lib/tokenkey/caddy/config
install -d -m 0755 -o 1000 -g 1000 /var/lib/tokenkey/app
COMPOSE_GZB64='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'
CADDY_GZB64='H4sIAAAAAAACA61VbW8TRxD+bP+KUcIHqLB9jhOEqFBxQwBLAaIkFe2nY323trc531539xyMFSkgEghNRIoCKi8SrXgroPJSUZEmRkj9KdRnO5/yFzp7F9s4pagfKiW2d3b2mZlnZp8dBMVnqDtDq/DnG8iemYIxu0hhShH8NLRtlNh2tcAcGh+EUYf7diLnMgW+pBKoW5F+XioocAF7atmJnHn09Mls7tTcfr0cPTlmjuFyHJfEtRFgT03vmsez02Nnst+Y2fHx02fGjpqjuaOTc0mYpA6pgkdUSQIRFFyuwPPzDrN0OIHhlMB9zlwlkzodKpTUWzA9PpXIjk+cShhp2MtdGB7O7PscTkxPT2gLk2AzSfIOtSFPLaJPqBKFcVYsKUmYg1gFJugscRyYodSTnbAeFwoOGmA5XFI7GY/X4jFaxhP95cVjg0CsMjUtAiWlPHkoldLrhEQemVtMVIyhJPFY0qFKUtcSVU8luSimbIxqKS6q8bk4JhFy73tSCUrKIF3meVRt1283l+abd5eCayuNzfvBlZfdnh06aGByrduXIO9wawaChVeNjWftd7ca6/ONzbdbCyutt8+368sIvXXhXbCwAmdL2AiHwhEslc9S2xSa87MQXF+GAtryBGE6TrU53Fi91Np83bz6IIIMXtTbl1836xdbjzf/mr+IwM21PzDg1uXl4OUr/N3YWGyso/NTiEC263c8we3eGEFw56fG+kZw7alGRL9uvVENzd9+bq390ilex9irZsyO0z7ADghaoUJSE4HPVXeRgduxguPLkolTQkWFOJBIo20QggsPg403zfvzzXsPW7/ebN14GKwuQ39yyDWxFKtQaP/+ElIlShxV2q5fsQVhboLM4lRu15c0oxpxKnf8q6nJNLxfvN7xPTxiZKD9/EkECTtgrWffB1c2m1ev9opt/vDj1q0HyI6NRVMR4kUQVokiDZqDHcyUozF2GhhmEkaMDoKgOGRCaZ6JlNoTqW0/fxAijhhDqRFjGCto3lhoXrrWWF8JVp+hOfN+fg3/WquLrbVX7ReP2o8v/IOKpfajxdadm62791pPNpp37+lexGJRTqYvWCe/nrFL+YjsGRUrU+4ryGhbAe+OafuCKIbXNHQrk3OmNkvQffLd6FjVxLKUL3UJ+D+MW07exPvfO50xZM/aDZ3eCW1TgVMDXycmES+Rm4CaoGWuqFniUs3t8jnGBfbWprb+9R89JwRXHGoS+1Wm/+52AkGg1oFSgrgyVBWtE+G0xmxGnC5LISUxLUIkbHt6yOi3mAxvlWlx15VwQNMSs3jZwymQmhNeKKAJI81pRemTZB0MtYfbFM5LZUPxPPPi8dgRzTNKnU5FSy+kcIxQplKfdQ0OL/Kk5xa7hgKpMMwgiR860k7l0IH6YpQgKYlRjnrNHRiIxHQ/YKcT+K4czqRHMgcMw9gPrFz2lRbmAcxE4QiEWWhiLY4rtKSTQ9FXBneYlD7G0dIaUfdRLdaMhmpvao5Nq4S6Rt0i7RCDNYeqZ0YvTa/wSvqDolkZU/2QhQqzKd+9Dh1CyD5B/R9AYztDyLxPvppR9I+qepgEK4fT9oGE9p/YzQQOksddGwaofotE90XWj6iWGsEsRe0BGDYyfUCfiiXodz4eNfPcjpLSV16y8zjdhnHyy8gJhyzcw0vg4T3ACcVfWjG4KBMF30ru6tuO0u9A7tSx09GI/w1OHHzNwAgAAA=='
PRUNE_B64='H4sIAAAAAAACA7VV627iRhT+76c48WYDSdfYRKutlgSkKCUpSpasAq3abio02Mcwwh67M2MSmiD1IfqEfZKe8YXAhk2bVisZCc+cy/d95+JXO+6YC3fM1NR6BcNkhuICFzDQbIIe/PXHn5DKTCBEic8iOP/+9BpYmgKP6R7ISAELNUpA5k/BT+I0UQhpFkVQ50LQhfIlT/V+g6IPUM4xgFAmMQwGH6DRaLiTqS+dPEVj/O4t1AkIvnu7f0QJWUDuTIObKenm+XOo2mCc4aLwcvIABMkxYBo5ixC1P0VCJgLAO/QV6ClXEPIIG3CBmJoDhDhRGiT6KDRcdLsfR/2C0HhR0juVyDQB1jzGN5BGmQKco1xQCjKEMJF5nMKYwAguJsAFySA044a8XYG1ib9RIMmkjy0IMI2Shctulatypf+JVV4JiaFE+j/nbEuEccajwPFDQeZG7atU80SwqAXDq4tu/6L788iUb5RTHZ6cDwxUd85IWz5e5XcbKOZQDzBkWaSh6VHlLIUaHMwSSHlKFzyyrEKwtr17/4XoLafpLW2r2/9xdNa77Lbt7alsy+IhfIIdcEKgaJX90oZfj4y8wgJAf5o8arlNohbEXCmj/0aIzt6hcb/jGjwr5BbJoqYYRdQe/gwCrtg4wvbgtOm99wqWzGpswsiPv2ElTOd3WKfc+3By3m05Lwa7GQAwTvViC9rr7ser9pN8r1sHBGsNjjEzEODhgc4eD9pPsb4YqZ9kUQAi0ZAySaMtMU2KCd4MvA39D/1+r38+6n3XtnO8QeLPaCi4UCn6GqrE0HEDnLvCLI3Dzl5zhW89wG79M29ql9r9faNnhm+5rK2i7dt5cpVIGl3jF8806UvHM5r8cONES5ZCTcZl7xU+JBH9z42Xdg26P/WGlnU7peVB5FkAjjSDeERsCGJZAjow+u/t5bPPRYZ059Mqq+5o1A5qxyIR2Kkd7K+s4OgIUDHfWJst82We5S4yTMuQh2uyUeHzYtaa77/1HK9Jz9DzWvnzS42YAnAePKPiM4G1zDCPkEouNNm/Vje6/AnjlCO3K297lzLZVkBU4RiOVwlNndRaczoO7c+Y1rvJf01dpbhO5GK5bNH7kE0MoG1I4IagPMDl6ejk8rJ9CqZo4GiKXK8A3miiDM6s+aYpobNWWMtq5e9lcauy9s4G7SfuVa1HPtEyFTcKblb9nk6L4f+s8sVsipztqocru0+lRMV4bt6vWr8azkLTzjpooA9ZKe8GtRdxGf17MjNaAUK3vRUvtTYfG5BLy936rQ9OlMOrzEzxwAmgBrX9ggEUq6rwMf0wMcNyX3xXKhhjQj4rEu/AhHYPOGd3vz222ir+Wqf8fw2/5pj/RyblFMmYPzOl67S+wuRZVrnc/wZ6n3lINAoAAA=='
RESTORE_SECRETS_B64='H4sIAAAAAAACA7VXUVPbOBB+969YQg5IOddJyvXumnM6mdbt9I5CJkmPdijNiFiOPdiSkeRQCulvv5VsJw6Bhj4cDwXJ2t1vd7/9pG5vOZkUznnEHMpmcE5kaEmqwKYZhzRKaUCi2LL6vUHvvTfyBm6tZh1/GPU/jPRfA+/tu+Mjt1a/6Z0Mx/nqhZ2vXntveh8OR4vd+bxm9Q4Pj0/Gb70jb9AbeW5AYkkt6yqMYgqnUKtv18CeKmjCWQd8bgFMiKS436pBxHAJYNspESShiooGVFDVb9oYotYBGUaBgjZ0OsV5nqk0Uw0oUT98ksQxv7KnlFFBFG3AHbRKZLS0KmyeNIBOQg41QaXigtrUn+I/bGZLOhFUyReQsQvGrxgQMc0SyhToZLo77Q7Qr5GCg2bui0oysXzOsBynuhI3i9zmNXC/wxdH8QvKLui1o2M4p8T+1rT/tM/2HanIlDadSlT7nEwusrQOZ2dwews32v8GmBGbkTjyQX+BRYkNUm2dQ7XmBbi8lhoZOE/KKBtjkHPJ40xRyFuCYVQIgl5mkaD+alE6YELZTEfLGYTRHhsI6Yd+pxFnP3BvmYSx0ePcbhwgC/caploxn5DY4HM1+XAHsQQai97TSGBnB/e2wD5c2UV46CoTDFrGqFbfI1cXsOsdvUbcqYiQAEcD2EeKz3eXlg3kPb2EZ2seciDYdZjwDG0Rc0Yxi5ROFPXHMWVTFeK5gAtzKmLQPx6O3g684bjfGw5Pjgev4e+T0XjovRp4Ixgdj/pj7+jV4FN/hDUd/+N9KiYN8ghugdh+44I90z71xOAvTG+33gLXNXEw+zz2Xr3ZgG65wE8NTK6FuRpv+/swh2rueRbr6RsAhlrmxDyvR2utHpBX4JEoy6gyO5dKINRf7wBtN+7DEQU5lNyfe09FUZ5USNndRrgHfyDFUNPWPjw/6EAQ5Vka3yaNYrRPcZCJHZztm4E1xMIT2+URXQlc33F5D92MfACSsiyNLsXWd3C+7K2lcLskxe09pGjUHe280uO/3MWqVfb4nPhua9FhM1u4BS+xvS/wQLW0KB2WqatNVwTE5HBajNFyN6+vZdq9NqQrRwt926xwCE+qiE0h3wHjKZKl8pVSV4qdLuhc30Bhwn1oPm82q2EXimr0Ut+cRRwgsaDEv0bmUanlHjUo5FLVShVtWsgDCxWW5sPmR4Kh2FadIw0jhqoex2D7YCfQ/D2PnlthdEWTVBsnF/qvyifnaelme/uJM3/60fxoj1QILkz5HmVojlfMV8ugjbH2mpULtwhrElPCsrQQUWz3hCcJYb6eURmiCkPX8enMYRmm1u7utBZthuK7nT3sHi2W1pr5SaHJD6ABM4rG+eaj2JS5pQRJoUgCvI/vRoaz5Eri46C4T6rXkZQJTKlavkjgs6XfEWU/q1e4bV9FKrR9ZMl1qrSn/OxlRsU19EsPT/81Al++WkDRrwq6S+Dt7h3oGwelMHz0mAyH74tbBmcjITHeLAldm452MR3JbK2wlRH52dkpUPkQCJ5oJEjbGDswFTRFQbuE3UWhjrh6gzeFvwsPFaRU8dVXHGLcckE/5Sois7kqyxbrqkRSaiHRzC5ei7qh+IVxBSRTIRfRt/Wi/ZbzDFAcly3FpbmkAkzurki7v8jPTGe4x1PKpIxBmGkK6VdoH+CjoXuvn6W0/8jBs/aDDu65EH7S0yPYuLHm5UPcX2qroCtyXb7nnnX+BzIuw+vHVRAJiS9WwWeRxGZrYhptecw46QsB9P+jqN/BRZDJyjUkuDL0Kcmi8BzYE2g12wdrClh9jR/oi+Q/6cn5Hr8NAAA='
printf '%s' "$COMPOSE_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/docker-compose.yml
printf '%s' "$CADDY_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/caddy/Caddyfile.template
envsubst '${API_DOMAIN} ${ACME_EMAIL} ${MAIN_GATEWAY_ALLOWED_CIDR}' \
  < /var/lib/tokenkey/caddy/Caddyfile.template > /var/lib/tokenkey/caddy/Caddyfile

printf '%s' "$PRUNE_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
chmod +x /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh

printf '%s' "$RESTORE_SECRETS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-restore-edge-env-secrets.sh
chmod 0755 /usr/local/bin/tokenkey-restore-edge-env-secrets.sh

SECRET_FILE=/var/lib/tokenkey/.env.secret
restore_secret_args=(
  --parameter "/tokenkey/edge/${EDGE_ID}/stage0/env-secrets-backup" \
  --output "$SECRET_FILE"
)
if [ "${ALLOW_SECRET_GENERATE}" = true ]; then
  restore_secret_args+=(--allow-generate)
fi
AWS_REGION="${LIGHTSAIL_REGION}" /usr/local/bin/tokenkey-restore-edge-env-secrets.sh \
  "${restore_secret_args[@]}"
set -a; . "$SECRET_FILE"; set +a

cat > /var/lib/tokenkey/.env <<ENVEOF
API_DOMAIN=${API_DOMAIN}
SERVER_FRONTEND_URL=https://${API_DOMAIN}
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
GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED=true
ENVEOF
chmod 0600 /var/lib/tokenkey/.env

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  GHCR_PAT="$(aws --region "${LIGHTSAIL_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text)"
  echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin
  unset GHCR_PAT
else
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
