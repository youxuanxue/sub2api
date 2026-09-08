# TokenKey Cursor service

Cursor supplies models through the official SDK. TokenKey keeps its existing
`newapi` platform, `apikey` account type and Anthropic channel type `14`. A
dedicated service group named `Cursor` exposes Messages, Chat Completions and
Responses. Model families and fixed model IDs stay independent of the supply.

The implementation uses new-api `7bbe85bcb09546e0b89572bf97198fc94889be3d`
and its RelayKit module from the same sibling checkout. The bridge is vendored
from PR #6869; provenance and licenses are in [VENDORED_FROM.md](VENDORED_FROM.md).
The SDK is pinned to `1.0.31` and the container to Node `24.20.0`.

## Connect an account

Configure the backend and bridge, create an empty `newapi` service group named
`Cursor`, and open **Accounts > Connect Cursor**. Select the group, authorize in
the official Cursor browser page, then save. TokenKey does not collect Cursor
passwords or require a pre-existing API key. The saved account carries the whole
fixed SDK catalog and explicitly selected regular-speed variants.

Authorization requires SDK access on the actual subscription/organization.
Login and catalog discovery do not prove inference permission, remaining quota,
or regional availability. `Auto` and `default` are excluded from fixed models.
The initial authorization session lasts ten minutes. The resulting user API key
expires after at most ninety days; reconnect the existing account before expiry.
There is no refresh token. Reconnection replaces the credential through the same
account transaction and preserves account/group identity.

Create a TokenKey API key for this service group. Existing Studio Chat and agent
clients use the normal gateway paths:

| Client contract | Gateway path | Bridge contract |
| --- | --- | --- |
| Anthropic Messages | `/v1/messages` | Messages |
| OpenAI Chat | `/v1/chat/completions` | Chat-to-Messages conversion |
| OpenAI Responses | `/v1/responses` | Responses-to-Messages conversion |

Clients must include tool history and results in subsequent requests. Server-side
`previous_response_id` recovery is not provided. The supported scope is text,
streaming and client-executed function tools. SDK agent behavior is not identical
to each provider's native API: sampling parameters, strict structured output,
thinking/cache controls and exact `max_tokens` enforcement are not promised.
The SDK does not receive the client's `max_tokens` as an upstream spending cap.
Use existing TokenKey quotas plus the bridge run and concurrency limits.

## Deployment

Build from this directory (never from the vendored direct entrypoint):

```sh
docker build -t tokenkey-cursor-bridge:review services/cursor-bridge
```

Build and publish a separate Bridge image and pin its reviewed digest. The
TokenKey release workflow publishes the gateway image; it does not publish or
enable this optional Bridge.
`deploy/aws/stage0/docker-compose.cursor.yml` is an optional overlay for the
existing Stage0 compose stack. Supply these values through its private environment:

| Variable | Owner and meaning |
| --- | --- |
| `CURSOR_BRIDGE_IMAGE` | Deployment: reviewed image by digest |
| `CURSOR_BRIDGE_URL` | Backend: exact internal origin, normally `http://cursor-bridge:3927` |
| `CURSOR_BRIDGE_SECRET` | Backend and bridge: same random secret, at least 32 bytes |
| `CURSOR_RELAY_SECRET` | Prod and enabled Edge: same independent secret, at least 32 bytes, when relaying |
| `CURSOR_AGENT_MAX_ACTIVE_SESSIONS` | Bridge: default 4, includes suspended tool runs |
| `CURSOR_AGENT_MAX_SESSIONS_PER_CREDENTIAL` | Bridge: default 4 |

The overlay exposes no bridge port. It uses the existing private network, a
non-root process, read-only filesystem, tmpfs, 2 GiB memory limit, one CPU,
256-process limit, log rotation
and a 35-second stop grace period. `/health` is public on the private network;
all other routes require internal authentication and a trusted owner. Backend
requests must match the configured bridge URL before a secret is attached.
Gateway startup has no dependency on Bridge health. Start or replace the Bridge
independently; keep Cursor admission disabled until its health and account probe
succeed. A failed Bridge must not prevent the remaining gateway supplies from
starting. The deployment contract and mutation tests run in preflight.

The production entrypoint owns the session defaults for both CLI and Docker.
These limits bound resource consumption; they are not a measured throughput
promise. Check host headroom and latency before increasing them.

Initial topology is one account, one bridge instance and a dedicated group on
one enabled Edge (or a single backend directly connected to the bridge). Do not
add other accounts to the group using generic administration, load-balance the
bridge, or configure cross-family model remapping. Tool sessions are bound to
the original user/API key/account and credential. Prod-to-Edge forwarding signs
the originating identity; ordinary client headers cannot impersonate it.
Live distributed Prod-to-Edge deployment has not been verified by the local tests.
Authorization import accepts exactly one service group. Key expiry remains a
runtime hard gate even if account auto-pause is disabled. Reauthorization
preserves the persisted scheduling state: an already paused account requires
the existing resume action after its credential is replaced. Renew an active
account before expiry to avoid that interruption, after draining its tool runs.

Runs have a 15-minute deadline and suspended calls expire. A restart invalidates
continuations; stale, foreign or replayed tool results fail explicitly. No tool
execution is replayed. Shutdown drains for 25 seconds before cancellation.
The wrapper always supplies an isolated official `JsonlLocalAgentStore`, deletes
completed agent state, and removes its temporary directory at process exit.
The container's tmpfs also discards it on replacement. Persistent run recovery
and fallback to credentials from the process environment are disabled.

To roll back, stop admission to the Cursor group, drain the bridge, and disable
its account before removing the overlay. This does not require a database schema
rollback. Do not remove an active bridge while expecting tool continuations to
survive. Production deployment is a separate reviewed operation.

## Usage and pricing

The shared Anthropic-to-OpenAI usage conversion now preserves ordinary input,
cache-read and cache-write buckets separately through settlement. Existing
Messages supplies can consequently charge ordinary input previously lost by
double cache subtraction. Rates and historical rows are not rewritten. The
rollout regression exercises both buffered and streamed responses through the
real billing command, for ordinary supplies and Cursor.

The bridge reports per-HTTP-turn deltas from the SDK's cumulative usage. Some
SDK runs report no usage while waiting for client tools. Those turns estimate
visible request/output tokens with `gpt-tokenizer@4.0.0`, with cache estimates
set to zero. Later cumulative buckets deduct previously emitted amounts.
Negative deltas are clamped to zero: an estimate above a final bucket is retained
as a floor, with no automatic refund. Abandoned runs retain their estimates.
This is an estimated billing policy, not exact reconciliation against a Cursor
invoice. All Cursor gateway usage records carry `cursor-sdk-estimated` in the
existing billing-tier field so the policy remains visible across protocols.

TokenKey's price owner remains `backend/internal/service/tk_pricing_overlay.json`.
Composer 2.5 regular uses $0.50/M input, $0.20/M cache-read and $2.50/M output.
Composer 2 uses $0.50/M input and $2.50/M output; its cache-read rate of $0.50/M
is explicitly a conservative TokenKey rate, not a separately published Cursor
cache price. Account subscriptions and Cursor's included pools do not establish
zero marginal cost or unlimited access to every model.

## Verification

The sanitized [2026-09-07 report](validation/2026-09-07.json) records 36 real UI
Chat attempts: 6 passed and 30 were blocked by Cursor regional policy. Composer
2.5 passed three-protocol tool continuation, parallel tools, Claude Code Read,
and gateway billing checks. These are historical local results.

The [prod egress comparison](validation/2026-09-07-prod-egress.json) used the same
account via the existing AWS US-East production host. Its US egress was verified:
Composer 2.5 succeeded, while Claude Sonnet 4.6, GPT 5.4 and Gemini 3.1 Pro still
returned the same regional restriction. The SDK still ran on the laptop through
an application proxy, so this did not establish native prod behavior.

The [2026-09-08 native prod verification](validation/2026-09-08-prod-native.json)
supersedes that deployment conclusion. SDK 1.0.31 ran directly in an isolated
ARM64 container on prod, without proxy variables. The same key served Composer,
Claude, GPT and Gemini. The actual Bridge then served the frozen 36-model catalog
through local TokenKey UI/gateway over SSM: all 36 returned HTTP 200. Two translated
the initial `OK` prompt; both passed an explicit no-translation UI retry.
Composer 2.5, Claude Sonnet 4.6, GPT 5.4 and Gemini 3.1 Pro passed tool continuation
on all three protocols, with 24 accepted turns matching the TokenKey ledger.
Parallel tools and Claude Code Read also passed. This verifies TokenKey's billing
policy, not the Cursor subscription invoice.

This run also exposed completed tool sessions occupying active capacity during
replay retention. Embedded mode now releases them immediately while continuing
to reject duplicate results with 409. The same four-slot test limit was retained.
The initial strict GPT tool probe needed a retry; the report preserves that result.

Deploy the Bridge itself on the tested supported egress. A browser IP check or
HTTP proxy flag alone does not establish every SDK request's network path. The
successful result applies to this account and tested environment, not all accounts
or future provider policy. No production gateway deployment, account import,
group change or pricing change was performed; temporary probe resources were
removed after verification.

Prerequisites: the pinned sibling new-api checkout, Go, Docker, Node >=22.19,
pnpm, and a Cursor account with SDK access. Run from the TokenKey root:

```sh
npm --prefix services/cursor-bridge ci --ignore-scripts
npm --prefix services/cursor-bridge/upstream ci --ignore-scripts
pnpm --dir frontend install --frozen-lockfile
node scripts/cursor/local-dev.mjs prepare
node scripts/cursor/local-dev.mjs start
```

The local UI is `http://127.0.0.1:15179`, the gateway is
`http://127.0.0.1:18097`, and the bridge binds loopback port `3927`. Generated
private credentials and evidence stay under `.cache/cursor-dev/`, excluded from
Git. The local script refuses occupied application ports. Stop with
`node scripts/cursor/local-dev.mjs stop`; PostgreSQL and Redis retain test data.

The UI probe uses bundled Playwright Chromium for TokenKey and opens the official
authorization page in the user's existing Chrome session on macOS. Account
creation and all-model Chat are exercised through the real UI. Local group,
balance and test-key setup are API fixtures, not counted as UI acceptance.

```sh
pnpm --dir frontend exec playwright install chromium
node frontend/e2e/cursor-live.tk.mjs authorize
node frontend/e2e/cursor-live.tk.mjs chat-all
CURSOR_E2E_MOBILE=1 node frontend/e2e/cursor-live.tk.mjs chat
npm install --prefix .cache/cursor-dev/clients --no-save --ignore-scripts openai@7.10.0 @anthropic-ai/sdk@0.124.0
node scripts/cursor/client-probe.mjs tools
node scripts/cursor/billing-audit.mjs
node scripts/cursor/client-probe.mjs parallel
node scripts/cursor/claude-code-probe.mjs
docker build -t tokenkey-cursor-bridge:dev services/cursor-bridge
node scripts/cursor/container-probe.mjs
npm --prefix services/cursor-bridge test
```

Real probes consume the authorized account's quota. The Chat probe creates an
isolated local key with a $5 quota and two-day expiry. Failed regions are reported
against the complete frozen catalog; they are not removed to improve pass rates.
The container probe calls the private bridge directly and is a deployment smoke
test, not UI e2e. It checks text/tools, credential rejection and state cleanup.

## Ownership and evolution

| Concern | Single owner |
| --- | --- |
| Browser authorization state | `frontend/src/composables/useCursorAuthorization.tk.ts` |
| Connect/reconnect display | `frontend/src/components/account/CursorConnectModal.tk.vue` |
| Account transaction | `backend/internal/service/account_tk_cursor.go` |
| Internal SDK client | `backend/internal/integration/cursor/client.go` |
| Model parameters and trusted transport identity | `backend/internal/service/gateway_tk_cursor.go` |
| Scheduling projection | `backend/internal/repository/scheduler_cache.go` |
| SDK lifecycle and tool continuation | Vendored `upstream/harness_messages.mjs` plus the TK wrapper |
| Shared accounting | `backend/internal/service/openai_gateway_usage.go` |
| Model admission and publication | Served-model manifest, pricing overlay and generated model-surface bundle |

Sub2api remains the application and gateway foundation. New-api is a pinned
adapter dependency, upgraded with compatibility tests. TokenKey owns the user
experience, policy and accounting. Keep Cursor-specific hooks in `.tk`/`_tk`
owners and register shared call sites in the existing gateway/frontend sentinels.
Do not fork a second gateway or replicate upstream's admin/account system.
Upgrade the sidecar and SDK independently with upstream tests, TokenKey boundary
tests and real-account probes. A future native new-api implementation can replace
the bridge behind these same account and protocol contracts after equivalence
is demonstrated.
