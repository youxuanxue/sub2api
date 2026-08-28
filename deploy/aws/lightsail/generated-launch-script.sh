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
