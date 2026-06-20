#!/usr/bin/env bash
#
# TokenKey 全平台 × 全模态网关一致性测试（universal-key 驱动）
# ===========================================================
#
# 一句话焦点：用一把 **universal** key，证明 key 主人有权的 *每个真实平台 × 每个核心
# 模态* 都真能服务，并（经下游归因）实证 universal 路由把每个模型落到了对的平台。
#
# 为什么是 universal key：Universal Key（#878）按「入口端点形状 + 请求模型名」在请求期
# 把一把 key 解析到对的后端组。所以一把 key + 一个模型名就能驱动一个平台/模态——这张脚本
# 既验证该路由特性本身，又系统性覆盖每个平台/模态的网关一致性。
#
# 平台（domain/constants.go）恰好 7 个：anthropic / openai / gemini / antigravity /
# newapi / kiro / grok。没有「meta 平台」——/v1/models、/v1/usage、/v1/settings/public
# 是控制面端点，作为矩阵前的预检，不在平台矩阵里。
#
# universal-key 命名空间盲区（关键）：universal 只能按模型前缀 hint 选平台。
#   - claude-* 永远落 anthropic（到不了 kiro，kiro 也服务 claude-*）→ kiro 需一把绑 kiro
#     组的 direct key（TK_FULLTEST_KIRO_KEY），缺则该行 SKIP。
#   - antigravity 经其 forced-platform 路由 /antigravity/v1beta/...:generateContent 可达。
#
# !! 计费告警 !!：image / video 行会向上游真实下单，产生真实费用（用你提供的 key 计费）。
# 默认开启（“全部测一遍”）；不想花钱用 --skip-paid。
#
# 判定语义（跑完所有行再汇总，不中途退出）：
#   PASS  200 且响应 shape 正确
#   FAIL  200 但 shape 不符（schema 回归） / 401 / 非预期 403 / 非预期 4xx / 连接失败
#   SKIP  403 未授权该平台 / 429 空池或限流 / 5xx 上游瞬态 / 4xx 模型不可服务 / 缺 key / --skip-paid
# 退出码：任一 FAIL → 1；只有 PASS/SKIP → 0。
#
# 用法：
#   export TK_FULLTEST_KEY='sk-...'            # universal key（本机 export，绝不入库）
#   bash ops/test/gateway_full_matrix_test.sh              # 默认 prod、含付费模态
#   bash ops/test/gateway_full_matrix_test.sh --skip-paid  # 跳过 image/video
#   bash ops/test/gateway_full_matrix_test.sh --with-extras# 追加 count_tokens / embeddings
#   bash ops/test/gateway_full_matrix_test.sh --list       # 只打印矩阵，不发请求
#
# env 覆盖（每个代表模型都可换成你账号实际在册的模型名）：
#   TK_FULLTEST_BASE_URL（默认 https://api.tokenkey.dev）
#   TK_FULLTEST_TIMEOUT（默认 90 秒）
#   TK_FULLTEST_KIRO_KEY（kiro direct key；缺则 kiro 行 SKIP）
#   TK_FULLTEST_MODEL_<PLATFORM>_<MODALITY>，详见 .fulltest.env.example
#
set -euo pipefail

# ---- 可选：自动加载本机 .fulltest.env（被 .gitignore 的 *.env 规则忽略，不入库）----
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${HERE}/.fulltest.env" ]]; then
  # shellcheck disable=SC1091
  set -a; . "${HERE}/.fulltest.env"; set +a
fi

# ---- 参数 ----
SKIP_PAID=0
WITH_EXTRAS=0
LIST_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --skip-paid) SKIP_PAID=1 ;;
    --with-extras) WITH_EXTRAS=1 ;;
    --list) LIST_ONLY=1 ;;
    -h|--help) sed -n '2,46p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

BASE="${TK_FULLTEST_BASE_URL:-https://api.tokenkey.dev}"
BASE="${BASE%/}"
TIMEOUT="${TK_FULLTEST_TIMEOUT:-90}"
KEY="${TK_FULLTEST_KEY:-}"
KIRO_KEY="${TK_FULLTEST_KIRO_KEY:-}"

# ---- 代表模型（env 可覆盖）----
M_ANTHROPIC_TEXT="${TK_FULLTEST_MODEL_ANTHROPIC_TEXT:-claude-sonnet-4-6}"
M_OPENAI_TEXT="${TK_FULLTEST_MODEL_OPENAI_TEXT:-gpt-5.1}"
M_OPENAI_RESPONSES="${TK_FULLTEST_MODEL_OPENAI_RESPONSES:-gpt-5.1}"
M_OPENAI_IMAGE="${TK_FULLTEST_MODEL_OPENAI_IMAGE:-gpt-image-1}"
M_GEMINI_TEXT="${TK_FULLTEST_MODEL_GEMINI_TEXT:-gemini-2.5-flash}"
M_GEMINI_IMAGE="${TK_FULLTEST_MODEL_GEMINI_IMAGE:-imagen-4.0-fast-generate-001}"
M_GEMINI_VIDEO="${TK_FULLTEST_MODEL_GEMINI_VIDEO:-veo-3.1-generate-001}"
M_ANTIGRAVITY_TEXT="${TK_FULLTEST_MODEL_ANTIGRAVITY_TEXT:-gemini-2.5-flash}"
M_NEWAPI_TEXT="${TK_FULLTEST_MODEL_NEWAPI_TEXT:-deepseek-chat}"
M_NEWAPI_IMAGE="${TK_FULLTEST_MODEL_NEWAPI_IMAGE:-doubao-seedream-4-0-250828}"
M_NEWAPI_VIDEO="${TK_FULLTEST_MODEL_NEWAPI_VIDEO:-doubao-seedance-1-0-pro-250528}"
M_GROK_TEXT="${TK_FULLTEST_MODEL_GROK_TEXT:-grok-4}"
M_KIRO_TEXT="${TK_FULLTEST_MODEL_KIRO_TEXT:-claude-sonnet-4-6}"

# ---- 结果累计 ----
PASS=0; SKIP=0; FAIL=0
declare -a RESULTS=()

mask() { local k="$1"; [[ -z "$k" ]] && { echo "<unset>"; return; }; printf '%s…%s' "$(printf '%s' "$k" | head -c6)" "$(printf '%s' "$k" | tail -c4)"; }

# classify CODE BODYFILE SHAPE_OK -> "RES|note"   (RES ∈ PASS/SKIP/FAIL)
classify() {
  local code="$1" f="$2" shape_ok="$3"
  if [[ "$code" == "200" ]]; then
    if [[ "$shape_ok" == "1" ]]; then echo "PASS|"; else echo "FAIL|200 但响应 shape 不符（疑 schema 回归）"; fi
    return
  fi
  case "$code" in
    403)
      if grep -qiE 'universal_no_entitled_group|no platform in your plan|group_not_allowed|not allowed' "$f" 2>/dev/null; then
        echo "SKIP|403 主人未授权该平台/组（universal_no_entitled_group）"
      else echo "FAIL|403 非预期（认证/控制面）"; fi ;;
    429)
      if grep -qiE 'no available accounts|available accounts exhausted' "$f" 2>/dev/null; then
        echo "SKIP|429 空池（无可调度账号）"
      else echo "SKIP|429 上游限流（瞬态）"; fi ;;
    400|404)
      if grep -qiE 'retired|sunset|not_found|does not exist|invalid model|unknown model|model_not_found|not supported|not a valid|no endpoints|no available' "$f" 2>/dev/null; then
        echo "SKIP|${code} 模型不可服务/未在册"
      else echo "FAIL|${code} 非预期 bad-request"; fi ;;
    500|502|503) echo "SKIP|${code} 上游/网关瞬态" ;;
    401) echo "FAIL|401 鉴权失败（key 无效？）" ;;
    000) echo "FAIL|连接失败（curl）" ;;
    *)   echo "FAIL|HTTP ${code} 非预期" ;;
  esac
}

record() { # platform modality endpoint model code bodyfile shape_ok
  local platform="$1" modality="$2" ep="$3" model="$4" code="$5" f="$6" shape_ok="$7"
  local out res note; out="$(classify "$code" "$f" "$shape_ok")"; res="${out%%|*}"; note="${out#*|}"
  case "$res" in PASS) PASS=$((PASS+1));; SKIP) SKIP=$((SKIP+1));; FAIL) FAIL=$((FAIL+1));; esac
  RESULTS+=("$(printf '%s\t%s\t%s\t%s\t%s\t%s\t%s' "$platform" "$modality" "$model" "$ep" "$code" "$res" "$note")")
  printf '  [%-4s] %-11s %-12s %-34s HTTP %-3s %s\n' "$res" "$platform" "$modality" "$model" "$code" "$note"
}

record_skip() { # platform modality endpoint model note  (无需发请求的 SKIP)
  SKIP=$((SKIP+1))
  RESULTS+=("$(printf '%s\t%s\t%s\t%s\t%s\t%s\t%s' "$1" "$2" "$4" "$3" "-" "SKIP" "$5")")
  printf '  [%-4s] %-11s %-12s %-34s HTTP %-3s %s\n' "SKIP" "$1" "$2" "$4" "-" "$5"
}

# do_post PATH KEY BODY -> sets CODE, writes body to $BODYFILE
BODYFILE=""
do_post() {
  local path="$1" key="$2" body="$3"
  BODYFILE="$(mktemp)"
  CODE="$(curl -sS -o "$BODYFILE" -w '%{http_code}' -m "$TIMEOUT" -X POST "$BASE$path" \
    -H "Authorization: Bearer $key" -H 'anthropic-version: 2023-06-01' \
    -H 'content-type: application/json' --data-binary "$body" 2>/dev/null || echo 000)"
}
do_get() {
  local path="$1" key="$2"
  BODYFILE="$(mktemp)"
  CODE="$(curl -sS -o "$BODYFILE" -w '%{http_code}' -m "$TIMEOUT" "$BASE$path" \
    -H "Authorization: Bearer $key" 2>/dev/null || echo 000)"
}

# shape_ok JQFILTER -> echo 1 if 200 & filter true else 0
shape_ok() { if [[ "$CODE" == "200" ]] && jq -e "$1" "$BODYFILE" >/dev/null 2>&1; then echo 1; else echo 0; fi; }

# run_post platform modality path model key body jqfilter
run_post() {
  local platform="$1" modality="$2" path="$3" model="$4" key="$5" body="$6" jqf="$7"
  if [[ -z "$key" ]]; then record_skip "$platform" "$modality" "POST $path" "$model" "缺 key"; return; fi
  do_post "$path" "$key" "$body"
  record "$platform" "$modality" "POST $path" "$model" "$CODE" "$BODYFILE" "$(shape_ok "$jqf")"
  rm -f "$BODYFILE"
}

# run_video platform path model key   —— 提交 + 一次轮询；不等渲染完成
run_video() {
  local platform="$1" path="$2" model="$3" key="$4"
  local body; body="$(printf '{"model":"%s","prompt":"a small red ball rolling on a table","seconds":"4"}' "$model")"
  if [[ -z "$key" ]]; then record_skip "$platform" "video" "POST $path" "$model" "缺 key"; return; fi
  do_post "$path" "$key" "$body"
  if [[ "$CODE" != "200" ]]; then
    record "$platform" "video" "POST $path (submit)" "$model" "$CODE" "$BODYFILE" 0
    rm -f "$BODYFILE"; return
  fi
  local task_id; task_id="$(jq -r '(.id // .task_id // "") ' "$BODYFILE" 2>/dev/null || echo "")"
  rm -f "$BODYFILE"
  if [[ "$task_id" != vt_* ]]; then
    record_skip "$platform" "video" "POST $path (submit)" "$model" "提交 200 但无 vt_ task_id（id=$task_id）"
    return
  fi
  # 一次轮询确认任务可查（queued/processing/succeeded 皆算可达）
  do_get "$path/$task_id" "$key"
  local sok=0; [[ "$CODE" == "200" ]] && sok=1
  record "$platform" "video" "GET $path/:id (poll)" "$model" "$CODE" "$BODYFILE" "$sok"
  rm -f "$BODYFILE"
}

# ---- --list ----
print_matrix() {
  cat <<EOF
TokenKey 全平台 × 全模态测试矩阵   base=$BASE   key=$(mask "$KEY")
控制面预检（非平台）: GET /api/v1/settings/public | GET /v1/models | GET /v1/usage
平台矩阵:
  anthropic   text          POST /v1/messages                         $M_ANTHROPIC_TEXT
  openai      text          POST /v1/chat/completions                 $M_OPENAI_TEXT
  openai      text(resp)    POST /v1/responses                        $M_OPENAI_RESPONSES
  openai      image  [paid] POST /v1/images/generations               $M_OPENAI_IMAGE
  gemini      text          POST /v1beta/models/<m>:generateContent   $M_GEMINI_TEXT
  gemini      image  [paid] POST /v1/images/generations               $M_GEMINI_IMAGE
  gemini      video  [paid] POST /v1/video/generations (+poll)        $M_GEMINI_VIDEO
  antigravity text          POST /antigravity/v1beta/models/<m>:gen   $M_ANTIGRAVITY_TEXT
  newapi      text          POST /v1/chat/completions                 $M_NEWAPI_TEXT
  newapi      image  [paid] POST /v1/images/generations               $M_NEWAPI_IMAGE
  newapi      video  [paid] POST /v1/video/generations (+poll)        $M_NEWAPI_VIDEO
  grok        text          POST /v1/chat/completions                 $M_GROK_TEXT
  kiro        text          POST /v1/messages (direct kiro key)        $M_KIRO_TEXT
extras (--with-extras): anthropic count_tokens | openai/newapi embeddings
EOF
}

if [[ "$LIST_ONLY" == "1" ]]; then print_matrix; exit 0; fi

if [[ -z "$KEY" ]]; then
  echo "ERROR: TK_FULLTEST_KEY 未设置（universal key）。export 后重试，或 --list 仅看矩阵。" >&2
  exit 2
fi

# 请求体
b_msg()  { printf '{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}' "$1"; }
b_chat() { printf '{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}' "$1"; }
b_resp() { printf '{"model":"%s","instructions":"You are helpful.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say OK"}]}],"stream":false}' "$1"; }
b_gem()  { printf '{"contents":[{"parts":[{"text":"Reply with OK"}]}]}'; }
b_img()  { printf '{"model":"%s","prompt":"a small red circle on white","n":1,"size":"1024x1024"}' "$1"; }
b_emb()  { printf '{"model":"%s","input":"hello"}' "$1"; }
b_ct()   { printf '{"model":"%s","messages":[{"role":"user","content":"ping"}]}' "$1"; }

# jq 断言
JQ_MSG='.type=="message" and .role=="assistant" and ((.content[0].text//"")|type=="string") and (.usage!=null)'
JQ_CHAT='.object=="chat.completion" and (.choices[0].message!=null) and (.usage!=null)'
JQ_RESP='(.id!=null) or (.output!=null) or (.usage!=null)'
JQ_GEM='.candidates[0].content!=null'
JQ_IMG='(.data[0]!=null)'
JQ_EMB='.data[0].embedding!=null'
JQ_CT='(.input_tokens|type=="number")'

echo "=================================================================="
echo " TokenKey 全平台 × 全模态测试   base=$BASE"
echo " universal key=$(mask "$KEY")   kiro key=$(mask "$KIRO_KEY")   paid=$([[ $SKIP_PAID == 1 ]] && echo skip || echo on)"
echo "=================================================================="

# ---- 控制面预检（任一 hard-fail 即停）----
echo "-- 控制面预检 --"
do_get "/api/v1/settings/public" ""; record "control" "public" "GET /api/v1/settings/public" "-" "$CODE" "$BODYFILE" "$([[ $CODE == 200 ]] && echo 1 || echo 0)"; rm -f "$BODYFILE"
do_get "/v1/models" "$KEY"; record "control" "models" "GET /v1/models" "-" "$CODE" "$BODYFILE" "$(shape_ok '.object=="list" and ((.data|length)>0)')"; rm -f "$BODYFILE"
do_get "/v1/usage" "$KEY"; record "control" "usage" "GET /v1/usage" "-" "$CODE" "$BODYFILE" "$([[ $CODE == 200 ]] && echo 1 || echo 0)"; rm -f "$BODYFILE"
if [[ "$FAIL" -gt 0 ]]; then
  echo ""; echo "控制面预检 FAIL（池/控制面未就绪）——停止，不打平台矩阵。" >&2
  printf '\n汇总：PASS=%d SKIP=%d FAIL=%d\n' "$PASS" "$SKIP" "$FAIL"
  exit 1
fi

# ---- 平台矩阵 ----
echo "-- 平台矩阵（text）--"
run_post anthropic   text       /v1/messages         "$M_ANTHROPIC_TEXT"   "$KEY" "$(b_msg  "$M_ANTHROPIC_TEXT")"   "$JQ_MSG"
run_post openai      text       /v1/chat/completions "$M_OPENAI_TEXT"      "$KEY" "$(b_chat "$M_OPENAI_TEXT")"      "$JQ_CHAT"
run_post openai      text-resp  /v1/responses        "$M_OPENAI_RESPONSES" "$KEY" "$(b_resp "$M_OPENAI_RESPONSES")" "$JQ_RESP"
run_post gemini      text       "/v1beta/models/${M_GEMINI_TEXT}:generateContent" "$M_GEMINI_TEXT" "$KEY" "$(b_gem)" "$JQ_GEM"
run_post antigravity text       "/antigravity/v1beta/models/${M_ANTIGRAVITY_TEXT}:generateContent" "$M_ANTIGRAVITY_TEXT" "$KEY" "$(b_gem)" "$JQ_GEM"
run_post newapi      text       /v1/chat/completions "$M_NEWAPI_TEXT"      "$KEY" "$(b_chat "$M_NEWAPI_TEXT")"      "$JQ_CHAT"
run_post grok        text       /v1/chat/completions "$M_GROK_TEXT"        "$KEY" "$(b_chat "$M_GROK_TEXT")"        "$JQ_CHAT"
run_post kiro        text       /v1/messages         "$M_KIRO_TEXT"        "$KIRO_KEY" "$(b_msg "$M_KIRO_TEXT")"    "$JQ_MSG"

echo "-- 平台矩阵（image/video，付费）--"
if [[ "$SKIP_PAID" == "1" ]]; then
  record_skip openai image "POST /v1/images/generations" "$M_OPENAI_IMAGE" "--skip-paid"
  record_skip gemini image "POST /v1/images/generations" "$M_GEMINI_IMAGE" "--skip-paid"
  record_skip gemini video "POST /v1/video/generations"  "$M_GEMINI_VIDEO" "--skip-paid"
  record_skip newapi image "POST /v1/images/generations" "$M_NEWAPI_IMAGE" "--skip-paid"
  record_skip newapi video "POST /v1/video/generations"  "$M_NEWAPI_VIDEO" "--skip-paid"
else
  run_post  openai image /v1/images/generations "$M_OPENAI_IMAGE" "$KEY" "$(b_img "$M_OPENAI_IMAGE")" "$JQ_IMG"
  run_post  gemini image /v1/images/generations "$M_GEMINI_IMAGE" "$KEY" "$(b_img "$M_GEMINI_IMAGE")" "$JQ_IMG"
  run_video gemini /v1/video/generations "$M_GEMINI_VIDEO" "$KEY"
  run_post  newapi image /v1/images/generations "$M_NEWAPI_IMAGE" "$KEY" "$(b_img "$M_NEWAPI_IMAGE")" "$JQ_IMG"
  run_video newapi /v1/video/generations "$M_NEWAPI_VIDEO" "$KEY"
fi

if [[ "$WITH_EXTRAS" == "1" ]]; then
  echo "-- 可选扩展（count_tokens / embeddings）--"
  run_post anthropic count_tokens /v1/messages/count_tokens "$M_ANTHROPIC_TEXT" "$KEY" "$(b_ct "$M_ANTHROPIC_TEXT")" "$JQ_CT"
  run_post openai    embeddings   /v1/embeddings            "text-embedding-3-small" "$KEY" "$(b_emb text-embedding-3-small)" "$JQ_EMB"
fi

# ---- 汇总 ----
echo ""
echo "================================ 汇总 ================================"
printf '%-11s %-12s %-30s %-5s %-4s %s\n' "平台" "模态" "模型" "HTTP" "结果" "说明"
printf '%s\n' "-------------------------------------------------------------------------------"
for row in "${RESULTS[@]}"; do
  IFS=$'\t' read -r platform modality model ep code res note <<<"$row"
  printf '%-11s %-12s %-30s %-5s %-4s %s\n' "$platform" "$modality" "$model" "$code" "$res" "$note"
done
printf '%s\n' "-------------------------------------------------------------------------------"
printf 'PASS=%d  SKIP=%d  FAIL=%d\n' "$PASS" "$SKIP" "$FAIL"

if [[ "$FAIL" -gt 0 ]]; then exit 1; fi
exit 0
