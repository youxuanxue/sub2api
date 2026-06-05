#!/bin/bash
# Publish DataVolume used_percent to CloudWatch. Installed by stage0-ec2-bootstrap.sh.
set -euo pipefail
IMDS_TOKEN="$(curl -fsS -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")"
REGION="$(curl -fsS -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/placement/region)"
IID="$(curl -fsS -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/instance-id)"
USED="$(df -P /var/lib/tokenkey 2>/dev/null | awk 'NR==2 {gsub(/%/,"",$5); print $5}')"
[ -z "${USED}" ] && exit 0
aws cloudwatch put-metric-data --region "${REGION}" \
  --namespace tokenkey/EC2 \
  --metric-data "MetricName=DataVolumeUsedPercent,Value=${USED},Unit=Percent,Dimensions=[{Name=InstanceId,Value=${IID}}]"
