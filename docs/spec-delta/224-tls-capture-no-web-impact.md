# Spec delta: PR #224 TLS capture and attribution no-web-impact

## Intent

PR #224 changes two internal surfaces:

- Claude Code local hook timing for `.tls_list/` capture (`Stop` plus `SessionEnd`).
- Backend upstream error attribution by adding TLS fingerprint profile id/name to the existing `ops_error_logs.upstream_errors` JSON payload.

## Web impact

None.

## Rationale

No frontend page, user-facing setting, route, request schema, response schema, or admin API contract changes are required. The backend change only adds optional fields inside an existing ops/debug JSON column used for failure attribution, and the hook change only affects local Claude Code session artifacts ignored by git.

## Validation

- `go test -tags=unit ./internal/service -run 'TestAppendOpsUpstreamError|TestSafeUpstreamURL'`
- `go test -tags=unit ./internal/service -run 'TestNonExistent'`
- `bash scripts/preflight.sh`
