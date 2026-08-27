.PHONY: build generate test test-unit test-integration test-e2e lint lint-security

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

# Toolchain pin — go.mod is the single source of truth.
#
# GOTOOLCHAIN=auto (the default) only ever upgrades: if the developer's ambient
# Go is NEWER than go.mod's `go` directive, every go command silently runs on
# that newer GOROOT instead of the declared one. CI never sees this because
# actions/setup-go installs exactly `go-version-file: backend/go.mod` (and
# backend-ci asserts it in "Verify Go version").
#
# The recurring symptom is a golangci-lint crash that looks like a code defect
# but is not: a v2.9.0 binary cannot decode a newer stdlib's export data
# ("export data version 4 is greater than maximum supported version 2"), so it
# panics or reports phantom typecheck errors on untouched files. What matters is
# the toolchain the linter RUNS under, not the Go that built the binary.
#
# Deriving the exact version here makes local `make lint` / `make test` match CI
# mechanically. Bumping go.mod is the only way to move it.
GO_MOD_VERSION := $(shell awk '/^go /{print $$2; exit}' go.mod)
GOTOOLCHAIN ?= go$(GO_MOD_VERSION)
export GOTOOLCHAIN

GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION ?= v2.9.0
GOLANGCI_LINT_CONCURRENCY ?= 4
GOLANGCI_LINT_FAST_ARGS ?= --concurrency=$(GOLANGCI_LINT_CONCURRENCY)
GOLANGCI_LINT_SECURITY_ARGS ?= --concurrency=$(GOLANGCI_LINT_CONCURRENCY)
UNIT_TEST_SERVICE_SHARD ?= 0

.PHONY: verify-toolchain
verify-toolchain:
	@actual=$$(go version | awk '{print $$3}'); \
	if [ "$$actual" != "go$(GO_MOD_VERSION)" ]; then \
		echo "toolchain mismatch: go.mod declares go$(GO_MOD_VERSION) but 'go version' reports $$actual"; \
		echo "  GOTOOLCHAIN=$${GOTOOLCHAIN:-<unset>}"; \
		echo "  This Makefile pins GOTOOLCHAIN=go$(GO_MOD_VERSION); an explicit override in the"; \
		echo "  environment or 'go env -w GOTOOLCHAIN=...' defeats it. Unset it, or bump go.mod."; \
		exit 1; \
	fi

.PHONY: ensure-golangci-lint
ensure-golangci-lint: verify-toolchain
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint not found; installing $(GOLANGCI_LINT_VERSION)..."; \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	}

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test: test-unit lint

test-unit:
ifeq ($(UNIT_TEST_SERVICE_SHARD),1)
	python3 ../scripts/ci/unit_test_runner.py --root .
else
	go test -tags=unit ./...
endif

test-integration:
	python3 ../scripts/ci/integration_test_runner.py --root .

lint: ensure-golangci-lint
	$(GOLANGCI_LINT) run ./... $(GOLANGCI_LINT_FAST_ARGS)

lint-security: ensure-golangci-lint
	$(GOLANGCI_LINT) run ./... --enable-only gosec $(GOLANGCI_LINT_SECURITY_ARGS)

test-e2e:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
