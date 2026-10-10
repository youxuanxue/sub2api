#!/bin/bash
# tokenkey Prod Hetzner bootstrap — generated; do not hand-edit.
set -euo pipefail
exec > >(tee -a /var/log/tokenkey-hetzner-bootstrap.log) 2>&1
echo "HETZNER_PROD_BOOTSTRAP_START $(date -u +%FT%TZ)"

: "${INSTANCE_NAME:?INSTANCE_NAME required}"
: "${API_DOMAIN:?API_DOMAIN required}"
: "${ACME_EMAIL:?ACME_EMAIL required}"
: "${MAIN_GATEWAY_ALLOWED_CIDR:?MAIN_GATEWAY_ALLOWED_CIDR required}"
: "${TOKENKEY_IMAGE:?TOKENKEY_IMAGE required}"
: "${SSM_REGION:?SSM_REGION required}"
: "${SSM_ACTIVATION_ID:?SSM_ACTIVATION_ID required}"
: "${SSM_ACTIVATION_CODE:?SSM_ACTIVATION_CODE required}"
: "${VOLUME_ID:?VOLUME_ID required}"
: "${VOLUME_MOUNT:=/var/lib/tokenkey}"
: "${GHCR_PAT_SSM_NAME:=}"
: "${GHCR_PULL_USER:=}"
: "${ALLOW_SECRET_GENERATE:=false}"
: "${AWS_ACCOUNT_ID:=}"
: "${TOKENKEY_PGDUMP_S3_URI:=}"

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
  ca-certificates curl gnupg openssl gzip gettext-base unzip e2fsprogs

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

# Mandatory Hetzner Volume → /var/lib/tokenkey (attach before first boot).
vol_dev="/dev/disk/by-id/scsi-0HC_Volume_${VOLUME_ID}"
echo "waiting for volume device ${vol_dev}"
for _ in $(seq 1 60); do
  if [ -e "${vol_dev}" ]; then break; fi
  sleep 5
done
if [ ! -e "${vol_dev}" ]; then
  echo "BOOTSTRAP_FAIL: volume device ${vol_dev} not present" >&2
  exit 1
fi
if ! blkid "${vol_dev}" >/dev/null 2>&1; then
  echo "formatting new volume ${vol_dev}"
  mkfs.ext4 -F -L tokenkey-data "${vol_dev}"
fi
mkdir -p "${VOLUME_MOUNT}"
vol_uuid="$(blkid -s UUID -o value "${vol_dev}")"
if ! grep -q "UUID=${vol_uuid}" /etc/fstab 2>/dev/null; then
  echo "UUID=${vol_uuid} ${VOLUME_MOUNT} ext4 defaults,nofail 0 2" >> /etc/fstab
fi
mount "${VOLUME_MOUNT}" || mount "${vol_dev}" "${VOLUME_MOUNT}"
findmnt -n -o SOURCE,TARGET "${VOLUME_MOUNT}" || {
  echo "BOOTSTRAP_FAIL: ${VOLUME_MOUNT} not mounted" >&2
  exit 1
}

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

mkdir -p "${VOLUME_MOUNT}/caddy/data" "${VOLUME_MOUNT}/caddy/config"
install -d -m 0755 -o 1000 -g 1000 "${VOLUME_MOUNT}/app"
COMPOSE_GZB64='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'
CADDY_GZB64='H4sIAAAAAAAC/61VbW8TRxD+bP+KUcIHqLB9jhOEqFBxQwKWAkRJKtpPx/pubW9zvr3u7hmMFSkgEghNRIoCKi8SrXgroPJSUZEmRkj9KdRnO5/yFzp7F9s4pagfKiW2d3b2mZlnZp8dBMVnqTtLq/DnG8ienoYxu0hhWhH8NLRtlNh2tcAcGh+EUYf7diLnMgW+pBKoW5F+XioocAF7atnJnHn01Ils7uTcfr0cPTFmjuFyApfEtRFgT03vmseyM2Ons9+Y2YmJU6fHjpqjuaNTc0mYog6pgkdUSQIRFFyuwPPzDrN0OIHhlMB9zlwlkzodKpTUWzAzMZ3ITkyeTBhp2MtdGB7O7Pscjs/MTGoLk2AzSfIOtSFPLaJPqBKFCVYsKUmYg1gFJuhZ4jgwS6knO2E9LhQcNMByuKR2Mh6vxWO0jCf6y4vHBoFYZWpaBEpKefJQKqXXCYk8MreYqBhDSeKxpEOVpK4lqp5KclFM2RjVUlxU43NxTCLk3vekEpSUQbrM86jart9uLs037y4F11Yam/eDKy+7PTt00MDkWrcvQd7h1iwEC68aG8/a72411ucbm2+3FlZab59v15cReuvCu2BhBc6UsBEOhSNYKj9LbVNozs9AcH0ZCmjLE4TpONXmcGP1UmvzdfPqgwgyeFFvX37drF9sPd78a/4iAjfX/sCAW5eXg5ev8HdjY7Gxjs5PIQLZrt/xBLd7YwTBnZ8a6xvBtacaEf269UY1NH/7ubX2S6d4HWOvmjU7TvsAOyBohQpJTQQ+V91FBm7HCo4vSyZOCRUV4kAijbZBCC48DDbeNO/PN+89bP16s3XjYbC6DP3JIdfEUqxCof37S0iVKHFUabt+xRaEuQlyFqdyu76kGdWI07ljX01PpeH94vWO7+ERIwPt508iSNgBaz37Priy2bx6tVds84cft249QHZsLJqKEC+CsEoUadAc7GCmHI2x08AwkzBidBAExSETSvNMpNSeSG37+YMQccQYSo0Yw1hB88ZC89K1xvpKsPoMzZn382v411pdbK29ar941H584R9ULLUfLbbu3Gzdvdd6stG8e0/3IhaLcjJ9wTr59Yxdykdkz6hYmXJfQUbbCnh3TNsXRDG8pqFbmZwztVmC7pPvRseqJpalfKlLwP9h3HLyJt7/3umMIXvWbuj0TmibCpwaSIyOJ0a56+I10xcxN9m3+3ViCqOhFWqClrmiZolLNbfLZ5wL7LxNbf3rP3pOCq441CR2s0z/3e04gkCtA6UEcWWoOVpFwlmO2Yw4XQ5DwmJaokg4FOkho99iMrxzpoUFSzigSYtZvOzhjEjNGC8U0ISR5rTe9Am2DobKxG0K56WyoXieefF47IjuAgqhTkULM6RwyFDEUp91DQ4v8qTnFruGAqkwzCCJHzrSTuXQgfpilCApuilKcAcGIqndDzgHCXx1DmfSI5kDhmHsB1Yu+0rL9gBmonBAwiw0sRbHFVrSyaHoK4M7TEof42jhjaj7qFJrRsO3wNQcm1YJVY+6RdohBmsONdGM3qFe4ZX0B0WzMqb6IQsVZlO+ex06hJB9cvs/gMZ2hpB5n3xTo+gf1fwwCVYOp+0Dge0/sZsJHCSPuzYMUP1Sie57rZ9YLUSCWYraAzBsZPqAPhVL0O98PGrmuR0lpQVBsvM43YZx4svICYcs3MNL4OE9wAnFX1pPuCgTBd9K7motwIfBgdzJ8VPRiP8NBi2old4IAAA='
PRUNE_B64='H4sIAAAAAAAC/7VV627iRhT+76c48WYDSdfYRKutlgSkKCUpSpasAq3abio02Mcwwh67M2MSmiD1IfqEfZKe8YXAhk2bVisZCc+cy/d95+JXO+6YC3fM1NR6BcNkhuICFzDQbIIe/PXHn5DKTCBEic8iOP/+9BpYmgKP6R7ISAELNUpA5k/BT+I0UQhpFkVQ50LQhfIlT/V+g6IPUM4xgFAmMQwGH6DRaLiTqS+dPEVj/O4t1AkIvnu7f0QJWUDuTIObKenm+XOo2mCc4aLwcvIABMkxYBo5ixC1P0VCJgLAO/QV6ClXEPIIG3CBmJoDhDhRGiT6KDRcdLsfR/2C0HhR0juVyDQB1jzGN5BGmQKco1xQCjKEMJF5nMKYwAguJsAFySA044a8XYG1ib9RIMmkjy0IMI2Shctulatypf+JVV4JiaFE+j/nbEuEccajwPFDQeZG7atU80SwqAXDq4tu/6L788iUb5RTHZ6cDwxUd85IWz5e5XcbKOZQDzBkWaSh6VHlLIUaHMwSSHlKFzyyrEKwtr17/4XoLafpLW2r2/9xdNa77Lbt7alsy+IhfIIdcEKgaJX90oZfj4y8wgJAf5o8arlNohbEXCmj/0aIzt6hcb/jGjwr5BbJoqYYRdQe/gwCrtg4wvbgtOm99wqWzGpswsiPv2ElTOd3WKfc+3By3m05Lwa7GQAwTvViC9rr7ser9pN8r1sHBGsNjjEzEODhgc4eD9pPsb4YqZ9kUQAi0ZAySaMtMU2KCd4MvA39D/1+r38+6n3XtnO8QeLPaCi4UCn6GqrE0HEDnLvCLI3Dzl5zhW89wG79M29ql9r9faNnhm+5rK2i7dt5cpVIGl3jF8806UvHM5r8cONES5ZCTcZl7xU+JBH9z42Xdg26P/WGlnU7peVB5FkAjjSDeERsCGJZAjow+u/t5bPPRYZ059Mqq+5o1A5qxyIR2Kkd7K+s4OgIUDHfWJst82We5S4yTMuQh2uyUeHzYtaa77/1HK9Jz9DzWvnzS42YAnAePKPiM4G1zDCPkEouNNm/Vje6/AnjlCO3K297lzLZVkBU4RiOVwlNndRaczoO7c+Y1rvJf01dpbhO5GK5bNH7kE0MoG1I4IagPMDl6ejk8rJ9CqZo4GiKXK8A3miiDM6s+aYpobNWWMtq5e9lcauy9s4G7SfuVa1HPtEyFTcKblb9nk6L4f+s8sVsipztqocru0+lRMV4bt6vWr8azkLTzjpooA9ZKe8GtRdxGf17MjNaAUK3vRUvtTYfG5BLy936rQ9OlMOrzEzxwAmgBrX9ggEUq6rwMf0wMcNyX3xXKhhjQj4rEu/AhHYPOGd3vz222ir+Wqf8fw2/5pj/RyblFMmYPzOl67S+wuRZVrnc/wZ6n3lINAoAAA=='
GHCR_DAILY_B64='H4sIAAAAAAAC/5VWf08jNxD9fz/FEBAkVGYTTrQq3KJykPshruVEOFVVlEbGO9lY2dgr25tcjqOfvWNvluR6hIJAJMyOn2fevJnZ7a34Vqr4lttxtA0XXOYLePf+/Bp4UTDHMyhMqRB+gpSrLJcqgwstJmhATnmGIHLkqiwO6OwHNdMTTOF2AY6+qAkuWDYWhgUElnroAyendFYrgtUpdM8PgasUPsps7Cw5AKYZWo/W613dHEOKRa4XMZ/b2FIw2I43Q1MCFh0wLDUUssARWaNIKjqY50PvPgzuw+A+LJV0ttmCuwgg14LnYBfW4TQdptIkjZ27zjGL0Yl4aV5+3jfIfwkKLAU2hfYvR0dAB9bOBy/BHZz+x/5U/GhmUmADXr/e61693Yv6nynEQXSBVhhZOKlVsrE+nsVSlZb4f6w+0dnIoUkUurk2E6YVVRIPHDcZMVaHVEcQ/cmVs487R1G/V3kNoptFgYlWaMfaRV01k0arKSr3VuaYsHjGTZzL24eM4wNUs6j7BUWPsFwSl5YcPPNBgU8Wlvh4OaFBa8+ls6Kxmes5c4aPRlLAXKpUz1uU8o1HGkRX6pznqFJukn1GP9A+Om636Te6Jv71VH7F9AJzvuihSF61p1JFn9BYSdEqlzhTImF9qLQzCCxj+maRhDhtTbBP9T6KZjyXKXc4nCAWQyr091r1VhJpJ6hxBFvQ73sJevN9A5J/4O9+h/066Lfpz/4ODAYn4MaoyNsjZBkJhLnNfQoNqUIEcHN12f3jsvvX0LM0vOx2Pw1vzt71kvqugGjQlUZBh/4ZSR89If3QcY/Ff7cB/pi9CtCVcwCpW3KDaqrgQx7LzrBMaIOknjUkPxNKgzZpk+lHitco/PZtPSuiuA/sK1Qx0OMB7O6SaQvYF28Mt3vzGs1V1C+I1ws9EFjftxH6WRW0vsvgIbfvKxY0Q/24sb4rJlYxrEXwzBi8BVZTytOPaWOJ8FCMnWaz/k6LptNqBYdABOYWn5+y0suLqhav53R95ZMXhuu24RqnekbzlKZ7mKAWlHYkhREaVKJabxyEVo7TVDTQ9I9pRC5WC5IGAPVz6yDg/c6dGBOKwSmmktTG/IpjqbQTIBO1vBKLekyfkJvIuZxauC1lnsaC02EI0rSlGAO3ATTTOV0WF9q6jHI47vwM8zFRG6rtQ6A6ARGh/HeiQEygKXia0iYtiodjsaGIqjiDHNL1xVHxyPgITuMUZ7EqiZHD093OS2W4AXVdCv9fl9APJMT6KcmSZQ7aL++K6l4IbxO+J1aIj0yyZ6eosOHH3pQ0sRxzgltcjQtZRciYxZw2sXWtDdPnqXkIJydLlKWuWXiHacHT7zj1sb29Fjw2l+vn+y1AMdbQ2JjpMb1iTJSeK6BVVfpVD7SB4HT38OSBuAoNLReekLpub85674e9q8/X591+e+A3FFnb60PNU0e23xoR8f4vCheAbZUKAAA='
PGDUMP_B64='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'
DISK_METRICS_B64='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'
RESTORE_SECRETS_B64='H4sIAAAAAAAC/7VXbVPbRhD+rl+xGBdwqCLboWkbV854EiWTloDHdkoyhHgOa2Vp0Bt3JxMCzm/v3kmyZQwx+VA+BO50u/vs7rPPXba3rExw6zyILYxncM6EbwiUYGKWQBqk6LEgNIx+b9B774ycgV2rGccfRv0PI/XXwHn77vjIrtVveifDcb56Year186b3ofD0WJ3Pq8ZvcPD45PxW+fIGfRGju2xUKBhXPlBiHAKtfp2DcyphCacdcBNDIAJE0j7rRoEMS0BTDNlnEUokTeggqp+06YQtQ4IP/AktKHTKc4nmUwz2YAS9cMnWRgmV+YUY+RMYgPuoJU8w9KqsHnSAJz4CdQ4CplwNNGd0j/xzBQ44SjFC8jiizi5ioHxaRZhLEEl091pdwC/BhIOmrkvFGxiuElM5ThVlbhZ5Davgf0dvlgyucD4Aq+tPRXk1kf5jYA2rFNmfmuaf5pn+5aQbIpNqxLfPGeTiyytw9kZ3N7CjYq0AfA5c2FRYw1VGeVYjXmBLi+mggbWk9L5RtfsXCRhJhHynlAY6QPHyyzg6K5WpQM6lBmraDmFKNpjAxH/yO80SOIfuDdmLAxc6vQ4txt7RMO9hi5SmExYqPHZin20Q1g8hUXtKSSws0N7W2AeruwSPHKV8Rha2qhW32NXF7DrHL0m3CkPiAFHA9gnjs93l5YNIj5ewrM1DzkQajtMkoxsCXOGlEWKE4nuOMR4Kn065yVcnwpi6B8PR28HznDc7w2HJ8eD1/D3yWg8dF4NnBGMjkf9sXP0avCpP6Kajv9xPhWjBnkEu0BsvrHBnCmfamToF6W3W2+Bbes4lH0ee6/ebEC3XNCnBiXXoly1t/19mEM19zyL9fQ1AE0tfWKe16O1Vg/IK/BIlGVUkZ0LyQnqr3eAthv34Qi8HEruz76noqRP0sf4biPsgz+IYiRqax+eH3TAC/IstW+dRjHbpzS/zPTO9vWcamLRie3yiKoEre+4vIduWj+ASFmWRpVi6ztYX/bWUrhdkuL2HlI06pZyXunxX/Zi1Sp7TFphtxYd1rOl5OMltfcFHaiWlqTD0HU1cUVAdA6nxRgtd/P6Grrda0O6crSQtc3CRvCEDOIp5DugPQWCRkbHKKWuFDtV0Lm6gvwocaH5vNmshl0IqYoD6uos4gALOTL3mpiHQuk9aZCfCFkrVbRpEA8MUljMh80NeExiW3VONAxiEvMwBNMFM4Lm73n03IqiS4xSZRxdqL8qn6ynpZvt7SfW/OlH/aM8IucJ1+V7lKE+XjFfLYMyptorVi7cEqxJiCzO0kJEqd2TJIpY7KoZFT6pMHQtF2dWnFFq7e5Oa9FmKL6b2cPuyWJprZgfFZr8ABrQo6idbz5KTZkbkrMUiiTA+fhupDnLrgS9Dor7pHodCRHBFOXySQKfDfWQKPtZvcNN8yqQvukSS65TqTzlZy8z5NfQLz08/VcLfPlsAYlfJXSXwNvdO9A3Dkph+OgxGQ7fF7cMzUbEQrpZIlybjnYxHdFsrbCVEfnZ2SlQueDxJFJIiLYhdWDKMSVBu4TdRaGOEvmGbgp3Fx4qSKniq884wrhlg3rLVURmc1WWLVZVCYRQQqKYXTwXVUPpS5xIYJn0Ex58Wy/abznPgMRx2VJa6kvKo+TuirT9i/gcqwz3khRjIULgepp8/ArtA3o0dO/1s5T2Hzl41n7QwT0Xwk96egQbN9a8fIm7S23luCLX5XvuWed/IOMyvHpceQEX9GLlySwQ1GxFTK0tjxkndSGA+o8Uuh1aeJmoXEM8kZo+JVkknQNzAq1m+2BNAauv8QN1kfwHEOWuo8ANAAA='
printf '%s' "$COMPOSE_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/docker-compose.yml
printf '%s' "$CADDY_GZB64" | base64 -d | gunzip > /var/lib/tokenkey/caddy/Caddyfile.template
envsubst '${API_DOMAIN} ${ACME_EMAIL} ${MAIN_GATEWAY_ALLOWED_CIDR}' \
  < /var/lib/tokenkey/caddy/Caddyfile.template > /var/lib/tokenkey/caddy/Caddyfile

printf '%s' "$PRUNE_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
chmod +x /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh

printf '%s' "$GHCR_DAILY_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-ghcr-prune-daily.sh
chmod +x /usr/local/bin/tokenkey-ghcr-prune-daily.sh
/usr/local/bin/tokenkey-ghcr-prune-daily.sh --selftest
/usr/local/bin/tokenkey-ghcr-prune-daily.sh --install-units

printf '%s' "$PGDUMP_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-pgdump.sh
chmod +x /usr/local/bin/tokenkey-pgdump.sh
mkdir -p /var/lib/tokenkey/pgdump

printf '%s' "$DISK_METRICS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-disk-metrics.sh
chmod +x /usr/local/bin/tokenkey-disk-metrics.sh
/usr/local/bin/tokenkey-disk-metrics.sh --selftest

cat > /etc/systemd/system/tokenkey-disk-metrics.service <<'DMSEOF'
[Unit]
Description=tokenkey PROD on-box disk-full and memory-pressure Feishu alerts
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
DMSEOF

cat > /etc/systemd/system/tokenkey-disk-metrics.timer <<'DMTEOF'
[Unit]
Description=Fire tokenkey PROD disk/memory pressure alerts every 5 minutes

[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
RandomizedDelaySec=30
Persistent=true

[Install]
WantedBy=timers.target
DMTEOF

cat > /etc/systemd/system/tokenkey-pgdump.service <<'PSEOF'
[Unit]
Description=tokenkey pg_dump (every 2 hours)
After=tokenkey.service
Requires=tokenkey.service

[Service]
Type=oneshot
Nice=19
CPUSchedulingPolicy=other
CPUQuota=40%
IOSchedulingClass=best-effort
IOSchedulingPriority=7
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-pgdump.sh
PSEOF

cat > /etc/systemd/system/tokenkey-pgdump.timer <<'PTEOF'
[Unit]
Description=Run tokenkey-pgdump every 2 hours

[Timer]
OnCalendar=*-*-* 00/2:00:00
Persistent=true
RandomizedDelaySec=2min

[Install]
WantedBy=timers.target
PTEOF

printf '%s' "$RESTORE_SECRETS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-restore-edge-env-secrets.sh
chmod 0755 /usr/local/bin/tokenkey-restore-edge-env-secrets.sh

SECRET_FILE=/var/lib/tokenkey/.env.secret
restore_secret_args=(
  --parameter "/tokenkey/hetzner/prod/stage0/env-secrets-backup" \
  --output "$SECRET_FILE"
)
if [ "${ALLOW_SECRET_GENERATE}" = true ]; then
  restore_secret_args+=(--allow-generate)
fi
AWS_REGION="${SSM_REGION}" /usr/local/bin/tokenkey-restore-edge-env-secrets.sh \
  "${restore_secret_args[@]}"
set -a; . "$SECRET_FILE"; set +a

# Resolve pgdump S3 URI (Hybrid IAM allows prod/pgdump/* on this account bucket).
if [ -z "${TOKENKEY_PGDUMP_S3_URI:-}" ]; then
  if [ -z "${AWS_ACCOUNT_ID:-}" ]; then
    AWS_ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || true)"
  fi
  if [ -n "${AWS_ACCOUNT_ID:-}" ] && [ "${AWS_ACCOUNT_ID}" != "None" ]; then
    TOKENKEY_PGDUMP_S3_URI="s3://tokenkey-prod-pgdump-${AWS_ACCOUNT_ID}/prod/pgdump"
  fi
fi

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
TOKENKEY_PGDUMP_S3_URI=${TOKENKEY_PGDUMP_S3_URI}
ENVEOF
chmod 0600 /var/lib/tokenkey/.env
if ! grep -q 'QA_CAPTURE_ENABLED=' /var/lib/tokenkey/docker-compose.yml; then
  sed -i '/^      - SERVER_FRONTEND_URL=/a\      - QA_CAPTURE_ENABLED=${QA_CAPTURE_ENABLED:-false}' \
    /var/lib/tokenkey/docker-compose.yml
fi
if ! grep -q 'QA_CAPTURE_ENABLED=' /var/lib/tokenkey/docker-compose.yml; then
  echo "BOOTSTRAP_FAIL: failed to insert compose QA_CAPTURE_ENABLED mapping" >&2
  exit 1
fi

if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then
  if GHCR_PAT="$(aws --region "${SSM_REGION}" ssm get-parameter \
    --name "${GHCR_PAT_SSM_NAME}" --with-decryption \
    --query Parameter.Value --output text 2>/dev/null)" && [ -n "${GHCR_PAT}" ]; then
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
Description=tokenkey prod hetzner stack (docker compose)
Requires=docker.service
After=docker.service network-online.target local-fs.target
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
# Host parity with AWS prod / edge HZ: dump + disk/mem Feishu + ghcr prune.
# QA maintenance/boundary + live QA_BUNDLE_* stay off until Wave B cutover
# (staging must not point empty DB at live SQS). Feishu webhook env still
# comes from post-boot sync (copy from AWS prod .env or sync-feishu-config).
systemctl enable --now tokenkey-pgdump.timer
systemctl enable --now tokenkey-disk-metrics.timer
systemctl enable --now tokenkey-ghcr-prune-daily.timer
sleep 30
docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true
echo "HETZNER_PROD_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
