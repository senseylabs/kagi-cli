.PHONY: build build-all clean test lint vet fmt cover

BINARY_NAME=kagi
# NOTE: this VERSION default (0.1.0) only stamps `make build`; it does not match
# the `main.go` "dev" sentinel (the compiled-in fallback when -ldflags is absent)
# nor the git tag that goreleaser stamps on a real release. Left as-is so plain
# `make build` produces a recognizable non-"dev" local binary; don't rely on it
# for released version numbers.
VERSION?=0.1.0
# COMMIT and DATE stamp `kagi version` (the plain `--version` output stays just
# the version string). Derived from git/date when unset; overridable for
# reproducible builds. goreleaser injects its own values on a real release.
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE?=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS=-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) .

build-all: build-darwin-arm64 build-darwin-amd64 build-linux-amd64

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-darwin-arm64 .

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-darwin-amd64 .

build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-linux-amd64 .

# Test both modules: the root module and the separate sdk/ module.
test:
	go test -race ./...
	cd sdk && go test -race ./...

vet:
	go vet ./...
	cd sdk && go vet ./...

# Lint both modules. golangci-lint won't cross module boundaries, so the sdk/
# module is linted from within sdk/; the root .golangci.yml is discovered by
# walking up the tree, so the same config applies.
lint:
	golangci-lint run
	cd sdk && golangci-lint run

fmt:
	gofmt -w .
	cd sdk && gofmt -w .

# Coverage for the root module only.
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

clean:
	rm -rf bin/ coverage.out
