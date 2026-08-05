#!/bin/bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BOOTSTRAP="${ROOT}/deploy/aws/stage0/stage0-ec2-bootstrap.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

FUNCTIONS="${TMP}/cloudwatch-alarm-functions.sh"
sed -n \
  '/^# BEGIN_TOKENKEY_CLOUDWATCH_ALARM_DELIVERY$/,/^# END_TOKENKEY_CLOUDWATCH_ALARM_DELIVERY$/p' \
  "${BOOTSTRAP}" \
  | sed '1d;$d' >"${FUNCTIONS}"
if [[ ! -s "${FUNCTIONS}" ]]; then
  echo "FAIL: CloudWatch alarm delivery functions are missing from bootstrap" >&2
  exit 1
fi

FAKE_BIN="${TMP}/bin"
mkdir -p "${FAKE_BIN}"

printf '%s\n' \
  '#!/bin/bash' \
  'set -euo pipefail' \
  'printf "1700000000\n"' >"${FAKE_BIN}/date"

printf '%s\n' \
  '#!/bin/bash' \
  'set -euo pipefail' \
  'printf "%s\n" "$*" >>"${FAKE_CURL_LOG}"' \
  '[[ "${FAKE_CURL_FAIL:-0}" != "1" ]] || exit 44' \
  'printf "%s\n" "${FAKE_FEISHU_RESPONSE}"' >"${FAKE_BIN}/curl"

printf '%s\n' \
  '#!/bin/bash' \
  'set -euo pipefail' \
  'printf "%s\n" "$*" >>"${FAKE_AWS_LOG}"' \
  '[[ "${FAKE_AWS_FAIL:-0}" != "1" ]] || exit 42' \
  '[[ "${1:-}" == "cloudwatch" && "${2:-}" == "describe-alarms" ]] || exit 43' \
  'cat "${FAKE_AWS_RESPONSE}"' >"${FAKE_BIN}/aws"
chmod +x "${FAKE_BIN}/date" "${FAKE_BIN}/curl" "${FAKE_BIN}/aws"

export PATH="${FAKE_BIN}:${PATH}"
export COOLDOWN=1800
export WEBHOOK="https://example.invalid/feishu"
export SECRET=""
export NODE="edge-test.example.com"
export REGION="us-west-2"
export FAKE_CURL_LOG="${TMP}/curl.log"
export FAKE_AWS_LOG="${TMP}/aws.log"
export FAKE_AWS_RESPONSE="${TMP}/alarms.json"

# shellcheck disable=SC1090
source "${FUNCTIONS}"

ALARM="tokenkey-edge-us4-cpu-24h-above-baseline"

reset_case() {
  local name="$1"
  export TOKENKEY_ALERT_STATE_DIR="${TMP}/${name}/state"
  mkdir -p "${TOKENKEY_ALERT_STATE_DIR}"
  : >"${FAKE_CURL_LOG}"
  : >"${FAKE_AWS_LOG}"
  export FAKE_FEISHU_RESPONSE='{"code":0}'
  export FAKE_CURL_FAIL=0
  export FAKE_AWS_FAIL=0
}

active_path() {
  printf '%s/tokenkey-cloudwatch-%s.active\n' "${TOKENKEY_ALERT_STATE_DIR}" "$1"
}

cooldown_path() {
  printf '%s/tokenkey-cloudwatch-%s.cooldown\n' "${TOKENKEY_ALERT_STATE_DIR}" "$1"
}

assert_exists() {
  [[ -e "$1" ]] || { echo "FAIL: expected $1 to exist" >&2; exit 1; }
}

assert_absent() {
  [[ ! -e "$1" ]] || { echo "FAIL: expected $1 to be absent" >&2; exit 1; }
}

reset_case alarm_accepted
handle_cloudwatch_alarm_state "${ALARM}" ALARM
assert_exists "$(active_path "${ALARM}")"
assert_exists "$(cooldown_path "${ALARM}")"

reset_case recovery_accepted
printf '1\n' >"$(active_path "${ALARM}")"
printf '1700000000\n' >"$(cooldown_path "${ALARM}")"
handle_cloudwatch_alarm_state "${ALARM}" OK
assert_absent "$(active_path "${ALARM}")"
assert_absent "$(cooldown_path "${ALARM}")"
grep -q 'state=OK' "${FAKE_CURL_LOG}" || {
  echo "FAIL: recovery notification was not posted" >&2
  exit 1
}

reset_case alarm_rejected
export FAKE_FEISHU_RESPONSE='{"code":19001}'
if handle_cloudwatch_alarm_state "${ALARM}" ALARM; then
  echo "FAIL: rejected firing notification returned success" >&2
  exit 1
fi
assert_absent "$(active_path "${ALARM}")"

reset_case alarm_without_webhook
WEBHOOK=""
if handle_cloudwatch_alarm_state "${ALARM}" ALARM; then
  echo "FAIL: firing notification without a webhook returned success" >&2
  exit 1
fi
assert_absent "$(active_path "${ALARM}")"
WEBHOOK="https://example.invalid/feishu"

reset_case alarm_curl_failure
export FAKE_CURL_FAIL=1
if handle_cloudwatch_alarm_state "${ALARM}" ALARM; then
  echo "FAIL: firing notification with a curl failure returned success" >&2
  exit 1
fi
assert_absent "$(active_path "${ALARM}")"

reset_case recovery_rejected
printf '1\n' >"$(active_path "${ALARM}")"
printf '1700000000\n' >"$(cooldown_path "${ALARM}")"
export FAKE_FEISHU_RESPONSE='{"code":19001}'
if handle_cloudwatch_alarm_state "${ALARM}" OK; then
  echo "FAIL: rejected recovery notification returned success" >&2
  exit 1
fi
assert_exists "$(active_path "${ALARM}")"
assert_exists "$(cooldown_path "${ALARM}")"

reset_case insufficient_data
printf '1\n' >"$(active_path "${ALARM}")"
handle_cloudwatch_alarm_state "${ALARM}" INSUFFICIENT_DATA
assert_exists "$(active_path "${ALARM}")"
[[ ! -s "${FAKE_CURL_LOG}" ]] || {
  echo "FAIL: INSUFFICIENT_DATA must not send a recovery" >&2
  exit 1
}

reset_case missing_alarm
SECOND_ALARM="tokenkey-edge-us4-cpu-surplus-borrowing"
export TOKENKEY_CLOUDWATCH_CPU_ALARM_NAMES="${ALARM},${SECOND_ALARM}"
printf '{"MetricAlarms":[{"AlarmName":"%s","StateValue":"ALARM"}]}\n' \
  "${ALARM}" >"${FAKE_AWS_RESPONSE}"
if poll_cloudwatch_cpu_alarms >/dev/null 2>&1; then
  echo "FAIL: missing configured alarm returned success" >&2
  exit 1
fi
assert_absent "$(active_path "${ALARM}")"
[[ "$(wc -l <"${FAKE_AWS_LOG}" | tr -d ' ')" == "1" ]] || {
  echo "FAIL: alarm states must be fetched in one AWS call" >&2
  exit 1
}

reset_case aws_failure
export TOKENKEY_CLOUDWATCH_CPU_ALARM_NAMES="${ALARM}"
export FAKE_AWS_FAIL=1
if poll_cloudwatch_cpu_alarms >/dev/null 2>&1; then
  echo "FAIL: AWS query failure returned success" >&2
  exit 1
fi
assert_absent "$(active_path "${ALARM}")"

reset_case invalid_alarm_name
export TOKENKEY_CLOUDWATCH_CPU_ALARM_NAMES='../escape'
printf '{"MetricAlarms":[]}\n' >"${FAKE_AWS_RESPONSE}"
if poll_cloudwatch_cpu_alarms >/dev/null 2>&1; then
  echo "FAIL: unsafe alarm name returned success" >&2
  exit 1
fi
assert_absent "${TMP}/escape.active"

echo "ok: EC2 CloudWatch alarm delivery state transitions"
