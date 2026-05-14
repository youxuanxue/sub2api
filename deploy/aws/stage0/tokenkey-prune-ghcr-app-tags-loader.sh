#!/bin/bash
# Installed on EC2 as /usr/local/bin/tokenkey-prune-ghcr-app-tags.sh by Stage0 UserData.
# Fetches the real script (tokenkey-prune-ghcr-app-tags-inner.sh logic in SSM) and execs it —
# keeps EC2 UserData under the 16 KiB limit. Do not edit the on-host file for logic changes;
# edit deploy/aws/stage0/tokenkey-prune-ghcr-app-tags.sh and run build-cfn.sh.

set -euo pipefail
PATHFILE=/etc/tokenkey/ghcr-prune-ssm.path
if [ ! -f "$PATHFILE" ]; then
  echo "tokenkey-prune-ghcr-app-tags: missing path file" >&2
  exit 0
fi
IMDS_TOKEN="$(curl -fsS -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")"
REGION="$(curl -fsS -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/placement/region)"
PNAME="$(tr -d '\n' <"$PATHFILE")"
RAW="$(aws ssm get-parameter --name "$PNAME" --region "$REGION" --query Parameter.Value --output text)"
TMP="$(mktemp)"
cleanup() { rm -f "$TMP"; }
trap cleanup EXIT
printf '%s' "$RAW" | base64 -d >"$TMP"
chmod +x "$TMP"
exec bash "$TMP"
