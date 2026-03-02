GO ?= go
BINARY ?= distil-proxy
CMD_PATH ?= ./cmd/distil-proxy
BIN_DIR ?= bin
DIST_DIR ?= dist
COVERAGE_FILE ?= coverage.out
GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.gomodcache
export GOCACHE
export GOMODCACHE

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS ?= -X github.com/distilnet/distil-proxy/internal/version.version=$(VERSION) \
	-X github.com/distilnet/distil-proxy/internal/version.commit=$(COMMIT) \
	-X github.com/distilnet/distil-proxy/internal/version.date=$(BUILD_DATE)

.PHONY: build run test test-race lint vuln coverage coverage-check clean build-artifacts install-local checksums

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD_PATH)

run:
	$(GO) run $(CMD_PATH)

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

coverage:
	$(GO) test -coverprofile=$(COVERAGE_FILE) ./...
	$(GO) tool cover -func=$(COVERAGE_FILE)

coverage-check:
	./scripts/coverage-gate.sh

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR) $(COVERAGE_FILE)

build-artifacts:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-darwin-amd64 $(CMD_PATH)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-darwin-arm64 $(CMD_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-linux-amd64 $(CMD_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-linux-arm64 $(CMD_PATH)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-windows-amd64.exe $(CMD_PATH)
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-windows-arm64.exe $(CMD_PATH)

install-local: build
	mkdir -p $$HOME/.distil-proxy/bin
	cp $(BIN_DIR)/$(BINARY) $$HOME/.distil-proxy/bin/$(BINARY)
	chmod +x $$HOME/.distil-proxy/bin/$(BINARY)
	@echo "installed $$HOME/.distil-proxy/bin/$(BINARY)"

checksums: build-artifacts
	cd $(DIST_DIR) && shasum -a 256 $(BINARY)-* > SHA256SUMS
