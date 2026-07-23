.PHONY: build generate test test-unit test-integration test-e2e lint lint-security

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION ?= v2.9.0
GOLANGCI_LINT_CONCURRENCY ?= 4
GOLANGCI_LINT_FAST_ARGS ?= --concurrency=$(GOLANGCI_LINT_CONCURRENCY)
GOLANGCI_LINT_SECURITY_ARGS ?= --concurrency=$(GOLANGCI_LINT_CONCURRENCY)

.PHONY: ensure-golangci-lint
ensure-golangci-lint:
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
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

lint: ensure-golangci-lint
	$(GOLANGCI_LINT) run ./... $(GOLANGCI_LINT_FAST_ARGS)

lint-security: ensure-golangci-lint
	$(GOLANGCI_LINT) run ./... --enable-only gosec $(GOLANGCI_LINT_SECURITY_ARGS)

test-e2e:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
