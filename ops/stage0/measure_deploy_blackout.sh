#!/usr/bin/env bash
# measure_deploy_blackout.sh — 量化一次发版的「客户端可见真空窗口」。
#
# 背景：Stage0 prod 单节点发版（ops/stage0/deploy_via_ssm.sh）已经做了预拉镜像
# + SIGUSR1 优雅排空 + Caddy 主动摘除 + 30s 排队重试，正常发版的真空只有
# old→new 切换的极短一刻。这个脚本用「数据」替代「假设」：在发版期间从一个
# 干净 vantage（本地 / CI runner）对**经过 Caddy 的公网端点**高频打只读探针，
# 捕获任何客户端可见的失败（连接拒绝 / 超时 / 5xx），输出最长连续真空 ms。
#
# 为什么打公网 /health 而非容器内部：drain 阶段旧容器 /health 返回 503，但那是
# Caddy active-health 内部消费的——客户端经 Caddy 打公网 URL 时，请求会被排队到
# 健康容器、不该看到 503。所以「探针看到 5xx/000」== 真正的客户端可见真空。
#
# 用法：
#   TOKENKEY_BASE_URL=https://api.tokenkey.dev bash ops/stage0/measure_deploy_blackout.sh
#   # 然后在另一个终端 / CI 步骤里触发一次发版（deploy_via_ssm.sh），探针自动捕获。
#   # 探针运行到 DURATION_SECONDS 结束，或随时 Ctrl-C 提前打印汇总。
#
# 环境变量：
#   TOKENKEY_BASE_URL / TK_GATEWAY_URL  必填，目标网关根 URL（经 Caddy 的公网入口）
#   PROBE_PATH        探测路径，默认 /health（公开、无需鉴权、最接近 LB 健康语义）
#   INTERVAL_SECONDS  探测间隔，默认 0.1（高频以捕获亚秒级真空）
#   DURATION_SECONDS  总运行时长，默认 0 = 无限，直到 Ctrl-C
#   CURL_TIMEOUT_SECONDS  单请求超时，默认 2。它定义「超过多久无响应即算真空」：
#                         网关用 Caddy lb_try_duration 排队重试时，赶上发版的请求会被排队
#                         （最长可达 lb_try_duration，prod=30s）。若把 timeout 设得比排队还长，
#                         串行探针会被单个请求阻塞、丢失时间分辨率，把稀疏失败误算成超长连续
#                         真空（实测教训）。取贴近「客户端卡顿耐受」的小值（1–2s）。
#   FAIL_IF_BLACKOUT_MS   非 0 时，最长真空 ≥ 该阈值则脚本 exit 1（CI gate 用）；默认 0=只报告
#   PROBE_FORCE_PROXY     默认 0：打 127.0.0.1/localhost 时自动绕过 HTTP_PROXY（否则本机代理会
#                         把回环请求变成假 502/连接失败）。设 1 强制经代理。
#
# 输出：每个真空窗口的 [起始相对 ms, 时长 ms, 首/末状态码]，结束打印汇总
# （总请求 / 失败数 / 真空窗口数 / 最长真空 ms / 出现过的状态码分布）。
#
# 依赖：curl + perl（perl 仅用于跨平台毫秒时间戳；macOS 自带 date 无 %N）。

set -uo pipefail   # 故意不加 -e：探针循环里单次 curl 失败必须继续探测，不能中止。

BASE="${TOKENKEY_BASE_URL:-${TK_GATEWAY_URL:-}}"
BASE="${BASE%/}"
PROBE_PATH="${PROBE_PATH:-/health}"
INTERVAL_SECONDS="${INTERVAL_SECONDS:-0.1}"
DURATION_SECONDS="${DURATION_SECONDS:-0}"
CURL_TIMEOUT_SECONDS="${CURL_TIMEOUT_SECONDS:-2}"
FAIL_IF_BLACKOUT_MS="${FAIL_IF_BLACKOUT_MS:-0}"

if [[ -z "${BASE}" ]]; then
  echo "measure_deploy_blackout: set TOKENKEY_BASE_URL (or TK_GATEWAY_URL)" >&2
  exit 2
fi
command -v curl >/dev/null 2>&1 || { echo "measure_deploy_blackout: curl not on PATH" >&2; exit 2; }
command -v perl >/dev/null 2>&1 || { echo "measure_deploy_blackout: perl not on PATH (needed for ms clock)" >&2; exit 2; }

URL="${BASE}${PROBE_PATH}"

# 打回环地址时，本机若设了 HTTP_PROXY，curl 会把请求经代理回环 → 假 502/连接失败。
# 对 localhost/127.0.0.1/[::1] 默认绕过代理；PROBE_FORCE_PROXY=1 可关闭。
noproxy_args=()
if [[ "${PROBE_FORCE_PROXY:-0}" != "1" ]] && [[ "${URL}" =~ ^https?://(127\.0\.0\.1|localhost|\[::1\]) ]]; then
  noproxy_args=(--noproxy '*')
fi

now_ms() { perl -MTime::HiRes -e 'printf("%d\n", Time::HiRes::time()*1000)'; }

# --- accounting state ---
total=0
failures=0
blackout_count=0
longest_ms=0
in_blackout=0
blackout_start_ms=0
blackout_first_code=""
# 固定分类计数器（bash 3.2 兼容，不用关联数组——macOS 自带 bash 无 declare -A）。
cnt_2xx=0; cnt_conn=0; cnt_5xx=0; cnt_4xx=0; cnt_other=0

start_ms="$(now_ms)"

print_summary() {
  # 若结束时仍在真空中，结算最后一个窗口（用最后一次探测时刻封口）。
  if [[ "${in_blackout}" == "1" ]]; then
    local end_ms dur
    end_ms="$(now_ms)"
    dur=$(( end_ms - blackout_start_ms ))
    (( dur > longest_ms )) && longest_ms=${dur}
    printf '  blackout #%d: start=+%dms duration=%dms first=%s last=(open-at-exit)\n' \
      "${blackout_count}" "$(( blackout_start_ms - start_ms ))" "${dur}" "${blackout_first_code}"
  fi
  echo "=== measure_deploy_blackout summary ==="
  echo "  url=${URL} interval=${INTERVAL_SECONDS}s"
  echo "  total_requests=${total} failures=${failures} blackout_windows=${blackout_count}"
  echo "  longest_blackout_ms=${longest_ms}"
  printf '  status_codes: 2xx=%d conn_fail_or_timeout(000)=%d 5xx=%d 4xx=%d other=%d\n' \
    "${cnt_2xx}" "${cnt_conn}" "${cnt_5xx}" "${cnt_4xx}" "${cnt_other}"
  if [[ "${failures}" == "0" ]]; then
    echo "  verdict: NO client-visible blackout observed — 发版对客户端无感。"
  else
    echo "  verdict: ${failures} failed probe(s) across ${blackout_count} window(s); 最长真空 ${longest_ms}ms。"
  fi
  if [[ "${FAIL_IF_BLACKOUT_MS}" != "0" ]] && (( longest_ms >= FAIL_IF_BLACKOUT_MS )); then
    echo "::error::longest blackout ${longest_ms}ms >= threshold ${FAIL_IF_BLACKOUT_MS}ms"
    exit 1
  fi
  exit 0
}
trap print_summary INT TERM

echo "measure_deploy_blackout: probing ${URL} every ${INTERVAL_SECONDS}s"
[[ "${DURATION_SECONDS}" != "0" ]] && echo "  (auto-stop after ${DURATION_SECONDS}s; Ctrl-C anytime for summary)" \
  || echo "  (runs until Ctrl-C)"

deadline_ms=0
[[ "${DURATION_SECONDS}" != "0" ]] && deadline_ms=$(( start_ms + DURATION_SECONDS * 1000 ))

while true; do
  # curl 的 -w '%{http_code}' 在连接失败/超时时自己就输出 "000"，无需再 `|| echo`
  # （那样会与 curl 的输出拼成 "000000"）。command-sub 里 curl 非零退出不影响赋值。
  code="$(curl -sS ${noproxy_args[@]+"${noproxy_args[@]}"} -o /dev/null -m "${CURL_TIMEOUT_SECONDS}" -w '%{http_code}' "${URL}" 2>/dev/null)"
  code="${code:-000}"
  total=$(( total + 1 ))
  case "${code}" in
    2[0-9][0-9]) cnt_2xx=$(( cnt_2xx + 1 )) ;;
    000)         cnt_conn=$(( cnt_conn + 1 )) ;;
    5[0-9][0-9]) cnt_5xx=$(( cnt_5xx + 1 )) ;;
    4[0-9][0-9]) cnt_4xx=$(( cnt_4xx + 1 )) ;;
    *)           cnt_other=$(( cnt_other + 1 )) ;;
  esac

  # 2xx == healthy. 其它一律视为客户端可见失败（000=连接拒绝/超时, 5xx, 4xx 异常）。
  if [[ "${code}" =~ ^2[0-9][0-9]$ ]]; then
    if [[ "${in_blackout}" == "1" ]]; then
      now="$(now_ms)"; dur=$(( now - blackout_start_ms ))
      (( dur > longest_ms )) && longest_ms=${dur}
      printf '  blackout #%d: start=+%dms duration=%dms first=%s recovered=%s\n' \
        "${blackout_count}" "$(( blackout_start_ms - start_ms ))" "${dur}" "${blackout_first_code}" "${code}"
      in_blackout=0
    fi
  else
    failures=$(( failures + 1 ))
    if [[ "${in_blackout}" == "0" ]]; then
      in_blackout=1
      blackout_start_ms="$(now_ms)"
      blackout_first_code="${code}"
      blackout_count=$(( blackout_count + 1 ))
    fi
  fi

  if [[ "${deadline_ms}" != "0" ]] && (( $(now_ms) >= deadline_ms )); then
    print_summary
  fi
  sleep "${INTERVAL_SECONDS}"
done
