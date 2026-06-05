#!/bin/bash
# Thin EC2 UserData launcher — embedded in stage0-single-ec2.yaml via build-cfn.sh.
# Fn::Sub replaces ${ApiDomain} etc; bash uses ${!TK_*} for runtime expansion.
set -euxo pipefail
exec > >(tee -a /var/log/tokenkey-bootstrap.log) 2>&1

export TK_API_DOMAIN='${ApiDomain}'
export TK_ACME_EMAIL='${AcmeEmail}'
export TK_ADMIN_EMAIL='${AdminEmail}'
export TK_TZ='${Timezone}'
export TK_AWS_REGION='${AWS::Region}'
export TK_GHCR_OWNER='${GhcrOwner}'
export TK_GHCR_IMAGE_NAME='${GhcrImageName}'
export TK_IMAGE_TAG='${ImageTag}'
export TK_GHCR_PULL_USER='${GhcrPullUser}'
export TK_GHCR_PAT_SSM_NAME='${GhcrPatSsmName}'
export TK_DATA_VOLUME_ID='${DataVolume}'
export TK_PROJECT_NAME='${ProjectName}'
export TK_ENVIRONMENT='${Environment}'
export TK_QA_STALE_RETENTION_DAYS='${QaStaleRetentionDays}'
export TK_TOKENKEY_IMAGE="ghcr.io/${GhcrOwner}/${GhcrImageName}:${ImageTag}"
export TK_STAGE0_PREFIX='/${ProjectName}/${Environment}/stage0'

B64=""
B64+="$(aws ssm get-parameter --name "${!TK_STAGE0_PREFIX}/bootstrap.sh.gzip.b64.part1" --region "${!TK_AWS_REGION}" --query Parameter.Value --output text)"
B64+="$(aws ssm get-parameter --name "${!TK_STAGE0_PREFIX}/bootstrap.sh.gzip.b64.part2" --region "${!TK_AWS_REGION}" --query Parameter.Value --output text)"
TMP="$(mktemp)"
cleanup() { rm -f "${!TMP}"; }
trap cleanup EXIT
printf '%s' "${!B64}" | base64 -d | gunzip > "${!TMP}"
chmod +x "${!TMP}"
exec bash "${!TMP}"
