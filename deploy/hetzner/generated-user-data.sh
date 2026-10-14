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

# Ubuntu ships apt-daily-upgrade.timer enabled (OnCalendar=6:00, RandomizedDelaySec=60m)
# with APT::Periodic::Unattended-Upgrade "1" from the installer image. On an edge relay
# that window is 14:00-15:00 CST — peak traffic — and a glibc upgrade's postinst runs
# `systemctl daemon-reexec`, which restarts systemd-networkd/resolved and severs every
# in-flight upstream TLS connection inside the containers. Automatic-Reboot stays false,
# so the host also never activates what it installed: patched on disk, unpatched in
# memory. Patching is a release here, performed blue/green on the standby side with a
# real reboot. mask (not disable) survives package upgrades and systemd presets.
systemctl mask apt-daily-upgrade.timer >/dev/null 2>&1 || true
systemctl stop apt-daily-upgrade.timer >/dev/null 2>&1 || true
# Stopping the timer does nothing to a service instance it already launched, and the
# image's timer can fire during first boot. Without this, the apt-get calls below would
# contend for the dpkg lock and — under `set -euo pipefail` — abort the whole bootstrap.
if systemctl is-active --quiet apt-daily-upgrade.service 2>/dev/null; then
  echo "bootstrap: apt-daily-upgrade.service was mid-run; stopping it"
  systemctl stop apt-daily-upgrade.service >/dev/null 2>&1 || true
fi
# apt-daily.timer stays enabled: it only refreshes package lists (no unit restarts),
# which is what lets the patch-debt probe report what is pending.
cat >/etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade <<'APTEOF'
// Owned by deploy/hetzner/render-bootstrap.sh. Patching is a blue/green release.
// Enforced by scripts/preflight.sh (fleet apt-exposure check) — do not re-enable here.
APT::Periodic::Unattended-Upgrade "0";
APTEOF
# The `|| true` above keeps bootstrap idempotent, but a silently-failed mask would
# provision a host that looks healthy and is still fully exposed, with nothing
# anywhere to notice. Assert the end state instead of trusting the command.
if [ "$(systemctl is-enabled apt-daily-upgrade.timer 2>/dev/null | sed -n 1p)" != "masked" ]; then
  echo "BOOTSTRAP_FAIL: apt-daily-upgrade.timer is not masked; host would auto-install packages at peak" >&2
  exit 1
fi
# Belt-and-braces for any apt invocation that still races a stray dpkg holder.
cat >/etc/apt/apt.conf.d/99tokenkey-lock-timeout <<'APTEOF'
// Wait for the dpkg lock instead of hard-failing a bootstrap mid-provision.
DPkg::Lock::Timeout "120";
APTEOF

apt-get update -y
apt-get install -y --no-install-recommends \
  ca-certificates curl gnupg openssl gzip gettext-base unzip jq

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
CADDY_GZB64='H4sIAAAAAAAC/61VT28TRxQ/25/iKeEQKmyvcUAoVVVcx4ClkERxKsppGe88e6fZ3dnOzDoxVqQUQSkViKriUuihVSu1olILFVIpCULqV6mdwImvwJvd2Ikjinroxd557837+3u/mQYj1zBawx78/SdULzWhzjsITcPo17GyGuO81xYB5qehFsiEFxqRMJBo1IBRVyctbaAtFRzrV5cb7vzSxWpjcfOEPdYu1t06HRfoyCJODo71rdY9X12tX6pedqsLC0uX6vNurTG/slmEFQxYD2JmfA1MIUTSQJy0AuHZcIrCGUV6KSKjizYdVEZbFawuNAvVheXFglOGGRnB7Gzl+PtwYXV12UqEBi40awXIoYUeszeMj7AgOr7RTATkqy0UrrMggDXEWI/CxlIZOOOAF0iNvJjP9/M5DOnGZHn53DQwL0TXY+AbE+u5UsmeC5r6KKJOoeucLLJYFAM0GiNP9WJTlKpT4hTVM1L18pt5SiLtfRJro5CFoCMRx2he79zfvbW1+92t4d07g+0fh18+Gs9s7oxDye3dvw6tQHprMLzxePDs15cvvh083RpsP391487e899e79wm168+fzG8cQeu+DSIAOEslSrXkbvK9vwKDL+5DW2StRi5GRn1N0nx9fW97Se7X/2UuRz+vvPy5pPdnWt7P2//s3WNHO/e+4sCvrp5e/joMX0Pnn0xeErGDyFz8nrnQawkP4ARDB98P3j6bHj3ofVIduN6sxp2//hh794vo+JtjBmz5o6MjgNNQGEXlUaXHG/0jjSD1Ll2kGjfJZSg6rIACmWSTUPTJ0wRAHqAts2UHbQVah/S/FpSGgrB4jko+cgC44Ncjwg4iomoRKG5iFAT7KyrRUko1Vp0ETwpA06W6Qow0DRtqtG2ESM+RxIvEIRbqjKQjAMFRANSpW5aiU6dwqmNDQgT2iOLeJ5uQRLZyRi0Vz5LUBPQZzQiTDbzuM0nS9dNlBilfiAcN+GUPhAaEaJMDFSsLGi5tFUuTxQzglan4hySjq+X969zVDQLKNTOFWoyigi8Ft6N5QntJ4UVCkRS6CsMpUHXl9psHrE5J9U6Uxy5/fqPlstKGgl97fkY4r+bXSAn0B+5oqlGOt1ku5spQnJcsGDch7Q3Obv4LLAzLZ90JiWuICS7HhWs4fSsVXkyjGmW2nZMttskokibdosnaNAGS+FCSy85zkGzWYd05Q7m3bkqYjC4YUoE68gU9rdhpj5/vu4267WPVxqrl4shp1nnc2eJUQwRky3CEiWUCIdEKqX3xoJAdmQxjjpjQZt1BeVepB+b437PYOTqwxqjdtpxGiUDmMqo7wSEbKNAr8AHlfKpymnHcU6ACMPEWBqdokxMoLMs7Eg8SSeSlIsns78KaYTWCcWxRJg1/a3MaWeRcrNrp+N6PrEQRh0ctZRqThvmZu/CQeHd8qGiRUipHu5CV3CUR8+pQepygv7+B6e5ffjSKN/1xmXR38rBaRIiTHF6iPAmbxztBEEwlkRkUymlqfH7aZ880hklPKKQKZh1KhOO3hVrn2/cluRZUgQEV4urtBeOc/GjzIhAlupofWLaIG04fVnqlSpkBj7VMrIsQogOoLF4bilbjjeaF9NPbggAAA=='
PRUNE_B64='H4sIAAAAAAAC/7VV627iRhT+76c48WYDSdfYRKutlgSkKCUpSpasAq3abio02Mcwwh67M2MSmiD1IfqEfZKe8YXAhk2bVisZCc+cy/d95+JXO+6YC3fM1NR6BcNkhuICFzDQbIIe/PXHn5DKTCBEic8iOP/+9BpYmgKP6R7ISAELNUpA5k/BT+I0UQhpFkVQ50LQhfIlT/V+g6IPUM4xgFAmMQwGH6DRaLiTqS+dPEVj/O4t1AkIvnu7f0QJWUDuTIObKenm+XOo2mCc4aLwcvIABMkxYBo5ixC1P0VCJgLAO/QV6ClXEPIIG3CBmJoDhDhRGiT6KDRcdLsfR/2C0HhR0juVyDQB1jzGN5BGmQKco1xQCjKEMJF5nMKYwAguJsAFySA044a8XYG1ib9RIMmkjy0IMI2Shctulatypf+JVV4JiaFE+j/nbEuEccajwPFDQeZG7atU80SwqAXDq4tu/6L788iUb5RTHZ6cDwxUd85IWz5e5XcbKOZQDzBkWaSh6VHlLIUaHMwSSHlKFzyyrEKwtr17/4XoLafpLW2r2/9xdNa77Lbt7alsy+IhfIIdcEKgaJX90oZfj4y8wgJAf5o8arlNohbEXCmj/0aIzt6hcb/jGjwr5BbJoqYYRdQe/gwCrtg4wvbgtOm99wqWzGpswsiPv2ElTOd3WKfc+3By3m05Lwa7GQAwTvViC9rr7ser9pN8r1sHBGsNjjEzEODhgc4eD9pPsb4YqZ9kUQAi0ZAySaMtMU2KCd4MvA39D/1+r38+6n3XtnO8QeLPaCi4UCn6GqrE0HEDnLvCLI3Dzl5zhW89wG79M29ql9r9faNnhm+5rK2i7dt5cpVIGl3jF8806UvHM5r8cONES5ZCTcZl7xU+JBH9z42Xdg26P/WGlnU7peVB5FkAjjSDeERsCGJZAjow+u/t5bPPRYZ059Mqq+5o1A5qxyIR2Kkd7K+s4OgIUDHfWJst82We5S4yTMuQh2uyUeHzYtaa77/1HK9Jz9DzWvnzS42YAnAePKPiM4G1zDCPkEouNNm/Vje6/AnjlCO3K297lzLZVkBU4RiOVwlNndRaczoO7c+Y1rvJf01dpbhO5GK5bNH7kE0MoG1I4IagPMDl6ejk8rJ9CqZo4GiKXK8A3miiDM6s+aYpobNWWMtq5e9lcauy9s4G7SfuVa1HPtEyFTcKblb9nk6L4f+s8sVsipztqocru0+lRMV4bt6vWr8azkLTzjpooA9ZKe8GtRdxGf17MjNaAUK3vRUvtTYfG5BLy936rQ9OlMOrzEzxwAmgBrX9ggEUq6rwMf0wMcNyX3xXKhhjQj4rEu/AhHYPOGd3vz222ir+Wqf8fw2/5pj/RyblFMmYPzOl67S+wuRZVrnc/wZ6n3lINAoAAA=='
GHCR_DAILY_B64='H4sIAAAAAAAC/5VWf08jNxD9fz/FEBAkVGYTTrQq3KJykPshruVEOFVVlEbGO9lY2dgr25tcjqOfvWNvluR6hIJAJMyOn2fevJnZ7a34Vqr4lttxtA0XXOYLePf+/Bp4UTDHMyhMqRB+gpSrLJcqgwstJmhATnmGIHLkqiwO6OwHNdMTTOF2AY6+qAkuWDYWhgUElnroAyendFYrgtUpdM8PgasUPsps7Cw5AKYZWo/W613dHEOKRa4XMZ/b2FIw2I43Q1MCFh0wLDUUssARWaNIKjqY50PvPgzuw+A+LJV0ttmCuwgg14LnYBfW4TQdptIkjZ27zjGL0Yl4aV5+3jfIfwkKLAU2hfYvR0dAB9bOBy/BHZz+x/5U/GhmUmADXr/e61693Yv6nynEQXSBVhhZOKlVsrE+nsVSlZb4f6w+0dnIoUkUurk2E6YVVRIPHDcZMVaHVEcQ/cmVs487R1G/V3kNoptFgYlWaMfaRV01k0arKSr3VuaYsHjGTZzL24eM4wNUs6j7BUWPsFwSl5YcPPNBgU8Wlvh4OaFBa8+ls6Kxmes5c4aPRlLAXKpUz1uU8o1HGkRX6pznqFJukn1GP9A+Om636Te6Jv71VH7F9AJzvuihSF61p1JFn9BYSdEqlzhTImF9qLQzCCxj+maRhDhtTbBP9T6KZjyXKXc4nCAWQyr091r1VhJpJ6hxBFvQ73sJevN9A5J/4O9+h/066Lfpz/4ODAYn4MaoyNsjZBkJhLnNfQoNqUIEcHN12f3jsvvX0LM0vOx2Pw1vzt71kvqugGjQlUZBh/4ZSR89If3QcY/Ff7cB/pi9CtCVcwCpW3KDaqrgQx7LzrBMaIOknjUkPxNKgzZpk+lHitco/PZtPSuiuA/sK1Qx0OMB7O6SaQvYF28Mt3vzGs1V1C+I1ws9EFjftxH6WRW0vsvgIbfvKxY0Q/24sb4rJlYxrEXwzBi8BVZTytOPaWOJ8FCMnWaz/k6LptNqBYdABOYWn5+y0suLqhav53R95ZMXhuu24RqnekbzlKZ7mKAWlHYkhREaVKJabxyEVo7TVDTQ9I9pRC5WC5IGAPVz6yDg/c6dGBOKwSmmktTG/IpjqbQTIBO1vBKLekyfkJvIuZxauC1lnsaC02EI0rSlGAO3ATTTOV0WF9q6jHI47vwM8zFRG6rtQ6A6ARGh/HeiQEygKXia0iYtiodjsaGIqjiDHNL1xVHxyPgITuMUZ7EqiZHD093OS2W4AXVdCv9fl9APJMT6KcmSZQ7aL++K6l4IbxO+J1aIj0yyZ6eosOHH3pQ0sRxzgltcjQtZRciYxZw2sXWtDdPnqXkIJydLlKWuWXiHacHT7zj1sb29Fjw2l+vn+y1AMdbQ2JjpMb1iTJSeK6BVVfpVD7SB4HT38OSBuAoNLReekLpub85674e9q8/X591+e+A3FFnb60PNU0e23xoR8f4vCheAbZUKAAA='
DISK_METRICS_B64='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'
RESTORE_SECRETS_B64='H4sIAAAAAAAC/7VXbVPbRhD+rl+xGBdwqCLboWkbV854EiWTloDHdkoyhHgOa2Vp0Bt3JxMCzm/v3kmyZQwx+VA+BO50u/vs7rPPXba3rExw6zyILYxncM6EbwiUYGKWQBqk6LEgNIx+b9B774ycgV2rGccfRv0PI/XXwHn77vjIrtVveifDcb56Year186b3ofD0WJ3Pq8ZvcPD45PxW+fIGfRGju2xUKBhXPlBiHAKtfp2DcyphCacdcBNDIAJE0j7rRoEMS0BTDNlnEUokTeggqp+06YQtQ4IP/AktKHTKc4nmUwz2YAS9cMnWRgmV+YUY+RMYgPuoJU8w9KqsHnSAJz4CdQ4CplwNNGd0j/xzBQ44SjFC8jiizi5ioHxaRZhLEEl091pdwC/BhIOmrkvFGxiuElM5ThVlbhZ5Davgf0dvlgyucD4Aq+tPRXk1kf5jYA2rFNmfmuaf5pn+5aQbIpNqxLfPGeTiyytw9kZ3N7CjYq0AfA5c2FRYw1VGeVYjXmBLi+mggbWk9L5RtfsXCRhJhHynlAY6QPHyyzg6K5WpQM6lBmraDmFKNpjAxH/yO80SOIfuDdmLAxc6vQ4txt7RMO9hi5SmExYqPHZin20Q1g8hUXtKSSws0N7W2AeruwSPHKV8Rha2qhW32NXF7DrHL0m3CkPiAFHA9gnjs93l5YNIj5ewrM1DzkQajtMkoxsCXOGlEWKE4nuOMR4Kn065yVcnwpi6B8PR28HznDc7w2HJ8eD1/D3yWg8dF4NnBGMjkf9sXP0avCpP6Kajv9xPhWjBnkEu0BsvrHBnCmfamToF6W3W2+Bbes4lH0ee6/ebEC3XNCnBiXXoly1t/19mEM19zyL9fQ1AE0tfWKe16O1Vg/IK/BIlGVUkZ0LyQnqr3eAthv34Qi8HEruz76noqRP0sf4biPsgz+IYiRqax+eH3TAC/IstW+dRjHbpzS/zPTO9vWcamLRie3yiKoEre+4vIduWj+ASFmWRpVi6ztYX/bWUrhdkuL2HlI06pZyXunxX/Zi1Sp7TFphtxYd1rOl5OMltfcFHaiWlqTD0HU1cUVAdA6nxRgtd/P6Grrda0O6crSQtc3CRvCEDOIp5DugPQWCRkbHKKWuFDtV0Lm6gvwocaH5vNmshl0IqYoD6uos4gALOTL3mpiHQuk9aZCfCFkrVbRpEA8MUljMh80NeExiW3VONAxiEvMwBNMFM4Lm73n03IqiS4xSZRxdqL8qn6ynpZvt7SfW/OlH/aM8IucJ1+V7lKE+XjFfLYMyptorVi7cEqxJiCzO0kJEqd2TJIpY7KoZFT6pMHQtF2dWnFFq7e5Oa9FmKL6b2cPuyWJprZgfFZr8ABrQo6idbz5KTZkbkrMUiiTA+fhupDnLrgS9Dor7pHodCRHBFOXySQKfDfWQKPtZvcNN8yqQvukSS65TqTzlZy8z5NfQLz08/VcLfPlsAYlfJXSXwNvdO9A3Dkph+OgxGQ7fF7cMzUbEQrpZIlybjnYxHdFsrbCVEfnZ2SlQueDxJFJIiLYhdWDKMSVBu4TdRaGOEvmGbgp3Fx4qSKniq884wrhlg3rLVURmc1WWLVZVCYRQQqKYXTwXVUPpS5xIYJn0Ex58Wy/abznPgMRx2VJa6kvKo+TuirT9i/gcqwz3khRjIULgepp8/ArtA3o0dO/1s5T2Hzl41n7QwT0Xwk96egQbN9a8fIm7S23luCLX5XvuWed/IOMyvHpceQEX9GLlySwQ1GxFTK0tjxkndSGA+o8Uuh1aeJmoXEM8kZo+JVkknQNzAq1m+2BNAauv8QN1kfwHEOWuo8ANAAA='
PGDUMP_B64='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'
PGDUMP_STORAGE_B64='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'
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

printf '%s' "$DISK_METRICS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-disk-metrics.sh
chmod +x /usr/local/bin/tokenkey-disk-metrics.sh
/usr/local/bin/tokenkey-disk-metrics.sh --selftest

cat > /etc/systemd/system/tokenkey-disk-metrics.service <<'DMSEOF'
[Unit]
Description=tokenkey EDGE on-box disk-full and memory-pressure Feishu alerts
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
DMSEOF

cat > /etc/systemd/system/tokenkey-disk-metrics.timer <<'DMTEOF'
[Unit]
Description=Fire tokenkey EDGE disk/memory pressure alerts every 5 minutes

[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
RandomizedDelaySec=30
Persistent=true

[Install]
WantedBy=timers.target
DMTEOF

printf '%s' "$PGDUMP_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-pgdump.sh
chmod 0755 /usr/local/bin/tokenkey-pgdump.sh
install -d -m 0700 /var/lib/tokenkey/pgdump
install -d -m 0755 /usr/local/lib/tokenkey
printf '%s' "$PGDUMP_STORAGE_B64" | base64 -d | gunzip > /usr/local/lib/tokenkey/pgdump_storage.py
chmod 0600 /usr/local/lib/tokenkey/pgdump_storage.py

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
# Lightsail installs these via post-provision SSM (user-data size cap); Hetzner
# user-data can ship them at boot. TOKENKEY_FEISHU_WEBHOOK_* still come from
# post-boot sync (copy from matching Lightsail .env or sync-feishu-config).
systemctl enable --now tokenkey-disk-metrics.timer
systemctl enable --now tokenkey-ghcr-prune-daily.timer
systemctl enable --now tokenkey-pgdump.timer
sleep 30
docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true
echo "HETZNER_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
