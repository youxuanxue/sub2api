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
: "${TOKENKEY_PGDUMP_S3_URI:=}"

case "${ALLOW_SECRET_GENERATE}" in
  true|false) ;;
  *) echo "BOOTSTRAP_FAIL: ALLOW_SECRET_GENERATE must be true or false" >&2; exit 1 ;;
esac

hostnamectl set-hostname "${INSTANCE_NAME}" || hostname "${INSTANCE_NAME}" || true
export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@${API_DOMAIN}}"
export TZ_VALUE="${TZ_VALUE:-UTC}"

export DEBIAN_FRONTEND=noninteractive

# Ubuntu ships apt-daily-upgrade.timer enabled (OnCalendar=6:00, RandomizedDelaySec=60m)
# with APT::Periodic::Unattended-Upgrade "1" from the installer image. On a gateway host
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
// Owned by deploy/hetzner/render-prod-bootstrap.sh. Patching is a blue/green release.
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
  ca-certificates curl gnupg openssl gzip gettext-base unzip e2fsprogs jq

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

# BBR is installed after embeds are decoded (see LAUNCH_TAIL); marker for checks.
# SSOT: deploy/host/ensure-tokenkey-bbr.sh

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
CADDY_GZB64='H4sIAAAAAAAC/61VT28TRxQ/25/iKeEQKmyvcUAoVVVcx4ClkERxKsppGe88e6fZ3dnOzDoxVqQUQSkViKriUuihVSu1olILFVIpCULqV6mdwImvwJvd2Ikjinroxd557837+3u/mQYj1zBawx78/SdULzWhzjsITcPo17GyGuO81xYB5qehFsiEFxqRMJBo1IBRVyctbaAtFRzrV5cb7vzSxWpjcfOEPdYu1t06HRfoyCJODo71rdY9X12tX6pedqsLC0uX6vNurTG/slmEFQxYD2JmfA1MIUTSQJy0AuHZcIrCGUV6KSKjizYdVEZbFawuNAvVheXFglOGGRnB7Gzl+PtwYXV12UqEBi40awXIoYUeszeMj7AgOr7RTATkqy0UrrMggDXEWI/CxlIZOOOAF0iNvJjP9/M5DOnGZHn53DQwL0TXY+AbE+u5UsmeC5r6KKJOoeucLLJYFAM0GiNP9WJTlKpT4hTVM1L18pt5SiLtfRJro5CFoCMRx2he79zfvbW1+92t4d07g+0fh18+Gs9s7oxDye3dvw6tQHprMLzxePDs15cvvh083RpsP391487e899e79wm168+fzG8cQeu+DSIAOEslSrXkbvK9vwKDL+5DW2StRi5GRn1N0nx9fW97Se7X/2UuRz+vvPy5pPdnWt7P2//s3WNHO/e+4sCvrp5e/joMX0Pnn0xeErGDyFz8nrnQawkP4ARDB98P3j6bHj3ofVIduN6sxp2//hh794vo+JtjBmz5o6MjgNNQGEXlUaXHG/0jjSD1Ll2kGjfJZSg6rIACmWSTUPTJ0wRAHqAts2UHbQVah/S/FpSGgrB4jko+cgC44Ncjwg4iomoRKG5iFAT7KyrRUko1Vp0ETwpA06W6Qow0DRtqtG2ESM+RxIvEIRbqjKQjAMFRANSpW5aiU6dwqmNDQgT2iOLeJ5uQRLZyRi0Vz5LUBPQZzQiTDbzuM0nS9dNlBilfiAcN+GUPhAaEaJMDFSsLGi5tFUuTxQzglan4hySjq+X969zVDQLKNTOFWoyigi8Ft6N5QntJ4UVCkRS6CsMpUHXl9psHrE5J9U6Uxy5/fqPlstKGgl97fkY4r+bXSAn0B+5oqlGOt1ku5spQnJcsGDch7Q3Obv4LLAzLZ90JiWuICS7HhWs4fSsVXkyjGmW2nZMttskokibdosnaNAGS+FCSy85zkGzWYd05Q7m3bkqYjC4YUoE68gU9rdhpj5/vu4267WPVxqrl4shp1nnc2eJUQwRky3CEiWUCIdEKqX3xoJAdmQxjjpjQZt1BeVepB+b437PYOTqwxqjdtpxGiUDmMqo7wSEbKNAr8AHlfKpymnHcU6ACMPEWBqdokxMoLMs7Eg8SSeSlIsns78KaYTWCcWxRJg1/a3MaWeRcrNrp+N6PrEQRh0ctZRqThvmZu/CQeHd8qGiRUipHu5CV3CUR8+pQepygv7+B6e5ffjSKN/1xmXR38rBaRIiTHF6iPAmbxztBEEwlkRkUymlqfH7aZ880hklPKKQKZh1KhOO3hVrn2/cluRZUgQEV4urtBeOc/GjzIhAlupofWLaIG04fVnqlSpkBj7VMrIsQogOoLF4bilbjjeaF9NPbggAAA=='
PRUNE_B64='H4sIAAAAAAAC/7VWbU8iSRD+3r+inHUdMM4A7ofNoUNiFD2iyxJ0L7enhjQzDfQx9Mx1Nyin/Per7pnBQV1z3mUNJnR3vTz1PFUVPmzVhlzUhlRNyAfoyblgcPbrcR80HasDuLz8AmEiGSR3gkUwYZL50GdjJpikmsGCUxjOeRx54Uj4auJjkKuv5+3uefv7wMQZnLfbvcHV0dklRGxE57FWoBNo1H1CFNPgsXkCKU/xjceEWOtu4Gw//CBK02vUVw5pd38bnHYu2huWvf63bntQPDW92oLKWsyHNZ1MmZiyZc1nYoHehI/gGrbAGwH6Fw4rB24PQE+YIAAsnCTgFI5eaojxxpNQejRNPUNOE2ZcKS7GsBGitbNv3O+5hjoZcYKEnCQa8wJXEFFN90AkGtSExbEJMUYeeSLguHPSV8i10JQLUCkNmQIqIggli5jQnMYKY83o8snIxAC1xNO9UYVGkIh4aUoAPqNjBpKNUDERMhAMo0QwNK8I5M9k6JM1cZ0vR2eGykq61JNEfALvGS+Hh27vO55dMpLJDFKqJ8grJkkTqaGHR5J/V0tFFjSeMwjAccgokRBz7CmEa8wq+O5TOV5cN26rvkTIA83udaXqqzTm2piqSrWJDAIg73ugWLoHIfLAI9NvgY3mp1RqbmirOIFTtdYoKdpaxtDRV1rytFKFAGFs1ulk0c1fAXQdv3BbW2DUmImKNaxCK4B9m8Ger+u3Jnz23Wvcbr5gwc6N45aylTNmZg3s5luSSi50noJYmkm16FHvbyh3uC2g6b27UzcDAJulevlKq/bbva/Bi3wfm7s4MyU4xsxAgMdHvHu6CF5ifTfSMJnHkZ0Q1FiZDk4TsE33TMVX0H/rdjvds0HnJHAs3igJp0yiEiploYYiMbRqEVvUxByHZ7+101jjKwfYrjzzxl3hPjz4HTNXq5W7joZSmeQKm59Fxm821cgvXk8ZS0cbN1rSFFw5yxdP5oMU4XdrvHJcaP/euSLkbsJjUzyOtCfNGB9gNQgxlwAvDP87O3YXcDFn+BZS5Ct/w+7bdQ9FIljL3a2ureDgAJiiobHWfMZ+XOcx5jbgsNI85H6JNhTeiuk2fvlc9+oN/FzV6037+cPFSnFyePQGi28E1hLnwESwc4H2H9WNzv+FcbLIncLb2cZMDomwVDiEw3VCo5MqNafn4S6aUW3z97GrFNeJXK5WTTxf0bEB9BoSuEEoj3BxPDi6uAiOwYgGngazLXOANxpLBm/a2GtIaJWEJaRpz7m4hayd08vghXuh9QD3/cgobhjcVP0Bb7Phf6Z8NpvCVrvu4cLuOqcoG8/N93XrF8OZcdoqgwbA/s7o3SjtXbUM/n0xU1wBQgf1dV2qNB8bkHPL7cpdCF5s4RVmRjzwInDBrWYVQLaqMh/TD2MzLA/ZD44CxhCRT7PEWzDG3QPe6f1fT622jl/qlP/P4c8c8/9YST5FcsbfmNJyWT9h8gjJl/s/WXEbmaQKAAA='
GHCR_DAILY_B64='H4sIAAAAAAAC/5VWf08jNxD9fz/FEBAkVGYTTrQq3KJykPshruVEOFVVlEbGO9lY2dgr25tcjqOfvWNvluR6hIJAJMyOn2fevJnZ7a34Vqr4lttxtA0XXOYLePf+/Bp4UTDHMyhMqRB+gpSrLJcqgwstJmhATnmGIHLkqiwO6OwHNdMTTOF2AY6+qAkuWDYWhgUElnroAyendFYrgtUpdM8PgasUPsps7Cw5AKYZWo/W613dHEOKRa4XMZ/b2FIw2I43Q1MCFh0wLDUUssARWaNIKjqY50PvPgzuw+A+LJV0ttmCuwgg14LnYBfW4TQdptIkjZ27zjGL0Yl4aV5+3jfIfwkKLAU2hfYvR0dAB9bOBy/BHZz+x/5U/GhmUmADXr/e61693Yv6nynEQXSBVhhZOKlVsrE+nsVSlZb4f6w+0dnIoUkUurk2E6YVVRIPHDcZMVaHVEcQ/cmVs487R1G/V3kNoptFgYlWaMfaRV01k0arKSr3VuaYsHjGTZzL24eM4wNUs6j7BUWPsFwSl5YcPPNBgU8Wlvh4OaFBa8+ls6Kxmes5c4aPRlLAXKpUz1uU8o1HGkRX6pznqFJukn1GP9A+Om636Te6Jv71VH7F9AJzvuihSF61p1JFn9BYSdEqlzhTImF9qLQzCCxj+maRhDhtTbBP9T6KZjyXKXc4nCAWQyr091r1VhJpJ6hxBFvQ73sJevN9A5J/4O9+h/066Lfpz/4ODAYn4MaoyNsjZBkJhLnNfQoNqUIEcHN12f3jsvvX0LM0vOx2Pw1vzt71kvqugGjQlUZBh/4ZSR89If3QcY/Ff7cB/pi9CtCVcwCpW3KDaqrgQx7LzrBMaIOknjUkPxNKgzZpk+lHitco/PZtPSuiuA/sK1Qx0OMB7O6SaQvYF28Mt3vzGs1V1C+I1ws9EFjftxH6WRW0vsvgIbfvKxY0Q/24sb4rJlYxrEXwzBi8BVZTytOPaWOJ8FCMnWaz/k6LptNqBYdABOYWn5+y0suLqhav53R95ZMXhuu24RqnekbzlKZ7mKAWlHYkhREaVKJabxyEVo7TVDTQ9I9pRC5WC5IGAPVz6yDg/c6dGBOKwSmmktTG/IpjqbQTIBO1vBKLekyfkJvIuZxauC1lnsaC02EI0rSlGAO3ATTTOV0WF9q6jHI47vwM8zFRG6rtQ6A6ARGh/HeiQEygKXia0iYtiodjsaGIqjiDHNL1xVHxyPgITuMUZ7EqiZHD093OS2W4AXVdCv9fl9APJMT6KcmSZQ7aL++K6l4IbxO+J1aIj0yyZ6eosOHH3pQ0sRxzgltcjQtZRciYxZw2sXWtDdPnqXkIJydLlKWuWXiHacHT7zj1sb29Fjw2l+vn+y1AMdbQ2JjpMb1iTJSeK6BVVfpVD7SB4HT38OSBuAoNLReekLpub85674e9q8/X591+e+A3FFnb60PNU0e23xoR8f4vCheAbZUKAAA='
PGDUMP_B64='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'
PGDUMP_STORAGE_B64='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'
DISK_METRICS_B64='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'
BBR_GZB64='H4sIAAAAAAAC/61XbW8aRxD+fr9iDFaBJMcBsiLFhFQJJmpUt06Lo6pKUnTszZmTj93z7oJNbaT+iP7C/pLO7r1ig3Gk+IONd2aeeXZ23qgfeAslvWnEPeRLmPpq5tQBuVpIdLW4RH6JK3c6lW01g//++ReiAOeJ0Mg1nBvxz7iCmVAa3r37nez8aYxzEradOuGMx2fnEAp5T/V8+BGY4BeodCS4+ailiKFJbuA5hFetNgwFUZhjANPVMQEBuBBgEouV518rT2n/AjvZHxdZz50KoZWWfmJ4Njlew2jYg0SKwLpUffIyT3ymIeJxxLG1CTpD/TdH6UnkAUrX2G2B/CnVqsDmMCIpSa04c43QRM1dRr6r1NxCxNESK7TQj3NzP5jTA9DxTYQK8EKiUqkDaPpJEq8oDMZHH5YoozCycbG2AIrJKNHKYzNkl8qCrCbkemLctZMVeGBFJSelhDYC1zUqLftSnxSRTyO94/HLnzqknJ6nbFaPWbmudW6tUm0QPF71AW8iDR2IwhBsXCh6GmHua9JXjkINLi4EJFGCoR/FjvPL2cloYB07zFcItcPb7rG7rtGDOgC12p3rWmkLSk3o90mWccgEKZ9UMCOjGcZJy7GRpLC6HBq9F92XSYMcdGpwZ08byvurDl9uOy+6X9ae17Dq6Q3sR4v2LEVBNhNQW9h4wmEHPme87jIaX2vw5odeidDLEVD5zF7z0+loPDk9e3syef/hdDTwUDNvLoJFjMqNhR+0A+/Vq81IUwmFzvjP8fD8tGKkVorp2Kh3t6j/8fbX89HJZDgc0FH+328nH8bDQXjlONc+1xhMMscT47jZgltiy3wNr183RmfvG/T0Z9fcpmNRSpRm3iMNxADZZkEopsTajmaJSViHAJ114Tgl/11cGm+mr0DiRzLiF22Ho6YgSGwHlF2LWE+ugkgxc28jiZLlUduwKpvUJGtSgwpRk7cTxjKKKV+TQHsgoPfGC3Dp8UUcw90daLnAAs7y2Ir4kO4uIH9JBWMa8V5yFc0n00yr2HaXDDwWzI+BMUhZWVBAKYVUA1MfjA1qh80sWq0anaTRzg/tf/bcmprz6hWsJArhMxyAG5q6f1Aj1AW+9kHPkFdK0PRAW3HHMI+UoneH7aZFOaaUD5vN9BPlTLdlihpjcn8A1JYTcK9uoJFlbGMHmceobDMo+GW4YCbUE3iF0f3IVFrA02OyafSEaCi0GveCsrOmGg+YVXhtDVFVuSC6E7/k/AjrLFpbWe+p9+/If4+nb7mJ/ZVOQvJoC2YN2TSkYWQOiw5PgmetdEoVc2or8YoFF2ZXgr1topk7bz2RfTYu7bjL0pfiyxjl3sHAfCxJ7EnhHYE0YMfugtMSsQYzTQabmN9QV8TGZtl9bnZM7qO3macZ0g5iOeC3cUtlZOZypGWqykaiXkgO3dykZCeIGmM2SmvY4LaG4qUH+aua5pthdUz7z12bXYocD9LlsuK6Mh+cbMU5/NEhDts2CtqvNOKunl4OIIfNyAw6L4+Odihvrg0l7r3mtgtxQ83e8gBITtv0FIu2XJmIxXXLuNot7/ihlVlfMcifthKeYqpmYeraKNXho8SQvmVoX14gXSkf3ulGqwQsuMTYN5KpNNuO+QIVKuoF3NTs3FeXZuNpO/nQTx6NQ3WgO/8DfFks3w8OAAA='
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
install -d -m 0755 /usr/local/lib/tokenkey
printf '%s' "$PGDUMP_STORAGE_B64" | base64 -d | gunzip > /usr/local/lib/tokenkey/pgdump_storage.py
chmod 0600 /usr/local/lib/tokenkey/pgdump_storage.py
mkdir -p /var/lib/tokenkey/pgdump

printf '%s' "$DISK_METRICS_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-disk-metrics.sh
chmod +x /usr/local/bin/tokenkey-disk-metrics.sh
/usr/local/bin/tokenkey-disk-metrics.sh --selftest

printf '%s' "$BBR_GZB64" | base64 -d | gunzip > /usr/local/bin/ensure-tokenkey-bbr.sh
chmod +x /usr/local/bin/ensure-tokenkey-bbr.sh
/usr/local/bin/ensure-tokenkey-bbr.sh --apply
/usr/local/bin/ensure-tokenkey-bbr.sh --check

cat > /etc/systemd/system/tokenkey-disk-metrics.service <<'DMSEOF'
[Unit]
Description=tokenkey PROD on-box disk-full and memory-pressure Feishu alerts
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
Environment=TOKENKEY_DATA_VOLUME_PATH=/var/lib/tokenkey
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

# Off-box storage is installed separately with private host-only credentials.
# Never infer an AWS backup bucket from the SSM control-plane identity.

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
# QA maintenance/boundary + live QA_ARCHIVE_* stay off until ops enables them
# (staging must not archive an empty DB into the live raw bucket). Feishu webhook env still
# comes from post-boot sync (copy from AWS prod .env or sync-feishu-config).
systemctl enable --now tokenkey-pgdump.timer
systemctl enable --now tokenkey-disk-metrics.timer
systemctl enable --now tokenkey-ghcr-prune-daily.timer
sleep 30
docker compose -f /var/lib/tokenkey/docker-compose.yml --env-file /var/lib/tokenkey/.env ps || true
echo "HETZNER_PROD_BOOTSTRAP_DONE $(date -u +%FT%TZ)"
