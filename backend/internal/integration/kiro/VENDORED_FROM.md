# Vendored: Kiro / CodeWhisperer protocol layer

This package vendors the Kiro/CodeWhisperer protocol translation + transport
layer from the open-source **Quorinex/Kiro-Go** project so TokenKey can add Kiro
as a sixth platform without taking a runtime dependency on the upstream repo.

## Source

- **Upstream:** `github.com/Quorinex/Kiro-Go`
- **Pinned commit:** `a2e3971ccf6bf550573282407359902a5f16a85a` (2026-05-31)
- **License:** MIT — `Copyright (c) 2026 Quorinex`. The upstream `LICENSE`
  (MIT) governs this vendored code; retain attribution when redistributing.

## What was vendored

Only the **stateless protocol/transport layer** was vendored. Upstream HTTP
handlers, the account pool, admin endpoints, response stores, and anything that
touched the upstream config DB / file store were intentionally left behind —
TokenKey owns scheduling, persistence, and request routing.

Its only external dependencies are the Go standard library and
`github.com/google/uuid` (already a TokenKey dependency). `headers.go` also uses
TokenKey's internal `internal/pkg/kiro` fingerprint owner. No new module was added
to `go.mod`.

## File mapping (TK file ← Kiro-Go file)

| TokenKey file | Kiro-Go source | Contents |
| --- | --- | --- |
| `translator.go` | `proxy/translator.go` | Claude ↔ Kiro and OpenAI → Kiro translation, model mapping, prompt filtering; OpenAI responses use the gateway encoder |
| `client.go` | `proxy/kiro.go` | HTTP client, official runtime + transitional q fallback, `CallKiroAPIWithDoerContext`, `parseEventStream`, AWS EventStream decode, tool-use handling |
| `headers.go` | `proxy/kiro_headers.go` | TokenKey Kiro CLI User-Agent / `x-amz-user-agent` adapter |
| `rest.go` | `proxy/kiro_api.go` | REST calls: usage limits (including user info), profile ARN resolution, `RefreshAccountInfo` |
| `refresh.go` | `auth/oidc.go` | `RefreshToken`: social + OIDC token refresh |
| `shim.go` | *(new, TK-authored)* | Local replacements for the upstream `config` / `logger` / `auth` packages |
| `translator_test.go`, `eventstream_test.go` | *(new, TK-authored)* | Golden unit tests |

## Change log vs. upstream

The following describes the initial import adaptations. Later TokenKey behavior
changes are tracked in git and guarded by `scripts/sentinels/kiro.json`.

1. **Package rename.** `package proxy` / `package auth` → `package kiro` across
   all files.

2. **External-package elimination → `shim.go`.** Every reference to the upstream
   `config.` / `logger.` / `auth.` packages was redirected to local symbols
   defined in `shim.go`:
   - `config.Account` → local `Account` (pure data carrier; field names match
     every access site verbatim, zero access-point changes).
   - `config.AccountInfo` → local `AccountInfo` (fields = exactly what
     `RefreshAccountInfo` populates).
   - `config.PromptFilterRule` → local `PromptFilterRule`.
   - `config.GetProxyURL` → `GetProxyURL()` returns `""` (TokenKey handles egress
     proxying; later PR wires this).
   - `config.GetEndpointFallback` → `true` (tries only the official runtime and transitional q chain).
   - `config.GetPreferredEndpoint` → `"auto"` (current runtime first, transitional q second).
   - `config.GetKiroClientConfig` is not mirrored in `shim.go`; the header path is
     the deliberate TokenKey adapter described below.
   - `config.GetFilterClaudeCode` → `true`; `StripBoundaries/EnvNoise` → `false`
     (preserve Claude Code identity while leaving the other filters disabled).
   - `config.GetPromptFilterRules` → `nil`.
   - Used `logger.Debugf/Infof/Warnf` calls → local `logDebugf/logInfof/logWarnf`
     (thin `log/slog` wrappers).
   - `auth.RefreshToken` → in-package `RefreshToken` (refresh.go).
   - `auth.GetAuthClientForProxy` → local `GetAuthClientForProxy` (30s-timeout
     client, reuses `buildKiroTransport`).

3. **Canonical fingerprint adapter** (`headers.go`). Instead of copying upstream
   config defaults and User-Agent formatting, every actual request path consumes
   the one real-CLI HTTP identity in `internal/pkg/kiro`. There is no per-account
   suffix, environment override, or alternate client mode. `headers_test.go` locks
   streaming and runtime requests to that owner.

4. **DB side effects removed.** The vendored package never writes a database.
   - `config.UpdateAccountProfileArn(...)` calls in `ResolveProfileArnWithDoer` deleted —
     the resolved ARN is set only on the in-memory `account.ProfileArn` and
     returned. TokenKey persists it.
   - `config.UpdateAccount(...)` calls in `RefreshAccountInfo` (ban/suspend/clear)
     deleted. Ban / suspended / auth-error detection now surfaces purely through
     the returned `error`; the passed `*Account` is no longer mutated. The
     TokenKey layer inspects the error and decides whether to disable/ban the ent
     account. Function signature `(*AccountInfo, error)` is preserved.

5. **Injected transport** (`client.go`, `rest.go`). Streaming uses
   `CallKiroAPIWithDoerContext`; REST usage/profile operations also accept
   `HTTPDoer`. TokenKey supplies its TLS/proxy-aware transport; nil keeps the
   built-in per-proxy fallback. Unused no-doer wrappers, standalone user/model
   discovery, and the unused context-window estimator have been removed.

`tool_history.go` owns current conversation normalization: retain valid tool
call/result pairs across history, preserve failure status in the result block,
and repair only unpaired results. Both translators use it. Do not restore
upstream's blanket history narration. The gateway returns completed model turns
to the client agent without injecting a private completion tool or user turn.

## Re-vendor procedure

To refresh from a newer upstream commit:

1. Clone upstream and check out the target commit:
   ```bash
   git clone https://github.com/Quorinex/Kiro-Go /tmp/kiro-go
   git -C /tmp/kiro-go checkout <new-sha>
   ```
2. Copy the five source files into this directory and rename their package
   clause to `kiro`:
   - `proxy/translator.go`   → `translator.go`
   - `proxy/kiro.go`         → `client.go`
   - `proxy/kiro_headers.go` → `headers.go`
   - `proxy/kiro_api.go`     → `rest.go`
   - `auth/oidc.go`          → `refresh.go`
3. Re-apply the change log above (package rename, config/logger/auth → shim,
   canonical `internal/pkg/kiro` header adapter, removed DB side effects, and the
   `HTTPDoer` seam). `shim.go` and the tests are TK-authored — do not overwrite
   them. Do not restore upstream `GetKiroClientConfig` literals; refresh canonical
   identity fields only through `internal/pkg/kiro` after the required evidence.
4. Update the pinned commit SHA + date at the top of this file.
5. Verify:
   ```bash
   cd backend
   go build ./internal/integration/kiro/...
   go vet   ./internal/integration/kiro/...
   go test -tags=unit ./internal/integration/kiro/...
   ```
