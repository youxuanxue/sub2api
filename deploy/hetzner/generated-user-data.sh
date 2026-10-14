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
CADDY_GZB64='H4sIAAAAAAAC/61VW28TRxR+tn/FUcIDVNhexwlCVKi4jgFLIURxEOVpGe+O7W12d7YzswZjRQqIBEITkaKAykWiFbcCKpeKijQxQupPob7lKX+hZ3ZjG6cU9aFSYnvOnPnOOd85880wSDZL3VlahT/fQvp0HrJmiUJeEvzUlC1DTLNatGwaHYaMzXwzlnMtCb6gAqhbEX5BSCgyDntq6amcPn7yRDo3ObdfLTMnsnoWlxO4JK6JAHtqalc/lp7Jnk6f0dMTEydPZ8f1TG58ei4O09QmVfCILAsgnILLJHh+wbYMFY5jOMlxn1muFHGVDuVSqC2YmcjH0hNTkzEtCXuZC6OjqX1fwvGZmSllsQSYliAFm5pQoAZRJ2SZwoRVKktBLBuxihan54htwyylnuiG9RiXcFADw2aCmvFotBaNUAdPDJYXjQwDMRyqGwTKUnriUCKh1jGBPFpuKVbRRuLEs+I2lYK6Bq96Ms54KWFiVEMyXo3ORTGJgHvfE5JT4oBwLc+jcrt+p7U037q31Ly+0th80Lz6qtezQwc1TK595zIUbGbMQnPhdWPjeef97cb6fGPz3dbCSvvdi+36MkJvXXzfXFiBs2VshE3hCJbKzlFT54rzs9C8sQxFtBUIwnSdanO4sXq5vfmmde1hCNl8We9cedOqX2o/2fxr/hICt9b+wIBbV5abr17j78bGYmMdnZ9BCLJdv+txZvbHCJp3f2qsbzSvP1OI6NerN6yh9dvP7bVfusWrGHvlrN512gfYAU4rlAuqI/D56i4ycDtStH1R1nFKKK8QG2JJtA1D8+Kj5sbb1oP51v1H7V9vtW8+aq4uw2ByyDUxpFWh0Pn9FSTKlNiyvF2/anJiuTFyDqdyu76kGFWI+dyxU/npJHxYvNH1PTympaDz4mkICTtg7effN69utq5d6xfb+uHHrdsPkR0Ti6Y8wAshjDJFGhQHO5gJW2HsNDDIJIgYHgROcci4VDwTIZQnUtt58TBAHNNGEmPaKFbQurnQuny9sb7SXH2O5tSH+TX8a68uttded14+7jy5+A8qljqPF9t3b7Xv3W8/3Wjdu696EYmEOek+t7r59Y09ysdE3ygthzJfQkrZinh3dNPnRFp4TQM3h5zXlVmA6pPvhseqOpYlfaFKwP9R3LILOt7//umUJvrWXujkTmiTcpwaiGWOxjLMdfGaqYuYmxrY/SY2jdHQCjVOHSapXmZCzu3yOco4dt6kpvr1Hz2nOJMMagK76dB/dzuOIFDrQklOXBFojlKRYJYjpkXsHocBYRElUSQYiuSINmjRLbxzuoEFCzigSIsYzPFwRoRijBWLaMJIc0pvBgRbBRuGSYYiazCTHoJ8PguBOIDjY45Ki0sXLA8kPS8TeAFdGdsZ5b3Z8WNZPZ/NnJrOzZyJO+Y+FMrIEdU/lFBVhJJ0SOB4ovwlvugZbFZicc8t9QxFUrEw9zh+qBx3OIMu1FcZgnSqdkrObBgKRXo/4ATF8L06nEqOpQ5omrYfLMfxpRL8IcxE4mgFWaiWGAxXaEnGR8KvFO5YQvgYR0l2SPonNV71InhFdNUd3SijXlK3RLuUYs0BYXr4gvULryQ/KtpyMNWPWahYJmW714FDADkg1P8DaGRnfLGVn3uNw+iffC2CJCwnmNOPpHnwxG4mcAQ95powRNUbx3svvXqclYRxy5DUHIJRLTUA9LlYnH7n41G9wMwwKSUlwrqA90LTTnwdOuGQBXt4fTy8QUKa+EspEeMOkfCtYK5SEZxoG3KTR0+Gl+NvP4snexgJAAA='
PRUNE_B64='H4sIAAAAAAAC/7VV627iRhT+76c48WYDSdfYRKutlgSkKCUpSpasAq3abio02Mcwwh67M2MSmiD1IfqEfZKe8YXAhk2bVisZCc+cy/d95+JXO+6YC3fM1NR6BcNkhuICFzDQbIIe/PXHn5DKTCBEic8iOP/+9BpYmgKP6R7ISAELNUpA5k/BT+I0UQhpFkVQ50LQhfIlT/V+g6IPUM4xgFAmMQwGH6DRaLiTqS+dPEVj/O4t1AkIvnu7f0QJWUDuTIObKenm+XOo2mCc4aLwcvIABMkxYBo5ixC1P0VCJgLAO/QV6ClXEPIIG3CBmJoDhDhRGiT6KDRcdLsfR/2C0HhR0juVyDQB1jzGN5BGmQKco1xQCjKEMJF5nMKYwAguJsAFySA044a8XYG1ib9RIMmkjy0IMI2Shctulatypf+JVV4JiaFE+j/nbEuEccajwPFDQeZG7atU80SwqAXDq4tu/6L788iUb5RTHZ6cDwxUd85IWz5e5XcbKOZQDzBkWaSh6VHlLIUaHMwSSHlKFzyyrEKwtr17/4XoLafpLW2r2/9xdNa77Lbt7alsy+IhfIIdcEKgaJX90oZfj4y8wgJAf5o8arlNohbEXCmj/0aIzt6hcb/jGjwr5BbJoqYYRdQe/gwCrtg4wvbgtOm99wqWzGpswsiPv2ElTOd3WKfc+3By3m05Lwa7GQAwTvViC9rr7ser9pN8r1sHBGsNjjEzEODhgc4eD9pPsb4YqZ9kUQAi0ZAySaMtMU2KCd4MvA39D/1+r38+6n3XtnO8QeLPaCi4UCn6GqrE0HEDnLvCLI3Dzl5zhW89wG79M29ql9r9faNnhm+5rK2i7dt5cpVIGl3jF8806UvHM5r8cONES5ZCTcZl7xU+JBH9z42Xdg26P/WGlnU7peVB5FkAjjSDeERsCGJZAjow+u/t5bPPRYZ059Mqq+5o1A5qxyIR2Kkd7K+s4OgIUDHfWJst82We5S4yTMuQh2uyUeHzYtaa77/1HK9Jz9DzWvnzS42YAnAePKPiM4G1zDCPkEouNNm/Vje6/AnjlCO3K297lzLZVkBU4RiOVwlNndRaczoO7c+Y1rvJf01dpbhO5GK5bNH7kE0MoG1I4IagPMDl6ejk8rJ9CqZo4GiKXK8A3miiDM6s+aYpobNWWMtq5e9lcauy9s4G7SfuVa1HPtEyFTcKblb9nk6L4f+s8sVsipztqocru0+lRMV4bt6vWr8azkLTzjpooA9ZKe8GtRdxGf17MjNaAUK3vRUvtTYfG5BLy936rQ9OlMOrzEzxwAmgBrX9ggEUq6rwMf0wMcNyX3xXKhhjQj4rEu/AhHYPOGd3vz222ir+Wqf8fw2/5pj/RyblFMmYPzOl67S+wuRZVrnc/wZ6n3lINAoAAA=='
GHCR_DAILY_B64='H4sIAAAAAAAC/5VWf08jNxD9fz/FEBAkVGYTTrQq3KJykPshruVEOFVVlEbGO9lY2dgr25tcjqOfvWNvluR6hIJAJMyOn2fevJnZ7a34Vqr4lttxtA0XXOYLePf+/Bp4UTDHMyhMqRB+gpSrLJcqgwstJmhATnmGIHLkqiwO6OwHNdMTTOF2AY6+qAkuWDYWhgUElnroAyendFYrgtUpdM8PgasUPsps7Cw5AKYZWo/W613dHEOKRa4XMZ/b2FIw2I43Q1MCFh0wLDUUssARWaNIKjqY50PvPgzuw+A+LJV0ttmCuwgg14LnYBfW4TQdptIkjZ27zjGL0Yl4aV5+3jfIfwkKLAU2hfYvR0dAB9bOBy/BHZz+x/5U/GhmUmADXr/e61693Yv6nynEQXSBVhhZOKlVsrE+nsVSlZb4f6w+0dnIoUkUurk2E6YVVRIPHDcZMVaHVEcQ/cmVs487R1G/V3kNoptFgYlWaMfaRV01k0arKSr3VuaYsHjGTZzL24eM4wNUs6j7BUWPsFwSl5YcPPNBgU8Wlvh4OaFBa8+ls6Kxmes5c4aPRlLAXKpUz1uU8o1HGkRX6pznqFJukn1GP9A+Om636Te6Jv71VH7F9AJzvuihSF61p1JFn9BYSdEqlzhTImF9qLQzCCxj+maRhDhtTbBP9T6KZjyXKXc4nCAWQyr091r1VhJpJ6hxBFvQ73sJevN9A5J/4O9+h/066Lfpz/4ODAYn4MaoyNsjZBkJhLnNfQoNqUIEcHN12f3jsvvX0LM0vOx2Pw1vzt71kvqugGjQlUZBh/4ZSR89If3QcY/Ff7cB/pi9CtCVcwCpW3KDaqrgQx7LzrBMaIOknjUkPxNKgzZpk+lHitco/PZtPSuiuA/sK1Qx0OMB7O6SaQvYF28Mt3vzGs1V1C+I1ws9EFjftxH6WRW0vsvgIbfvKxY0Q/24sb4rJlYxrEXwzBi8BVZTytOPaWOJ8FCMnWaz/k6LptNqBYdABOYWn5+y0suLqhav53R95ZMXhuu24RqnekbzlKZ7mKAWlHYkhREaVKJabxyEVo7TVDTQ9I9pRC5WC5IGAPVz6yDg/c6dGBOKwSmmktTG/IpjqbQTIBO1vBKLekyfkJvIuZxauC1lnsaC02EI0rSlGAO3ATTTOV0WF9q6jHI47vwM8zFRG6rtQ6A6ARGh/HeiQEygKXia0iYtiodjsaGIqjiDHNL1xVHxyPgITuMUZ7EqiZHD093OS2W4AXVdCv9fl9APJMT6KcmSZQ7aL++K6l4IbxO+J1aIj0yyZ6eosOHH3pQ0sRxzgltcjQtZRciYxZw2sXWtDdPnqXkIJydLlKWuWXiHacHT7zj1sb29Fjw2l+vn+y1AMdbQ2JjpMb1iTJSeK6BVVfpVD7SB4HT38OSBuAoNLReekLpub85674e9q8/X591+e+A3FFnb60PNU0e23xoR8f4vCheAbZUKAAA='
DISK_METRICS_B64='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'
RESTORE_SECRETS_B64='H4sIAAAAAAAC/7VXbVPbRhD+rl+xGBdwqCLboWkbV854EiWTloDHdkoyhHgOa2Vp0Bt3JxMCzm/v3kmyZQwx+VA+BO50u/vs7rPPXba3rExw6zyILYxncM6EbwiUYGKWQBqk6LEgNIx+b9B774ycgV2rGccfRv0PI/XXwHn77vjIrtVveifDcb56Year186b3ofD0WJ3Pq8ZvcPD45PxW+fIGfRGju2xUKBhXPlBiHAKtfp2DcyphCacdcBNDIAJE0j7rRoEMS0BTDNlnEUokTeggqp+06YQtQ4IP/AktKHTKc4nmUwz2YAS9cMnWRgmV+YUY+RMYgPuoJU8w9KqsHnSAJz4CdQ4CplwNNGd0j/xzBQ44SjFC8jiizi5ioHxaRZhLEEl091pdwC/BhIOmrkvFGxiuElM5ThVlbhZ5Davgf0dvlgyucD4Aq+tPRXk1kf5jYA2rFNmfmuaf5pn+5aQbIpNqxLfPGeTiyytw9kZ3N7CjYq0AfA5c2FRYw1VGeVYjXmBLi+mggbWk9L5RtfsXCRhJhHynlAY6QPHyyzg6K5WpQM6lBmraDmFKNpjAxH/yO80SOIfuDdmLAxc6vQ4txt7RMO9hi5SmExYqPHZin20Q1g8hUXtKSSws0N7W2AeruwSPHKV8Rha2qhW32NXF7DrHL0m3CkPiAFHA9gnjs93l5YNIj5ewrM1DzkQajtMkoxsCXOGlEWKE4nuOMR4Kn065yVcnwpi6B8PR28HznDc7w2HJ8eD1/D3yWg8dF4NnBGMjkf9sXP0avCpP6Kajv9xPhWjBnkEu0BsvrHBnCmfamToF6W3W2+Bbes4lH0ee6/ebEC3XNCnBiXXoly1t/19mEM19zyL9fQ1AE0tfWKe16O1Vg/IK/BIlGVUkZ0LyQnqr3eAthv34Qi8HEruz76noqRP0sf4biPsgz+IYiRqax+eH3TAC/IstW+dRjHbpzS/zPTO9vWcamLRie3yiKoEre+4vIduWj+ASFmWRpVi6ztYX/bWUrhdkuL2HlI06pZyXunxX/Zi1Sp7TFphtxYd1rOl5OMltfcFHaiWlqTD0HU1cUVAdA6nxRgtd/P6Grrda0O6crSQtc3CRvCEDOIp5DugPQWCRkbHKKWuFDtV0Lm6gvwocaH5vNmshl0IqYoD6uos4gALOTL3mpiHQuk9aZCfCFkrVbRpEA8MUljMh80NeExiW3VONAxiEvMwBNMFM4Lm73n03IqiS4xSZRxdqL8qn6ynpZvt7SfW/OlH/aM8IucJ1+V7lKE+XjFfLYMyptorVi7cEqxJiCzO0kJEqd2TJIpY7KoZFT6pMHQtF2dWnFFq7e5Oa9FmKL6b2cPuyWJprZgfFZr8ABrQo6idbz5KTZkbkrMUiiTA+fhupDnLrgS9Dor7pHodCRHBFOXySQKfDfWQKPtZvcNN8yqQvukSS65TqTzlZy8z5NfQLz08/VcLfPlsAYlfJXSXwNvdO9A3Dkph+OgxGQ7fF7cMzUbEQrpZIlybjnYxHdFsrbCVEfnZ2SlQueDxJFJIiLYhdWDKMSVBu4TdRaGOEvmGbgp3Fx4qSKniq884wrhlg3rLVURmc1WWLVZVCYRQQqKYXTwXVUPpS5xIYJn0Ex58Wy/abznPgMRx2VJa6kvKo+TuirT9i/gcqwz3khRjIULgepp8/ArtA3o0dO/1s5T2Hzl41n7QwT0Xwk96egQbN9a8fIm7S23luCLX5XvuWed/IOMyvHpceQEX9GLlySwQ1GxFTK0tjxkndSGA+o8Uuh1aeJmoXEM8kZo+JVkknQNzAq1m+2BNAauv8QN1kfwHEOWuo8ANAAA='
PGDUMP_B64='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'
PGDUMP_STORAGE_B64='H4sIAAAAAAAC/51Z63fixhX/zl8xUXq6UgrCj5OkIYf2EFu7dtevGjZp6+WoQhqwYiFpZ0a2WYf/vffOQ4xA9nrLB5Dmcee+7+8O337Trzjrz9K8T/N7Uq7EbZEfdhzHOSm46BV5tiLlIqmWJRkfkjShuUjFivDbiNGEzFakKrMiSjiJ8oQwykXBKImjPGIp5X6nEzyWWRqngpxQ8TmnjMRFPk8XFYtEWuQkp/cwNo+yjJNZFN8RURBxS8notzFJcy6iPKb1qX5nTGNGBRwGh8zTjMKJc8ooLOJdTSsulkvkJWKLagn7OCkYWUSCPkQrAiKmrMhx3EcZO3NWLEkYzitRMRqGJF2WBRMgTF4IySHvdMwYW5QR49S8K0HkEDNjv/MiN88FV9TLSNxm6cyQvoJXs4TVxEBSUT9Xs5IVIBKvR1b1Y8UyIOYrTjpHlxdvT9+FV6PJCRlK0q7TpyLui+KO5nd01Ve266FdogX1kUHH60xG1++CSXgdwC5GfdBZCdp0meP+fQBnJ3/QZEEHN1Hv8xS/9no/TZ/2u/vfrz3YfPnLP4Kjts3m0N7H5Omv6wl8/7D+z0eff8o++ovPsLPTibOIczJW3ASMFcy9rsC4S/XiDToEPiUsgsUJnZOSpfdgvBCt7aIqB1JMj/T+Bt4nKFc7BFupB/yk+bwA3nC1n6FmXW8zNycu2FYq3B+Hp+Pr4J2LG3wuwmWRUA/9xQxUaUK+GYIp/QUVFN4sSuYDyw2x88vjYIvYn8lesffjj96gsY9FKadNNTjaRMqvlxUXZEbBEUnxAGGjApHRRZVFTC5xNpxASFQsV/IyGiWhVIxmlT7GtBTkciyPIRHHkQ03bZzE0v0JkjL6JzZ3jkekZwMhbaWsiGW0uCx6GMBa1iUC4oUK+UL+IBdFTsEk+CNNJyBt0Bu5EL6mih+wDewH3bG0dD1UPLyifqN85caQccAucmgOY+bdefPR+e+fPn50vJeFSvP7KANzbpLZh+tTrUQZTQnwZ0eXDy/yAYXS6yDdpI/StXCDLzWu2HX6mhY6mJ7m8S1dUpTD4YcOCqLU6s+rLFtGIr6FoDHxpX/9HkbaD/trM+50zWk5FaDnpgMCzZoXFi25NfCpomxlvc9BGZj4vqAnrR9QTu2E/HDQ78+q+I6KvlLBRtYWiewc0m/LITopoWSS2tdZbpsD5Wgk5ZIX9LCBHe11qrN4VFs83NNc/0oW1H4rAuljSWMhXchB2Y2EFn9DPeUQmgH9uSPV86RmfQ5VUrhvBm+6ZN+72Z+uDQVbFO1+4E/mvC9z3lAaSQqq1CT10JSjziKWsxkLbWdudS5GfhimeSrC0OU0m3fJdy8EfncndeoaGm7yOiy1alrLDqjgzeW66N1HrA+huyl8Pqx0PMu1kEEYTMoizYVmqTkJ6RUxSdsUwA4JQiLAKW3znNMQTt2e+5Zk9DHlAEIY/R0sBiCHIYNQ0ZdZmt/BOLAPqEXcQqYXkHFFmi8QAS0xU0czLpGK5QNQilRdU3RdS4NbNaZREpsKBzYRCPgSubmNEmvT2611wACnQi+SGfrJkVkuCgF7cdAeBLWj3aqLaS+sWIpPRvH4rPSMT5Za5fFyD6gyRmWqkfWuEM/5epXzqkSUROtINWWrgTqdVsHEqjTi32wLNfVMfkHfKQyK3V2HKtnHBW1px2zS+pk+k4C+IhEZ8QxCfkaynQwp0fo2O8i7Dt5X89OqX6gZWZEvOIL5CI6UDr6dMhvho4pLV73UVbZGFbW2lTtNuzu879Ktg9ks1V43bdOQXqwMDD495/k++mI+W8jfW5rtO2vb8LU/S7XNnVshSg5l8kmRWvurogLUNpNRr8E3IGXn//Nm0zzVpz6jRzPf3Upou1x3tcztdJrpTqZYQ2InZKfP8LJJiY39W+E9bfVYidC3GPFTHkJCLLJKUNczaMo+qrni9Zo2e3bALmZBTN2fqhT4aFF6U092R+gfyZcr+QI9gaCsLDLpzkOJgl+ipVA8Ast80UzP20rx/ITG0Gi47cnapsnBE7GhVcj6RiMD8F9sYDCpb9ZuJnWOjx5AszG2pNJyaYJBgYNcNuTW3Nfk661ktmEADFFs9ThbzhFlWTvDPpCssPmRHpLRvEV/2l08KdzhwdczXGc9y/wFRmfMVqXMgKiJJvO6D3N/Rf4kvS6ZQMnRj+/pSj81vEh1xTtd26u0qp355Uq46eRqRgGevgJJKD2D198AkKHYfkABwmTtDB3v5mAqmzScwoxqUJtybEEfoSVXmBdX8JauumFxeQI02UzwhxTh3uTyfXDxPvh3ePXu+MP5VTg+DKFhgYOnbTp/rvd9bQ98W2AfFMV3Vfll9RmWwfOUirwaEyBBNXazN30dG+2Cqs4sKksKPTB9jGKRQaeHV2VY16Fjy4u8R5flDiB4TbGtOTRQ3uvUaF9frmmwD7N8AMbhAjv5qUT/OgagNSGzosiA8FuIDtX01ysbDVoTl7e3ZKo1ucGUA5nnOzx4aoHsa7os7kH45SyFcFSXhxoRQZ0rMtqtK6fU0NHZadOQvkWscR/KaEzTe/Bzef8ir2ZkUfgZVExU8tN24OjnpcwD4/H5hiCUcez+yE0qANRjUGBRhLWA5PV1JGoBy5ft4SBDiO6Fl1G4ETa4Tq8C6TFzbYQ3152Y0bHjAe3IE7ttETWXZMcno+vgODyCr+Bicjo6G4dvT8+C4dN2cVk7rVQkEd2lyY39hN73c4C5WBVw8ur6Uk6YPv9ZKsHRQXgeTEbHo8koPD4dj345C46HglXUkDof/SscTSbB+dVkPDx4gdR18M8PwXgSHp0ER+/HH87Do9HZ0Yez0eT08mL420lwIVecgtAvEhlfXV6Mgw2VX0dnp8evJ2I5KAjQ6xm361UMtbMF0mBB3QtZgG1qh8cmoDrNRk7Z/S9o+F4PSlovljoLxgff/+BI2nKwh6WoC6ZH550hQrVr4NrCwzrKNOVN0Ksbfh3zHGBtTK1LWIjnRjjXt8NW46M2+Xm0pHXPUwM4NZcqNOl6r7+MwYyVmfKG5WcOidBKeNAqEITm8vrqyUp9a/2mch++bfhbO5Y+eJVh6G4u5H1W5RqD6UyITYnsZEulc8wTPX5bPPQocoveABrSGvC6yNS0+0K1a/nUHjCcQGAAjTgq5d8VRSXKSshRyNXpksLA8Ke9vcZtt5LCV8ZFqPhlBRfzeW9WPGrDk3kEhkmIq/QttQ3UIqjJibd7Cw0C6kvhJSxx6xt9hDOgS/Mvij/S/89ohJxQHrNUgqdhGCZFHIbW3SzzoyQJzV86kAh7Kn2DdrFtH6I3dtFZI5B1aN0gvUij0ZM+s0bpwD5HLccINxfBTF0Y4z4DZRqQyUCvodGzvoAc4gZfPXftuzA1oW9ZbGPKccVS04wA9oDfuYbBA6JNCGaDEH0yDaiOZIuKt7ZvMXeQnyIr74qQLnefTA8+2ASWJr6JLT2gw8v5krtvutKBUZWdI2v3t6ZNb2ldaay9xv8dru3SXQMBu3YsT1TEBI8ldndW4lFS17o05pNZR8KFfiMyJBZAzIF/YyJM/Jm0hArmYFg95CtoxUQC2WEndA7se9g9iKIU71cxK4WhvD8OQ4ypMNS3CDp2VxwAQvCYCldFnNf5HxIXbLzVHQAA'
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
