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

This package depends **only on the Go standard library + `github.com/google/uuid`**
(already a TokenKey dependency). No new module was added to `go.mod`.

## File mapping (TK file ← Kiro-Go file)

| TokenKey file | Kiro-Go source | Contents |
| --- | --- | --- |
| `translator.go` | `proxy/translator.go` | Claude ↔ Kiro and OpenAI ↔ Kiro request/response translation, model mapping, prompt filtering |
| `client.go` | `proxy/kiro.go` | HTTP client, official runtime + transitional q fallback, `CallKiroAPI`, `parseEventStream`, AWS EventStream decode, tool-use handling |
| `headers.go` | `proxy/kiro_headers.go` | aws-sdk-js style User-Agent / `x-amz-user-agent` builder |
| `rest.go` | `proxy/kiro_api.go` | REST calls: usage limits, user info, model list, profile ARN resolution, `RefreshAccountInfo` |
| `refresh.go` | `auth/oidc.go` | `RefreshToken`: social + OIDC token refresh |
| `shim.go` | *(new, TK-authored)* | Local replacements for the upstream `config` / `logger` / `auth` packages |
| `translator_test.go`, `eventstream_test.go` | *(new, TK-authored)* | Golden unit tests |

## Change log vs. upstream

The vendored `.go` files are byte-for-byte upstream **except** for the
mechanical, scoped edits below. Translation/transport logic is unchanged.

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
   - `config.GetKiroClientConfig` → local `GetKiroClientConfig()` with defaults
     copied from upstream (`KiroVersion "0.11.107"`, `NodeVersion "22.22.0"`,
     `SystemVersion` OS-derived).
   - `config.GetFilterClaudeCode/StripBoundaries/EnvNoise` → `false` (prompt
     filtering disabled in vendored default; later PR wires to TK settings).
   - `config.GetPromptFilterRules` → `nil`.
   - `logger.Debugf/Infof/Warnf/Errorf` → local `logDebugf/logInfof/logWarnf/logErrorf`
     (thin `log/slog` wrappers).
   - `auth.RefreshToken` → in-package `RefreshToken` (refresh.go).
   - `auth.GetAuthClientForProxy` → local `GetAuthClientForProxy` (30s-timeout
     client, reuses `buildKiroTransport`).

3. **DB side effects removed.** The vendored package never writes a database.
   - `config.UpdateAccountProfileArn(...)` calls in `ResolveProfileArn` deleted —
     the resolved ARN is set only on the in-memory `account.ProfileArn` and
     returned. TokenKey persists it.
   - `config.UpdateAccount(...)` calls in `RefreshAccountInfo` (ban/suspend/clear)
     deleted. Ban / suspended / auth-error detection now surfaces purely through
     the returned `error`; the passed `*Account` is no longer mutated. The
     TokenKey layer inspects the error and decides whether to disable/ban the ent
     account. Function signature `(*AccountInfo, error)` is preserved.

4. **`HTTPDoer` decoupling point added** (`client.go`). New
   `type HTTPDoer interface { Do(*http.Request) (*http.Response, error) }` and
   `CallKiroAPIWithDoer(doer HTTPDoer, ...)`. `CallKiroAPI` now delegates to it
   with `nil` (built-in per-proxy client, identical behavior). This lets a later
   PR inject TokenKey's TLS/proxy-aware doer. (REST functions still use the
   built-in client; a doer seam there can be added at first need.)

No other upstream logic was touched.

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
   remove DB side effects, keep the `HTTPDoer` seam). `shim.go` and the tests are
   TK-authored — do not overwrite them; only update `shim.go` defaults if the
   upstream `config` package defaults (e.g. `KiroVersion`) changed.
4. Update the pinned commit SHA + date at the top of this file.
5. Verify:
   ```bash
   cd backend
   go build ./internal/integration/kiro/...
   go vet   ./internal/integration/kiro/...
   go test -tags=unit ./internal/integration/kiro/...
   ```
