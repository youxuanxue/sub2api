.PHONY: build generate test test-unit test-integration test-e2e lint lint-security

VERSION ?= $(shell tr -d '\r\n' < ./cmd/server/VERSION)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)
GOLANGCI_LINT_CONCURRENCY ?= 4
GOLANGCI_LINT_FAST_ARGS ?= --concurrency=$(GOLANGCI_LINT_CONCURRENCY)
GOLANGCI_LINT_SECURITY_ARGS ?= --concurrency=$(GOLANGCI_LINT_CONCURRENCY)

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

lint:
	golangci-lint run ./... $(GOLANGCI_LINT_FAST_ARGS)

lint-security:
	golangci-lint run ./... --enable-only gosec $(GOLANGCI_LINT_SECURITY_ARGS)

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
