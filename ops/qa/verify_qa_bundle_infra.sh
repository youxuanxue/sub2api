#!/usr/bin/env bash
# Read-only readiness gate for the prod QA Bundle infrastructure.
set -euo pipefail

STACK="${QA_RAW_ARCHIVE_STACK:-tokenkey-prod-qa-raw-archive}"
REGION="${AWS_REGION:-us-east-1}"
EXPECTED_IMAGE="${QA_BUNDLE_WORKER_IMAGE:-}"
EXPECTED_DESIRED="${QA_BUNDLE_WORKER_DESIRED_COUNT:-1}"

[[ "${EXPECTED_DESIRED}" =~ ^[1-9][0-9]*$ ]] || { echo "QA_BUNDLE_WORKER_DESIRED_COUNT must be positive" >&2; exit 1; }

stack_json="$(aws cloudformation describe-stacks --region "${REGION}" --stack-name "${STACK}" --output json)"
stack_status="$(jq -r '.Stacks[0].StackStatus // empty' <<<"${stack_json}")"
[[ "${stack_status}" =~ _COMPLETE$ ]] || { echo "QA Bundle stack is not complete: ${stack_status}" >&2; exit 1; }
if [ -z "${EXPECTED_IMAGE}" ]; then
  EXPECTED_IMAGE="$(jq -r '.Stacks[0].Parameters[]? | select(.ParameterKey == "BundleWorkerImage") | .ParameterValue' <<<"${stack_json}")"
fi
[[ -n "${EXPECTED_IMAGE}" && "${EXPECTED_IMAGE}" != None ]] || { echo "QA Bundle worker image expectation is unavailable" >&2; exit 1; }

parameter() {
  local key="$1"
  jq -r --arg key "${key}" '.Stacks[0].Parameters[]? | select(.ParameterKey == $key) | .ParameterValue' <<<"${stack_json}"
}

output() {
  local key="$1"
  jq -r --arg key "${key}" '.Stacks[0].Outputs[]? | select(.OutputKey == $key) | .OutputValue' <<<"${stack_json}"
}

browser_origin="$(parameter BundleBrowserAllowedOrigin)"
retention_days="$(parameter BundleRetentionDays)"
[[ -n "${browser_origin}" && "${browser_origin}" != None ]] || { echo "QA Bundle browser origin expectation is unavailable" >&2; exit 1; }
[[ "${retention_days}" =~ ^[1-9][0-9]*$ ]] || { echo "QA Bundle retention expectation is unavailable" >&2; exit 1; }

bucket="$(output QaBundleBucketName)"
queue_url="$(output QaBundleQueueUrl)"
dlq_url="$(output QaBundleDeadLetterQueueUrl)"
cluster="$(output QaBundleWorkerClusterName)"
service="$(output QaBundleWorkerServiceName)"
for value in "${bucket}" "${queue_url}" "${dlq_url}" "${cluster}" "${service}"; do
  [[ -n "${value}" && "${value}" != None ]] || { echo "QA Bundle stack output is incomplete" >&2; exit 1; }
done

aws s3api head-bucket --region "${REGION}" --bucket "${bucket}" >/dev/null
cors_json="$(aws s3api get-bucket-cors --region "${REGION}" --bucket "${bucket}" --output json)"
jq -e --arg origin "${browser_origin}" '
  (.CORSRules | length) == 1 and
  .CORSRules[0].AllowedOrigins == [$origin] and
  .CORSRules[0].AllowedHeaders == ["*"] and
  (.CORSRules[0].AllowedMethods | sort) == ["GET", "HEAD"] and
  (.CORSRules[0].ExposeHeaders | sort) == ["Content-Encoding", "Content-Length", "ETag"] and
  .CORSRules[0].MaxAgeSeconds == 300
' <<<"${cors_json}" >/dev/null || { echo "QA Bundle bucket CORS drift" >&2; exit 1; }

encryption_json="$(aws s3api get-bucket-encryption --region "${REGION}" --bucket "${bucket}" --output json)"
jq -e '
  (.ServerSideEncryptionConfiguration.Rules | length) == 1 and
  .ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm == "AES256"
' <<<"${encryption_json}" >/dev/null || { echo "QA Bundle bucket encryption drift" >&2; exit 1; }

lifecycle_json="$(aws s3api get-bucket-lifecycle-configuration --region "${REGION}" --bucket "${bucket}" --output json)"
jq -e --argjson retention_days "${retention_days}" '
  [.Rules[]? | select(.ID == "expire-qa-bundle-job-surfaces")] as $rules |
  ($rules | length) == 1 and
  $rules[0].Status == "Enabled" and
  ($rules[0].Filter.Prefix // $rules[0].Prefix // "") == "user-qa/qa-bundles/v1/jobs/" and
  $rules[0].Expiration.Days == $retention_days
' <<<"${lifecycle_json}" >/dev/null || { echo "QA Bundle bucket lifecycle drift" >&2; exit 1; }

queue_attrs="$(aws sqs get-queue-attributes --region "${REGION}" --queue-url "${queue_url}" --attribute-names QueueArn ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible --output json)"
dlq_attrs="$(aws sqs get-queue-attributes --region "${REGION}" --queue-url "${dlq_url}" --attribute-names QueueArn ApproximateNumberOfMessages --output json)"
dlq_depth="$(jq -r '.Attributes.ApproximateNumberOfMessages // "0"' <<<"${dlq_attrs}")"
[[ "${dlq_depth}" = 0 ]] || { echo "QA Bundle DLQ is not empty: ${dlq_depth}" >&2; exit 1; }

service_json="$(aws ecs describe-services --region "${REGION}" --cluster "${cluster}" --services "${service}" --output json)"
failures="$(jq '.failures | length' <<<"${service_json}")"
status="$(jq -r '.services[0].status // empty' <<<"${service_json}")"
desired="$(jq -r '.services[0].desiredCount // -1' <<<"${service_json}")"
running="$(jq -r '.services[0].runningCount // -1' <<<"${service_json}")"
task_definition="$(jq -r '.services[0].taskDefinition // empty' <<<"${service_json}")"
[[ "${failures}" = 0 && "${status}" = ACTIVE ]] || { echo "QA Bundle ECS service is unavailable" >&2; exit 1; }
[[ "${desired}" = "${EXPECTED_DESIRED}" && "${running}" -ge "${EXPECTED_DESIRED}" ]] || {
  echo "QA Bundle ECS capacity mismatch desired=${desired} running=${running} expected=${EXPECTED_DESIRED}" >&2
  exit 1
}

task_json="$(aws ecs describe-task-definition --region "${REGION}" --task-definition "${task_definition}" --output json)"
actual_image="$(jq -r '.taskDefinition.containerDefinitions[] | select(.name == "qa-bundle-worker") | .image' <<<"${task_json}")"
[[ "${actual_image}" = "${EXPECTED_IMAGE}" ]] || {
  echo "QA Bundle worker image mismatch expected=${EXPECTED_IMAGE} actual=${actual_image}" >&2
  exit 1
}

jq -n \
  --arg stack "${STACK}" --arg status "${stack_status}" --arg bucket "${bucket}" \
  --arg browser_origin "${browser_origin}" --argjson retention_days "${retention_days}" \
  --arg queue_url "${queue_url}" --arg dlq_url "${dlq_url}" \
  --arg cluster "${cluster}" --arg service "${service}" --arg image "${actual_image}" \
  --argjson desired "${desired}" --argjson running "${running}" --argjson dlq_depth "${dlq_depth}" \
  '{ok:true,stack:$stack,stack_status:$status,bucket:$bucket,browser_origin:$browser_origin,retention_days:$retention_days,queue_url:$queue_url,dlq_url:$dlq_url,cluster:$cluster,service:$service,image:$image,desired_count:$desired,running_count:$running,dlq_depth:$dlq_depth}'
