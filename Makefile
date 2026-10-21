# Metricule build and verification targets.
#
# CGO is off everywhere. §4.3 forbids a CGO requirement in the default release, and
# the SQLite adapter uses modernc.org/sqlite specifically so that holds.
export CGO_ENABLED := 0

BINARY     := metricule
PKG        := github.com/teagan42/metricule
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE       ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE      ?= ghcr.io/teagan42/metricule

LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)

GO_FILES := $(shell find . -name '*.go' -not -path './vendor/*')

.DEFAULT_GOAL := check

.PHONY: build
build: ## Build the binary into bin/
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w $(GO_FILES)

.PHONY: fmt-check
fmt-check: ## Fail if any file is unformatted
	@unformatted=$$(gofmt -l $(GO_FILES)); \
	if [ -n "$$unformatted" ]; then \
		echo "unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint when available
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping (CI runs it)"; \
	fi

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: test-race
test-race: ## Run unit tests under the race detector
	CGO_ENABLED=1 go test -race ./...

.PHONY: test-integration
test-integration: ## Run integration tests (requires Docker)
	go test -tags=integration -timeout 15m ./tests/integration/...

.PHONY: cover
cover: ## Write a coverage profile and HTML report
	go test -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: check
check: fmt-check vet lint test ## Run every fast verification gate

.PHONY: schema
schema: build ## Print the configuration JSON Schema
	./bin/$(BINARY) config schema

.PHONY: validate-examples
validate-examples: build ## Validate every example configuration
	@for f in examples/*.yaml examples/*/*.yaml; do \
		[ -f "$$f" ] || continue; \
		echo "validating $$f"; \
		./bin/$(BINARY) validate --config "$$f" || exit 1; \
	done

.PHONY: docker
docker: ## Build the container image for the host platform
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t $(IMAGE):$(VERSION) .

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out coverage.html

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
