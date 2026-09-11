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
CADDY_GZB64='H4sIAAAAAAACA61VbW8TRxD+bP+KUcIHqLB9jhOEqFBxQwKWAkRJKtpPx/pubW9zvr3u7hmMFSkgEghNRIoCKi8SrXgroPJSUZEmRkj9KdRnO5/yFzp7F9s4pagfKiW2d3b2mZlnZp8dBMVnqTtLq/DnG8ienoYxu0hhWhH8NLRtlNh2tcAcGh+EUYf7diLnMgW+pBKoW5F+XioocAF7atnJnHn01Ils7uTcfr0cPTFmjuFyApfEtRFgT03vmseyM2Ons9+Y2YmJU6fHjpqjuaNTc0mYog6pgkdUSQIRFFyuwPPzDrN0OIHhlMB9zlwlkzodKpTUWzAzMZ3ITkyeTBhp2MtdGB7O7Pscjs/MTGoLk2AzSfIOtSFPLaJPqBKFCVYsKUmYg1gFJuhZ4jgwS6knO2E9LhQcNMByuKR2Mh6vxWO0jCf6y4vHBoFYZWpaBEpKefJQKqXXCYk8MreYqBhDSeKxpEOVpK4lqp5KclFM2RjVUlxU43NxTCLk3vekEpSUQbrM86jart9uLs037y4F11Yam/eDKy+7PTt00MDkWrcvQd7h1iwEC68aG8/a72411ucbm2+3FlZab59v15cReuvCu2BhBc6UsBEOhSNYKj9LbVNozs9AcH0ZCmjLE4TpONXmcGP1UmvzdfPqgwgyeFFvX37drF9sPd78a/4iAjfX/sCAW5eXg5ev8HdjY7Gxjs5PIQLZrt/xBLd7YwTBnZ8a6xvBtacaEf269UY1NH/7ubX2S6d4HWOvmjU7TvsAOyBohQpJTQQ+V91FBm7HCo4vSyZOCRUV4kAijbZBCC48DDbeNO/PN+89bP16s3XjYbC6DP3JIdfEUqxCof37S0iVKHFUabt+xRaEuQlyFqdyu76kGdWI07ljX01PpeH94vWO7+ERIwPt508iSNgBaz37Priy2bx6tVds84cft249QHZsLJqKEC+CsEoUadAc7GCmHI2x08AwkzBidBAExSETSvNMpNSeSG37+YMQccQYSo0Yw1hB88ZC89K1xvpKsPoMzZn382v411pdbK29ar941H584R9ULLUfLbbu3Gzdvdd6stG8e0/3IhaLcjJ9wTr59Yxdykdkz6hYmXJfQUbbCnh3TNsXRDG8pqFbmZwztVmC7pPvRseqJpalfKlLwP9h3HLyJt7/3umMIXvWbuj0TmibCpwaSIyOJ0a56+I10xcxN9m3+3ViCqOhFWqClrmiZolLNbfLZ5wL7LxNbf3rP3pOCq441CR2s0z/3e04gkCtA6UEcWWoOVpFwlmO2Yw4XQ5DwmJaokg4FOkho99iMrxzpoUFSzigSYtZvOzhjEjNGC8U0ISR5rTe9Am2DobKxG0K56WyoXieefF47IjuAgqhTkULM6RwyFDEUp91DQ4v8qTnFruGAqkwzCCJHzrSTuXQgfpilCApuilKcAcGIqndDzgHCXx1DmfSI5kDhmHsB1Yu+0rL9gBmonBAwiw0sRbHFVrSyaHoK4M7TEof42jhjaj7qFJrRsO3wNQcm1YJVY+6RdohBmsONdGM3qFe4ZX0B0WzMqb6IQsVZlO+ex06hJB9cvs/gMZ2hpB5n3xTo+gf1fwwCVYOp+0Dge0/sZsJHCSPuzYMUP1Sie57rZ9YLUSCWYraAzBsZPqAPhVL0O98PGrmuR0lpQVBsvM43YZx4svICYcs3MNL4OE9wAnFX1pPuCgTBd9K7motwIfBgdzJ8VPRiP8NBi2old4IAAA='
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
