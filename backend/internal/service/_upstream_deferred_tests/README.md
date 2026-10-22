# Upstream-deferred service tests (A-commit parking)

These MERGE_HEAD-only tests are parked with `//go:build ignore` because they
depend on MH service APIs that are only partially grafted in the A harness
commit (gemini cache billing, payment confirm, opencode routing probes, etc.).

B-commit work: restore each file to `backend/internal/service/`, drop the ignore
tag, and finish the corresponding MH symbol grafts until
`go test -tags=unit ./internal/service/` compiles and passes.
