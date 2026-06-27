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
#   ANTIGRAVITY_CHAT_MODELS  -> POST <prod>/v1/chat/completions  (native antigravity, PROD gateway)
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
# Every family resolves its probe key through the reserved __tk_probe_<scope>_*
# group/key (probe_reserved_resources.sh), copying schedulable accounts from a
# canonical source group per platform. There is NO direct-key / customer-key
# fallback — when delivered via run-probe.sh you MUST pass
#   --with ops/pricing/probe_reserved_resources.sh
# so the companion library lands on the remote host (the orchestrator and every
# documented manual invocation already do). A missing companion fails loudly.
#
# Optional env:
#   ARK_ACCOUNT_ID           default 7   (accounts row holding the ark api_key + base_url)
#   PROD_BASE                default https://api.tokenkey.dev
#   PROBE_OPENAI_SOURCE_GROUP default GPT专线 (schedulable accounts copied into probe group)
#   PROBE_ANTHROPIC_SOURCE_GROUP default `default` (the edge's native OAuth anthropic group;
#                            probe runs ON an edge, NOT prod — see refresh-allowlist ANTHROPIC_EDGES)
#   ANTHROPIC_APP_CONTAINER  default tokenkey-caddy (edge app container; busybox wget, bypass Caddy)
#   ANTHROPIC_APP_URL        default http://tokenkey:8080 (edge app, reached internally)
#   PROBE_GEMINI_SOURCE_GROUP default google-vertex (PROD newapi Vertex group; ids 47/57/58/59)
#   PROBE_DASHSCOPE_SOURCE_GROUP default Qwen (verified prod group, capital Q; CASE-SENSITIVE)
#   PROBE_ZHIPU_SOURCE_GROUP default GLM (prod GLM group; CASE-SENSITIVE)
#   PROBE_GROK_SOURCE_GROUP  default grok (verified edge native grok group; CASE-SENSITIVE)
#   GROK_APP_CONTAINER       default tokenkey-caddy
#   GROK_APP_URL             default http://tokenkey:8080
#   PROBE_ANTIGRAVITY_SOURCE_GROUP default Google-Gemini (PROD antigravity group; CASE-SENSITIVE)
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
PROBE_OPENAI_SOURCE_GROUP="${PROBE_OPENAI_SOURCE_GROUP:-GPT专线}"
# anthropic probes an EDGE's native OAuth pool directly (the edge `default` group),
# not the prod cc-* mirror accounts: the mirrors relay prod->edge and cool down on any
# edge upstream blip, so a prod-gateway probe empty-pools (429 not_allowlisted) whenever
# the mirror pool is exhausted — a false "unservable". One healthy edge is enough to
# confirm a model; the orchestrator rotates across deployable edges (refresh-servable-
# allowlist.py ANTHROPIC_EDGES) and a model is servable if ANY edge serves it.
PROBE_ANTHROPIC_SOURCE_GROUP="${PROBE_ANTHROPIC_SOURCE_GROUP:-default}"  # edge native OAuth group
ANTHROPIC_APP_CONTAINER="${ANTHROPIC_APP_CONTAINER:-tokenkey-caddy}"
ANTHROPIC_APP_URL="${ANTHROPIC_APP_URL:-http://tokenkey:8080}"
# Prod relay-health probe (ANTHROPIC_PROD_MIRROR_MODELS, warning-only — does NOT feed
# the allowlist). prod serves claude via api-key MIRROR accounts that all sit in the
# `claude` group but split by NAME prefix into two upstream relays: `cc-*` (anthropic
# OAuth edges) and `kiro-*` (Kiro edges). Each sub-pool is probed on its own key via the
# prod gateway and emits a distinct platform tag (anthropic_prodmirror_cc / _kiro) that
# parse_results ignores; the orchestrator warns when an edge-servable model fails here.
PROBE_ANTHROPIC_MIRROR_GROUP="${PROBE_ANTHROPIC_MIRROR_GROUP:-claude}"
PROBE_GEMINI_SOURCE_GROUP="${PROBE_GEMINI_SOURCE_GROUP:-google-vertex}"
PROBE_DASHSCOPE_SOURCE_GROUP="${PROBE_DASHSCOPE_SOURCE_GROUP:-Qwen}"
PROBE_ZHIPU_SOURCE_GROUP="${PROBE_ZHIPU_SOURCE_GROUP:-GLM}"
PROBE_GROK_SOURCE_GROUP="${PROBE_GROK_SOURCE_GROUP:-grok}"
# Gemini/Vertex now serves from the PROD `google-vertex` group (not edge us6), so the
# probe goes through the PROD public gateway with external curl like the other newapi
# families — no edge-internal wget hop is needed (prod's Caddy is not CIDR-restricted).
GROK_APP_CONTAINER="${GROK_APP_CONTAINER:-tokenkey-caddy}"
GROK_APP_URL="${GROK_APP_URL:-http://tokenkey:8080}"
# antigravity accounts (e.g. antigravity-us3/us4 in the "Google-Gemini" group) live in
# the PROD DB and are scheduled from prod, so antigravity probes the PROD public gateway
# with external curl — same transport as gemini/zhipu (prod Caddy is not CIDR-restricted),
# NOT the edge-internal wget hop. The "-usN" suffix is an upstream label, not an edge.
PROBE_ANTIGRAVITY_SOURCE_GROUP="${PROBE_ANTIGRAVITY_SOURCE_GROUP:-Google-Gemini}"
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

# probe_anthropic_internal: edge-native probe. Runs ON an edge host and hits the app
# container directly (busybox wget, bypassing the edge Caddy /v1/* CIDR restriction),
# so the request is scheduled onto the edge's OWN native OAuth account (the `default`
# group) and relayed straight to Anthropic — no prod cc-* mirror hop. Key is the reserved
# __tk_probe_anthropic_key bound to the edge `default` accounts; never printed.
probe_anthropic_internal() { # $1=key  (models from $ANTHROPIC_MODELS)
	local key="$1" m hf bf code body
	for m in $ANTHROPIC_MODELS; do
		hf="$(mktemp)"; bf="$(mktemp)"
		body="{\"model\":\"$m\",\"max_tokens\":32,\"system\":[{\"type\":\"text\",\"text\":\"$SYS\"}],\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: OK\"}],\"metadata\":{\"user_id\":\"servable-probe\"}}"
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
	local key="$1" tag="$2" m f code
	for m in $ANTHROPIC_PROD_MIRROR_MODELS; do
		f="$(mktemp)"
		code="$(curl -s -o "$f" -w '%{http_code}' -m 75 -X POST "$PROD/v1/messages" \
			-H "x-api-key: $key" -H 'anthropic-version: 2023-06-01' \
			-H 'anthropic-beta: claude-code-20250219' -H 'X-App: cli' \
			-H "User-Agent: $UA" -H 'content-type: application/json' \
			--data-binary "{\"model\":\"$m\",\"max_tokens\":32,\"system\":[{\"type\":\"text\",\"text\":\"$SYS\"}],\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: OK\"}],\"metadata\":{\"user_id\":\"servable-probe\"}}")"
		emit "$tag" "$m" "$code" "$(verdict "$code" "$f")"
		rm -f "$f"
		sleep "$REQ_SLEEP"
	done
}

# probe_compat_endpoint: OpenAI-compatible Bearer-auth probe against an arbitrary
# base. Used by openai, gemini, and zhipu/dashscope (all PROD / OpenAI-compat) families —
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

# probe_edge_internal_compat: native edge-served OpenAI-compat probe. Runs ON an edge
# host and hits the app container directly via the caddy container (busybox wget over the
# docker network, bypassing the edge Caddy /v1/* CIDR restriction). Shared by the native
# edge platforms grok + antigravity — only the emit-tag, container, and app URL differ.
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

# Resolve a catalog probe key via __tk_probe_<scope>_*. Tracks scopes for EXIT cleanup.
TK_PROBE_CATALOG_SCOPES=""
tk_probe_catalog_key() { # $1=scope $2=platform $3=bind_kind $4=bind_val -> sets REPLY_KEY
	local scope="$1" platform="$2" bind_kind="$3" bind_val="$4"
	if ! tk_probe_prepare_catalog "$scope" "$platform" "$bind_kind" "$bind_val"; then
		return 1
	fi
	TK_PROBE_CATALOG_SCOPES="${TK_PROBE_CATALOG_SCOPES} ${scope}"
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
	local reply_key okey gkey grkey dkey zkey arkacct arkkey arkbase
	if [ -n "${ANTHROPIC_MODELS:-}" ]; then
		if tk_probe_catalog_key anthropic anthropic source_group "$PROBE_ANTHROPIC_SOURCE_GROUP"; then
			reply_key="$REPLY_KEY"
			probe_anthropic_internal "$reply_key"
		else
			cfgerr anthropic "failed to prepare __tk_probe_anthropic_* (source_group=$PROBE_ANTHROPIC_SOURCE_GROUP — this edge has no schedulable native OAuth account; rotate to another edge)"
		fi
	fi
	# Prod relay-health probe (warning-only): probe each prod mirror sub-pool (cc-* and
	# kiro-*, split out of the `claude` group by name prefix) through the prod gateway.
	# Reserved-resources only — distinct scopes/tags; the orchestrator diffs the results
	# against the edge-native servable set. A cold sub-pool config_errors (rotation cue).
	if [ -n "${ANTHROPIC_PROD_MIRROR_MODELS:-}" ]; then
		if tk_probe_catalog_key anthropic_prodmirror_cc anthropic group_like "${PROBE_ANTHROPIC_MIRROR_GROUP}|cc-%"; then
			probe_anthropic_prod_mirror "$REPLY_KEY" anthropic_prodmirror_cc
		else
			cfgerr anthropic_prodmirror_cc "no schedulable cc-* mirror in group '$PROBE_ANTHROPIC_MIRROR_GROUP' (prod anthropic-OAuth relay pool empty)"
		fi
		if tk_probe_catalog_key anthropic_prodmirror_kiro anthropic group_like "${PROBE_ANTHROPIC_MIRROR_GROUP}|kiro-%"; then
			probe_anthropic_prod_mirror "$REPLY_KEY" anthropic_prodmirror_kiro
		else
			cfgerr anthropic_prodmirror_kiro "no schedulable kiro-* mirror in group '$PROBE_ANTHROPIC_MIRROR_GROUP' (prod Kiro relay pool empty)"
		fi
	fi
	if [ -n "${OPENAI_CHAT_MODELS:-}${OPENAI_RESPONSES_MODELS:-}${OPENAI_IMAGE_MODELS:-}" ]; then
		if tk_probe_catalog_key openai openai source_group "$PROBE_OPENAI_SOURCE_GROUP"; then
			okey="$REPLY_KEY"
			[ -n "${OPENAI_CHAT_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/chat/completions "$OPENAI_CHAT_MODELS" body_chat
			[ -n "${OPENAI_RESPONSES_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/responses "$OPENAI_RESPONSES_MODELS" body_resp
			[ -n "${OPENAI_IMAGE_MODELS:-}" ] && probe_openai_endpoint "$okey" /v1/images/generations "$OPENAI_IMAGE_MODELS" body_img
		else
			cfgerr openai "failed to prepare __tk_probe_openai_* (source_group=$PROBE_OPENAI_SOURCE_GROUP)"
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
			[ -n "${ARK_IMAGE_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/images/generations "$ARK_IMAGE_MODELS" body_img
			[ -n "${ARK_VIDEO_MODELS:-}" ] && probe_compat_endpoint volcengine "$arkbase" "$arkkey" /api/v3/contents/generations/tasks "$ARK_VIDEO_MODELS" body_ark_video
		fi
	fi
	# Dashscope/qwen family: newapi channel_type=17 (account 60 "Qwen", Ali) served
	# through the PROD TK gateway /v1/chat/completions. Probe key is the reserved
	# __tk_probe_newapi_qwen_key (accounts copied from PROBE_DASHSCOPE_SOURCE_GROUP);
	# never printed. Routes at prod (the newapi pool is prod-served, unlike gemini).
	if [ -n "${DASHSCOPE_CHAT_MODELS:-}" ]; then
		if tk_probe_catalog_key newapi_qwen newapi source_group "$PROBE_DASHSCOPE_SOURCE_GROUP"; then
			dkey="$REPLY_KEY"
			probe_dashscope "$dkey"
		else
			cfgerr newapi "failed to prepare __tk_probe_newapi_qwen_* (source_group=$PROBE_DASHSCOPE_SOURCE_GROUP)"
		fi
	fi
	# Zhipu/GLM family: newapi channel_type=26 (account 67 "GLM") served through
	# the PROD TK gateway /v1/chat/completions. Probe key is the reserved
	# __tk_probe_newapi_glm_key (accounts copied from PROBE_ZHIPU_SOURCE_GROUP); never printed.
	if [ -n "${ZHIPU_CHAT_MODELS:-}" ]; then
		if tk_probe_catalog_key newapi_glm newapi source_group "$PROBE_ZHIPU_SOURCE_GROUP"; then
			zkey="$REPLY_KEY"
			probe_zhipu "$zkey"
		else
			cfgerr newapi "failed to prepare __tk_probe_newapi_glm_* (source_group=$PROBE_ZHIPU_SOURCE_GROUP)"
		fi
	fi
	# Grok family: native xAI OAuth pool on the edge (currently us4). Probe key is
	# a TK api_key BOUND TO the edge-side grok group; never printed. The probe runs
	# on the edge host and hits the app container directly, bypassing edge Caddy.
	if [ -n "${GROK_CHAT_MODELS:-}" ]; then
		if tk_probe_catalog_key grok grok source_group "$PROBE_GROK_SOURCE_GROUP"; then
			grkey="$REPLY_KEY"
			probe_grok_internal "$grkey" /v1/chat/completions "$GROK_CHAT_MODELS" body_chat
		else
			cfgerr grok "failed to prepare __tk_probe_grok_* (source_group=$PROBE_GROK_SOURCE_GROUP)"
		fi
	fi
	# Antigravity family: native edge-served (accounts e.g. antigravity-us3/us4 in the
	# "Google-Gemini" group). Same edge-internal transport as grok (caddy -> app:8080).
	# Probe key is a TK api_key bound to the edge-side antigravity group; never printed.
	if [ -n "${ANTIGRAVITY_CHAT_MODELS:-}" ]; then
		if tk_probe_catalog_key antigravity antigravity source_group "$PROBE_ANTIGRAVITY_SOURCE_GROUP"; then
			agkey="$REPLY_KEY"
			probe_compat_endpoint antigravity "$PROD" "$agkey" /v1/chat/completions "$ANTIGRAVITY_CHAT_MODELS" body_chat
		else
			cfgerr antigravity "failed to prepare __tk_probe_antigravity_* (source_group=$PROBE_ANTIGRAVITY_SOURCE_GROUP — this edge has no schedulable antigravity account; rotate to another edge)"
		fi
	fi
	# Gemini family: newapi/Vertex models. Live Vertex capacity moved from edge us6
	# to the PROD `google-vertex` group (account ids 47/57/58/59), so gemini now probes
	# the PROD public gateway with external curl — identical transport to the other
	# newapi families (zhipu/dashscope), NOT the edge-internal wget hop (that was only
	# needed to bypass an edge Caddy /v1/* CIDR restriction; prod's gateway is public).
	# emit tag stays `gemini` so parse_results maps results to the gemini allowlist.
	if [ -n "${GEMINI_CHAT_MODELS:-}${GEMINI_IMAGE_MODELS:-}${GEMINI_VIDEO_MODELS:-}" ]; then
		if tk_probe_catalog_key newapi_google newapi source_group "$PROBE_GEMINI_SOURCE_GROUP"; then
			gkey="$REPLY_KEY"
			[ -n "${GEMINI_CHAT_MODELS:-}" ] && probe_compat_endpoint gemini "$PROD" "$gkey" /v1/chat/completions "$GEMINI_CHAT_MODELS" body_chat
			[ -n "${GEMINI_IMAGE_MODELS:-}" ] && probe_compat_endpoint gemini "$PROD" "$gkey" /v1/images/generations "$GEMINI_IMAGE_MODELS" body_img
			[ -n "${GEMINI_VIDEO_MODELS:-}" ] && probe_compat_endpoint gemini "$PROD" "$gkey" /v1/video/generations "$GEMINI_VIDEO_MODELS" body_video
		else
			cfgerr gemini "failed to prepare __tk_probe_newapi_google_* (source_group=$PROBE_GEMINI_SOURCE_GROUP)"
		fi
	fi
	# Verdicts live in the emitted TSV, never in the exit code (config_error rows
	# exit 0 too). Force success so a trailing `[ -n "$X" ] && cmd` guard whose env
	# is empty (the common single-family batch shape) cannot leave $? = 1 and make
	# run-probe / the orchestrator treat a completed probe batch as a transport FATAL.
	return 0
}

main
