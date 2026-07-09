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
#   GEMINI_CHAT_MODELS       -> POST <prod>/v1/chat/completions  (newapi/Vertex via PROD gateway)
#   GEMINI_CHATIMAGE_MODELS  -> POST <prod>/v1/chat/completions  (gemini-*-image via chat/generateContent)
#   GEMINI_IMAGE_MODELS      -> POST <prod>/v1/images/generations  (imagen-* predict API)
#   GEMINI_VIDEO_MODELS      -> POST <prod>/v1/video/generations (async submit; 200-on-submit=servable, best-effort)
#   GROK_CHAT_MODELS         -> POST app:8080/v1/chat/completions  (native grok, edge-internal)
#   GROK_IMAGE_MODELS        -> POST app:8080/v1/images/generations (native grok, edge-internal)
#   GROK_VIDEO_MODELS        -> POST app:8080/v1/video/generations (native grok, edge-internal; async submit)
#   ANTIGRAVITY_CHAT_MODELS  -> POST <prod>/antigravity/v1beta/models/{model}:generateContent
#                               (native antigravity text, PROD gateway; env name kept for compatibility)
#   ANTIGRAVITY_IMAGE_MODELS -> POST <prod>/v1/chat/completions
#                               (Studio gemini-native image path through the antigravity account pool)
#     NB grok lives on its native edge pool (currently edge-us4). Like gemini,
#     the edge-local probe hits the app container directly instead of the public
#     Caddy path. Use run-probe with --target edge:us4 and a key bound to the
#     edge-side grok group.
#   DASHSCOPE_CHAT_MODELS    -> POST <prod>/v1/chat/completions  (newapi channel_type=17, qwen3 dense)
#     This family routes through the normal prod TK gateway
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
#   ZHIPU_CHAT_MODELS        -> legacy alias for GLM chat probes via Qwen/DashScope
#     (newapi channel_type=17). The old GLM/ZhipuV4 direct account/group was removed;
#     prefer DASHSCOPE_CHAT_MODELS for new GLM probe runs.
#   VOLCENGINE_IMAGE_MODELS  -> POST <prod>/v1/images/generations (newapi channel_type=45 via TK gateway)
#   VOLCENGINE_VIDEO_MODELS  -> POST <prod>/v1/video/generations  (newapi channel_type=45 via TK gateway; REAL paid async video submit)
#     Routes through the normal prod TK gateway with a TK api_key bound to the
#     volcengine group_id=5. This is the SERVABLE-end-to-end truth probe for
#     Ark/VolcEngine media after model_mapping + pricing are live. Keep it
#     separate from ARK_* below: ARK_* proves upstream account activation by
#     direct data-plane calls, while VOLCENGINE_* proves TokenKey gateway serving.
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
# Every family resolves its probe key through the reserved __tk_probe_<scope>_*
# group/key (probe_reserved_resources.sh), copying schedulable accounts from a
# canonical source group id per platform. There is NO direct-key / customer-key
# fallback — when delivered via run-probe.sh you MUST pass
#   --with ops/pricing/probe_reserved_resources.sh
# so the companion library lands on the remote host (the orchestrator and every
# documented manual invocation already do). A missing companion fails loudly.
#
# Optional env:
#   ARK_ACCOUNT_ID           default 7   (accounts row holding the ark api_key + base_url)
#   PROD_BASE                default https://api.tokenkey.dev
#   PROBE_OPENAI_SOURCE_GROUP_ID defaults via probe_source_group_id openai
#   PROBE_OPENAI_SOURCE_GROUP optional legacy override by group name
#   PROBE_ANTHROPIC_SOURCE_GROUP_ID defaults via probe_source_group_id anthropic_edge
#                            probe runs ON an edge, NOT prod — see refresh-allowlist ANTHROPIC_EDGES)
#   PROBE_ANTHROPIC_SOURCE_GROUP optional legacy override by group name
#   ANTHROPIC_APP_CONTAINER  default tokenkey-caddy (edge app container; busybox wget, bypass Caddy)
#   ANTHROPIC_APP_URL        default http://tokenkey:8080 (edge app, reached internally)
#   PROBE_ANTHROPIC_MIRROR_GROUP_ID defaults via probe_source_group_id anthropic_mirror
#   PROBE_ANTHROPIC_MIRROR_GROUP optional legacy override by group name
#   PROBE_GEMINI_SOURCE_GROUP_ID defaults via probe_source_group_id gemini_vertex
#   PROBE_GEMINI_SOURCE_GROUP optional legacy override by group name
#   PROBE_DASHSCOPE_SOURCE_GROUP_ID defaults via probe_source_group_id dashscope
#   PROBE_DASHSCOPE_SOURCE_GROUP optional legacy override by group name
#   PROBE_ZHIPU_SOURCE_GROUP_ID is a legacy GLM alias and defaults via
#     probe_source_group_id glm_dashscope (same pool as qwen)
#   PROBE_ZHIPU_SOURCE_GROUP optional legacy override by group name
#   PROBE_VOLCENGINE_SOURCE_GROUP_ID defaults via probe_source_group_id volcengine
#   PROBE_VOLCENGINE_SOURCE_GROUP optional legacy override by group name
#   PROBE_GROK_SOURCE_GROUP_ID defaults via probe_source_group_id grok_edge
#   PROBE_GROK_SOURCE_GROUP optional legacy override by group name
#   GROK_APP_CONTAINER       default tokenkey-caddy
#   GROK_APP_URL             default http://tokenkey:8080
#   PROBE_ANTIGRAVITY_SOURCE_GROUP_ID defaults via probe_source_group_id antigravity
#   PROBE_ANTIGRAVITY_SOURCE_GROUP optional legacy override by group name
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
#   400 "Unsupported model: X"             -> usually inconclusive here: TokenKey may have
#                                            rejected before account selection because the
#                                            prod allowlist/model_mapping floor lacks X. Use
#                                            ops/stage0/probe_account_model.sh on a specific
#                                            prod/edge account, or a platform direct probe, to
#                                            separate local floor from upstream capability.
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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Reserved __tk_probe_* group/key helpers are mandatory — there is no direct-key
# fallback. When delivered via run-probe.sh the companion must be uploaded with
# `--with ops/pricing/probe_reserved_resources.sh`; fail loudly if it is absent
# (otherwise every family would silently config_error on undefined tk_probe_*).
# Check existence separately from sourcing: a MISSING file is the --with mistake,
# but a present-but-broken file must surface its OWN error (do not mask it here).
if [ ! -f "$SCRIPT_DIR/probe_reserved_resources.sh" ]; then
	echo "probe-servable-models: companion $SCRIPT_DIR/probe_reserved_resources.sh not found — deliver it with 'run-probe.sh --with ops/pricing/probe_reserved_resources.sh'" >&2
	exit 2
fi
# shellcheck source=probe_reserved_resources.sh
. "$SCRIPT_DIR/probe_reserved_resources.sh"

PSQL_ARRAY=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)
PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'
PROD="${PROD_BASE:-https://api.tokenkey.dev}"

# Single runtime source for canonical source group ids. Display names are
# operator-editable and drift often enough to produce false config_error rows;
# every probe family below defaults through this function and only uses the
# legacy PROBE_*_SOURCE_GROUP name override when explicitly set for diagnostics.
probe_source_group_id() { # $1=logical source pool
	case "$1" in
	openai) echo 2 ;;
	anthropic_edge) echo 1 ;;
	anthropic_mirror) echo 1 ;;
	gemini_vertex) echo 16 ;;
	dashscope) echo 18 ;;
	glm_dashscope) echo 18 ;;
	zhipu) echo 18 ;; # legacy alias; direct GLM/ZhipuV4 group was removed
	volcengine) echo 5 ;;
	grok_edge) echo 4 ;;
	antigravity) echo 21 ;;
	*)
		echo "probe-servable-models: unknown source group key '$1'" >&2
		return 1
		;;
	esac
}

PROBE_OPENAI_SOURCE_GROUP_ID="${PROBE_OPENAI_SOURCE_GROUP_ID:-$(probe_source_group_id openai)}"
PROBE_OPENAI_SOURCE_GROUP="${PROBE_OPENAI_SOURCE_GROUP:-}"
# anthropic probes an EDGE's native OAuth pool directly (group_id=1 on deployable edges),
# not the prod cc-* mirror accounts: the mirrors relay prod->edge and cool down on any
# edge upstream blip, so a prod-gateway probe empty-pools (429 not_allowlisted) whenever
# the mirror pool is exhausted — a false "unservable". One healthy edge is enough to
# confirm a model; the orchestrator rotates across deployable edges (refresh-servable-
# allowlist.py ANTHROPIC_EDGES) and a model is servable if ANY edge serves it.
PROBE_ANTHROPIC_SOURCE_GROUP_ID="${PROBE_ANTHROPIC_SOURCE_GROUP_ID:-$(probe_source_group_id anthropic_edge)}"  # edge native OAuth group id
PROBE_ANTHROPIC_SOURCE_GROUP="${PROBE_ANTHROPIC_SOURCE_GROUP:-}"
ANTHROPIC_APP_CONTAINER="${ANTHROPIC_APP_CONTAINER:-tokenkey-caddy}"
ANTHROPIC_APP_URL="${ANTHROPIC_APP_URL:-http://tokenkey:8080}"
# Prod relay-health probe (ANTHROPIC_PROD_MIRROR_MODELS, warning-only — does NOT feed
# the allowlist). prod serves claude via api-key MIRROR accounts that all sit in
# group_id=1 (current display name: claude) but split by NAME prefix into two upstream relays: `cc-*` (anthropic
# OAuth edges) and `kiro-*` (Kiro edges). Each sub-pool is probed on its own key via the
# prod gateway and emits a distinct platform tag (anthropic_prodmirror_cc / _kiro) that
# parse_results ignores; the orchestrator warns when an edge-servable model fails here.
PROBE_ANTHROPIC_MIRROR_GROUP_ID="${PROBE_ANTHROPIC_MIRROR_GROUP_ID:-$(probe_source_group_id anthropic_mirror)}"
PROBE_ANTHROPIC_MIRROR_GROUP="${PROBE_ANTHROPIC_MIRROR_GROUP:-}"
PROBE_GEMINI_SOURCE_GROUP_ID="${PROBE_GEMINI_SOURCE_GROUP_ID:-$(probe_source_group_id gemini_vertex)}"
PROBE_GEMINI_SOURCE_GROUP="${PROBE_GEMINI_SOURCE_GROUP:-}"
PROBE_DASHSCOPE_SOURCE_GROUP_ID="${PROBE_DASHSCOPE_SOURCE_GROUP_ID:-$(probe_source_group_id dashscope)}"
PROBE_DASHSCOPE_SOURCE_GROUP="${PROBE_DASHSCOPE_SOURCE_GROUP:-}"
PROBE_ZHIPU_SOURCE_GROUP_ID="${PROBE_ZHIPU_SOURCE_GROUP_ID:-$(probe_source_group_id glm_dashscope)}"
PROBE_ZHIPU_SOURCE_GROUP="${PROBE_ZHIPU_SOURCE_GROUP:-}"
PROBE_VOLCENGINE_SOURCE_GROUP_ID="${PROBE_VOLCENGINE_SOURCE_GROUP_ID:-$(probe_source_group_id volcengine)}"
PROBE_VOLCENGINE_SOURCE_GROUP="${PROBE_VOLCENGINE_SOURCE_GROUP:-}"
PROBE_GROK_SOURCE_GROUP_ID="${PROBE_GROK_SOURCE_GROUP_ID:-$(probe_source_group_id grok_edge)}"
PROBE_GROK_SOURCE_GROUP="${PROBE_GROK_SOURCE_GROUP:-}"
# Gemini/Vertex now serves from the PROD group_id=16 (currently named `Google-Vertex`;
# display name is operator-editable, so the default binding is by id, not name). The
# probe goes through the PROD public gateway with external curl like the other newapi
# families — no edge-internal wget hop is needed (prod's Caddy is not CIDR-restricted).
GROK_APP_CONTAINER="${GROK_APP_CONTAINER:-tokenkey-caddy}"
GROK_APP_URL="${GROK_APP_URL:-http://tokenkey:8080}"
# antigravity accounts (e.g. antigravity-us3/us4 in prod group_id=21) live in
# the PROD DB and are scheduled from prod. Text probes use the PROD public
# /antigravity/v1beta surface; Studio gemini-native IMAGE probes intentionally use
# /v1/chat/completions because that is the customer UI path for image generation.
PROBE_ANTIGRAVITY_SOURCE_GROUP_ID="${PROBE_ANTIGRAVITY_SOURCE_GROUP_ID:-$(probe_source_group_id antigravity)}"
PROBE_ANTIGRAVITY_SOURCE_GROUP="${PROBE_ANTIGRAVITY_SOURCE_GROUP:-}"
REQ_SLEEP="${REQ_SLEEP:-2}"
UA='claude-cli/2.1.165 (external, cli)'
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

# Pick the stable-id binding by default. A non-empty legacy group-name override
# is still supported for one-off diagnostics, but no default path depends on a
# mutable display name.
probe_bind_source() { # $1=group_id $2=legacy_group_name -> REPLY_BIND_KIND/VAL/LABEL
	local group_id="$1" legacy_name="${2:-}"
	if [ -n "$legacy_name" ]; then
		REPLY_BIND_KIND=source_group
		REPLY_BIND_VAL="$legacy_name"
		REPLY_BIND_LABEL="source_group=$legacy_name"
	else
		REPLY_BIND_KIND=source_group_id
		REPLY_BIND_VAL="$group_id"
		REPLY_BIND_LABEL="source_group_id=$group_id"
	fi
}

probe_bind_group_like() { # $1=group_id $2=legacy_group_name $3=SQL LIKE pattern
	local group_id="$1" legacy_name="${2:-}" pattern="$3"
	if [ -n "$legacy_name" ]; then
		REPLY_BIND_KIND=group_like
		REPLY_BIND_VAL="${legacy_name}|${pattern}"
		REPLY_BIND_LABEL="source_group=${legacy_name} name LIKE ${pattern}"
	else
		REPLY_BIND_KIND=group_id_like
		REPLY_BIND_VAL="${group_id}|${pattern}"
		REPLY_BIND_LABEL="source_group_id=${group_id} name LIKE ${pattern}"
	fi
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

# probe_anthropic_internal: edge-native probe. Runs ON an edge host and hits the app
# container directly (busybox wget, bypassing the edge Caddy /v1/* CIDR restriction),
# so the request is scheduled onto the edge's OWN native OAuth account (the `default`
# group) and relayed straight to Anthropic — no prod cc-* mirror hop. Key is the reserved
# __tk_probe_anthropic_key bound to the edge `default` accounts; never printed.
probe_anthropic_internal() { # $1=key  (models from $ANTHROPIC_MODELS)
	local key="$1" m hf bf code body realistic_py
	realistic_py="${SCRIPT_DIR}/../stage0/smoke_anthropic_realistic.py"
	for m in $ANTHROPIC_MODELS; do
		hf="$(mktemp)"; bf="$(mktemp)"
		if [[ -f "$realistic_py" ]]; then
			body="$(python3 "$realistic_py" --model "$m" --max-tokens 32 --prompt hi)"
		else
			body="{\"model\":\"$m\",\"max_tokens\":32,\"system\":[{\"type\":\"text\",\"text\":\"$SYS\"}],\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"metadata\":{\"user_id\":\"servable-probe\"}}"
		fi
		sudo docker exec "$ANTHROPIC_APP_CONTAINER" wget -S -q -O - --timeout=90 \
			--header="x-api-key: $key" --header='anthropic-version: 2023-06-01' \
			--header='anthropic-beta: claude-code-20250219' --header='X-App: cli' \
			--header="User-Agent: $UA" --header='content-type: application/json' \
			--post-data="$body" "$ANTHROPIC_APP_URL/v1/messages" >"$bf" 2>"$hf" || true # preflight-allow: swallow (status parsed from -S header file)
		code="$(grep -oE 'HTTP/[0-9.]+ [0-9]{3}' "$hf" | tail -1 | grep -oE '[0-9]{3}$' || true)" # preflight-allow: swallow (no status line -> code stays empty -> next line sets 000)
		[ -z "$code" ] && code=000
		emit anthropic "$m" "$code" "$(verdict "$code" "$bf")"
		rm -f "$hf" "$bf"
		sleep "$REQ_SLEEP"
	done
}

# probe_anthropic_prod_mirror: PROD relay-health probe (warning-only). Hits the PROD
# public gateway /v1/messages with a key bound to one prod mirror sub-pool (cc-* or
# kiro-*), so the request traverses the real prod->edge relay a customer would use.
# Emits under a CALLER-SUPPLIED tag (anthropic_prodmirror_cc / _kiro) that parse_results
# ignores — these rows never enter the allowlist; the orchestrator only diffs them
# against the edge-native servable set to warn on "edge serves but prod relay doesn't".
probe_anthropic_prod_mirror() { # $1=key $2=emit-tag  (models from $ANTHROPIC_PROD_MIRROR_MODELS)
	local key="$1" tag="$2" m f code realistic_py
	realistic_py="${SCRIPT_DIR}/../stage0/smoke_anthropic_realistic.py"
	for m in $ANTHROPIC_PROD_MIRROR_MODELS; do
		f="$(mktemp)"
		if [[ -f "$realistic_py" ]]; then
			payload="$(python3 "$realistic_py" --model "$m" --max-tokens 32 --prompt hi)"
		else
			payload="{\"model\":\"$m\",\"max_tokens\":32,\"system\":[{\"type\":\"text\",\"text\":\"$SYS\"}],\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"metadata\":{\"user_id\":\"servable-probe\"}}"
		fi
		code="$(curl -s -o "$f" -w '%{http_code}' -m 75 -X POST "$PROD/v1/messages" \
			-H "x-api-key: $key" -H 'anthropic-version: 2023-06-01' \
			-H 'anthropic-beta: claude-code-20250219' -H 'X-App: cli' \
			-H "User-Agent: $UA" -H 'content-type: application/json' \
			--data-binary "$payload")"
		emit "$tag" "$m" "$code" "$(verdict "$code" "$f")"
		rm -f "$f"
		sleep "$REQ_SLEEP"
	done
}

# probe_compat_endpoint: OpenAI-compatible Bearer-auth probe against an arbitrary
# base. Used by openai, gemini, and dashscope/GLM legacy alias (all PROD / OpenAI-compat) families —
# they share the same /v1/* OpenAI-compat surface, only base + key + emit-tag differ.
# This proves the current PROD catalog/menu surface for prod-backed compatible
# families, not raw upstream capability. New ids can be locally blocked by the
# SSOT account model_mapping floor before any upstream account is selected
# (observed 2026-07-08: gpt-5.6*). Follow up with probe_account_model.sh or
# the platform's direct capability probe before promoting such a model.
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

# probe_edge_internal_compat: native edge-served OpenAI-compat probe. Runs ON an edge
# host and hits the app container directly via the caddy container (busybox wget over the
# docker network, bypassing the edge Caddy /v1/* CIDR restriction). Used by grok (the only
# edge-internal native platform today); parametrized over emit-tag/container/app-url so a
# future edge-internal platform can reuse it. NB antigravity does NOT use this — its
# accounts are prod-served, so it probes the prod public gateway via probe_compat_endpoint.
probe_edge_internal_compat() { # $1=tag $2=container $3=app-url $4=key $5=endpoint $6=models $7=jsonbody-template-fn
	local tag="$1" container="$2" appurl="$3" key="$4" path="$5" models="$6" buildfn="$7" m hf bf code body
	for m in $models; do
		hf="$(mktemp)"; bf="$(mktemp)"; body="$($buildfn "$m")"
		sudo docker exec "$container" wget -S -q -O - --timeout=90 \
			--header="Authorization: Bearer $key" --header='content-type: application/json' \
			--post-data="$body" "$appurl$path" >"$bf" 2>"$hf" || true # preflight-allow: swallow (status parsed from -S header file)
		code="$(grep -oE 'HTTP/[0-9.]+ [0-9]{3}' "$hf" | tail -1 | grep -oE '[0-9]{3}$' || true)" # preflight-allow: swallow (no status line -> code stays empty -> next line sets 000)
		[ -z "$code" ] && code=000
		emit "$tag" "$m" "$code" "$(verdict "$code" "$bf")"
		rm -f "$hf" "$bf"
		sleep "$REQ_SLEEP"
	done
}

probe_grok_internal() { # $1=key $2=endpoint $3=models $4=jsonbody-template-fn (back-compat wrapper)
	probe_edge_internal_compat grok "$GROK_APP_CONTAINER" "$GROK_APP_URL" "$1" "$2" "$3" "$4"
}

body_chat() { printf '{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}' "$1"; }
body_resp() { printf '{"model":"%s","instructions":"You are a helpful assistant.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say OK"}]}],"stream":false}' "$1"; }
body_img() { printf '{"model":"%s","prompt":"a small red circle on white","n":1,"size":"1024x1024"}' "$1"; }
# Gemini-native image models generate through the chat surface. The model id itself
# selects IMAGE output; the aspect-ratio payload mirrors Studio's optional extra_body.
body_chat_image() { printf '{"model":"%s","max_tokens":1024,"stream":false,"messages":[{"role":"user","content":"Create a simple image of a small red circle on a white background."}],"extra_body":{"google":{"image_config":{"aspect_ratio":"1:1"}}}}' "$1"; }
# Ark seedream 4.5/5.0 reject 1024x1024 with "image size must be at least
# 3686400 pixels"; use the same 2K square tier Studio sends for Seedream.
body_ark_img() { printf '{"model":"%s","prompt":"a small red circle on white","n":1,"size":"2048x2048"}' "$1"; }
body_video() { printf '{"model":"%s","prompt":"a small red ball rolling on a table","seconds":"4"}' "$1"; }
# VolcEngine through TokenKey uses the OpenAI-video gateway shape. The new-api
# task adaptor turns this into Ark's upstream create-task shape; do not send the
# direct Ark `content[]` body on the gateway path.
body_volcengine_video() { printf '{"model":"%s","prompt":"a small red ball rolling on a table --resolution 480p --duration 5"}' "$1"; }
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

probe_zhipu() { # $1=key  (legacy alias models from $ZHIPU_CHAT_MODELS)
	probe_compat_endpoint newapi "$PROD" "$1" /v1/chat/completions "$ZHIPU_CHAT_MODELS" body_chat
}

probe_antigravity_v1beta() { # $1=key  (models from $ANTIGRAVITY_CHAT_MODELS)
	local key="$1" m f code path
	for m in $ANTIGRAVITY_CHAT_MODELS; do
		f="$(mktemp)"
		path="/antigravity/v1beta/models/${m}:generateContent"
		code="$(curl -s -o "$f" -w '%{http_code}' -m 90 -X POST "$PROD$path" \
			-H "x-goog-api-key: $key" -H 'content-type: application/json' \
			--data-binary '{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":16}}')"
		emit antigravity "$m" "$code" "$(verdict "$code" "$f")"
		rm -f "$f"
		sleep "$REQ_SLEEP"
	done
}

# Resolve a catalog probe key via __tk_probe_<scope>_*. Tracks scopes for EXIT cleanup.
TK_PROBE_CATALOG_SCOPES=""
tk_probe_catalog_key() { # $1=scope $2=platform $3=bind_kind $4=bind_val -> sets REPLY_KEY
	local scope="$1" platform="$2" bind_kind="$3" bind_val="$4"
	if ! tk_probe_prepare_catalog "$scope" "$platform" "$bind_kind" "$bind_val"; then
		return 1
	fi
	TK_PROBE_CATALOG_SCOPES="${TK_PROBE_CATALOG_SCOPES} ${TK_PROBE_SCOPE:-$scope}"
	REPLY_KEY="$TK_PROBE_KEY"
}

tk_probe_catalog_cleanup() {
	local scope
	for scope in $TK_PROBE_CATALOG_SCOPES; do
		[ -n "$scope" ] || continue
		tk_probe_clear_bindings "$scope" || true # preflight-allow: swallow
	done
	tk_probe_release_reuse_locks
}

main() {
	trap tk_probe_catalog_cleanup EXIT
	local reply_key okey gkey grkey dkey zkey vkey arkacct arkkey arkbase
	local REPLY_BIND_KIND REPLY_BIND_VAL REPLY_BIND_LABEL
	if [ -n "${ANTHROPIC_MODELS:-}" ]; then
		probe_bind_source "$PROBE_ANTHROPIC_SOURCE_GROUP_ID" "$PROBE_ANTHROPIC_SOURCE_GROUP"
		if tk_probe_catalog_key anthropic anthropic "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			reply_key="$REPLY_KEY"
			probe_anthropic_internal "$reply_key"
		else
			cfgerr anthropic "failed to prepare __tk_probe_anthropic_* ($REPLY_BIND_LABEL — this edge has no schedulable native OAuth account; rotate to another edge)"
		fi
	fi
	# Prod relay-health probe (warning-only): probe each prod mirror sub-pool (cc-* and
	# kiro-*, split out of prod group_id=1 by name prefix) through the prod gateway.
	# Reserved-resources only — distinct scopes/tags; the orchestrator diffs the results
	# against the edge-native servable set. A cold sub-pool config_errors (rotation cue).
	if [ -n "${ANTHROPIC_PROD_MIRROR_MODELS:-}" ]; then
		probe_bind_group_like "$PROBE_ANTHROPIC_MIRROR_GROUP_ID" "$PROBE_ANTHROPIC_MIRROR_GROUP" "cc-%"
		if tk_probe_catalog_key anthropic_prodmirror_cc anthropic "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			probe_anthropic_prod_mirror "$REPLY_KEY" anthropic_prodmirror_cc
		else
			cfgerr anthropic_prodmirror_cc "no schedulable cc-* mirror in $REPLY_BIND_LABEL (prod anthropic-OAuth relay pool empty)"
		fi
		probe_bind_group_like "$PROBE_ANTHROPIC_MIRROR_GROUP_ID" "$PROBE_ANTHROPIC_MIRROR_GROUP" "kiro-%"
		if tk_probe_catalog_key anthropic_prodmirror_kiro anthropic "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			probe_anthropic_prod_mirror "$REPLY_KEY" anthropic_prodmirror_kiro
		else
			cfgerr anthropic_prodmirror_kiro "no schedulable kiro-* mirror in $REPLY_BIND_LABEL (prod Kiro relay pool empty)"
		fi
	fi
	if [ -n "${OPENAI_CHAT_MODELS:-}${OPENAI_RESPONSES_MODELS:-}${OPENAI_IMAGE_MODELS:-}" ]; then
		# OpenAI catalog probing uses prod group_id=2 as the current customer
		# serving truth. A 400 "Unsupported model: X" can mean prod mapping
		# floor rejected the id before upstream; do not treat it as raw upstream proof.
		probe_bind_source "$PROBE_OPENAI_SOURCE_GROUP_ID" "$PROBE_OPENAI_SOURCE_GROUP"
		if tk_probe_catalog_key openai openai "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			okey="$REPLY_KEY"
			[ -n "${OPENAI_CHAT_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/chat/completions "$OPENAI_CHAT_MODELS" body_chat
			[ -n "${OPENAI_RESPONSES_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/responses "$OPENAI_RESPONSES_MODELS" body_resp
			[ -n "${OPENAI_IMAGE_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/images/generations "$OPENAI_IMAGE_MODELS" body_img
		else
			cfgerr openai "failed to prepare __tk_probe_openai_* ($REPLY_BIND_LABEL)"
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
			# NOTE: doubao-seed-translation-* 400s on the bare chat probe AND on a plain
			# translate prompt — Ark's translation models need a proprietary request shape
			# (translation params) not documented in the new-api volcengine adaptor. Until
			# that authoritative shape is known it stays inconclusive (the model is priced +
			# served in prod regardless; this only affects probe classification). Do NOT ship
			# a guessed shape. See SKILL.md "translation 族探测" note.
			[ -n "${ARK_CHAT_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/chat/completions "$ARK_CHAT_MODELS" body_chat
			[ -n "${ARK_IMAGE_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/images/generations "$ARK_IMAGE_MODELS" body_ark_img
			[ -n "${ARK_VIDEO_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/contents/generations/tasks "$ARK_VIDEO_MODELS" body_ark_video
		fi
	fi
	# Dashscope/qwen family: newapi channel_type=17 (account 60 "Qwen", Ali) served
	# through the PROD TK gateway /v1/chat/completions. Probe key is the reserved
	# __tk_probe_newapi_qwen_key (accounts copied from PROBE_DASHSCOPE_SOURCE_GROUP_ID);
	# never printed. Routes at prod.
	if [ -n "${DASHSCOPE_CHAT_MODELS:-}" ]; then
		probe_bind_source "$PROBE_DASHSCOPE_SOURCE_GROUP_ID" "$PROBE_DASHSCOPE_SOURCE_GROUP"
		if tk_probe_catalog_key newapi_qwen newapi "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			dkey="$REPLY_KEY"
			probe_dashscope "$dkey"
		else
			cfgerr newapi "failed to prepare __tk_probe_newapi_qwen_* ($REPLY_BIND_LABEL)"
		fi
	fi
	# Legacy Zhipu/GLM env alias: direct GLM/ZhipuV4 is gone; GLM chat models
	# are served through Qwen/China pools. Reuse the qwen reserved key/group so
	# this path does not create a separate GLM probe resource.
	if [ -n "${ZHIPU_CHAT_MODELS:-}" ]; then
		probe_bind_source "$PROBE_ZHIPU_SOURCE_GROUP_ID" "$PROBE_ZHIPU_SOURCE_GROUP"
		if tk_probe_catalog_key newapi_qwen newapi "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			zkey="$REPLY_KEY"
			probe_zhipu "$zkey"
		else
			cfgerr newapi "failed to prepare __tk_probe_newapi_qwen_* for legacy GLM alias ($REPLY_BIND_LABEL)"
		fi
	fi
	# VolcEngine/Ark media family: newapi channel_type=45 (account 7 "volcengine")
	# served through the PROD TK gateway. This is the end-to-end serving probe
	# (pricing gate + scheduler + model_mapping + new-api adaptor), distinct from
	# ARK_* direct data-plane activation probes above.
	if [ -n "${VOLCENGINE_IMAGE_MODELS:-}${VOLCENGINE_VIDEO_MODELS:-}" ]; then
		probe_bind_source "$PROBE_VOLCENGINE_SOURCE_GROUP_ID" "$PROBE_VOLCENGINE_SOURCE_GROUP"
		if tk_probe_catalog_key newapi_volcengine newapi "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			vkey="$REPLY_KEY"
			[ -n "${VOLCENGINE_IMAGE_MODELS:-}" ] && probe_compat_endpoint volcengine "$PROD" "$vkey" /v1/images/generations "$VOLCENGINE_IMAGE_MODELS" body_ark_img
			[ -n "${VOLCENGINE_VIDEO_MODELS:-}" ] && probe_compat_endpoint volcengine "$PROD" "$vkey" /v1/video/generations "$VOLCENGINE_VIDEO_MODELS" body_volcengine_video
		else
			cfgerr volcengine "failed to prepare __tk_probe_newapi_volcengine_* ($REPLY_BIND_LABEL)"
		fi
	fi
	# Grok family: native xAI OAuth pool on the edge (currently us4). Probe key is
	# a TK api_key BOUND TO the edge-side grok group_id=4; never printed. The probe runs
	# on the edge host and hits the app container directly, bypassing edge Caddy.
	if [ -n "${GROK_CHAT_MODELS:-}${GROK_IMAGE_MODELS:-}${GROK_VIDEO_MODELS:-}" ]; then
		probe_bind_source "$PROBE_GROK_SOURCE_GROUP_ID" "$PROBE_GROK_SOURCE_GROUP"
		if tk_probe_catalog_key grok grok "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			grkey="$REPLY_KEY"
			[ -n "${GROK_CHAT_MODELS:-}" ] && probe_grok_internal "$grkey" /v1/chat/completions "$GROK_CHAT_MODELS" body_chat
			[ -n "${GROK_IMAGE_MODELS:-}" ] && probe_grok_internal "$grkey" /v1/images/generations "$GROK_IMAGE_MODELS" body_img
			[ -n "${GROK_VIDEO_MODELS:-}" ] && probe_grok_internal "$grkey" /v1/video/generations "$GROK_VIDEO_MODELS" body_video
		else
			cfgerr grok "failed to prepare __tk_probe_grok_* ($REPLY_BIND_LABEL)"
		fi
	fi
	# Antigravity family: accounts (e.g. antigravity-us3/us4 in prod group_id=21)
	# live in the PROD DB and schedule from prod. Text/capability probes use the native
	# /antigravity/v1beta Gemini surface. Studio image generation is different: the UI
	# goes through /v1/chat/completions, where the gateway adapts gemini-*-image models
	# to Antigravity/Gemini image output. Keep the families separate so a v1beta-only
	# 404 cannot falsely eject a Studio-served image model.
	if [ -n "${ANTIGRAVITY_CHAT_MODELS:-}${ANTIGRAVITY_IMAGE_MODELS:-}" ]; then
		probe_bind_source "$PROBE_ANTIGRAVITY_SOURCE_GROUP_ID" "$PROBE_ANTIGRAVITY_SOURCE_GROUP"
		if tk_probe_catalog_key antigravity antigravity "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			agkey="$REPLY_KEY"
			[ -n "${ANTIGRAVITY_CHAT_MODELS:-}" ] && probe_antigravity_v1beta "$agkey"
			[ -n "${ANTIGRAVITY_IMAGE_MODELS:-}" ] && probe_compat_endpoint antigravity "$PROD" "$agkey" /v1/chat/completions "$ANTIGRAVITY_IMAGE_MODELS" body_chat_image
		else
			cfgerr antigravity "failed to prepare __tk_probe_antigravity_* ($REPLY_BIND_LABEL — the prod antigravity source group has no schedulable antigravity account)"
		fi
	fi
	# Gemini family: newapi/Vertex models. Live Vertex capacity moved from edge us6
	# to the PROD Vertex group_id=16 (account ids 47/57/58/59/74), so gemini now probes
	# the PROD public gateway with external curl — identical transport to the other
	# newapi families (dashscope/GLM legacy alias), NOT the edge-internal wget hop (that was only
	# needed to bypass an edge Caddy /v1/* CIDR restriction; prod's gateway is public).
	# emit tag stays `gemini` so parse_results maps results to the gemini allowlist.
	if [ -n "${GEMINI_CHAT_MODELS:-}${GEMINI_CHATIMAGE_MODELS:-}${GEMINI_IMAGE_MODELS:-}${GEMINI_VIDEO_MODELS:-}" ]; then
		probe_bind_source "$PROBE_GEMINI_SOURCE_GROUP_ID" "$PROBE_GEMINI_SOURCE_GROUP"
		if tk_probe_catalog_key newapi_google newapi "$REPLY_BIND_KIND" "$REPLY_BIND_VAL"; then
			gkey="$REPLY_KEY"
			[ -n "${GEMINI_CHAT_MODELS:-}" ] && probe_compat_endpoint gemini "$PROD" "$gkey" /v1/chat/completions "$GEMINI_CHAT_MODELS" body_chat
			# gemini-*-image generate via the chat/generateContent surface, NOT the
			# images predict API — probe through /v1/chat/completions (emit `gemini`).
			[ -n "${GEMINI_CHATIMAGE_MODELS:-}" ] && probe_compat_endpoint gemini "$PROD" "$gkey" /v1/chat/completions "$GEMINI_CHATIMAGE_MODELS" body_chat
			[ -n "${GEMINI_IMAGE_MODELS:-}" ] && probe_compat_endpoint gemini "$PROD" "$gkey" /v1/images/generations "$GEMINI_IMAGE_MODELS" body_img
			[ -n "${GEMINI_VIDEO_MODELS:-}" ] && probe_compat_endpoint gemini "$PROD" "$gkey" /v1/video/generations "$GEMINI_VIDEO_MODELS" body_video
		else
			cfgerr gemini "failed to prepare __tk_probe_newapi_google_* ($REPLY_BIND_LABEL)"
		fi
	fi
	# Verdicts live in the emitted TSV, never in the exit code (config_error rows
	# exit 0 too). Force success so a trailing `[ -n "$X" ] && cmd` guard whose env
	# is empty (the common single-family batch shape) cannot leave $? = 1 and make
	# run-probe / the orchestrator treat a completed probe batch as a transport FATAL.
	return 0
}

main
