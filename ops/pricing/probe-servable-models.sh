#!/usr/bin/env bash
# probe-servable-models.sh — runs ON the prod host (delivered via
# ops/observability/run-probe.sh) and classifies whether each candidate model
# is currently SERVABLE through TokenKey, by sending one minimal real request
# per model and reading the HTTP status.
#
# Inputs (env, space-separated model-id lists; any may be empty):
#   ANTHROPIC_MODELS         -> POST <edge>/v1/messages  (Claude-Code shape)
#   OPENAI_CHAT_MODELS       -> POST <prod>/v1/chat/completions
#   OPENAI_RESPONSES_MODELS  -> POST <prod>/v1/responses   (codex family)
#   OPENAI_IMAGE_MODELS      -> POST <prod>/v1/images/generations (best-effort)
#   GEMINI_CHAT_MODELS       -> POST app:8080/v1/chat/completions  (newapi/Vertex, edge-internal)
#   GEMINI_IMAGE_MODELS      -> POST app:8080/v1/images/generations
#   GEMINI_VIDEO_MODELS      -> POST app:8080/v1/video/generations (async submit; 200-on-submit=servable, best-effort)
#     NB gemini families run ON the edge host and hit the app container directly
#     (the edge Caddy 403s host-local /v1/* — it only allows the prod gateway CIDR).
#   GROK_CHAT_MODELS         -> POST app:8080/v1/chat/completions  (native grok, edge-internal)
#     NB grok lives on its native edge pool (currently edge-us4). Like gemini,
#     the edge-local probe hits the app container directly instead of the public
#     Caddy path. Use run-probe with --target edge:us4 and a key bound to the
#     edge-side grok group.
#   DASHSCOPE_CHAT_MODELS    -> POST <prod>/v1/chat/completions  (newapi channel_type=17, qwen3 dense)
#     The newapi fifth-platform pool IS served at prod (unlike gemini, which is
#     edge-internal), so this family routes through the normal prod TK gateway
#     with a TK api_key BOUND TO the newapi/qwen group (account 60 lives there).
#     This is the SERVABLE-end-to-end truth probe for channel_type=17: a real 200
#     proves the model id is both upstream-activated AND allowlisted at the TK
#     scheduling layer (account model_mapping). The empty-pool 429 ("No available
#     accounts" + Retry-After: 5) is NOT an upstream rate-limit — it is the
#     TK-scheduling gap that hid qwen3-8b/14b/32b in the #812 incident, so it gets
#     its own verdict (not_allowlisted) instead of inconclusive.
#     Dashscope HARD CONSTRAINT: qwen3 DENSE thinking mode must STREAM; a
#     non-streaming call must explicitly set enable_thinking=false, else upstream
#     400s with "enable_thinking must be set to false for non-streaming calls".
#     This family therefore probes BOTH variants per model — thinking (stream:true,
#     enable_thinking:true) and non-thinking (stream:false, enable_thinking:false)
#     — so a default (thinking) call that 400s on shape alone never masks a
#     genuinely servable model.
#   ZHIPU_CHAT_MODELS        -> POST <prod>/v1/chat/completions  (newapi channel_type=26, GLM/Z.AI)
#     Routes through the normal prod TK gateway with a TK api_key bound to the GLM
#     group. This is the end-to-end truth probe after account 67 switches from
#     legacy Zhipu v3 (channel_type=16) to ZhipuV4/OpenAI-compatible (26).
#   ARK_CHAT_MODELS          -> POST <ark>/api/v3/chat/completions  (DIRECT ark data plane)
#   ARK_IMAGE_MODELS         -> POST <ark>/api/v3/images/generations (direct; a servable model bills ~1 image)
#   ARK_VIDEO_MODELS         -> POST <ark>/api/v3/contents/generations/tasks (direct; a servable model creates a REAL paid video task — probe sparingly)
#     NB ark families bypass the TK gateway entirely: credentials come from the
#     accounts row id=ARK_ACCOUNT_ID, so NO schedulable window is needed and the
#     account may stay disabled. This is the ACTIVATION-truth probe: ark's GET
#     /api/v3/models is the platform CATALOG, not the activation list — a model
#     can be listed there yet reject every call. Per-model classification
#     (verified 2026-06-10 against prod account 7): 200 = activated; 404
#     InvalidEndpointOrModel.NotFound "does not exist or you do not have access"
#     = not activated / retired (-> unsupported via the does-not-exist match);
#     429/5xx = transient (-> inconclusive). The TK-gateway probe path wraps that
#     404 into an opaque 502, which is why these families talk to ark directly.
# Optional env:
#   ARK_ACCOUNT_ID           default 7   (accounts row holding the ark api_key + base_url)
#   ANTHROPIC_EDGE_BASE      default https://api-us7.tokenkey.dev
#   ANTHROPIC_KEY_ACCOUNT_ID default 54  (its credentials.api_key relays to the edge)
#   PROD_BASE                default https://api.tokenkey.dev
#   OPENAI_KEY_NAME          default TK_SMOKE_PROD_OPENAI_OAUTH_KEY (api_keys.user_id=1)
#   GEMINI_GROUP_NAME        default google  (verified us6 newapi Vertex group; CASE-SENSITIVE)
#   GEMINI_APP_CONTAINER     default tokenkey-caddy (a container on the compose net with busybox wget)
#   GEMINI_APP_URL           default http://tokenkey:8080 (the app, reached internally)
#   GROK_GROUP_NAME          default grok  (verified edge native grok group; CASE-SENSITIVE)
#   GROK_APP_CONTAINER       default tokenkey-caddy
#   GROK_APP_URL             default http://tokenkey:8080
#   DASHSCOPE_GROUP_NAME     default Qwen  (verified prod group, capital Q; CASE-SENSITIVE; never printed)
#   ZHIPU_GROUP_NAME         default GLM   (prod GLM group; CASE-SENSITIVE; never printed)
#   REQ_SLEEP                default 2  (seconds between requests; avoids pool exhaustion)
#
# Output: one TSV line per model on stdout (keys never printed):
#   <platform>\t<model>\t<http_code>\t<verdict>
# verdict in: servable | unsupported | inconclusive | not_allowlisted | auth_error | config_error
#
# Classification (a model is "servable" iff a real 200 came back):
#   200                                   -> servable
#   400/404 + retired/not-found/invalid   -> unsupported (deprecated gate / upstream reject)
#   400 "not supported when using Codex"  -> unsupported (this account does not serve it)
#   429 + "No available accounts"          -> not_allowlisted (TK empty-pool: model not allowlisted at scheduling layer)
#   429(other) / 502 / 503                  -> inconclusive (capacity / wrong protocol / rate-limit)
#   401/403                               -> auth_error (upstream rejected the key — not a model signal)
#   (no key/group/account resolvable)     -> config_error (probe SETUP bug: group name/case, missing
#                                            key, or wrong account id; printed with a stderr diagnostic
#                                            naming what failed; NEVER a model or upstream-auth signal)
#
# Determinism / safety: keys are pulled from the local DB into shell vars and
# never echoed; only model + status are emitted. No unquoted parens in echo
# (host-shell parse trap, see check-stage0-ssm-host-parse.sh).
set -uo pipefail

PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'
AEDGE="${ANTHROPIC_EDGE_BASE:-https://api-us7.tokenkey.dev}"
AACCT="${ANTHROPIC_KEY_ACCOUNT_ID:-54}"
PROD="${PROD_BASE:-https://api.tokenkey.dev}"
OKEY_NAME="${OPENAI_KEY_NAME:-TK_SMOKE_PROD_OPENAI_OAUTH_KEY}"
GEMINI_GROUP_NAME="${GEMINI_GROUP_NAME:-google}"
# Gemini/Vertex lives on an EDGE node whose Caddy restricts /v1/* to the prod
# gateway CIDR (a host-local request to the public api-<edge> domain 403s with
# "edge relay path is restricted"). The probe therefore hits the app container
# directly on the docker network, bypassing the edge Caddy. The caddy container
# carries busybox wget and resolves the app service name on the compose network.
GEMINI_APP_CONTAINER="${GEMINI_APP_CONTAINER:-tokenkey-caddy}"
GEMINI_APP_URL="${GEMINI_APP_URL:-http://tokenkey:8080}"
GROK_GROUP_NAME="${GROK_GROUP_NAME:-grok}"
GROK_APP_CONTAINER="${GROK_APP_CONTAINER:-tokenkey-caddy}"
GROK_APP_URL="${GROK_APP_URL:-http://tokenkey:8080}"
DASHSCOPE_GROUP_NAME="${DASHSCOPE_GROUP_NAME:-Qwen}"
ZHIPU_GROUP_NAME="${ZHIPU_GROUP_NAME:-GLM}"
REQ_SLEEP="${REQ_SLEEP:-2}"
UA='claude-cli/2.1.165 (external, sdk-cli)'
SYS='You are Claude Code, the official CLI for Claude.'

emit() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4"; }

# cfgerr: a probe-SETUP failure (key/group/account not resolvable) — emitted as a
# distinct config_error verdict (NOT auth_error) plus a stderr diagnostic naming what
# failed to resolve, so a group-name typo/case-mismatch self-diagnoses instead of hiding
# behind a generic auth_error (a 'qwen' vs 'Qwen' default cost a livefire this session).
cfgerr() { # $1=platform $2=diagnostic
	printf '%s\t%s\t%s\t%s\n' "$1" "*" "000" "config_error"
	printf 'probe-setup [%s]: %s\n' "$1" "$2" >&2
}

verdict() { # $1=code $2=bodyfile -> echoes verdict
	local code="$1" f="$2"
	case "$code" in
	200) echo "servable" ;;
	400 | 404)
		if grep -qiE 'retired|sunset|not_found|does not exist|invalid model|unknown model|model_not_found|not supported when using|removed from this|not a valid' "$f"; then
			echo "unsupported"
		else
			echo "inconclusive"
		fi
		;;
	429)
		# Distinguish the TK empty-pool 429 ("No available accounts" + Retry-After: 5)
		# from a genuine upstream rate-limit. The former is NOT a capacity problem —
		# it means the requested model id is not allowlisted on any schedulable
		# account at the TK scheduling layer (the #812 / channel_type=17 funnel), the
		# actionable onboarding signal. The latter is transient (inconclusive).
		if grep -qiE 'no available accounts' "$f"; then
			echo "not_allowlisted"
		else
			echo "inconclusive"
		fi
		;;
	502 | 503) echo "inconclusive" ;;
	401 | 403) echo "auth_error" ;;
	*) echo "inconclusive" ;;
	esac
}

probe_anthropic() {
	local key="$1" m f code
	for m in $ANTHROPIC_MODELS; do
		f="$(mktemp)"
		code="$(curl -s -o "$f" -w '%{http_code}' -m 45 -X POST "$AEDGE/v1/messages" \
			-H "x-api-key: $key" -H 'anthropic-version: 2023-06-01' \
			-H 'anthropic-beta: claude-code-20250219' -H 'X-App: cli' \
			-H "User-Agent: $UA" -H 'content-type: application/json' \
			--data-binary "{\"model\":\"$m\",\"max_tokens\":32,\"system\":[{\"type\":\"text\",\"text\":\"$SYS\"}],\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: OK\"}],\"metadata\":{\"user_id\":\"servable-probe\"}}")"
		emit anthropic "$m" "$code" "$(verdict "$code" "$f")"
		rm -f "$f"
		sleep "$REQ_SLEEP"
	done
}

# probe_compat_endpoint: OpenAI-compatible Bearer-auth probe against an arbitrary
# base. Used by both openai (PROD) and gemini (us6 / newapi+Vertex) families —
# they share the same /v1/* OpenAI-compat surface, only base + key + emit-tag differ.
probe_compat_endpoint() { # $1=platform-tag $2=base $3=key $4=endpoint $5=models $6=jsonbody-template-fn
	local platform="$1" base="$2" key="$3" path="$4" models="$5" buildfn="$6" m f code
	for m in $models; do
		f="$(mktemp)"
		code="$(curl -s -o "$f" -w '%{http_code}' -m 75 -X POST "$base$path" \
			-H "Authorization: Bearer $key" -H 'content-type: application/json' \
			--data-binary "$($buildfn "$m")")"
		emit "$platform" "$m" "$code" "$(verdict "$code" "$f")"
		rm -f "$f"
		sleep "$REQ_SLEEP"
	done
}

probe_openai_endpoint() { # back-compat wrapper: openai family always targets PROD
	probe_compat_endpoint openai "$PROD" "$1" "$2" "$3" "$4"
}

# probe_gemini_internal: same OpenAI-compat surface, but reached via the app
# container on the docker network (bypasses the edge Caddy /v1/* restriction).
# Uses busybox wget inside GEMINI_APP_CONTAINER: -S prints the response status
# line to stderr (captured to a header file), -O - streams the body to stdout.
# Key value lives in a shell var only; never emitted.
probe_gemini_internal() { # $1=key $2=endpoint $3=models $4=jsonbody-template-fn
	local key="$1" path="$2" models="$3" buildfn="$4" m hf bf code body
	for m in $models; do
		hf="$(mktemp)"; bf="$(mktemp)"; body="$($buildfn "$m")"
		sudo docker exec "$GEMINI_APP_CONTAINER" wget -S -q -O - --timeout=90 \
			--header="Authorization: Bearer $key" --header='content-type: application/json' \
			--post-data="$body" "$GEMINI_APP_URL$path" >"$bf" 2>"$hf" || true
		code="$(grep -oE 'HTTP/[0-9.]+ [0-9]{3}' "$hf" | tail -1 | grep -oE '[0-9]{3}$')"
		[ -z "$code" ] && code=000
		emit gemini "$m" "$code" "$(verdict "$code" "$bf")"
		rm -f "$hf" "$bf"
		sleep "$REQ_SLEEP"
	done
}

probe_grok_internal() { # $1=key $2=endpoint $3=models $4=jsonbody-template-fn
	local key="$1" path="$2" models="$3" buildfn="$4" m hf bf code body
	for m in $models; do
		hf="$(mktemp)"; bf="$(mktemp)"; body="$($buildfn "$m")"
		sudo docker exec "$GROK_APP_CONTAINER" wget -S -q -O - --timeout=90 \
			--header="Authorization: Bearer $key" --header='content-type: application/json' \
			--post-data="$body" "$GROK_APP_URL$path" >"$bf" 2>"$hf" || true
		code="$(grep -oE 'HTTP/[0-9.]+ [0-9]{3}' "$hf" | tail -1 | grep -oE '[0-9]{3}$')"
		[ -z "$code" ] && code=000
		emit grok "$m" "$code" "$(verdict "$code" "$bf")"
		rm -f "$hf" "$bf"
		sleep "$REQ_SLEEP"
	done
}

body_chat() { printf '{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}' "$1"; }
body_resp() { printf '{"model":"%s","instructions":"You are a helpful assistant.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say OK"}]}],"stream":false}' "$1"; }
body_img() { printf '{"model":"%s","prompt":"a small red circle on white","n":1,"size":"1024x1024"}' "$1"; }
body_video() { printf '{"model":"%s","prompt":"a small red ball rolling on a table","seconds":"4"}' "$1"; }
# ark create-task shape (seedance text commands ride inside the prompt text);
# smallest billable settings so an activated model costs as little as possible.
body_ark_video() { printf '{"model":"%s","content":[{"type":"text","text":"a small red ball rolling on a table --resolution 480p --duration 5"}]}' "$1"; }
# Dashscope qwen3 DENSE bodies. Thinking mode REQUIRES streaming; the non-thinking
# variant MUST set enable_thinking=false explicitly (else upstream 400s). Probing
# both per model means a default-thinking call that only 400s on request shape
# never masks a genuinely servable model.
body_qwen_thinking() { printf '{"model":"%s","max_tokens":16,"stream":true,"enable_thinking":true,"messages":[{"role":"user","content":"hi"}]}' "$1"; }
body_qwen_nonthinking() { printf '{"model":"%s","max_tokens":16,"stream":false,"enable_thinking":false,"messages":[{"role":"user","content":"hi"}]}' "$1"; }

# probe_dashscope: routes qwen3 dense through the PROD TK gateway /v1/chat/completions
# (the newapi channel_type=17 pool is served at prod). Bearer auth uses a TK api_key
# bound to the newapi/qwen group (pulled in main(), never printed). For each model it
# fires the thinking (streaming) variant and the non-thinking variant; a real 200 (a
# valid SSE stream opens with 200 on the thinking path, or a plain 200 on the
# non-thinking path) => servable. The emitted model carries a (thinking)/(non-thinking)
# suffix so the two paths are distinguishable in the TSV. -N keeps curl from buffering
# the stream; we only need the status line + a short body sample for classification.
probe_dashscope() { # $1=key  (models from $DASHSCOPE_CHAT_MODELS)
	local key="$1" m f code variant buildfn
	for m in $DASHSCOPE_CHAT_MODELS; do
		for variant in thinking nonthinking; do
			if [ "$variant" = thinking ]; then buildfn=body_qwen_thinking; else buildfn=body_qwen_nonthinking; fi
			f="$(mktemp)"
			code="$(curl -s -N -o "$f" -w '%{http_code}' -m 75 -X POST "$PROD/v1/chat/completions" \
				-H "Authorization: Bearer $key" -H 'content-type: application/json' \
				--data-binary "$($buildfn "$m")")"
			emit newapi "$m ($variant)" "$code" "$(verdict "$code" "$f")"
			rm -f "$f"
			sleep "$REQ_SLEEP"
		done
	done
}

probe_zhipu() { # $1=key  (models from $ZHIPU_CHAT_MODELS)
	probe_compat_endpoint newapi "$PROD" "$1" /v1/chat/completions "$ZHIPU_CHAT_MODELS" body_chat
}

main() {
	local akey okey gkey grkey dkey zkey arkacct arkkey arkbase
	if [ -n "${ANTHROPIC_MODELS:-}" ]; then
		akey="$($PSQL -c "SELECT credentials->>'api_key' FROM accounts WHERE id=$AACCT AND deleted_at IS NULL" | tr -d '[:space:]')"
		if [ -z "$akey" ]; then
			cfgerr anthropic "no api_key on account id=$AACCT (ANTHROPIC_KEY_ACCOUNT_ID)"
		else
			probe_anthropic "$akey"
		fi
	fi
	if [ -n "${OPENAI_CHAT_MODELS:-}${OPENAI_RESPONSES_MODELS:-}${OPENAI_IMAGE_MODELS:-}" ]; then
		okey="$($PSQL -c "SELECT key FROM api_keys WHERE user_id=1 AND name='$OKEY_NAME' AND deleted_at IS NULL LIMIT 1" | tr -d '[:space:]')"
		if [ -z "$okey" ]; then
			cfgerr openai "no api_key user_id=1 name='$OKEY_NAME' (OPENAI_KEY_NAME)"
		else
			[ -n "${OPENAI_CHAT_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/chat/completions "$OPENAI_CHAT_MODELS" body_chat
			[ -n "${OPENAI_RESPONSES_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/responses "$OPENAI_RESPONSES_MODELS" body_resp
			[ -n "${OPENAI_IMAGE_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/images/generations "$OPENAI_IMAGE_MODELS" body_img
		fi
	fi
	# Ark families: DIRECT volcengine data-plane probe (activation truth). Bypasses
	# the TK gateway — no schedulable window, the account row may stay disabled.
	# 404 InvalidEndpointOrModel.NotFound = not activated (verdict: unsupported).
	if [ -n "${ARK_CHAT_MODELS:-}${ARK_IMAGE_MODELS:-}${ARK_VIDEO_MODELS:-}" ]; then
		arkacct="${ARK_ACCOUNT_ID:-7}"
		arkkey="$($PSQL -c "SELECT credentials->>'api_key' FROM accounts WHERE id=$arkacct AND deleted_at IS NULL" | tr -d '[:space:]')"
		arkbase="$($PSQL -c "SELECT credentials->>'base_url' FROM accounts WHERE id=$arkacct AND deleted_at IS NULL" | tr -d '[:space:]')"
		arkbase="${arkbase%/}"
		# Mirror NormalizeArkChannelBaseURL (integration/newapi): operators commonly
		# paste .../api/v3 into base_url; the data plane wants the host root.
		arkbase="${arkbase%/api/v3}"
		if [ -z "$arkkey" ] || [ -z "$arkbase" ]; then
			cfgerr volcengine "no api_key/base_url on ark account id=$arkacct (ARK_ACCOUNT_ID)"
		else
			[ -n "${ARK_CHAT_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/chat/completions "$ARK_CHAT_MODELS" body_chat
			[ -n "${ARK_IMAGE_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/images/generations "$ARK_IMAGE_MODELS" body_img
			[ -n "${ARK_VIDEO_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/contents/generations/tasks "$ARK_VIDEO_MODELS" body_ark_video
		fi
	fi
	# Dashscope/qwen family: newapi channel_type=17 (account 60 "Qwen", Ali) served
	# through the PROD TK gateway /v1/chat/completions. Probe key is a TK api_key BOUND
	# TO the newapi/qwen group (api_keys.group_id -> groups.id, joined by group name);
	# never printed. Routes at prod (the newapi pool is prod-served, unlike gemini).
	if [ -n "${DASHSCOPE_CHAT_MODELS:-}" ]; then
		dkey="$($PSQL -c "SELECT ak.key FROM api_keys ak JOIN groups g ON g.id=ak.group_id WHERE g.name='$DASHSCOPE_GROUP_NAME' AND ak.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY ak.id LIMIT 1" | tr -d '[:space:]')"
		if [ -z "$dkey" ]; then
			cfgerr newapi "no api_key bound to group '$DASHSCOPE_GROUP_NAME' (DASHSCOPE_GROUP_NAME) — check name/case + that a key is bound"
		else
			probe_dashscope "$dkey"
		fi
	fi
	# Zhipu/GLM family: newapi channel_type=26 (account 67 "GLM") served through
	# the PROD TK gateway /v1/chat/completions. Probe key is a TK api_key BOUND TO
	# the GLM group; never printed.
	if [ -n "${ZHIPU_CHAT_MODELS:-}" ]; then
		zkey="$($PSQL -c "SELECT ak.key FROM api_keys ak JOIN groups g ON g.id=ak.group_id WHERE g.name='$ZHIPU_GROUP_NAME' AND ak.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY ak.id LIMIT 1" | tr -d '[:space:]')"
		if [ -z "$zkey" ]; then
			cfgerr newapi "no api_key bound to group '$ZHIPU_GROUP_NAME' (ZHIPU_GROUP_NAME) — check name/case + that a key is bound"
		else
			probe_zhipu "$zkey"
		fi
	fi
	# Grok family: native xAI OAuth pool on the edge (currently us4). Probe key is
	# a TK api_key BOUND TO the edge-side grok group; never printed. The probe runs
	# on the edge host and hits the app container directly, bypassing edge Caddy.
	if [ -n "${GROK_CHAT_MODELS:-}" ]; then
		grkey="$($PSQL -c "SELECT ak.key FROM api_keys ak JOIN groups g ON g.id=ak.group_id WHERE g.name='$GROK_GROUP_NAME' AND g.platform='grok' AND ak.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY ak.id LIMIT 1" | tr -d '[:space:]')"
		if [ -z "$grkey" ]; then
			cfgerr grok "no api_key bound to group '$GROK_GROUP_NAME' (GROK_GROUP_NAME) — check name/case + that a key is bound"
		else
			probe_grok_internal "$grkey" /v1/chat/completions "$GROK_CHAT_MODELS" body_chat
		fi
	fi
	# Gemini family: newapi/Vertex models served through the google group on GEMINI_BASE.
	# Key is an api_key BOUND TO that group (api_keys.group_id -> groups.id); never printed.
	if [ -n "${GEMINI_CHAT_MODELS:-}${GEMINI_IMAGE_MODELS:-}${GEMINI_VIDEO_MODELS:-}" ]; then
		gkey="$($PSQL -c "SELECT ak.key FROM api_keys ak JOIN groups g ON g.id=ak.group_id WHERE g.name='$GEMINI_GROUP_NAME' AND ak.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY ak.id LIMIT 1" | tr -d '[:space:]')"
		if [ -z "$gkey" ]; then
			cfgerr gemini "no api_key bound to group '$GEMINI_GROUP_NAME' (GEMINI_GROUP_NAME) — check name/case + that a key is bound"
		else
			[ -n "${GEMINI_CHAT_MODELS:-}" ] && probe_gemini_internal "$gkey" /v1/chat/completions "$GEMINI_CHAT_MODELS" body_chat
			[ -n "${GEMINI_IMAGE_MODELS:-}" ] && probe_gemini_internal "$gkey" /v1/images/generations "$GEMINI_IMAGE_MODELS" body_img
			[ -n "${GEMINI_VIDEO_MODELS:-}" ] && probe_gemini_internal "$gkey" /v1/video/generations "$GEMINI_VIDEO_MODELS" body_video
		fi
	fi
}

main
