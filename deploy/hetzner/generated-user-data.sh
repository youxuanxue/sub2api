#!/bin/bash
# tokenkey Edge Hetzner bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-hetzner-bootstrap.log) 2>&1
echo "HETZNER_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${EDGE_ID:?EDGE_ID required}"
: "${INSTANCE_NAME:?INSTANCE_NAME required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${SSM_REGION:?SSM_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"
: "${ALLOW_SECRET_GENERATE:=false}"

case "${ALLOW_SECRET_GENERATE}" in
  true|false) ;;
  *) echo "BOOTSTRAP_FAIL: ALLOW_SECRET_GENERATE must be true or false" >&2; exit 1 ;;
esac

hostnamectl set-hostname "${INSTANCE_NAME}" || hostname "${INSTANCE_NAME}" || true
export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@${API_DOMAIN}}"
export TZ_VALUE="${TZ_VALUE:-UTC}"

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y --no-install-recommends \
  ca-certificates curl gnupg openssl gzip gettext-base unzip

# Ubuntu 24.04 has no awscli apt package; install AWS CLI v2 for the host arch.
if ! command -v aws >/dev/null 2>&1; then
  arch="$(uname -m)"
  case "${arch}" in
    aarch64|arm64) aws_cli_arch=aarch64 ;;
    x86_64|amd64) aws_cli_arch=x86_64 ;;
    *) echo "BOOTSTRAP_FAIL: unsupported arch for awscliv2 ${arch}" >&2; exit 1 ;;
  esac
  tmp_aws="$(mktemp -d)"
  curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${aws_cli_arch}.zip" -o "${tmp_aws}/awscliv2.zip"
  unzip -q "${tmp_aws}/awscliv2.zip" -d "${tmp_aws}"
  "${tmp_aws}/aws/install" --update
  rm -rf "${tmp_aws}"
fi
command -v aws >/dev/null 2>&1 || {
  echo "BOOTSTRAP_FAIL: aws CLI missing after install" >&2
  exit 1
}

if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker
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

# amazon-ssm-agent (arm64/amd64 deb from AWS)
arch="$(dpkg --print-architecture)"
case "${arch}" in
  arm64) ssm_deb_arch=arm64 ;;
  amd64) ssm_deb_arch=amd64 ;;
  *) echo "BOOTSTRAP_FAIL: unsupported arch ${arch}" >&2; exit 1 ;;
esac
if ! systemctl is-active --quiet amazon-ssm-agent 2>/dev/null; then
  tmp="$(mktemp -d)"
  curl -fsSL "https://s3.amazonaws.com/ec2-downloads-windows/SSMAgent/latest/debian_${ssm_deb_arch}/amazon-ssm-agent.deb" \
    -o "${tmp}/amazon-ssm-agent.deb"
  dpkg -i "${tmp}/amazon-ssm-agent.deb" || apt-get install -fy
  rm -rf "${tmp}"
fi
systemctl enable amazon-ssm-agent
if ! /usr/bin/amazon-ssm-agent -register -y \
      -id "${SSM_ACTIVATION_ID}" \
      -code "${SSM_ACTIVATION_CODE}" \
      -region "${SSM_REGION}"; then
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent -register failed" >&2
  exit 1
fi
systemctl restart amazon-ssm-agent
for i in 1 2 3 4 5 6; do
  systemctl is-active --quiet amazon-ssm-agent && break
  sleep 5
  systemctl restart amazon-ssm-agent || true
done
systemctl is-active --quiet amazon-ssm-agent || {
  echo "BOOTSTRAP_FAIL: amazon-ssm-agent not active" >&2
  exit 1
}

mkdir -p /var/lib/tokenkey/caddy/data /var/lib/tokenkey/caddy/config
install -d -m 0755 -o 1000 -g 1000 /var/lib/tokenkey/app
COMPOSE_GZB64='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'
CADDY_GZB64='H4sIAAAAAAACA61VbW8TRxD+bP+KUcIHqLB9jhOEqFBxQwKWAkRJKtpPx/pubW9zvr3u7hmMFSkgEghNRIoCKi8SrXgroPJSUZEmRkj9KdRnO5/yFzp7F9s4pagfKiW2d3b2mZlnZp8dBMVnqTtLq/DnG8ienoYxu0hhWhH8NLRtlNh2tcAcGh+EUYf7diLnMgW+pBKoW5F+XioocAF7atnJnHn01Ils7uTcfr0cPTFmjuFyApfEtRFgT03vmseyM2Ons9+Y2YmJU6fHjpqjuaNTc0mYog6pgkdUSQIRFFyuwPPzDrN0OIHhlMB9zlwlkzodKpTUWzAzMZ3ITkyeTBhp2MtdGB7O7Pscjs/MTGoLk2AzSfIOtSFPLaJPqBKFCVYsKUmYg1gFJuhZ4jgwS6knO2E9LhQcNMByuKR2Mh6vxWO0jCf6y4vHBoFYZWpaBEpKefJQKqXXCYk8MreYqBhDSeKxpEOVpK4lqp5KclFM2RjVUlxU43NxTCLk3vekEpSUQbrM86jart9uLs037y4F11Yam/eDKy+7PTt00MDkWrcvQd7h1iwEC68aG8/a72411ucbm2+3FlZab59v15cReuvCu2BhBc6UsBEOhSNYKj9LbVNozs9AcH0ZCmjLE4TpONXmcGP1UmvzdfPqgwgyeFFvX37drF9sPd78a/4iAjfX/sCAW5eXg5ev8HdjY7Gxjs5PIQLZrt/xBLd7YwTBnZ8a6xvBtacaEf269UY1NH/7ubX2S6d4HWOvmjU7TvsAOyBohQpJTQQ+V91FBm7HCo4vSyZOCRUV4kAijbZBCC48DDbeNO/PN+89bP16s3XjYbC6DP3JIdfEUqxCof37S0iVKHFUabt+xRaEuQlyFqdyu76kGdWI07ljX01PpeH94vWO7+ERIwPt508iSNgBaz37Priy2bx6tVds84cft249QHZsLJqKEC+CsEoUadAc7GCmHI2x08AwkzBidBAExSETSvNMpNSeSG37+YMQccQYSo0Yw1hB88ZC89K1xvpKsPoMzZn382v411pdbK29ar941H584R9ULLUfLbbu3Gzdvdd6stG8e0/3IhaLcjJ9wTr59Yxdykdkz6hYmXJfQUbbCnh3TNsXRDG8pqFbmZwztVmC7pPvRseqJpalfKlLwP9h3HLyJt7/3umMIXvWbuj0TmibCpwaSIyOJ0a56+I10xcxN9m3+3ViCqOhFWqClrmiZolLNbfLZ5wL7LxNbf3rP3pOCq441CR2s0z/3e04gkCtA6UEcWWoOVpFwlmO2Yw4XQ5DwmJaokg4FOkho99iMrxzpoUFSzigSYtZvOzhjEjNGC8U0ISR5rTe9Am2DobKxG0K56WyoXieefF47IjuAgqhTkULM6RwyFDEUp91DQ4v8qTnFruGAqkwzCCJHzrSTuXQgfpilCApuilKcAcGIqndDzgHCXx1DmfSI5kDhmHsB1Yu+0rL9gBmonBAwiw0sRbHFVrSyaHoK4M7TEof42jhjaj7qFJrRsO3wNQcm1YJVY+6RdohBmsONdGM3qFe4ZX0B0WzMqb6IQsVZlO+ex06hJB9cvs/gMZ2hpB5n3xTo+gf1fwwCVYOp+0Dge0/sZsJHCSPuzYMUP1Sie57rZ9YLUSCWYraAzBsZPqAPhVL0O98PGrmuR0lpQVBsvM43YZx4svICYcs3MNL4OE9wAnFX1pPuCgTBd9K7motwIfBgdzJ8VPRiP8NBi2old4IAAA='
PRUNE_B64='H4sIAAAAAAACA7VV627iRhT+76c48WYDSdfYRKutlgSkKCUpSpasAq3abio02Mcwwh67M2MSmiD1IfqEfZKe8YXAhk2bVisZCc+cy/d95+JXO+6YC3fM1NR6BcNkhuICFzDQbIIe/PXHn5DKTCBEic8iOP/+9BpYmgKP6R7ISAELNUpA5k/BT+I0UQhpFkVQ50LQhfIlT/V+g6IPUM4xgFAmMQwGH6DRaLiTqS+dPEVj/O4t1AkIvnu7f0QJWUDuTIObKenm+XOo2mCc4aLwcvIABMkxYBo5ixC1P0VCJgLAO/QV6ClXEPIIG3CBmJoDhDhRGiT6KDRcdLsfR/2C0HhR0juVyDQB1jzGN5BGmQKco1xQCjKEMJF5nMKYwAguJsAFySA044a8XYG1ib9RIMmkjy0IMI2Shctulatypf+JVV4JiaFE+j/nbEuEccajwPFDQeZG7atU80SwqAXDq4tu/6L788iUb5RTHZ6cDwxUd85IWz5e5XcbKOZQDzBkWaSh6VHlLIUaHMwSSHlKFzyyrEKwtr17/4XoLafpLW2r2/9xdNa77Lbt7alsy+IhfIIdcEKgaJX90oZfj4y8wgJAf5o8arlNohbEXCmj/0aIzt6hcb/jGjwr5BbJoqYYRdQe/gwCrtg4wvbgtOm99wqWzGpswsiPv2ElTOd3WKfc+3By3m05Lwa7GQAwTvViC9rr7ser9pN8r1sHBGsNjjEzEODhgc4eD9pPsb4YqZ9kUQAi0ZAySaMtMU2KCd4MvA39D/1+r38+6n3XtnO8QeLPaCi4UCn6GqrE0HEDnLvCLI3Dzl5zhW89wG79M29ql9r9faNnhm+5rK2i7dt5cpVIGl3jF8806UvHM5r8cONES5ZCTcZl7xU+JBH9z42Xdg26P/WGlnU7peVB5FkAjjSDeERsCGJZAjow+u/t5bPPRYZ059Mqq+5o1A5qxyIR2Kkd7K+s4OgIUDHfWJst82We5S4yTMuQh2uyUeHzYtaa77/1HK9Jz9DzWvnzS42YAnAePKPiM4G1zDCPkEouNNm/Vje6/AnjlCO3K297lzLZVkBU4RiOVwlNndRaczoO7c+Y1rvJf01dpbhO5GK5bNH7kE0MoG1I4IagPMDl6ejk8rJ9CqZo4GiKXK8A3miiDM6s+aYpobNWWMtq5e9lcauy9s4G7SfuVa1HPtEyFTcKblb9nk6L4f+s8sVsipztqocru0+lRMV4bt6vWr8azkLTzjpooA9ZKe8GtRdxGf17MjNaAUK3vRUvtTYfG5BLy936rQ9OlMOrzEzxwAmgBrX9ggEUq6rwMf0wMcNyX3xXKhhjQj4rEu/AhHYPOGd3vz222ir+Wqf8fw2/5pj/RyblFMmYPzOl67S+wuRZVrnc/wZ6n3lINAoAAA=='
RESTORE_SECRETS_B64='H4sIAAAAAAACA7VXbVPbRhD+rl+xGBdwqCLboWkbV854EiWTloDHdkoyhHgOa2Vp0Bt3JxMCzm/v3kmyZQwx+VA+BO50u/vs7rPPXba3rExw6zyILYxncM6EbwiUYGKWQBqk6LEgNIx+b9B774ycgV2rGccfRv0PI/XXwHn77vjIrtVveifDcb56Year186b3ofD0WJ3Pq8ZvcPD45PxW+fIGfRGju2xUKBhXPlBiHAKtfp2DcyphCacdcBNDIAJE0j7rRoEMS0BTDNlnEUokTeggqp+06YQtQ4IP/AktKHTKc4nmUwz2YAS9cMnWRgmV+YUY+RMYgPuoJU8w9KqsHnSAJz4CdQ4CplwNNGd0j/xzBQ44SjFC8jiizi5ioHxaRZhLEEl091pdwC/BhIOmrkvFGxiuElM5ThVlbhZ5Davgf0dvlgyucD4Aq+tPRXk1kf5jYA2rFNmfmuaf5pn+5aQbIpNqxLfPGeTiyytw9kZ3N7CjYq0AfA5c2FRYw1VGeVYjXmBLi+mggbWk9L5RtfsXCRhJhHynlAY6QPHyyzg6K5WpQM6lBmraDmFKNpjAxH/yO80SOIfuDdmLAxc6vQ4txt7RMO9hi5SmExYqPHZin20Q1g8hUXtKSSws0N7W2AeruwSPHKV8Rha2qhW32NXF7DrHL0m3CkPiAFHA9gnjs93l5YNIj5ewrM1DzkQajtMkoxsCXOGlEWKE4nuOMR4Kn065yVcnwpi6B8PR28HznDc7w2HJ8eD1/D3yWg8dF4NnBGMjkf9sXP0avCpP6Kajv9xPhWjBnkEu0BsvrHBnCmfamToF6W3W2+Bbes4lH0ee6/ebEC3XNCnBiXXoly1t/19mEM19zyL9fQ1AE0tfWKe16O1Vg/IK/BIlGVUkZ0LyQnqr3eAthv34Qi8HEruz76noqRP0sf4biPsgz+IYiRqax+eH3TAC/IstW+dRjHbpzS/zPTO9vWcamLRie3yiKoEre+4vIduWj+ASFmWRpVi6ztYX/bWUrhdkuL2HlI06pZyXunxX/Zi1Sp7TFphtxYd1rOl5OMltfcFHaiWlqTD0HU1cUVAdA6nxRgtd/P6Grrda0O6crSQtc3CRvCEDOIp5DugPQWCRkbHKKWuFDtV0Lm6gvwocaH5vNmshl0IqYoD6uos4gALOTL3mpiHQuk9aZCfCFkrVbRpEA8MUljMh80NeExiW3VONAxiEvMwBNMFM4Lm73n03IqiS4xSZRxdqL8qn6ynpZvt7SfW/OlH/aM8IucJ1+V7lKE+XjFfLYMyptorVi7cEqxJiCzO0kJEqd2TJIpY7KoZFT6pMHQtF2dWnFFq7e5Oa9FmKL6b2cPuyWJprZgfFZr8ABrQo6idbz5KTZkbkrMUiiTA+fhupDnLrgS9Dor7pHodCRHBFOXySQKfDfWQKPtZvcNN8yqQvukSS65TqTzlZy8z5NfQLz08/VcLfPlsAYlfJXSXwNvdO9A3Dkph+OgxGQ7fF7cMzUbEQrpZIlybjnYxHdFsrbCVEfnZ2SlQueDxJFJIiLYhdWDKMSVBu4TdRaGOEvmGbgp3Fx4qSKniq884wrhlg3rLVURmc1WWLVZVCYRQQqKYXTwXVUPpS5xIYJn0Ex58Wy/abznPgMRx2VJa6kvKo+TuirT9i/gcqwz3khRjIULgepp8/ArtA3o0dO/1s5T2Hzl41n7QwT0Xwk96egQbN9a8fIm7S23luCLX5XvuWed/IOMyvHpceQEX9GLlySwQ1GxFTK0tjxkndSGA+o8Uuh1aeJmoXEM8kZo+JVkknQNzAq1m+2BNAauv8QN1kfwHEOWuo8ANAAA='
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
  --parameter "/tokenkey/hetzner/${EDGE_ID}/stage0/env-secrets-backup" \
  --output "$SECRET_FILE"
)
if [ "${ALLOW_SECRET_GENERATE}" = true ]; then
  restore_secret_args+=(--allow-generate)
fi
AWS_REGION="${SSM_REGION}" /usr/local/bin/tokenkey-restore-edge-env-secrets.sh \
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
QA_CAPTURE_ENABLED=false
ENVEOF
chmod 0600 /var/lib/tokenkey/.env
# Edge Stage0 compose does not map QA_CAPTURE by default; inject like Lightsail SSM deploy.
if ! grep -q 'QA_CAPTURE_ENABLED=' /var/lib/tokenkey/docker-compose.yml; then
  sed -i '/SERVER_FRONTEND_URL=/a\      - QA_CAPTURE_ENABLED=${QA_CAPTURE_ENABLED:-false}' \
    /var/lib/tokenkey/docker-compose.yml
fi

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  if GHCR_PAT="$(aws --region "${SSM_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text 2>/dev/null)" && [ -n "${GHCR_PAT}" ]; then
    # Stale/invalid PAT must not abort bootstrap; anonymous pull often works for public images.
    if ! echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin; then
      echo "GHCR docker login failed; continuing with anonymous pull for ${TOKENKEY_IMAGE}"
    fi
    unset GHCR_PAT
  else
    echo "GHCR PAT unavailable at ${GHCR_PAT_SSM_NAME}; anonymous pull for ${TOKENKEY_IMAGE}"
  fi
else
  echo "GHCR_PAT_SSM_NAME unset; anonymous pull for ${TOKENKEY_IMAGE}"
fi

cat > /etc/systemd/system/tokenkey.service <<'UNITEOF'
[Unit]
Description=tokenkey edge hetzner stack (docker compose)
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/var/lib/tokenkey
EnvironmentFile=/var/lib/tokenkey/.env
ExecStartPre=-/usr/bin/docker compose --env-file /var/lib/tokenkey/.env pull
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
echo "HETZNER_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
