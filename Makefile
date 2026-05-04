# Moorpost — top-level Makefile
# All real work happens in cli/. This is just convenience wrappers.

.PHONY: build test test-race e2e cover install clean lint release help

CLI := cli
BIN := moorpost
VERSION_PKG := github.com/latent-advisory/moorpost/cli/internal/version

# Build-time version metadata. `git describe` returns the most recent
# tag if HEAD is on it (e.g. v0.1.0); otherwise <tag>-<n>-g<sha>.
VERSION ?= $(shell git -C $(CLI)/.. describe --tags --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git -C $(CLI)/.. rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) \
           -X $(VERSION_PKG).Commit=$(COMMIT) \
           -X $(VERSION_PKG).Date=$(DATE)

help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nMoorpost dev commands:\n\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the moorpost binary into cli/moorpost (with version injected)
	cd $(CLI) && go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test: ## Run unit tests
	cd $(CLI) && go test ./...

test-race: ## Run unit tests with -race
	cd $(CLI) && go test -race ./...

cover: ## Show test coverage per package
	cd $(CLI) && go test -cover ./...

e2e: ## Run real-GCP E2E tests (creates VMs; cost guardrails apply)
	cd $(CLI) && go test -v -tags=gcp_e2e -timeout=18m ./internal/provider/gcp/...

install: build ## Install the binary to /usr/local/bin
	install -m 0755 $(CLI)/$(BIN) /usr/local/bin/$(BIN)
	@echo "Installed: $$(/usr/local/bin/$(BIN) --version)"

lint: ## Run go vet
	cd $(CLI) && go vet ./...

clean: ## Remove built binary and test artifacts
	rm -f $(CLI)/$(BIN)
	rm -rf dist/
	cd $(CLI) && go clean ./...

release: ## Cross-compile binaries for darwin/linux × arm64/amd64 into dist/
	@mkdir -p dist
	@echo "Building $(VERSION) for 4 targets..."
	@for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do \
		os=$${target%-*}; arch=$${target#*-}; \
		out=dist/$(BIN)-$$os-$$arch; \
		echo "  → $$out"; \
		(cd $(CLI) && GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o ../$$out .) || exit 1; \
	done
	@echo "Computing SHA256SUMS..."
	@cd dist && shasum -a 256 $(BIN)-* > SHA256SUMS && cat SHA256SUMS
	@echo "Done. Artifacts in dist/."
